// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

//go:build linux

package btrfs

import (
	"fmt"
	goio "io"
	"os"
	"path/filepath"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// This file implements RECEIVE-APPLY: replaying a btrfs send stream in
// userspace to recreate the sender's subvolume tree under a destination mount.
// It is the consumer side of the send/receive story, the mirror of Send: a real
// `btrfs send` stream applied by Receive reproduces the source tree, and our own
// Send -> Receive round-trips entirely in-library. As with the rest of the
// package it is pure Go (no cgo) and never shells out to the btrfs CLI; the
// replay is ordinary syscalls relative to the new subvolume's root plus three
// ioctls (SUBVOL_CREATE / SNAP_CREATE_V2 for the subvolume, FICLONERANGE for
// CLONE, SET_RECEIVED_SUBVOL + SUBVOL_SETFLAGS to finalise).
//
// The wire contract is the v1 send stream a default `btrfs send` emits. Paths in
// the stream are relative to the subvolume root and are used as-is: the *sender*
// already resolves the orphan/temp-name ("o<ino>-<gen>") dance and emits final
// paths, so the receiver simply applies each command's PATH against the subvol
// root. Both full streams (SUBVOL) and incremental streams (SNAPSHOT, which
// reference an already-received parent by its received UUID) are supported.
//
// DEFERRED: v2 encoded/compressed writes (BTRFS_SEND_FLAG_COMPRESSED;
// BTRFS_SEND_C_ENCODED_WRITE / FALLOCATE / SETFLAGS / ENABLE_VERITY, commands
// 23-26). A default `btrfs send` does not emit these; Receive recognises them
// and returns ErrUnsupportedCommand rather than silently corrupting the tree.
// FALLOCATE/UPDATE_EXTENT for preallocation is treated as a no-op for file data
// (the subsequent WRITEs materialise the bytes).

// ErrUnsupportedCommand is returned by Receive when the stream carries a
// command the replay does not implement (notably the v2 encoded-write family);
// the partially-created subvolume is left in place for inspection.
var ErrUnsupportedCommand = fmt.Errorf("btrfs: unsupported send-stream command")

// ReceiveOpts controls a Receive. The zero value is the common case: replay the
// stream under destPath, finalise each subvolume (stamp its received UUID and
// set it read-only) exactly as `btrfs receive` does.
type ReceiveOpts struct {
	// NoReadonly leaves the received subvolume writable instead of setting the
	// read-only flag at the END of the stream. The received UUID is still
	// stamped. Off by default (a received subvolume is normally read-only).
	NoReadonly bool

	// NoSetReceived skips the BTRFS_IOC_SET_RECEIVED_SUBVOL stamp at END. The
	// subvolume is still created and populated, but `btrfs subvolume show` will
	// not report a Received UUID and it cannot serve as an incremental parent.
	// Off by default.
	NoSetReceived bool
}

// receiver holds the mutable state of an in-progress receive: the destination
// mount it writes under, the current subvolume being materialised, and the
// cached write fd reused across consecutive WRITE/CLONE to the same file.
type receiver struct {
	destPath string       // destination mount (where subvolumes are created)
	opts     ReceiveOpts

	subvolName string   // name of the subvolume currently being received
	subvolPath string   // destPath/subvolName, the root commands apply under
	subvolUUID [16]byte  // stream's subvol UUID (for SET_RECEIVED_SUBVOL)
	ctransid   uint64    // stream's subvol ctransid
	otime      sendTimespec // subvol otime (used as the received stime)

	curPath string   // path (relative to subvol) of the cached write fd
	curFile *os.File // cached O_RDWR fd for consecutive writes/clones
}

// Receive replays the btrfs send stream read from r, recreating the sender's
// subvolume(s) under destPath via ordinary syscalls and a few ioctls. destPath
// must be a directory on a mounted btrfs filesystem (typically the mount point
// or a directory within it) under which the new subvolume is created.
//
// For a SUBVOL command Receive creates a fresh subvolume; for a SNAPSHOT command
// (incremental stream) it snapshots the already-received parent found by the
// stream's clone/parent UUID. It then applies every filesystem command
// (MKFILE/MKDIR/.../WRITE/CLONE/...) relative to that subvolume's root, and at
// END stamps the received UUID and sets the subvolume read-only, exactly as the
// real `btrfs receive` does — so `btrfs subvolume show` reports the correct
// Received UUID and read-only flag, and the result can serve as the parent of a
// later incremental receive.
//
// Receive requires privileges sufficient to create subvolumes and set the
// received-subvol metadata (typically root). It returns the first error
// encountered; on error the partially-created subvolume is left in place.
func Receive(destPath string, r goio.Reader, opts ReceiveOpts) error {
	h, err := ParseHeader(r)
	if err != nil {
		return fmt.Errorf("Receive: %w", err)
	}
	if h.Version != btrfsSendStreamVersion {
		return fmt.Errorf("Receive: unsupported stream version %d (only v%d supported)", h.Version, btrfsSendStreamVersion)
	}

	rc := &receiver{destPath: destPath, opts: opts}
	defer rc.closeCurFile()

	cr := NewCommandReader(r).WithData()
	for {
		cmd, err := cr.Next()
		if err == goio.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("Receive: reading command: %w", err)
		}
		if err := rc.apply(cmd); err != nil {
			return fmt.Errorf("Receive: cmd %d: %w", cmd.Cmd, err)
		}
		if cmd.IsEnd() {
			break
		}
	}
	return nil
}

// apply decodes one command's attributes and dispatches to the handler.
func (rc *receiver) apply(cmd Command) error {
	a, err := decodeAttrs(cmd.Data)
	if err != nil {
		return err
	}
	if os.Getenv("BTRFS_RECV_TRACE") != "" {
		fmt.Fprintf(os.Stderr, "TRACE cmd=%d path=%q pathTo=%q pathLink=%q ino=%d off=%d datalen=%d mode=%#o\n",
			cmd.Cmd, a.path, a.pathTo, a.pathLink, a.ino, a.fileOffset, len(a.data), a.mode)
	}
	switch cmd.Cmd {
	case btrfsSendCmdSubvol:
		return rc.doSubvol(&a)
	case btrfsSendCmdSnapshot:
		return rc.doSnapshot(&a)
	case sendCmdMkfile:
		return rc.doMknod(&a, unix.S_IFREG)
	case sendCmdMkdir:
		return rc.doMkdir(&a)
	case sendCmdMknod:
		return rc.doMknodDev(&a)
	case sendCmdMkfifo:
		return rc.doMknod(&a, unix.S_IFIFO)
	case sendCmdMksock:
		return rc.doMknod(&a, unix.S_IFSOCK)
	case sendCmdSymlink:
		return rc.doSymlink(&a)
	case sendCmdRename:
		return rc.doRename(&a)
	case sendCmdLink:
		return rc.doLink(&a)
	case sendCmdUnlink:
		return rc.doUnlink(&a)
	case sendCmdRmdir:
		return rc.doRmdir(&a)
	case sendCmdSetXattr:
		return rc.doSetXattr(&a)
	case sendCmdRemoveXattr:
		return rc.doRemoveXattr(&a)
	case sendCmdWrite:
		return rc.doWrite(&a)
	case sendCmdClone:
		return rc.doClone(&a)
	case sendCmdTruncate:
		return rc.doTruncate(&a)
	case sendCmdChmod:
		return rc.doChmod(&a)
	case sendCmdChown:
		return rc.doChown(&a)
	case sendCmdUtimes:
		return rc.doUtimes(&a)
	case sendCmdUpdateExtent, sendCmdFallocate:
		// Data is materialised by the WRITE commands; preallocation is a no-op
		// for correctness on a normal (non-NoData) stream.
		return nil
	case btrfsSendCmdEnd:
		return rc.doEnd()
	case sendCmdEncodedWrite, sendCmdSetFileXattr, sendCmdEnableVerity:
		return fmt.Errorf("%w: v2 encoded command %d (compressed send not supported)", ErrUnsupportedCommand, cmd.Cmd)
	default:
		return fmt.Errorf("%w: %d", ErrUnsupportedCommand, cmd.Cmd)
	}
}

// full joins a stream PATH (relative to the subvolume root) onto the current
// subvolume's path. It rejects a command that arrives before a SUBVOL/SNAPSHOT
// has set up the destination subvolume.
func (rc *receiver) full(p string) (string, error) {
	if rc.subvolPath == "" {
		return "", fmt.Errorf("filesystem command before SUBVOL/SNAPSHOT")
	}
	return filepath.Join(rc.subvolPath, p), nil
}

// doSubvol handles BTRFS_SEND_C_SUBVOL: create a fresh subvolume named by the
// stream's PATH under destPath, and record the UUID/ctransid for the END stamp.
func (rc *receiver) doSubvol(a *sendAttrs) error {
	if a.path == "" {
		return fmt.Errorf("SUBVOL: empty path")
	}
	rc.startSubvol(a)
	if err := SubvolCreate(rc.destPath, a.path); err != nil {
		return err
	}
	return nil
}

// doSnapshot handles BTRFS_SEND_C_SNAPSHOT (incremental stream): snapshot the
// already-received parent (located by the stream's CLONE_UUID, i.e. the
// received UUID of the parent subvolume) into a new subvolume named by PATH.
func (rc *receiver) doSnapshot(a *sendAttrs) error {
	if a.path == "" {
		return fmt.Errorf("SNAPSHOT: empty path")
	}
	if !a.has(sendACloneUUID) {
		return fmt.Errorf("SNAPSHOT: missing clone (parent) UUID")
	}
	parent, err := rc.findReceivedSubvol(a.cloneUUID)
	if err != nil {
		return fmt.Errorf("SNAPSHOT: locating parent by received UUID: %w", err)
	}
	rc.startSubvol(a)
	// Snapshot the parent (writable) into the destination under PATH. We create
	// it writable so the incremental commands can apply; END sets it read-only.
	if err := SnapshotCreate(parent, rc.destPath, a.path, false); err != nil {
		return err
	}
	return nil
}

// startSubvol records the new subvolume's identity (name, path under destPath,
// UUID, ctransid, otime) and clears any cached per-file state.
func (rc *receiver) startSubvol(a *sendAttrs) {
	rc.closeCurFile()
	rc.subvolName = a.path
	rc.subvolPath = filepath.Join(rc.destPath, a.path)
	rc.subvolUUID = a.uuid
	rc.ctransid = a.ctransid
	rc.otime = a.otime
}

// findReceivedSubvol scans the subvolumes under destPath for one whose received
// UUID equals uuid, returning its path. This is how an incremental SNAPSHOT
// resolves its parent: the parent was stamped with the sender's subvol UUID by
// a prior receive's SET_RECEIVED_SUBVOL, so the incremental stream's CLONE_UUID
// (the parent's UUID on the sender) matches the parent's received UUID here.
func (rc *receiver) findReceivedSubvol(uuid [16]byte) (string, error) {
	subvols, err := ListSubvolumes(rc.destPath)
	if err != nil {
		return "", err
	}
	for _, s := range subvols {
		cand := filepath.Join(rc.destPath, s.Path)
		if receivedUUID(cand) == uuid {
			return cand, nil
		}
	}
	return "", fmt.Errorf("no received subvolume with UUID %x found under %s", uuid, rc.destPath)
}

// receivedUUID returns the received UUID of the subvolume rooted at path, or the
// zero UUID if it cannot be read. GetSubvolInfo does not surface received_uuid,
// so this reads it directly via the same ioctl struct.
func receivedUUID(path string) [16]byte {
	var args btrfsIoctlGetSubvolInfoArgs
	if err := ioctlDir(path, BTRFS_IOC_GET_SUBVOL_INFO, unsafe.Pointer(&args)); err != nil {
		return [16]byte{}
	}
	runtime.KeepAlive(&args)
	return args.ReceivedUUID
}

func (rc *receiver) doMkdir(a *sendAttrs) error {
	p, err := rc.full(a.path)
	if err != nil {
		return err
	}
	// Mode is applied by a later CHMOD; create with a permissive default.
	if err := unix.Mkdir(p, 0700); err != nil {
		return fmt.Errorf("mkdir %s: %w", p, err)
	}
	return nil
}

// doMknod creates a regular file, fifo, or socket via mknod with the given
// file-type bits. The permission bits are set by a later CHMOD.
func (rc *receiver) doMknod(a *sendAttrs, ifmt uint32) error {
	p, err := rc.full(a.path)
	if err != nil {
		return err
	}
	if err := unix.Mknod(p, ifmt|0600, 0); err != nil {
		return fmt.Errorf("mknod %s (fmt %#o): %w", p, ifmt, err)
	}
	return nil
}

// doMknodDev creates a character/block device node, taking the type bits from
// the stream MODE and the device number from RDEV.
func (rc *receiver) doMknodDev(a *sendAttrs) error {
	p, err := rc.full(a.path)
	if err != nil {
		return err
	}
	// MODE carries the full st_mode including the S_IFCHR/S_IFBLK type bits.
	mode := uint32(a.mode)
	dev := int(a.rdev)
	if err := unix.Mknod(p, mode, dev); err != nil {
		return fmt.Errorf("mknod %s (mode %#o dev %d): %w", p, mode, dev, err)
	}
	return nil
}

func (rc *receiver) doSymlink(a *sendAttrs) error {
	p, err := rc.full(a.path)
	if err != nil {
		return err
	}
	if err := unix.Symlink(a.pathLink, p); err != nil {
		return fmt.Errorf("symlink %s -> %s: %w", p, a.pathLink, err)
	}
	return nil
}

func (rc *receiver) doRename(a *sendAttrs) error {
	from, err := rc.full(a.path)
	if err != nil {
		return err
	}
	to, err := rc.full(a.pathTo)
	if err != nil {
		return err
	}
	// A rename may move the file whose write fd we have cached; drop it.
	if rc.curPath == a.path || rc.curPath == a.pathTo {
		rc.closeCurFile()
	}
	if err := unix.Rename(from, to); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", from, to, err)
	}
	return nil
}

func (rc *receiver) doLink(a *sendAttrs) error {
	// PATH is the new link name; PATH_LINK is the existing target, relative to
	// the subvol root.
	newp, err := rc.full(a.path)
	if err != nil {
		return err
	}
	target, err := rc.full(a.pathLink)
	if err != nil {
		return err
	}
	if err := unix.Link(target, newp); err != nil {
		return fmt.Errorf("link %s -> %s: %w", newp, target, err)
	}
	return nil
}

func (rc *receiver) doUnlink(a *sendAttrs) error {
	p, err := rc.full(a.path)
	if err != nil {
		return err
	}
	if rc.curPath == a.path {
		rc.closeCurFile()
	}
	if err := unix.Unlink(p); err != nil {
		return fmt.Errorf("unlink %s: %w", p, err)
	}
	return nil
}

func (rc *receiver) doRmdir(a *sendAttrs) error {
	p, err := rc.full(a.path)
	if err != nil {
		return err
	}
	if err := unix.Rmdir(p); err != nil {
		return fmt.Errorf("rmdir %s: %w", p, err)
	}
	return nil
}

func (rc *receiver) doSetXattr(a *sendAttrs) error {
	p, err := rc.full(a.path)
	if err != nil {
		return err
	}
	if err := unix.Lsetxattr(p, a.xattrName, a.xattrData, 0); err != nil {
		return fmt.Errorf("lsetxattr %s %q: %w", p, a.xattrName, err)
	}
	return nil
}

func (rc *receiver) doRemoveXattr(a *sendAttrs) error {
	p, err := rc.full(a.path)
	if err != nil {
		return err
	}
	if err := unix.Lremovexattr(p, a.xattrName); err != nil {
		return fmt.Errorf("lremovexattr %s %q: %w", p, a.xattrName, err)
	}
	return nil
}

// writeFile returns an O_RDWR fd for the subvol-relative path p, reusing the
// cached fd when the path is unchanged (consecutive WRITE/CLONE to one inode).
func (rc *receiver) writeFile(p string) (*os.File, error) {
	if rc.curFile != nil && rc.curPath == p {
		return rc.curFile, nil
	}
	rc.closeCurFile()
	full, err := rc.full(p)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(full, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open for write %s: %w", full, err)
	}
	rc.curFile = f
	rc.curPath = p
	return f, nil
}

func (rc *receiver) doWrite(a *sendAttrs) error {
	f, err := rc.writeFile(a.path)
	if err != nil {
		return err
	}
	if _, err := f.WriteAt(a.data, int64(a.fileOffset)); err != nil {
		return fmt.Errorf("pwrite %s @%d (%d bytes): %w", a.path, a.fileOffset, len(a.data), err)
	}
	return nil
}

// doClone handles BTRFS_SEND_C_CLONE: reflink cloneLen bytes from the source
// file (clonePath in the subvolume identified by cloneUUID) at cloneOffset into
// the destination file (PATH) at fileOffset, via FICLONERANGE.
func (rc *receiver) doClone(a *sendAttrs) error {
	dst, err := rc.writeFile(a.path)
	if err != nil {
		return err
	}
	// Resolve the clone source. When CLONE_UUID matches the subvolume currently
	// being received, the source is within this same subvolume; otherwise it is
	// another already-received subvolume located by its received UUID.
	srcSubvol := rc.subvolPath
	if a.has(sendACloneUUID) && a.cloneUUID != rc.subvolUUID {
		s, err := rc.findReceivedSubvol(a.cloneUUID)
		if err != nil {
			return fmt.Errorf("CLONE: locating source subvol: %w", err)
		}
		srcSubvol = s
	}
	srcFull := filepath.Join(srcSubvol, a.clonePath)
	src, err := os.OpenFile(srcFull, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("CLONE: open source %s: %w", srcFull, err)
	}
	defer src.Close()

	args := fileCloneRange{
		SrcFd:     int64(src.Fd()),
		SrcOffset: a.cloneOffset,
		SrcLength: a.cloneLen,
		DestOff:   a.fileOffset,
	}
	err = ioctlFd(dst.Fd(), FICLONERANGE, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	runtime.KeepAlive(src)
	if err != nil {
		return fmt.Errorf("FICLONERANGE %s <- %s: %w", a.path, srcFull, err)
	}
	return nil
}

func (rc *receiver) doTruncate(a *sendAttrs) error {
	p, err := rc.full(a.path)
	if err != nil {
		return err
	}
	// If we have the file open for writing, truncate the fd; else truncate by
	// path. Using the path is simplest and matches receive's behaviour.
	if rc.curFile != nil && rc.curPath == a.path {
		if err := rc.curFile.Truncate(int64(a.size)); err != nil {
			return fmt.Errorf("ftruncate %s -> %d: %w", p, a.size, err)
		}
		return nil
	}
	if err := unix.Truncate(p, int64(a.size)); err != nil {
		return fmt.Errorf("truncate %s -> %d: %w", p, a.size, err)
	}
	return nil
}

func (rc *receiver) doChmod(a *sendAttrs) error {
	p, err := rc.full(a.path)
	if err != nil {
		return err
	}
	if err := unix.Chmod(p, uint32(a.mode)&0o7777); err != nil {
		return fmt.Errorf("chmod %s %#o: %w", p, a.mode, err)
	}
	return nil
}

func (rc *receiver) doChown(a *sendAttrs) error {
	p, err := rc.full(a.path)
	if err != nil {
		return err
	}
	// Lchown so a symlink's own ownership is set rather than its target's.
	if err := unix.Lchown(p, int(a.uid), int(a.gid)); err != nil {
		return fmt.Errorf("lchown %s %d:%d: %w", p, a.uid, a.gid, err)
	}
	return nil
}

func (rc *receiver) doUtimes(a *sendAttrs) error {
	p, err := rc.full(a.path)
	if err != nil {
		return err
	}
	// btrfs receive sets atime and mtime (ctime cannot be set explicitly).
	ts := []unix.Timespec{
		unix.NsecToTimespec(time.Unix(int64(a.atime.Sec), int64(a.atime.Nsec)).UnixNano()),
		unix.NsecToTimespec(time.Unix(int64(a.mtime.Sec), int64(a.mtime.Nsec)).UnixNano()),
	}
	if err := unix.UtimesNanoAt(unix.AT_FDCWD, p, ts, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("utimensat %s: %w", p, err)
	}
	return nil
}

// doEnd finalises the current subvolume: drop the cached write fd, fsync the
// subvolume to push the data to disk, stamp the received UUID/ctransid (so
// `btrfs subvolume show` reports the Received UUID and the subvolume can serve
// as an incremental parent), then set it read-only — the same sequence
// `btrfs receive` performs.
func (rc *receiver) doEnd() error {
	if rc.subvolPath == "" {
		// END with no subvolume (e.g. an empty stream): nothing to finalise.
		return nil
	}
	rc.closeCurFile()

	// fsync the subvolume root to flush the replayed tree.
	if d, err := os.Open(rc.subvolPath); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}

	if !rc.opts.NoSetReceived {
		sf, err := os.Open(rc.subvolPath)
		if err != nil {
			return fmt.Errorf("END: open subvol %s: %w", rc.subvolPath, err)
		}
		times := SetReceivedTimes{
			Stime: ReceivedTimespec{Sec: rc.otime.Sec, Nsec: rc.otime.Nsec},
		}
		_, err = SetReceivedSubvol(int(sf.Fd()), rc.subvolUUID, rc.ctransid, times)
		sf.Close()
		if err != nil {
			return fmt.Errorf("END: %w", err)
		}
	}

	if !rc.opts.NoReadonly {
		if err := SetReadonly(rc.subvolPath, true); err != nil {
			return fmt.Errorf("END: set read-only: %w", err)
		}
	}

	rc.subvolPath = ""
	rc.subvolName = ""
	return nil
}

// closeCurFile drops the cached per-file write fd, if any.
func (rc *receiver) closeCurFile() {
	if rc.curFile != nil {
		_ = rc.curFile.Close()
		rc.curFile = nil
		rc.curPath = ""
	}
}
