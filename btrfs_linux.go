// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

//go:build linux

// Package btrfs drives btrfs kernel operations directly via BTRFS_IOC_*
// ioctls on directory file descriptors. It is pure Go: no cgo, and it never
// shells out to the btrfs CLI. It is the btrfs sibling of github.com/go-fsctl/zfs
// in the OpenZFS-style go-fsctl family.
package btrfs

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ioctlDir issues a btrfs ioctl against the directory at path. btrfs control
// ioctls are addressed to a directory fd (the containing directory for
// create/destroy, or the subvolume root itself for flag/info ops), never to a
// global control device.
func ioctlDir(path string, req uintptr, arg unsafe.Pointer) error {
	f, err := osOpen(path)
	if err != nil {
		return fmt.Errorf("open %q: %w", path, err)
	}
	defer f.Close()
	return ioctlFd(f.Fd(), req, arg)
}

// ioctlFd issues a single ioctl on an already-open fd.
func ioctlFd(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	if errno := doIoctl(fd, req, arg); errno != 0 {
		return errno
	}
	return nil
}

// putName copies a Go string into a fixed-size NUL-terminated byte field,
// returning an error if it does not fit (leaving room for the terminator).
func putName(dst []byte, name string) error {
	if len(name)+1 > len(dst) {
		return fmt.Errorf("name %q too long: %d bytes (max %d)", name, len(name), len(dst)-1)
	}
	copy(dst, name)
	dst[len(name)] = 0
	return nil
}

// cstr converts a NUL-terminated C string in a byte slice to a Go string.
func cstr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// SubvolCreate creates a new subvolume named name inside the directory
// parentDir, via BTRFS_IOC_SUBVOL_CREATE (btrfs_ioctl_vol_args). parentDir
// must itself be on a btrfs filesystem.
func SubvolCreate(parentDir, name string) error {
	if name == "" {
		return fmt.Errorf("SubvolCreate: empty name")
	}
	var args btrfsIoctlVolArgs
	if err := putName(args.Name[:], name); err != nil {
		return fmt.Errorf("SubvolCreate: %w", err)
	}
	err := ioctlDir(parentDir, BTRFS_IOC_SUBVOL_CREATE, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err != nil {
		return fmt.Errorf("BTRFS_IOC_SUBVOL_CREATE %s/%s: %w", parentDir, name, err)
	}
	return nil
}

// SnapshotCreate creates a snapshot named name inside destParentDir from the
// subvolume rooted at srcSubvolPath, via BTRFS_IOC_SNAP_CREATE_V2. When
// readonly is true the snapshot is created with BTRFS_SUBVOL_RDONLY.
//
// The V2 arg carries the source subvolume as an open fd (args.Fd); the ioctl
// itself is issued on destParentDir.
func SnapshotCreate(srcSubvolPath, destParentDir, name string, readonly bool) error {
	if name == "" {
		return fmt.Errorf("SnapshotCreate: empty name")
	}
	src, err := osOpen(srcSubvolPath)
	if err != nil {
		return fmt.Errorf("SnapshotCreate: open src %q: %w", srcSubvolPath, err)
	}
	defer src.Close()

	var args btrfsIoctlVolArgsV2
	args.Fd = int64(src.Fd())
	if readonly {
		args.Flags = SubvolRDONLY
	}
	if err := putName(args.Name[:], name); err != nil {
		return fmt.Errorf("SnapshotCreate: %w", err)
	}
	err = ioctlDir(destParentDir, BTRFS_IOC_SNAP_CREATE_V2, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	runtime.KeepAlive(src)
	if err != nil {
		return fmt.Errorf("BTRFS_IOC_SNAP_CREATE_V2 %s -> %s/%s: %w", srcSubvolPath, destParentDir, name, err)
	}
	return nil
}

// SubvolDelete removes the subvolume (or snapshot) named name inside parentDir
// via BTRFS_IOC_SNAP_DESTROY (btrfs_ioctl_vol_args; the same ioctl deletes
// both plain subvolumes and snapshots).
func SubvolDelete(parentDir, name string) error {
	if name == "" {
		return fmt.Errorf("SubvolDelete: empty name")
	}
	var args btrfsIoctlVolArgs
	if err := putName(args.Name[:], name); err != nil {
		return fmt.Errorf("SubvolDelete: %w", err)
	}
	err := ioctlDir(parentDir, BTRFS_IOC_SNAP_DESTROY, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err != nil {
		return fmt.Errorf("BTRFS_IOC_SNAP_DESTROY %s/%s: %w", parentDir, name, err)
	}
	return nil
}

// SubvolGetFlags returns the subvolume flag bits for the subvolume rooted at
// subvolPath, via BTRFS_IOC_SUBVOL_GETFLAGS. Test against SubvolRDONLY to learn
// whether the subvolume is read-only.
func SubvolGetFlags(subvolPath string) (uint64, error) {
	var flags uint64
	err := ioctlDir(subvolPath, BTRFS_IOC_SUBVOL_GETFLAGS, unsafe.Pointer(&flags))
	runtime.KeepAlive(&flags)
	if err != nil {
		return 0, fmt.Errorf("BTRFS_IOC_SUBVOL_GETFLAGS %s: %w", subvolPath, err)
	}
	return flags, nil
}

// SubvolSetFlags sets the subvolume flag bits for the subvolume rooted at
// subvolPath, via BTRFS_IOC_SUBVOL_SETFLAGS. Only BTRFS_SUBVOL_RDONLY is
// settable; pass SubvolRDONLY to mark read-only or 0 to clear it.
func SubvolSetFlags(subvolPath string, flags uint64) error {
	err := ioctlDir(subvolPath, BTRFS_IOC_SUBVOL_SETFLAGS, unsafe.Pointer(&flags))
	runtime.KeepAlive(&flags)
	if err != nil {
		return fmt.Errorf("BTRFS_IOC_SUBVOL_SETFLAGS %s: %w", subvolPath, err)
	}
	return nil
}

// SetReadonly is a convenience wrapper toggling the read-only flag on a
// subvolume while preserving its other flag bits.
func SetReadonly(subvolPath string, ro bool) error {
	flags, err := SubvolGetFlags(subvolPath)
	if err != nil {
		return err
	}
	if ro {
		flags |= SubvolRDONLY
	} else {
		flags &^= SubvolRDONLY
	}
	return SubvolSetFlags(subvolPath, flags)
}

// SubvolID returns the subvolume (tree) id of the subvolume containing path,
// via BTRFS_IOC_INO_LOOKUP with objectid = BTRFS_FIRST_FREE_OBJECTID, which
// makes the kernel report the tree id of the inode the ioctl was issued on.
// The top-level (default) subvolume reports id 5 (BTRFS_FS_TREE_OBJECTID).
func SubvolID(path string) (uint64, error) {
	var args btrfsIoctlInoLookupArgs
	args.Objectid = btrfsFirstFreeObjectID
	err := ioctlDir(path, BTRFS_IOC_INO_LOOKUP, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err != nil {
		return 0, fmt.Errorf("BTRFS_IOC_INO_LOOKUP %s: %w", path, err)
	}
	return args.Treeid, nil
}

// SubvolInfo is the decoded result of BTRFS_IOC_GET_SUBVOL_INFO.
type SubvolInfo struct {
	ID         uint64 // this subvolume's tree id
	Name       string // subvolume name at its mount point
	ParentID   uint64 // containing subvolume id (0 for top-level/deleted)
	Dirid      uint64 // inode of the containing directory
	Generation uint64 // latest transaction id
	Flags      uint64 // on-disk root_item flags (test against RootSubvolRDONLY)
	UUID       [16]byte
	ParentUUID [16]byte // snapshot source UUID (zero if not a snapshot)
}

// GetSubvolInfo returns metadata for the subvolume rooted at subvolPath via
// BTRFS_IOC_GET_SUBVOL_INFO. subvolPath must be the root of a subvolume.
func GetSubvolInfo(subvolPath string) (*SubvolInfo, error) {
	var args btrfsIoctlGetSubvolInfoArgs
	err := ioctlDir(subvolPath, BTRFS_IOC_GET_SUBVOL_INFO, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err != nil {
		return nil, fmt.Errorf("BTRFS_IOC_GET_SUBVOL_INFO %s: %w", subvolPath, err)
	}
	return &SubvolInfo{
		ID:         args.Treeid,
		Name:       cstr(args.Name[:]),
		ParentID:   args.ParentID,
		Dirid:      args.Dirid,
		Generation: args.Generation,
		Flags:      args.Flags,
		UUID:       args.UUID,
		ParentUUID: args.ParentUUID,
	}, nil
}

// IsReadonly reports whether the subvolume rooted at subvolPath is read-only.
// It uses the BTRFS_IOC_SUBVOL_GETFLAGS ioctl (a single __u64), which is the
// authoritative source for the runtime read-only state and uses the
// SubvolRDONLY (bit 1) namespace. Note that GetSubvolInfo reports the same
// state in a different bit position (RootSubvolRDONLY, bit 0).
func IsReadonly(subvolPath string) (bool, error) {
	flags, err := SubvolGetFlags(subvolPath)
	if err != nil {
		return false, err
	}
	return flags&SubvolRDONLY != 0, nil
}

// Sync forces a transaction commit on the btrfs filesystem containing path via
// BTRFS_IOC_SYNC. path may be the mount point or any file/dir on the fs.
func Sync(path string) error {
	if err := ioctlDir(path, BTRFS_IOC_SYNC, nil); err != nil {
		return fmt.Errorf("BTRFS_IOC_SYNC %s: %w", path, err)
	}
	return nil
}

// Available reports whether path lives on a mounted btrfs filesystem, via
// statfs and the btrfs superblock magic. Integration tests use this to skip
// when not running against a real btrfs mount.
func Available(path string) bool {
	var st unix.Statfs_t
	if err := unixStatfs(path, &st); err != nil {
		return false
	}
	return uint32(st.Type) == unix.BTRFS_SUPER_MAGIC
}
