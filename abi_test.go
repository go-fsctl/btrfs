// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

package btrfs

import (
	"testing"
	"unsafe"
)

// TestIocNumbers pins the BTRFS_IOC_* request numbers derived in abi.go to the
// values produced by the C preprocessor over linux/btrfs.h (verified against a
// 6.12 kernel: magic 'X' = 0x94, encoding (dir<<30)|(size<<16)|(type<<8)|nr).
func TestIocNumbers(t *testing.T) {
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"SUBVOL_CREATE", BTRFS_IOC_SUBVOL_CREATE, 0x5000940e},
		{"SNAP_DESTROY", BTRFS_IOC_SNAP_DESTROY, 0x5000940f},
		{"INO_LOOKUP", BTRFS_IOC_INO_LOOKUP, 0xd0009412},
		{"SNAP_CREATE_V2", BTRFS_IOC_SNAP_CREATE_V2, 0x50009417},
		{"SUBVOL_CREATE_V2", BTRFS_IOC_SUBVOL_CREATE_V2, 0x50009418},
		{"SUBVOL_GETFLAGS", BTRFS_IOC_SUBVOL_GETFLAGS, 0x80089419},
		{"SUBVOL_SETFLAGS", BTRFS_IOC_SUBVOL_SETFLAGS, 0x4008941a},
		{"GET_SUBVOL_INFO", BTRFS_IOC_GET_SUBVOL_INFO, 0x81f8943c},
		{"SYNC", BTRFS_IOC_SYNC, 0x9408},
	} {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x", c.name, c.got, c.want)
		}
	}
}

// TestStructSizes pins the ioctl struct sizes to the C sizeof() values from
// linux/btrfs.h on a 64-bit kernel. A mismatch means the Go struct diverges
// from the kernel ABI and the ioctl size field would be wrong.
func TestStructSizes(t *testing.T) {
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"btrfs_ioctl_vol_args", unsafe.Sizeof(btrfsIoctlVolArgs{}), 4096},
		{"btrfs_ioctl_vol_args_v2", unsafe.Sizeof(btrfsIoctlVolArgsV2{}), 4096},
		{"btrfs_ioctl_ino_lookup_args", unsafe.Sizeof(btrfsIoctlInoLookupArgs{}), 4096},
		{"btrfs_ioctl_get_subvol_info_args", unsafe.Sizeof(btrfsIoctlGetSubvolInfoArgs{}), 504},
	} {
		if c.got != c.want {
			t.Errorf("sizeof(%s) = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestStructOffsets pins the field byte offsets that the kernel ABI is
// sensitive to (captured via offsetof() against linux/btrfs.h).
func TestStructOffsets(t *testing.T) {
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		// btrfs_ioctl_vol_args_v2: fd, transid, flags, then the name union.
		{"vol_args_v2.Transid", unsafe.Offsetof(btrfsIoctlVolArgsV2{}.Transid), 8},
		{"vol_args_v2.Flags", unsafe.Offsetof(btrfsIoctlVolArgsV2{}.Flags), 16},
		{"vol_args_v2.Name", unsafe.Offsetof(btrfsIoctlVolArgsV2{}.Name), 56},
		// btrfs_ioctl_ino_lookup_args: treeid, objectid, name.
		{"ino_lookup.Objectid", unsafe.Offsetof(btrfsIoctlInoLookupArgs{}.Objectid), 8},
		{"ino_lookup.Name", unsafe.Offsetof(btrfsIoctlInoLookupArgs{}.Name), 16},
		// btrfs_ioctl_get_subvol_info_args: flags and uuid placement.
		{"subvol_info.Flags", unsafe.Offsetof(btrfsIoctlGetSubvolInfoArgs{}.Flags), 288},
		{"subvol_info.UUID", unsafe.Offsetof(btrfsIoctlGetSubvolInfoArgs{}.UUID), 296},
	} {
		if c.got != c.want {
			t.Errorf("offsetof(%s) = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestFlagBits pins the two distinct read-only flag namespaces: the
// SUBVOL_GET/SETFLAGS ioctls use bit 1 (BTRFS_SUBVOL_RDONLY) while
// GET_SUBVOL_INFO surfaces the on-disk root_item bit 0
// (BTRFS_ROOT_SUBVOL_RDONLY). Conflating them is a real bug, so pin both.
func TestFlagBits(t *testing.T) {
	if SubvolRDONLY != 0x2 {
		t.Errorf("SubvolRDONLY = %#x, want 0x2", SubvolRDONLY)
	}
	if RootSubvolRDONLY != 0x1 {
		t.Errorf("RootSubvolRDONLY = %#x, want 0x1", RootSubvolRDONLY)
	}
}

// TestIocEncoding sanity-checks the ioctl encoding helper independently of the
// btrfs magic, against the canonical asm-generic field layout.
func TestIocEncoding(t *testing.T) {
	// _IOW('X', 0, 0) with X=0x94: dir=1<<30, type=0x94<<8.
	if got := iow(0x94, 0, 0); got != (1<<30)|(0x94<<8) {
		t.Errorf("iow base = %#x", got)
	}
	// _IO('X', 8) == BTRFS_IOC_SYNC.
	if got := io(0x94, 8); got != 0x9408 {
		t.Errorf("io('X',8) = %#x, want 0x9408", got)
	}
	// _IOR('X', 25, 8) == BTRFS_IOC_SUBVOL_GETFLAGS (dir=READ=2).
	if got := ior(0x94, 25, 8); got != 0x80089419 {
		t.Errorf("ior('X',25,8) = %#x, want 0x80089419", got)
	}
}
