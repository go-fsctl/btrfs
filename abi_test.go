// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

package btrfs

import (
	"testing"
	"unsafe"
)

// abiWant picks the expected value for this target's word size: lp64 on a
// 64-bit Go target, ilp32 on a 32-bit one (386, arm, mips, mipsle).
//
// The two differ only for the structs holding a pointer or a
// struct btrfs_ioctl_timespec { __u64 sec; __u32 nsec; }. A 32-bit Go target
// aligns uint64 to 4 bytes, so those structs come out with no padding, and the
// request number (which encodes sizeof) changes with them. That packed layout
// is not an accident to paper over: it is the one fs/btrfs/ioctl.c declares
// for "a 32-bit userspace and 64-bit kernel" -- btrfs_ioctl_timespec_32,
// btrfs_ioctl_received_subvol_args_32, btrfs_ioctl_get_subvol_info_args_32 and
// btrfs_ioctl_send_args_32, all __attribute__((__packed__)) -- and dispatches
// as BTRFS_IOC_SET_RECEIVED_SUBVOL_32, BTRFS_IOC_GET_SUBVOL_INFO_32 and
// BTRFS_IOC_SEND_32. The ilp32 values below are computed from those packed
// declarations by hand (offsets, then _IOC with the packed sizeof: 68, 192,
// 488), not read back from the Go structs.
//
// A native 32-bit arm or mips KERNEL is a different ABI: there the C compiler
// aligns __u64 to 8, the uapi structs keep their 64-bit padding, and the
// request numbers carry the 64-bit sizes. This package's 32-bit numbers are
// unknown to such a kernel, so those three calls fail with ENOTTY there rather
// than being misread.
func abiWant(lp64, ilp32 uintptr) uintptr {
	if unsafe.Sizeof(uintptr(0)) == 4 {
		return ilp32
	}
	return lp64
}

// TestIocNumbers pins the BTRFS_IOC_* request numbers derived in abi.go to the
// values produced by the C preprocessor over linux/btrfs.h (verified against a
// 6.12 x86-64 kernel: magic 'X' = 0x94, asm-generic encoding
// (dir<<30)|(size<<16)|(type<<8)|nr). The table is written in that layout;
// kernelIOC re-encodes it for powerpc and mips, and TestIocAgainstXSys checks
// the helpers against an independent per-architecture source.
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
		{"GET_SUBVOL_INFO", BTRFS_IOC_GET_SUBVOL_INFO, abiWant(0x81f8943c, 0x81e8943c)},
		{"SYNC", BTRFS_IOC_SYNC, 0x9408},
	} {
		if want := kernelIOC(c.want); c.got != want {
			t.Errorf("%s = %#x, want %#x", c.name, c.got, want)
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
		{"btrfs_ioctl_get_subvol_info_args", unsafe.Sizeof(btrfsIoctlGetSubvolInfoArgs{}), abiWant(504, 488)},
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

// TestIocEncoding sanity-checks the ioctl encoding helpers independently of
// the btrfs request table. The expected values are written in the asm-generic
// layout and re-encoded by kernelIOC for powerpc and mips.
func TestIocEncoding(t *testing.T) {
	// _IOW('X', 0, 0) with X=0x94: dir=WRITE, type=0x94<<8.
	if got, want := iow(0x94, 0, 0), kernelIOC((1<<30)|(0x94<<8)); got != want {
		t.Errorf("iow base = %#x, want %#x", got, want)
	}
	// _IO('X', 8) == BTRFS_IOC_SYNC.
	if got, want := io(0x94, 8), kernelIOC(0x9408); got != want {
		t.Errorf("io('X',8) = %#x, want %#x", got, want)
	}
	// _IOR('X', 25, 8) == BTRFS_IOC_SUBVOL_GETFLAGS (dir=READ).
	if got, want := ior(0x94, 25, 8), kernelIOC(0x80089419); got != want {
		t.Errorf("ior('X',25,8) = %#x, want %#x", got, want)
	}
}
