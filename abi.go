// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

package btrfs

import "unsafe"

// btrfs ioctl numbers are derived here from the kernel uapi header
// linux/btrfs.h the same way the C preprocessor does it: each is an
// _IO/_IOR/_IOW/_IOWR macro over magic 'X' = 0x94. We recompute the numbers
// in Go rather than hard-coding hex so the derivation is self-documenting and
// unit-testable; the expected hex (verified against linux/btrfs.h on a 6.12
// kernel) is recorded in the comments and pinned in abi_test.go.
//
// The encoding is the asm-generic ioctl layout used on all the architectures
// btrfs runs on (x86-64, arm64, ...):
//
//	(dir << 30) | (size << 16) | (type << 8) | nr
//
// where size is sizeof(parameter type) and dir is one of NONE/WRITE/READ.
// "READ"/"WRITE" are from the kernel's point of view: _IOR means the kernel
// writes back to userspace, _IOW means userspace writes to the kernel.
const (
	iocNone  = 0
	iocWrite = 1
	iocRead  = 2

	iocNRBits   = 8
	iocTypeBits = 8
	iocSizeBits = 14
	iocDirBits  = 2

	iocNRShift   = 0
	iocTypeShift = iocNRShift + iocNRBits
	iocSizeShift = iocTypeShift + iocTypeBits
	iocDirShift  = iocSizeShift + iocSizeBits
)

func ioc(dir, typ, nr, size uintptr) uintptr {
	return (dir << iocDirShift) |
		(typ << iocTypeShift) |
		(nr << iocNRShift) |
		(size << iocSizeShift)
}

func iow(typ, nr, size uintptr) uintptr  { return ioc(iocWrite, typ, nr, size) }
func ior(typ, nr, size uintptr) uintptr  { return ioc(iocRead, typ, nr, size) }
func iowr(typ, nr, size uintptr) uintptr { return ioc(iocWrite|iocRead, typ, nr, size) }
func io(typ, nr uintptr) uintptr         { return ioc(iocNone, typ, nr, 0) }

// btrfsIoctlMagic is BTRFS_IOCTL_MAGIC ('X').
const btrfsIoctlMagic = 0x94

// uapi sizes from linux/btrfs.h.
const (
	btrfsPathNameMax   = 4087 // BTRFS_PATH_NAME_MAX
	btrfsSubvolNameMax = 4039 // BTRFS_SUBVOL_NAME_MAX
	btrfsVolNameMax    = 255  // BTRFS_VOL_NAME_MAX
	btrfsInoLookupPath = 4080 // BTRFS_INO_LOOKUP_PATH_MAX
	btrfsUUIDSize      = 16   // BTRFS_UUID_SIZE
)

// Subvolume flags shared by SNAP_CREATE_V2 / SUBVOL_CREATE_V2 and the
// SUBVOL_GET/SETFLAGS ioctls.
const (
	SubvolRDONLY        = 1 << 1 // BTRFS_SUBVOL_RDONLY
	SubvolQGroupInherit = 1 << 2 // BTRFS_SUBVOL_QGROUP_INHERIT
)

// RootSubvolRDONLY is the read-only bit as it appears in the on-disk root_item
// flags reported by BTRFS_IOC_GET_SUBVOL_INFO (struct
// btrfs_ioctl_get_subvol_info_args.flags). This is a DIFFERENT namespace from
// the SUBVOL_GET/SETFLAGS ioctl flags above: GET_SUBVOL_INFO surfaces the raw
// root_item.flags where read-only is bit 0 (BTRFS_ROOT_SUBVOL_RDONLY), whereas
// the SUBVOL_GETFLAGS ioctl reports read-only as bit 1 (BTRFS_SUBVOL_RDONLY).
const RootSubvolRDONLY = 1 << 0 // BTRFS_ROOT_SUBVOL_RDONLY (btrfs_tree.h)

// Well-known object ids (linux/btrfs_tree.h). The first subvolume the kernel
// will hand out lives at BTRFS_FIRST_FREE_OBJECTID; the FS tree root is 5.
const (
	btrfsFSTreeObjectID    = 5   // BTRFS_FS_TREE_OBJECTID
	btrfsFirstFreeObjectID = 256 // BTRFS_FIRST_FREE_OBJECTID
)

// btrfsIoctlVolArgs mirrors struct btrfs_ioctl_vol_args (4096 bytes):
//
//	struct btrfs_ioctl_vol_args { __s64 fd; char name[BTRFS_PATH_NAME_MAX+1]; };
//
// Used by SUBVOL_CREATE and SNAP_DESTROY. x/sys/unix does not define this
// type at the pinned version, so we define it here.
type btrfsIoctlVolArgs struct {
	Fd   int64
	Name [btrfsPathNameMax + 1]byte
}

// btrfsIoctlVolArgsV2 mirrors struct btrfs_ioctl_vol_args_v2 (4096 bytes):
//
//	struct btrfs_ioctl_vol_args_v2 {
//	  __s64 fd; __u64 transid; __u64 flags;
//	  union { struct { __u64 size; struct btrfs_qgroup_inherit *qgroup_inherit; };
//	          __u64 unused[4]; };
//	  union { char name[BTRFS_SUBVOL_NAME_MAX+1]; __u64 devid; __u64 subvolid; };
//	};
//
// We model the two anonymous unions as fixed byte fields: Unused is the
// 32-byte size/qgroup union, Name is the 4040-byte name union. Used by
// SNAP_CREATE_V2 (fd = source subvolume fd, flags = BTRFS_SUBVOL_RDONLY).
type btrfsIoctlVolArgsV2 struct {
	Fd      int64
	Transid uint64
	Flags   uint64
	Unused  [4]uint64 // size + qgroup_inherit pointer union
	Name    [btrfsSubvolNameMax + 1]byte
}

// btrfsIoctlInoLookupArgs mirrors struct btrfs_ioctl_ino_lookup_args (4096):
//
//	struct btrfs_ioctl_ino_lookup_args { __u64 treeid; __u64 objectid;
//	                                     char name[BTRFS_INO_LOOKUP_PATH_MAX]; };
//
// With objectid = BTRFS_FIRST_FREE_OBJECTID the kernel fills treeid with the
// subvolume (tree) id of the inode the ioctl was issued on.
type btrfsIoctlInoLookupArgs struct {
	Treeid   uint64
	Objectid uint64
	Name     [btrfsInoLookupPath]byte
}

// btrfsIoctlTimespec mirrors struct btrfs_ioctl_timespec { __u64 sec; __u32 nsec; }.
type btrfsIoctlTimespec struct {
	Sec  uint64
	Nsec uint32
}

// btrfsIoctlGetSubvolInfoArgs mirrors struct
// btrfs_ioctl_get_subvol_info_args (504 bytes). All fields are filled by the
// kernel for BTRFS_IOC_GET_SUBVOL_INFO issued on a subvolume root fd.
type btrfsIoctlGetSubvolInfoArgs struct {
	Treeid      uint64
	Name        [btrfsVolNameMax + 1]byte
	ParentID    uint64
	Dirid       uint64
	Generation  uint64
	Flags       uint64
	UUID        [btrfsUUIDSize]byte
	ParentUUID  [btrfsUUIDSize]byte
	ReceivedUUID [btrfsUUIDSize]byte
	Ctransid    uint64
	Otransid    uint64
	Stransid    uint64
	Rtransid    uint64
	Ctime       btrfsIoctlTimespec
	Otime       btrfsIoctlTimespec
	Stime       btrfsIoctlTimespec
	Rtime       btrfsIoctlTimespec
	Reserved    [8]uint64
}

// BTRFS_IOC_* request numbers, derived from linux/btrfs.h. The trailing hex
// comments are the values produced by the C preprocessor on a 6.12 kernel and
// are pinned in abi_test.go.
var (
	// _IOW(0x94, 14, struct btrfs_ioctl_vol_args)
	BTRFS_IOC_SUBVOL_CREATE = iow(btrfsIoctlMagic, 14, unsafe.Sizeof(btrfsIoctlVolArgs{})) // 0x5000940e
	// _IOW(0x94, 15, struct btrfs_ioctl_vol_args)
	BTRFS_IOC_SNAP_DESTROY = iow(btrfsIoctlMagic, 15, unsafe.Sizeof(btrfsIoctlVolArgs{})) // 0x5000940f
	// _IOWR(0x94, 18, struct btrfs_ioctl_ino_lookup_args)
	BTRFS_IOC_INO_LOOKUP = iowr(btrfsIoctlMagic, 18, unsafe.Sizeof(btrfsIoctlInoLookupArgs{})) // 0xd0009412
	// _IOW(0x94, 23, struct btrfs_ioctl_vol_args_v2)
	BTRFS_IOC_SNAP_CREATE_V2 = iow(btrfsIoctlMagic, 23, unsafe.Sizeof(btrfsIoctlVolArgsV2{})) // 0x50009417
	// _IOW(0x94, 24, struct btrfs_ioctl_vol_args_v2)
	BTRFS_IOC_SUBVOL_CREATE_V2 = iow(btrfsIoctlMagic, 24, unsafe.Sizeof(btrfsIoctlVolArgsV2{})) // 0x50009418
	// _IOR(0x94, 25, __u64)
	BTRFS_IOC_SUBVOL_GETFLAGS = ior(btrfsIoctlMagic, 25, 8) // 0x80089419
	// _IOW(0x94, 26, __u64)
	BTRFS_IOC_SUBVOL_SETFLAGS = iow(btrfsIoctlMagic, 26, 8) // 0x4008941a
	// _IOR(0x94, 60, struct btrfs_ioctl_get_subvol_info_args)
	BTRFS_IOC_GET_SUBVOL_INFO = ior(btrfsIoctlMagic, 60, unsafe.Sizeof(btrfsIoctlGetSubvolInfoArgs{})) // 0x81f8943c
	// _IO(0x94, 8)
	BTRFS_IOC_SYNC = io(btrfsIoctlMagic, 8) // 0x9408
)
