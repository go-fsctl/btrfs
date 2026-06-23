// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

//go:build linux

package btrfs

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// These tests bring the filesystem-admin (btrfs_fsadmin_linux.go) and
// receive-apply (btrfs_receive_linux.go) kernel paths to full coverage without
// root or a real btrfs filesystem. The pure-ioctl operations drive the doIoctl
// seam exactly as the rest of coverage_linux_test.go does; the receive replay's
// ordinary-syscall handlers (mkdir/symlink/write/...) run against a real temp
// directory, which works as an unprivileged user; and the btrfs-specific parts
// of receive (subvolume create/snapshot/finalise/clone) drive the same seams.
// The root-only integration tests still exercise the genuine ioctls end to end.

// ---- btrfs_fsadmin_linux.go: label / resize / default-subvol / features ----

func TestGetLabel(t *testing.T) {
	defer snapshotSeams()()

	installIoctl(ioctlErrno(unix.EPERM))
	if _, err := GetLabel(3); err == nil {
		t.Fatal("want ioctl error")
	}
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		buf := unsafe.Slice((*byte)(arg), btrfsLabelSize)
		copy(buf, "mylabel\x00")
		return 0
	})
	got, err := GetLabel(3)
	if err != nil {
		t.Fatalf("GetLabel: %v", err)
	}
	if got != "mylabel" {
		t.Fatalf("label=%q", got)
	}
}

func TestSetLabel(t *testing.T) {
	defer snapshotSeams()()

	// A NUL in the label is rejected before any ioctl.
	if err := SetLabel(3, "bad\x00label"); err == nil {
		t.Fatal("want NUL-in-label error")
	}
	// A label too long for the fixed field is rejected by putName.
	long := make([]byte, btrfsLabelSize)
	for i := range long {
		long[i] = 'x'
	}
	if err := SetLabel(3, string(long)); err == nil {
		t.Fatal("want too-long error")
	}
	// ioctl failure propagates.
	installIoctl(ioctlErrno(unix.EPERM))
	if err := SetLabel(3, "ok"); err == nil {
		t.Fatal("want ioctl error")
	}
	// Success: the label bytes reach the buffer.
	var seen string
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		buf := unsafe.Slice((*byte)(arg), btrfsLabelSize)
		seen = cstr(buf)
		return 0
	})
	if err := SetLabel(3, "freshlabel"); err != nil {
		t.Fatalf("SetLabel: %v", err)
	}
	if seen != "freshlabel" {
		t.Fatalf("buffer label=%q", seen)
	}
}

func TestResize(t *testing.T) {
	defer snapshotSeams()()

	if err := Resize(3, ""); err == nil {
		t.Fatal("want empty-size error")
	}
	// A size string too long for the vol-args name field is rejected by putName.
	long := make([]byte, 4097)
	for i := range long {
		long[i] = '0'
	}
	if err := Resize(3, string(long)); err == nil {
		t.Fatal("want too-long error")
	}
	installIoctl(ioctlErrno(unix.EPERM))
	if err := Resize(3, "+1G"); err == nil {
		t.Fatal("want ioctl error")
	}
	var seen string
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		a := (*btrfsIoctlVolArgs)(arg)
		seen = cstr(a.Name[:])
		return 0
	})
	if err := Resize(3, "max"); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if seen != "max" {
		t.Fatalf("resize spec=%q", seen)
	}
}

func TestSetDefaultSubvol(t *testing.T) {
	defer snapshotSeams()()

	installIoctl(ioctlErrno(unix.EPERM))
	if err := SetDefaultSubvol(3, 257); err == nil {
		t.Fatal("want ioctl error")
	}
	var seen uint64
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		seen = *(*uint64)(arg)
		return 0
	})
	if err := SetDefaultSubvol(3, 257); err != nil {
		t.Fatalf("SetDefaultSubvol: %v", err)
	}
	if seen != 257 {
		t.Fatalf("subvolid=%d", seen)
	}
}

// makeDirItemBody builds a btrfs_dir_item search-result body whose location key
// objectid is subvolid and whose trailing name is name. The packed layout
// (matching GetDefaultSubvol's decoder) is: location.objectid at [0:8],
// name_len at [27:29], then the name from byte 30.
func makeDirItemBody(subvolid uint64, name string) []byte {
	b := make([]byte, btrfsDirItemHdrSize+len(name))
	binary.LittleEndian.PutUint64(b[0:8], subvolid)
	binary.LittleEndian.PutUint16(b[27:29], uint16(len(name)))
	copy(b[btrfsDirItemHdrSize:], name)
	return b
}

func TestGetDefaultSubvol(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)

	// ioctl error (non-ENOTTY) propagates through searchTreeTypeRange.
	installIoctl(ioctlErrno(unix.EPERM))
	if _, err := GetDefaultSubvol(3); err == nil {
		t.Fatal("want ioctl error")
	}

	// No matching "default" dir item: fall back to the FS tree id (5). The body
	// set covers every emit filter branch: wrong type, wrong objectid, a
	// too-short body, and a correctly-typed item whose name is not "default".
	round := 0
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		round++
		if round == 1 {
			writeV2Result(arg, []struct {
				hdr  btrfsIoctlSearchHeader
				body []byte
			}{
				{btrfsIoctlSearchHeader{Objectid: btrfsRootTreeDirObjectID, Type: btrfsRootItemKey}, makeDirItemBody(99, "default")},      // wrong type
				{btrfsIoctlSearchHeader{Objectid: 999, Type: btrfsDirItemKey}, makeDirItemBody(99, "default")},                            // wrong objectid
				{btrfsIoctlSearchHeader{Objectid: btrfsRootTreeDirObjectID, Type: btrfsDirItemKey}, []byte{1, 2, 3}},                      // too short
				{btrfsIoctlSearchHeader{Objectid: btrfsRootTreeDirObjectID, Type: btrfsDirItemKey}, makeDirItemBody(99, "notthedefault")}, // name mismatch
			})
			return 0
		}
		h := (*btrfsIoctlSearchArgsV2Hdr)(arg)
		h.Key.NrItems = 0
		return 0
	})
	id, err := GetDefaultSubvol(3)
	if err != nil {
		t.Fatalf("GetDefaultSubvol: %v", err)
	}
	if id != btrfsFSTreeObjectID {
		t.Fatalf("want FS-tree fallback %d, got %d", btrfsFSTreeObjectID, id)
	}

	// A "default" dir item whose declared name_len overruns the body (clamped),
	// pointing at subvolume 271.
	over := makeDirItemBody(271, "default")
	binary.LittleEndian.PutUint16(over[27:29], 100) // claims 100 bytes
	round = 0
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		round++
		if round == 1 {
			writeV2Result(arg, []struct {
				hdr  btrfsIoctlSearchHeader
				body []byte
			}{
				{btrfsIoctlSearchHeader{Objectid: btrfsRootTreeDirObjectID, Type: btrfsDirItemKey}, over},
			})
			return 0
		}
		h := (*btrfsIoctlSearchArgsV2Hdr)(arg)
		h.Key.NrItems = 0
		return 0
	})
	id, err = GetDefaultSubvol(3)
	if err != nil {
		t.Fatalf("GetDefaultSubvol: %v", err)
	}
	if id != 271 {
		t.Fatalf("want default subvol 271, got %d", id)
	}
}

func TestGetFeatures(t *testing.T) {
	defer snapshotSeams()()

	installIoctl(ioctlErrno(unix.EPERM))
	if _, err := GetFeatures(3); err == nil {
		t.Fatal("want ioctl error")
	}
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		ff := (*btrfsIoctlFeatureFlags)(arg)
		ff.CompatRO = FeatureCompatROFreeSpaceTree
		ff.Incompat = FeatureIncompatNoHoles
		return 0
	})
	f, err := GetFeatures(3)
	if err != nil {
		t.Fatalf("GetFeatures: %v", err)
	}
	if f.Incompat != FeatureIncompatNoHoles || f.CompatRO != FeatureCompatROFreeSpaceTree {
		t.Fatalf("features=%+v", f)
	}
}

func TestGetSupportedFeatures(t *testing.T) {
	defer snapshotSeams()()

	installIoctl(ioctlErrno(unix.EPERM))
	if _, err := GetSupportedFeatures(3); err == nil {
		t.Fatal("want ioctl error")
	}
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		arr := unsafe.Slice((*btrfsIoctlFeatureFlags)(arg), 3)
		arr[0] = btrfsIoctlFeatureFlags{Incompat: FeatureIncompatNoHoles}
		arr[1] = btrfsIoctlFeatureFlags{CompatRO: FeatureCompatROFreeSpaceTree}
		arr[2] = btrfsIoctlFeatureFlags{Incompat: FeatureIncompatRAID56}
		return 0
	})
	sf, err := GetSupportedFeatures(3)
	if err != nil {
		t.Fatalf("GetSupportedFeatures: %v", err)
	}
	if sf.Supported.Incompat != FeatureIncompatNoHoles ||
		sf.SafeSet.CompatRO != FeatureCompatROFreeSpaceTree ||
		sf.SafeClear.Incompat != FeatureIncompatRAID56 {
		t.Fatalf("supported=%+v", sf)
	}
}

func TestSetFeatures(t *testing.T) {
	defer snapshotSeams()()

	installIoctl(ioctlErrno(unix.EPERM))
	if err := SetFeatures(3, FeatureChange{SetIncompat: FeatureIncompatNoHoles}); err == nil {
		t.Fatal("want ioctl error")
	}
	var clear, set btrfsIoctlFeatureFlags
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		arr := unsafe.Slice((*btrfsIoctlFeatureFlags)(arg), 2)
		clear = arr[0]
		set = arr[1]
		return 0
	})
	change := FeatureChange{
		ClearCompat:   1,
		SetCompat:     2,
		ClearCompatRO: 3,
		SetCompatRO:   4,
		ClearIncompat: 5,
		SetIncompat:   6,
	}
	if err := SetFeatures(3, change); err != nil {
		t.Fatalf("SetFeatures: %v", err)
	}
	if clear.Compat != 1 || clear.CompatRO != 3 || clear.Incompat != 5 ||
		set.Compat != 2 || set.CompatRO != 4 || set.Incompat != 6 {
		t.Fatalf("clear=%+v set=%+v", clear, set)
	}
}

// writeDataContainer fills the btrfs_data_container the kernel would write at
// the pointer recorded in containerPtr (an args.Inodes / args.Fspath field),
// with the given elem_missed counters and val[] u64 payload.
func writeDataContainer(containerPtr uint64, elemCnt, elemMissed uint32, vals []uint64, extra []byte) {
	const bufSize = 64 << 10
	// containerPtr holds the address of the live btrfs_data_container buffer that
	// LogicalToIno/InoToPath allocated and stored via uint64(uintptr(unsafe.Pointer(...))).
	// Reinterpret those address bits back into a pointer. All six target arches
	// are 64-bit, so a uint64 holds a full pointer; this round-trips cleanly and
	// keeps `go vet`'s unsafeptr check satisfied (no direct uintptr->Pointer).
	cp := *(*unsafe.Pointer)(unsafe.Pointer(&containerPtr))
	buf := unsafe.Slice((*byte)(cp), bufSize)
	hdr := (*btrfsDataContainerHdr)(unsafe.Pointer(&buf[0]))
	hdr.ElemCnt = elemCnt
	hdr.ElemMissed = elemMissed
	val := buf[btrfsDataContainerHdrSize:]
	for i, v := range vals {
		binary.LittleEndian.PutUint64(val[i*8:], v)
	}
	if extra != nil {
		copy(val[len(vals)*8:], extra)
	}
}

func TestLogicalToIno(t *testing.T) {
	defer snapshotSeams()()

	installIoctl(ioctlErrno(unix.EPERM))
	if _, err := LogicalToIno(3, 0x1000); err == nil {
		t.Fatal("want ioctl error")
	}

	// Truncation (elem_missed != 0) surfaces as an error.
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		a := (*btrfsIoctlLogicalInoArgs)(arg)
		writeDataContainer(a.Inodes, 0, 2, nil, nil)
		return 0
	})
	if _, err := LogicalToIno(3, 0x1000); err == nil {
		t.Fatal("want truncation error")
	}

	// Two full triples plus a trailing partial triple that the loop stops short
	// of (elem_cnt = 7 but only two complete triples fit the loop guard).
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		a := (*btrfsIoctlLogicalInoArgs)(arg)
		writeDataContainer(a.Inodes, 7, 0, []uint64{
			100, 0, 5, // inode 100, offset 0, root 5
			101, 4096, 256, // inode 101, offset 4096, root 256
			102, // dangling, ignored
		}, nil)
		return 0
	})
	owners, err := LogicalToIno(3, 0x1000)
	if err != nil {
		t.Fatalf("LogicalToIno: %v", err)
	}
	if len(owners) != 2 ||
		owners[0] != (InodeOwner{Inode: 100, Offset: 0, Root: 5}) ||
		owners[1] != (InodeOwner{Inode: 101, Offset: 4096, Root: 256}) {
		t.Fatalf("owners=%+v", owners)
	}
}

func TestInoToPath(t *testing.T) {
	defer snapshotSeams()()

	installIoctl(ioctlErrno(unix.EPERM))
	if _, err := InoToPath(3, 257); err == nil {
		t.Fatal("want ioctl error")
	}

	// Truncation surfaces as an error.
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		a := (*btrfsIoctlInoPathArgs)(arg)
		writeDataContainer(a.Fspath, 0, 1, nil, nil)
		return 0
	})
	if _, err := InoToPath(3, 257); err == nil {
		t.Fatal("want truncation error")
	}

	// Two valid path offsets plus one whose offset is out of range (loop breaks).
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		a := (*btrfsIoctlInoPathArgs)(arg)
		// val[] holds 3 u64 offsets (24 bytes); strings live after them.
		strs := append([]byte("dir/a\x00"), []byte("dir/b\x00")...)
		writeDataContainer(a.Fspath, 3, 0, []uint64{24, 24 + 6, 1 << 30}, strs)
		return 0
	})
	paths, err := InoToPath(3, 257)
	if err != nil {
		t.Fatalf("InoToPath: %v", err)
	}
	if len(paths) != 2 || paths[0] != "dir/a" || paths[1] != "dir/b" {
		t.Fatalf("paths=%+v", paths)
	}
}

// ---- btrfs_receive_linux.go: send-stream replay ----

// tlv (a single send-stream TLV attribute: type, len, value) is defined in
// recv_attrs_test.go and reused here.

func tlvStr(typ uint16, s string) []byte { return tlv(typ, []byte(s)) }

func tlvU64(typ uint16, v uint64) []byte {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	return tlv(typ, b[:])
}

func tlvUUID(typ uint16, u [16]byte) []byte { return tlv(typ, u[:]) }

func tlvTime(typ uint16, sec uint64, nsec uint32) []byte {
	var b [12]byte
	binary.LittleEndian.PutUint64(b[0:8], sec)
	binary.LittleEndian.PutUint32(b[8:12], nsec)
	return tlv(typ, b[:])
}

// newReceiver makes a receiver whose subvolPath is a fresh temp directory, so
// the ordinary-syscall command handlers operate on a real (non-btrfs) tree as
// an unprivileged user.
func newReceiver(t *testing.T) *receiver {
	t.Helper()
	dir := t.TempDir()
	sub := filepath.Join(dir, "subvol")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	return &receiver{destPath: dir, subvolName: "subvol", subvolPath: sub}
}

// TestReceiverFullGuard checks that every filesystem command that resolves a
// path before SUBVOL/SNAPSHOT has set up a destination subvolume is rejected by
// the rc.full() guard (rc.subvolPath == "").
func TestReceiverFullGuard(t *testing.T) {
	rc := &receiver{destPath: t.TempDir()}
	if _, err := rc.full("x"); err == nil {
		t.Fatal("want 'before SUBVOL' error")
	}
	a := &sendAttrs{path: "p", pathTo: "pt", pathLink: "pl", xattrName: "user.x", clonePath: "cp"}
	guards := []struct {
		name string
		fn   func() error
	}{
		{"doMkdir", func() error { return rc.doMkdir(a) }},
		{"doMknod", func() error { return rc.doMknod(a, unix.S_IFREG) }},
		{"doMknodDev", func() error { return rc.doMknodDev(a) }},
		{"doSymlink", func() error { return rc.doSymlink(a) }},
		{"doRename", func() error { return rc.doRename(a) }},
		{"doLink", func() error { return rc.doLink(a) }},
		{"doUnlink", func() error { return rc.doUnlink(a) }},
		{"doRmdir", func() error { return rc.doRmdir(a) }},
		{"doSetXattr", func() error { return rc.doSetXattr(a) }},
		{"doRemoveXattr", func() error { return rc.doRemoveXattr(a) }},
		{"doWrite", func() error { return rc.doWrite(a) }},
		{"doClone", func() error { return rc.doClone(a) }},
		{"doTruncate", func() error { return rc.doTruncate(a) }},
		{"doChmod", func() error { return rc.doChmod(a) }},
		{"doChown", func() error { return rc.doChown(a) }},
		{"doUtimes", func() error { return rc.doUtimes(a) }},
	}
	for _, g := range guards {
		if err := g.fn(); err == nil {
			t.Errorf("%s: want guard error before SUBVOL", g.name)
		}
	}
}

// TestReceiverFileOps drives the ordinary-syscall handlers against a real tree.
func TestReceiverFileOps(t *testing.T) {
	rc := newReceiver(t)
	defer rc.closeCurFile()

	// mkdir
	if err := rc.doMkdir(&sendAttrs{path: "d"}); err != nil {
		t.Fatalf("doMkdir: %v", err)
	}
	if err := rc.doMkdir(&sendAttrs{path: "d"}); err == nil {
		t.Fatal("doMkdir over existing dir want error")
	}

	// mkfile (regular), fifo, socket via doMknod
	if err := rc.doMknod(&sendAttrs{path: "f"}, unix.S_IFREG); err != nil {
		t.Fatalf("doMknod reg: %v", err)
	}
	if err := rc.doMknod(&sendAttrs{path: "fifo"}, unix.S_IFIFO); err != nil {
		t.Fatalf("doMknod fifo: %v", err)
	}
	if err := rc.doMknod(&sendAttrs{path: "f"}, unix.S_IFREG); err == nil {
		t.Fatal("doMknod over existing want error")
	}

	// symlink
	if err := rc.doSymlink(&sendAttrs{path: "lnk", pathLink: "f"}); err != nil {
		t.Fatalf("doSymlink: %v", err)
	}
	if err := rc.doSymlink(&sendAttrs{path: "lnk", pathLink: "f"}); err == nil {
		t.Fatal("doSymlink over existing want error")
	}

	// write (opens and caches the fd), then a second write reuses the cache.
	if err := rc.doWrite(&sendAttrs{path: "f", fileOffset: 0, data: []byte("hello")}); err != nil {
		t.Fatalf("doWrite: %v", err)
	}
	if rc.curPath != "f" {
		t.Fatalf("write fd not cached: %q", rc.curPath)
	}
	if err := rc.doWrite(&sendAttrs{path: "f", fileOffset: 5, data: []byte(" world")}); err != nil {
		t.Fatalf("doWrite append: %v", err)
	}
	// open error on a nonexistent file
	if err := rc.doWrite(&sendAttrs{path: "missing", fileOffset: 0, data: []byte("x")}); err == nil {
		t.Fatal("doWrite missing file want error")
	}

	// truncate via the cached fd, then via path
	rc.curPath, rc.curFile = "", nil // force the path branch first
	if err := rc.doWrite(&sendAttrs{path: "f", fileOffset: 0, data: []byte("data")}); err != nil {
		t.Fatalf("reopen write: %v", err)
	}
	if err := rc.doTruncate(&sendAttrs{path: "f", size: 2}); err != nil { // cached-fd branch
		t.Fatalf("doTruncate fd: %v", err)
	}
	if err := rc.doTruncate(&sendAttrs{path: "fifo", size: 0}); err == nil {
		// truncating a fifo by path is allowed to fail; but a regular other file:
		_ = err
	}
	rc.closeCurFile()
	if err := rc.doTruncate(&sendAttrs{path: "f", size: 1}); err != nil { // path branch
		t.Fatalf("doTruncate path: %v", err)
	}
	if err := rc.doTruncate(&sendAttrs{path: "missing", size: 0}); err == nil {
		t.Fatal("doTruncate missing want error")
	}

	// chmod, chown (lchown to self uid is permitted), utimes
	if err := rc.doChmod(&sendAttrs{path: "f", mode: 0o644}); err != nil {
		t.Fatalf("doChmod: %v", err)
	}
	if err := rc.doChmod(&sendAttrs{path: "missing", mode: 0o644}); err == nil {
		t.Fatal("doChmod missing want error")
	}
	if err := rc.doChown(&sendAttrs{path: "f", uid: uint64(os.Getuid()), gid: uint64(os.Getgid())}); err != nil {
		t.Fatalf("doChown: %v", err)
	}
	if err := rc.doChown(&sendAttrs{path: "missing"}); err == nil {
		t.Fatal("doChown missing want error")
	}
	if err := rc.doUtimes(&sendAttrs{path: "f", atime: sendTimespec{Sec: 1}, mtime: sendTimespec{Sec: 2}}); err != nil {
		t.Fatalf("doUtimes: %v", err)
	}
	if err := rc.doUtimes(&sendAttrs{path: "missing"}); err == nil {
		t.Fatal("doUtimes missing want error")
	}

	// xattr set/remove. user.* xattrs are settable unprivileged on most fses;
	// tmpfs (t.TempDir under /tmp) supports them. If not supported we still
	// exercise the error branch.
	if err := rc.doSetXattr(&sendAttrs{path: "f", xattrName: "user.k", xattrData: []byte("v")}); err == nil {
		if err := rc.doRemoveXattr(&sendAttrs{path: "f", xattrName: "user.k"}); err != nil {
			t.Fatalf("doRemoveXattr: %v", err)
		}
	}
	if err := rc.doSetXattr(&sendAttrs{path: "missing", xattrName: "user.k", xattrData: []byte("v")}); err == nil {
		t.Fatal("doSetXattr missing want error")
	}
	if err := rc.doRemoveXattr(&sendAttrs{path: "missing", xattrName: "user.k"}); err == nil {
		t.Fatal("doRemoveXattr missing want error")
	}

	// link (hardlink) then unlink, and rmdir.
	if err := rc.doLink(&sendAttrs{path: "hl", pathLink: "f"}); err != nil {
		t.Fatalf("doLink: %v", err)
	}
	if err := rc.doLink(&sendAttrs{path: "hl", pathLink: "f"}); err == nil {
		t.Fatal("doLink over existing want error")
	}
	if err := rc.doUnlink(&sendAttrs{path: "hl"}); err != nil {
		t.Fatalf("doUnlink: %v", err)
	}
	if err := rc.doUnlink(&sendAttrs{path: "missing"}); err == nil {
		t.Fatal("doUnlink missing want error")
	}
	if err := rc.doRmdir(&sendAttrs{path: "d"}); err != nil {
		t.Fatalf("doRmdir: %v", err)
	}
	if err := rc.doRmdir(&sendAttrs{path: "missing"}); err == nil {
		t.Fatal("doRmdir missing want error")
	}
}

// TestReceiverRenameAndUnlinkDropCache checks that rename/unlink of the cached
// path drops the cached write fd.
func TestReceiverRenameAndUnlinkDropCache(t *testing.T) {
	rc := newReceiver(t)
	defer rc.closeCurFile()

	if err := rc.doMknod(&sendAttrs{path: "a"}, unix.S_IFREG); err != nil {
		t.Fatal(err)
	}
	// Cache a write fd on "a".
	if err := rc.doWrite(&sendAttrs{path: "a", data: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if rc.curPath != "a" {
		t.Fatal("expected cached fd on a")
	}
	// Rename a -> b drops the cache (curPath matches the source).
	if err := rc.doRename(&sendAttrs{path: "a", pathTo: "b"}); err != nil {
		t.Fatalf("doRename: %v", err)
	}
	if rc.curFile != nil {
		t.Fatal("rename should have dropped cached fd")
	}
	// Rename of a missing file errors.
	if err := rc.doRename(&sendAttrs{path: "missing", pathTo: "c"}); err == nil {
		t.Fatal("doRename missing want error")
	}
	// Cache on b again, then a rename whose *destination* matches the cache.
	if err := rc.doWrite(&sendAttrs{path: "b", data: []byte("y")}); err != nil {
		t.Fatal(err)
	}
	if err := rc.doMknod(&sendAttrs{path: "src"}, unix.S_IFREG); err != nil {
		t.Fatal(err)
	}
	if err := rc.doRename(&sendAttrs{path: "src", pathTo: "b"}); err != nil {
		t.Fatalf("doRename onto cached: %v", err)
	}
	if rc.curFile != nil {
		t.Fatal("rename onto cached path should have dropped cached fd")
	}
	// Unlink the cached path drops the cache too.
	if err := rc.doWrite(&sendAttrs{path: "b", data: []byte("z")}); err != nil {
		t.Fatal(err)
	}
	if err := rc.doUnlink(&sendAttrs{path: "b"}); err != nil {
		t.Fatalf("doUnlink cached: %v", err)
	}
	if rc.curFile != nil {
		t.Fatal("unlink of cached path should have dropped cached fd")
	}
}

// TestReceiverMknodDev drives doMknodDev through the unixMknod seam: the success
// path (which needs CAP_MKNOD, unreachable unprivileged) and the error path are
// both exercised deterministically without root.
func TestReceiverMknodDev(t *testing.T) {
	defer snapshotSeams()()
	rc := newReceiver(t)
	a := &sendAttrs{path: "dev", mode: unix.S_IFCHR | 0o600, rdev: unix.Mkdev(1, 3)}

	// Success via the seam.
	var gotPath string
	var gotMode uint32
	var gotDev int
	unixMknod = func(p string, mode uint32, dev int) error {
		gotPath, gotMode, gotDev = p, mode, dev
		return nil
	}
	if err := rc.doMknodDev(a); err != nil {
		t.Fatalf("doMknodDev: %v", err)
	}
	if filepath.Base(gotPath) != "dev" || gotMode != unix.S_IFCHR|0o600 || gotDev != int(unix.Mkdev(1, 3)) {
		t.Fatalf("mknod args path=%q mode=%#o dev=%d", gotPath, gotMode, gotDev)
	}

	// Error via the seam.
	unixMknod = func(string, uint32, int) error { return unix.EPERM }
	if err := rc.doMknodDev(a); err == nil {
		t.Fatal("doMknodDev want seam error")
	}

	// Guard branch (no subvol).
	bad := &receiver{}
	if err := bad.doMknodDev(&sendAttrs{path: "x"}); err == nil {
		t.Fatal("doMknodDev guard want error")
	}
}

// TestReceiverSubvolSnapshot drives the btrfs-ioctl-backed receive handlers via
// the seams: SUBVOL/SNAPSHOT create, findReceivedSubvol, receivedUUID, END.
func TestReceiverSubvolSnapshot(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(ioctlOK)

	dir := t.TempDir()
	rc := &receiver{destPath: dir}

	// SUBVOL with empty path is rejected.
	if err := rc.doSubvol(&sendAttrs{}); err == nil {
		t.Fatal("doSubvol empty path want error")
	}
	// SUBVOL success: SubvolCreate goes through the seams (ioctlOK).
	if err := rc.doSubvol(&sendAttrs{path: "sv", uuid: [16]byte{1}, ctransid: 7}); err != nil {
		t.Fatalf("doSubvol: %v", err)
	}
	if rc.subvolName != "sv" || rc.subvolUUID != [16]byte{1} || rc.ctransid != 7 {
		t.Fatalf("subvol state not recorded: %+v", rc)
	}
	// SubvolCreate failure path.
	installIoctl(ioctlErrno(unix.EPERM))
	if err := rc.doSubvol(&sendAttrs{path: "sv2"}); err == nil {
		t.Fatal("doSubvol create error want propagated")
	}
	installIoctl(ioctlOK)

	// SNAPSHOT requires a clone (parent) UUID.
	if err := rc.doSnapshot(&sendAttrs{path: "snap"}); err == nil {
		t.Fatal("doSnapshot needs clone UUID")
	}
	if err := rc.doSnapshot(&sendAttrs{}); err == nil {
		t.Fatal("doSnapshot empty path want error")
	}

	// SNAPSHOT with a parent that cannot be located (no subvolume reports the
	// received UUID) surfaces the locate error.
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		h := (*btrfsIoctlSearchArgsV2Hdr)(arg)
		h.Key.NrItems = 0 // ListSubvolumes finds nothing
		return 0
	})
	snap := &sendAttrs{path: "snap", present: map[uint16]bool{sendACloneUUID: true}, cloneUUID: [16]byte{7}}
	if err := rc.doSnapshot(snap); err == nil {
		t.Fatal("doSnapshot want parent-not-found error")
	}
}

// TestReceiverSnapshotSuccess drives doSnapshot's success path: the parent is
// found by received UUID via ListSubvolumes + GET_SUBVOL_INFO, then snapshotted.
func TestReceiverSnapshotSuccess(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)

	dir := t.TempDir()
	rc := &receiver{destPath: dir}
	parentUUID := [16]byte{0x55}

	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		switch req {
		case BTRFS_IOC_GET_SUBVOL_INFO:
			a := (*btrfsIoctlGetSubvolInfoArgs)(arg)
			a.ReceivedUUID = parentUUID
			return 0
		case BTRFS_IOC_SNAP_CREATE_V2:
			return 0
		default:
			h := (*btrfsIoctlSearchArgsV2Hdr)(arg)
			if h.Key.NrItems == 0 {
				return 0
			}
			if h.Key.MinObjectid == 0 && h.Key.MinOffset == 0 {
				writeV2Result(arg, []struct {
					hdr  btrfsIoctlSearchHeader
					body []byte
				}{
					{btrfsIoctlSearchHeader{Objectid: 5, Offset: 256, Type: btrfsRootRefKey}, makeRootRefBody("parent")},
				})
				return 0
			}
			h.Key.NrItems = 0
			return 0
		}
	})

	snap := &sendAttrs{path: "snap", uuid: [16]byte{1}, ctransid: 3, present: map[uint16]bool{sendACloneUUID: true}, cloneUUID: parentUUID}
	if err := rc.doSnapshot(snap); err != nil {
		t.Fatalf("doSnapshot: %v", err)
	}
	if rc.subvolName != "snap" {
		t.Fatalf("snapshot state not recorded: %+v", rc)
	}

	// Same parent located, but SNAP_CREATE_V2 fails: the create error propagates.
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		switch req {
		case BTRFS_IOC_GET_SUBVOL_INFO:
			a := (*btrfsIoctlGetSubvolInfoArgs)(arg)
			a.ReceivedUUID = parentUUID
			return 0
		case BTRFS_IOC_SNAP_CREATE_V2:
			return unix.EPERM
		default:
			h := (*btrfsIoctlSearchArgsV2Hdr)(arg)
			if h.Key.NrItems == 0 {
				return 0
			}
			if h.Key.MinObjectid == 0 && h.Key.MinOffset == 0 {
				writeV2Result(arg, []struct {
					hdr  btrfsIoctlSearchHeader
					body []byte
				}{
					{btrfsIoctlSearchHeader{Objectid: 5, Offset: 256, Type: btrfsRootRefKey}, makeRootRefBody("parent")},
				})
				return 0
			}
			h.Key.NrItems = 0
			return 0
		}
	})
	if err := rc.doSnapshot(snap); err == nil {
		t.Fatal("doSnapshot want SnapshotCreate error")
	}
}

// TestReceiverWriteTruncateErrors covers the WriteAt and ftruncate error
// branches by pointing the cached write fd at a read-only file so the kernel
// rejects the modification.
func TestReceiverWriteTruncateErrors(t *testing.T) {
	rc := newReceiver(t)
	defer rc.closeCurFile()

	// Create a file, then cache a read-only fd on it under rc.curPath = "f".
	p := filepath.Join(rc.subvolPath, "f")
	if err := os.WriteFile(p, []byte("abc"), 0644); err != nil {
		t.Fatal(err)
	}
	ro, err := os.OpenFile(p, os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	rc.curFile = ro
	rc.curPath = "f"

	// doWrite reuses the cached (read-only) fd; WriteAt fails with EBADF.
	if err := rc.doWrite(&sendAttrs{path: "f", data: []byte("x")}); err == nil {
		t.Fatal("doWrite on read-only fd want error")
	}
	// doTruncate via the cached (read-only) fd fails too.
	if err := rc.doTruncate(&sendAttrs{path: "f", size: 1}); err == nil {
		t.Fatal("doTruncate on read-only fd want error")
	}
}

// TestReceiverWriteCacheReuse covers writeFile's cached-fd fast path and
// doTruncate's cached-fd branch on a real temp tree.
func TestReceiverWriteCacheReuse(t *testing.T) {
	rc := newReceiver(t)
	defer rc.closeCurFile()

	if err := rc.doMknod(&sendAttrs{path: "f"}, unix.S_IFREG); err != nil {
		t.Fatal(err)
	}
	// First write opens and caches the fd.
	if err := rc.doWrite(&sendAttrs{path: "f", data: []byte("abcdef")}); err != nil {
		t.Fatal(err)
	}
	cached := rc.curFile
	// A second write to the SAME path reuses the cached fd (writeFile fast path).
	if err := rc.doWrite(&sendAttrs{path: "f", fileOffset: 6, data: []byte("ghi")}); err != nil {
		t.Fatal(err)
	}
	if rc.curFile != cached {
		t.Fatal("writeFile should have reused the cached fd")
	}
	// Truncate via the cached fd (curFile != nil && curPath == path).
	if err := rc.doTruncate(&sendAttrs{path: "f", size: 3}); err != nil {
		t.Fatalf("doTruncate cached: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(rc.subvolPath, "f"))
	if err != nil || len(b) != 3 {
		t.Fatalf("truncate result b=%q err=%v", b, err)
	}
}

// TestReceiveTruncatedStream covers Receive's command-read error path: a stream
// whose final record header is truncated mid-stream.
func TestReceiveTruncatedStream(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(ioctlOK)
	dst := t.TempDir()
	if err := os.Mkdir(filepath.Join(dst, "sv"), 0755); err != nil {
		t.Fatal(err)
	}

	full := buildStream(btrfsSendStreamVersion, []Command{
		cmd(btrfsSendCmdSubvol, tlvStr(sendAPath, "sv"), tlvUUID(sendAUUID, [16]byte{1}), tlvU64(sendACtransid, 1)),
	})
	// Drop the trailing END record's last few bytes so CommandReader.Next fails
	// on a short read rather than reaching a clean EOF.
	truncated := full[:len(full)-3]
	if err := Receive(dst, bytes.NewReader(truncated), ReceiveOpts{}); err == nil {
		t.Fatal("want command-read error on truncated stream")
	}
}

// TestReceiveNoEnd covers Receive's clean-EOF break: a stream with a SUBVOL but
// no END record terminates the loop at io.EOF.
func TestReceiveNoEnd(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(ioctlOK)
	dst := t.TempDir()
	if err := os.Mkdir(filepath.Join(dst, "sv"), 0755); err != nil {
		t.Fatal(err)
	}

	// Header + a single complete SUBVOL record, with no END appended.
	var b bytes.Buffer
	magic := make([]byte, btrfsSendStreamMagicSize)
	copy(magic, btrfsSendStreamMagic)
	b.Write(magic)
	var v [4]byte
	binary.LittleEndian.PutUint32(v[:], btrfsSendStreamVersion)
	b.Write(v[:])
	rec := cmd(btrfsSendCmdSubvol, tlvStr(sendAPath, "sv"), tlvUUID(sendAUUID, [16]byte{1}), tlvU64(sendACtransid, 1))
	var hdr [btrfsCmdHeaderSize]byte
	binary.LittleEndian.PutUint32(hdr[0:4], uint32(len(rec.Data)))
	binary.LittleEndian.PutUint16(hdr[4:6], rec.Cmd)
	b.Write(hdr[:])
	b.Write(rec.Data)

	if err := Receive(dst, bytes.NewReader(b.Bytes()), ReceiveOpts{}); err != nil {
		t.Fatalf("Receive no-END: %v", err)
	}
}

// TestDataContainerOverflow covers the bounds-break in LogicalToIno/InoToPath
// when the kernel reports more elements than the fixed buffer can hold.
func TestDataContainerOverflow(t *testing.T) {
	defer snapshotSeams()()

	// LogicalToIno: elem_cnt huge so a triple's base offset exceeds val[].
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		a := (*btrfsIoctlLogicalInoArgs)(arg)
		writeDataContainer(a.Inodes, 1<<20, 0, []uint64{1, 2, 3}, nil)
		return 0
	})
	if _, err := LogicalToIno(3, 0x1000); err != nil {
		t.Fatalf("LogicalToIno overflow: %v", err)
	}

	// InoToPath: elem_cnt huge so an offset index runs past val[].
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		a := (*btrfsIoctlInoPathArgs)(arg)
		writeDataContainer(a.Fspath, 1<<20, 0, nil, nil)
		return 0
	})
	if _, err := InoToPath(3, 257); err != nil {
		t.Fatalf("InoToPath overflow: %v", err)
	}
}

// TestApplySnapshotDispatch drives apply's SNAPSHOT dispatch arm through a
// synthesised command record (the success of the snapshot itself is covered by
// TestReceiverSnapshotSuccess; here we only need apply to reach doSnapshot).
func TestApplySnapshotDispatch(t *testing.T) {
	rc := &receiver{destPath: t.TempDir()}
	// No clone UUID -> doSnapshot errors, but apply has dispatched to it.
	if err := rc.apply(cmd(btrfsSendCmdSnapshot, tlvStr(sendAPath, "snap"))); err == nil {
		t.Fatal("apply SNAPSHOT want doSnapshot error")
	}
}

// TestReceivedUUIDAndFind drives receivedUUID + findReceivedSubvol through the
// ListSubvolumes + GET_SUBVOL_INFO seams.
func TestReceivedUUIDAndFind(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)

	dir := t.TempDir()
	rc := &receiver{destPath: dir}
	target := [16]byte{0xde, 0xad}

	// ListSubvolumes returns one subvolume "child"; its GET_SUBVOL_INFO reports
	// the received UUID we want; findReceivedSubvol must locate it.
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		switch req {
		case BTRFS_IOC_GET_SUBVOL_INFO:
			a := (*btrfsIoctlGetSubvolInfoArgs)(arg)
			a.ReceivedUUID = target
			return 0
		default:
			// TREE_SEARCH (v2/v1) for ListSubvolumes: emit one ROOT_REF then drain.
			h := (*btrfsIoctlSearchArgsV2Hdr)(arg)
			if h.Key.NrItems == 0 {
				return 0
			}
			if h.Key.MinObjectid == 0 && h.Key.MinOffset == 0 {
				writeV2Result(arg, []struct {
					hdr  btrfsIoctlSearchHeader
					body []byte
				}{
					{btrfsIoctlSearchHeader{Objectid: 5, Offset: 256, Type: btrfsRootRefKey}, makeRootRefBody("child")},
				})
				return 0
			}
			h.Key.NrItems = 0
			return 0
		}
	})

	got, err := rc.findReceivedSubvol(target)
	if err != nil {
		t.Fatalf("findReceivedSubvol: %v", err)
	}
	if filepath.Base(got) != "child" {
		t.Fatalf("found %q, want .../child", got)
	}

	// No match: findReceivedSubvol errors.
	if _, err := rc.findReceivedSubvol([16]byte{0xff}); err == nil {
		t.Fatal("findReceivedSubvol no-match want error")
	}

	// ListSubvolumes failure propagates.
	installIoctl(ioctlErrno(unix.EPERM))
	if _, err := rc.findReceivedSubvol(target); err == nil {
		t.Fatal("findReceivedSubvol list error want propagated")
	}

	// receivedUUID returns the zero UUID when the ioctl fails.
	if u := receivedUUID(filepath.Join(dir, "x")); u != ([16]byte{}) {
		t.Fatalf("receivedUUID on error want zero, got %x", u)
	}
}

// TestReceiverEnd drives doEnd's three finalisation modes via the seams.
func TestReceiverEnd(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(ioctlOK)

	// END with no subvolume set up is a no-op.
	rc0 := &receiver{}
	if err := rc0.doEnd(); err != nil {
		t.Fatalf("doEnd no subvol: %v", err)
	}

	// Full finalisation: SET_RECEIVED_SUBVOL + set read-only, both via seams.
	rc := newReceiver(t)
	if err := rc.doEnd(); err != nil {
		t.Fatalf("doEnd: %v", err)
	}
	if rc.subvolPath != "" {
		t.Fatal("doEnd should clear subvolPath")
	}

	// NoSetReceived + NoReadonly skips both ioctls.
	rc = newReceiver(t)
	rc.opts = ReceiveOpts{NoSetReceived: true, NoReadonly: true}
	if err := rc.doEnd(); err != nil {
		t.Fatalf("doEnd skip-all: %v", err)
	}

	// SET_RECEIVED_SUBVOL ioctl failure propagates.
	rc = newReceiver(t)
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		if req == BTRFS_IOC_SET_RECEIVED_SUBVOL {
			return unix.EPERM
		}
		return 0
	})
	if err := rc.doEnd(); err == nil {
		t.Fatal("doEnd set-received error want propagated")
	}

	// SetReadonly ioctl failure propagates (set-received off so we reach it).
	rc = newReceiver(t)
	rc.opts = ReceiveOpts{NoSetReceived: true}
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		if req == BTRFS_IOC_SUBVOL_SETFLAGS {
			return unix.EPERM
		}
		return 0
	})
	if err := rc.doEnd(); err == nil {
		t.Fatal("doEnd set-readonly error want propagated")
	}

	// doEnd open-subvol error: the SET_RECEIVED_SUBVOL step opens rc.subvolPath
	// with the real os.Open; point it at a path that does not exist so the open
	// (and thus doEnd) fails.
	installIoctl(ioctlOK)
	rc = &receiver{destPath: t.TempDir(), subvolName: "gone", subvolPath: filepath.Join(t.TempDir(), "does-not-exist")}
	if err := rc.doEnd(); err == nil {
		t.Fatal("doEnd open-subvol error want propagated")
	}
}

// TestReceiverClone drives doClone via the seams + a real source/dest file.
func TestReceiverClone(t *testing.T) {
	defer snapshotSeams()()

	rc := newReceiver(t)
	defer rc.closeCurFile()

	// Create the destination (write target) and a same-subvol clone source.
	if err := rc.doMknod(&sendAttrs{path: "dst"}, unix.S_IFREG); err != nil {
		t.Fatal(err)
	}
	srcFull := filepath.Join(rc.subvolPath, "src")
	if err := os.WriteFile(srcFull, []byte("0123456789"), 0644); err != nil {
		t.Fatal(err)
	}

	// FICLONERANGE ok (seam) -> clone within the same subvolume.
	installIoctl(ioctlOK)
	a := &sendAttrs{path: "dst", clonePath: "src", cloneLen: 4, cloneOffset: 0, fileOffset: 0}
	if err := rc.doClone(a); err != nil {
		t.Fatalf("doClone: %v", err)
	}

	// FICLONERANGE failure propagates.
	installIoctl(ioctlErrno(unix.EOPNOTSUPP))
	if err := rc.doClone(a); err == nil {
		t.Fatal("doClone ioctl error want propagated")
	}

	// Clone source open error (nonexistent clonePath).
	installIoctl(ioctlOK)
	if err := rc.doClone(&sendAttrs{path: "dst", clonePath: "nope"}); err == nil {
		t.Fatal("doClone missing source want error")
	}

	// writeFile (dst) open error: a clone whose PATH cannot be opened.
	rc.closeCurFile()
	if err := rc.doClone(&sendAttrs{path: "absent-dst", clonePath: "src"}); err == nil {
		t.Fatal("doClone missing dst want error")
	}
}

// TestReceiverCloneCrossSubvol drives the cross-subvolume clone branch where the
// source is located by received UUID via findReceivedSubvol.
func TestReceiverCloneCrossSubvol(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)

	dir := t.TempDir()
	// Build the destination subvolume tree with a dst file.
	sub := filepath.Join(dir, "subvol")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "dst"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	// Build the "other" subvolume that ListSubvolumes will report as "child",
	// containing the clone source.
	other := filepath.Join(dir, "child")
	if err := os.MkdirAll(other, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "src"), []byte("abcdef"), 0644); err != nil {
		t.Fatal(err)
	}

	rc := &receiver{destPath: dir, subvolName: "subvol", subvolPath: sub, subvolUUID: [16]byte{1}}
	defer rc.closeCurFile()

	srcUUID := [16]byte{0xab}
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		switch req {
		case FICLONERANGE:
			return 0
		case BTRFS_IOC_GET_SUBVOL_INFO:
			a := (*btrfsIoctlGetSubvolInfoArgs)(arg)
			a.ReceivedUUID = srcUUID
			return 0
		default:
			h := (*btrfsIoctlSearchArgsV2Hdr)(arg)
			if h.Key.NrItems == 0 {
				return 0
			}
			if h.Key.MinObjectid == 0 && h.Key.MinOffset == 0 {
				writeV2Result(arg, []struct {
					hdr  btrfsIoctlSearchHeader
					body []byte
				}{
					{btrfsIoctlSearchHeader{Objectid: 5, Offset: 256, Type: btrfsRootRefKey}, makeRootRefBody("child")},
				})
				return 0
			}
			h.Key.NrItems = 0
			return 0
		}
	})

	a := &sendAttrs{
		path: "dst", clonePath: "src", cloneLen: 4,
		present:   map[uint16]bool{sendACloneUUID: true},
		cloneUUID: srcUUID,
	}
	if err := rc.doClone(a); err != nil {
		t.Fatalf("doClone cross-subvol: %v", err)
	}

	// Cross-subvol source not found -> error.
	bad := &sendAttrs{
		path: "dst", clonePath: "src",
		present:   map[uint16]bool{sendACloneUUID: true},
		cloneUUID: [16]byte{0x99},
	}
	if err := rc.doClone(bad); err == nil {
		t.Fatal("doClone unknown source subvol want error")
	}
}

// ---- recv_attrs.go: decodeAttrs branches not reached off-root ----

// TestDecodeAttrsAllBranches feeds a record carrying every attribute kind so
// decodeAttrs covers the path-link, clone-uuid, ino and gid assignment lines
// that only the root integration replay otherwise reaches.
func TestDecodeAttrsAllBranches(t *testing.T) {
	var payload []byte
	payload = append(payload, tlvStr(sendAPath, "p")...)
	payload = append(payload, tlvStr(sendAPathTo, "pt")...)
	payload = append(payload, tlvStr(sendAPathLink, "pl")...)
	payload = append(payload, tlvStr(sendAClonePath, "cp")...)
	payload = append(payload, tlvStr(sendAXattrName, "user.x")...)
	payload = append(payload, tlv(sendAXattrData, []byte("xd"))...)
	payload = append(payload, tlv(sendAData, []byte("dd"))...)
	payload = append(payload, tlvUUID(sendAUUID, [16]byte{1})...)
	payload = append(payload, tlvUUID(sendACloneUUID, [16]byte{2})...)
	payload = append(payload, tlvU64(sendAIno, 11)...)
	payload = append(payload, tlvU64(sendASize, 12)...)
	payload = append(payload, tlvU64(sendAMode, 0o644)...)
	payload = append(payload, tlvU64(sendAUID, 14)...)
	payload = append(payload, tlvU64(sendAGID, 15)...)
	payload = append(payload, tlvU64(sendARdev, 16)...)
	payload = append(payload, tlvU64(sendAFileOffset, 17)...)
	payload = append(payload, tlvU64(sendACtransid, 18)...)
	payload = append(payload, tlvU64(sendACloneCtransid, 19)...)
	payload = append(payload, tlvU64(sendACloneOffset, 20)...)
	payload = append(payload, tlvU64(sendACloneLen, 21)...)
	payload = append(payload, tlvTime(sendAAtime, 100, 1)...)
	payload = append(payload, tlvTime(sendAMtime, 200, 2)...)
	payload = append(payload, tlvTime(sendACtime, 300, 3)...)
	payload = append(payload, tlvTime(sendAOtime, 400, 4)...)
	// An unknown attribute type hits the default (skip) branch.
	payload = append(payload, tlv(0xFFFF, []byte("ignored"))...)

	a, err := decodeAttrs(payload)
	if err != nil {
		t.Fatalf("decodeAttrs: %v", err)
	}
	if a.path != "p" || a.pathTo != "pt" || a.pathLink != "pl" || a.clonePath != "cp" {
		t.Fatalf("paths wrong: %+v", a)
	}
	if a.xattrName != "user.x" || !bytes.Equal(a.xattrData, []byte("xd")) || !bytes.Equal(a.data, []byte("dd")) {
		t.Fatalf("byte attrs wrong: %+v", a)
	}
	if a.uuid != ([16]byte{1}) || a.cloneUUID != ([16]byte{2}) {
		t.Fatalf("uuids wrong: %+v", a)
	}
	if a.ino != 11 || a.size != 12 || a.mode != 0o644 || a.uid != 14 || a.gid != 15 ||
		a.rdev != 16 || a.fileOffset != 17 || a.ctransid != 18 || a.cloneCtransid != 19 ||
		a.cloneOffset != 20 || a.cloneLen != 21 {
		t.Fatalf("u64 attrs wrong: %+v", a)
	}
	if a.atime != (sendTimespec{Sec: 100, Nsec: 1}) || a.otime != (sendTimespec{Sec: 400, Nsec: 4}) {
		t.Fatalf("timespecs wrong: %+v", a)
	}

	// A bad CLONE_UUID length surfaces the wrapped error.
	if _, err := decodeAttrs(tlv(sendACloneUUID, []byte{1, 2, 3})); err == nil {
		t.Fatal("bad CLONE_UUID len want error")
	}
}

// ---- Receive: end-to-end stream replay through the seams ----

// cmd builds a Command record carrying the concatenated TLV attrs.
func cmd(c uint16, attrs ...[]byte) Command {
	var data []byte
	for _, a := range attrs {
		data = append(data, a...)
	}
	return Command{Cmd: c, Data: data}
}

// TestReceiveEndToEnd runs the full Receive driver over a synthesised v1 stream:
// a SUBVOL create followed by one of every ordinary-syscall command and the
// no-op extent commands, then END. SubvolCreate and the END ioctls are faked
// through the seams; the filesystem commands apply for real under a temp dir
// (pre-created to stand in for the subvolume the faked SUBVOL_CREATE "made").
func TestReceiveEndToEnd(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(ioctlOK)

	dst := t.TempDir()
	// The faked BTRFS_IOC_SUBVOL_CREATE does not create a directory, so make the
	// subvolume root the replay will write under.
	subRoot := filepath.Join(dst, "sv")
	if err := os.Mkdir(subRoot, 0755); err != nil {
		t.Fatal(err)
	}
	// A clone source file inside the subvolume for the CLONE command.
	if err := os.WriteFile(filepath.Join(subRoot, "src"), []byte("0123456789"), 0644); err != nil {
		t.Fatal(err)
	}

	uid := uint64(os.Getuid())
	gid := uint64(os.Getgid())
	stream := buildStream(btrfsSendStreamVersion, []Command{
		cmd(btrfsSendCmdSubvol, tlvStr(sendAPath, "sv"), tlvUUID(sendAUUID, [16]byte{9}), tlvU64(sendACtransid, 5)),
		cmd(sendCmdMkdir, tlvStr(sendAPath, "d")),
		cmd(sendCmdMkfile, tlvStr(sendAPath, "f")),
		cmd(sendCmdMkfifo, tlvStr(sendAPath, "p")),
		cmd(sendCmdMksock, tlvStr(sendAPath, "s")),
		cmd(sendCmdSymlink, tlvStr(sendAPath, "lnk"), tlvStr(sendAPathLink, "f")),
		cmd(sendCmdWrite, tlvStr(sendAPath, "f"), tlvU64(sendAFileOffset, 0), tlv(sendAData, []byte("hello"))),
		cmd(sendCmdClone, tlvStr(sendAPath, "f"), tlvU64(sendAFileOffset, 5),
			tlvStr(sendAClonePath, "src"), tlvU64(sendACloneOffset, 0), tlvU64(sendACloneLen, 4)),
		cmd(sendCmdTruncate, tlvStr(sendAPath, "f"), tlvU64(sendASize, 5)),
		cmd(sendCmdLink, tlvStr(sendAPath, "hl"), tlvStr(sendAPathLink, "f")),
		cmd(sendCmdRename, tlvStr(sendAPath, "hl"), tlvStr(sendAPathTo, "hl2")),
		cmd(sendCmdUnlink, tlvStr(sendAPath, "hl2")),
		cmd(sendCmdSetXattr, tlvStr(sendAPath, "f"), tlvStr(sendAXattrName, "user.k"), tlv(sendAXattrData, []byte("v"))),
		cmd(sendCmdRemoveXattr, tlvStr(sendAPath, "f"), tlvStr(sendAXattrName, "user.k")),
		cmd(sendCmdChmod, tlvStr(sendAPath, "f"), tlvU64(sendAMode, 0o644)),
		cmd(sendCmdChown, tlvStr(sendAPath, "f"), tlvU64(sendAUID, uid), tlvU64(sendAGID, gid)),
		cmd(sendCmdUtimes, tlvStr(sendAPath, "f"), tlvTime(sendAAtime, 1, 0), tlvTime(sendAMtime, 2, 0)),
		cmd(sendCmdUpdateExtent, tlvStr(sendAPath, "f"), tlvU64(sendAFileOffset, 0), tlvU64(sendASize, 5)),
		cmd(sendCmdFallocate, tlvStr(sendAPath, "f")),
		cmd(sendCmdRmdir, tlvStr(sendAPath, "d")),
		// A character-device node is last: it needs root, so off-root the stream
		// errors here after every other command has applied.
		cmd(sendCmdMknod, tlvStr(sendAPath, "dev"), tlvU64(sendAMode, unix.S_IFCHR|0o600), tlvU64(sendARdev, uint64(unix.Mkdev(1, 3)))),
	})

	// doMknodDev (MKNOD) needs root; off-root the stream errors at that command.
	// Run the whole stream and accept either outcome, then verify the parts that
	// must have applied.
	err := Receive(dst, bytes.NewReader(stream), ReceiveOpts{})
	if os.Getuid() == 0 {
		if err != nil {
			t.Fatalf("Receive as root: %v", err)
		}
	} else {
		// Non-root: the MKNOD device command fails; everything before it applied.
		if err == nil {
			t.Fatal("expected MKNOD device failure off-root")
		}
	}
	// Regardless, the symlink and file created before MKNOD must exist.
	if _, err := os.Lstat(filepath.Join(subRoot, "lnk")); err != nil {
		t.Fatalf("symlink not created: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(subRoot, "f")); err != nil || len(b) != 5 {
		t.Fatalf("file f wrong: b=%q err=%v", b, err)
	}
}

// TestReceiveStreamErrors covers Receive's header/version/command error paths.
func TestReceiveStreamErrors(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(ioctlOK)
	dst := t.TempDir()

	// Bad header (truncated) -> ParseHeader error.
	if err := Receive(dst, bytes.NewReader([]byte("btrfs")), ReceiveOpts{}); err == nil {
		t.Fatal("want header error")
	}

	// Wrong stream version.
	if err := Receive(dst, bytes.NewReader(buildStream(99, nil)), ReceiveOpts{}); err == nil {
		t.Fatal("want version error")
	}

	// An empty stream (header + END only): END with no subvol is a no-op.
	if err := Receive(dst, bytes.NewReader(buildStream(btrfsSendStreamVersion, nil)), ReceiveOpts{}); err != nil {
		t.Fatalf("empty stream: %v", err)
	}

	// A filesystem command before SUBVOL -> apply returns the full() guard error,
	// wrapped by Receive.
	pre := buildStream(btrfsSendStreamVersion, []Command{cmd(sendCmdMkdir, tlvStr(sendAPath, "d"))})
	if err := Receive(dst, bytes.NewReader(pre), ReceiveOpts{}); err == nil {
		t.Fatal("want pre-SUBVOL command error")
	}

	// An unsupported (v2 encoded) command -> ErrUnsupportedCommand.
	enc := buildStream(btrfsSendStreamVersion, []Command{cmd(sendCmdEncodedWrite, tlvStr(sendAPath, "f"))})
	if err := Receive(dst, bytes.NewReader(enc), ReceiveOpts{}); err == nil {
		t.Fatal("want unsupported-command error")
	}

	// An unknown command number -> ErrUnsupportedCommand (default arm).
	unk := buildStream(btrfsSendStreamVersion, []Command{cmd(250, tlvStr(sendAPath, "f"))})
	if err := Receive(dst, bytes.NewReader(unk), ReceiveOpts{}); err == nil {
		t.Fatal("want unknown-command error")
	}

	// A command whose attribute payload is malformed -> decodeAttrs error via apply.
	bad := buildStream(btrfsSendStreamVersion, []Command{{Cmd: sendCmdMkdir, Data: []byte{0x01}}})
	if err := Receive(dst, bytes.NewReader(bad), ReceiveOpts{}); err == nil {
		t.Fatal("want malformed-attr error")
	}
}

// TestApplyTrace exercises the BTRFS_RECV_TRACE diagnostic branch in apply.
func TestApplyTrace(t *testing.T) {
	t.Setenv("BTRFS_RECV_TRACE", "1")
	rc := newReceiver(t)
	// MKDIR applies for real under the temp subvol; the trace line is printed.
	if err := rc.apply(cmd(sendCmdMkdir, tlvStr(sendAPath, "traced"))); err != nil {
		t.Fatalf("apply mkdir: %v", err)
	}
}
