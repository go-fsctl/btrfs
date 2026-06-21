// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

package btrfs

import "unsafe"

// This file extends abi.go / abi_admin.go with the ABI for quota groups
// (qgroups) and defragmentation.
//
// As elsewhere in the package the BTRFS_IOC_* numbers are recomputed in Go
// from the _IO/_IOR/_IOW/_IOWR encoding rather than hard-coded, and the C
// struct layouts are mirrored as Go structs whose sizes and offsets are pinned
// in abi_quota_test.go against the values produced by linux/btrfs.h /
// linux/btrfs_tree.h on a 6.12 kernel (captured with a cpp/offsetof probe on
// dc3, btrfs-progs v6.14).

// Quota control commands for BTRFS_IOC_QUOTA_CTL (btrfs_ioctl_quota_ctl_args.cmd).
const (
	quotaCtlEnable  = 1 // BTRFS_QUOTA_CTL_ENABLE
	quotaCtlDisable = 2 // BTRFS_QUOTA_CTL_DISABLE
)

// Qgroup limit flags (linux/btrfs.h), exported for QgroupLimit. They select
// which of the limit fields the kernel should enforce.
const (
	QgroupLimitMaxRfer = 1 << 0 // BTRFS_QGROUP_LIMIT_MAX_RFER
	QgroupLimitMaxExcl = 1 << 1 // BTRFS_QGROUP_LIMIT_MAX_EXCL
	QgroupLimitRsvRfer = 1 << 2 // BTRFS_QGROUP_LIMIT_RSV_RFER
	QgroupLimitRsvExcl = 1 << 3 // BTRFS_QGROUP_LIMIT_RSV_EXCL
)

// Defrag range flags (linux/btrfs.h), exported for DefragRange.
const (
	DefragRangeCompress = 1 << 0 // BTRFS_DEFRAG_RANGE_COMPRESS
	DefragRangeStartIO  = 1 << 1 // BTRFS_DEFRAG_RANGE_START_IO
)

// Well-known tree object id and item key types used by the qgroup listing walk
// over the quota tree (linux/btrfs_tree.h).
const (
	btrfsQuotaTreeObjectID = 8   // BTRFS_QUOTA_TREE_OBJECTID
	btrfsQgroupStatusKey   = 240 // BTRFS_QGROUP_STATUS_KEY
	btrfsQgroupInfoKey     = 242 // BTRFS_QGROUP_INFO_KEY
	btrfsQgroupLimitKey    = 244 // BTRFS_QGROUP_LIMIT_KEY
	btrfsQgroupRelationKey = 246 // BTRFS_QGROUP_RELATION_KEY
)

// btrfsIoctlQuotaCtlArgs mirrors struct btrfs_ioctl_quota_ctl_args (16 bytes):
//
//	struct btrfs_ioctl_quota_ctl_args { __u64 cmd; __u64 status; };
//
// cmd is in (BTRFS_QUOTA_CTL_ENABLE/DISABLE); status is reserved/out.
type btrfsIoctlQuotaCtlArgs struct {
	Cmd    uint64
	Status uint64
}

// btrfsIoctlQgroupCreateArgs mirrors struct btrfs_ioctl_qgroup_create_args
// (16 bytes):
//
//	struct btrfs_ioctl_qgroup_create_args { __u64 create; __u64 qgroupid; };
//
// create is 1 to create, 0 to destroy; qgroupid is the target qgroup id.
type btrfsIoctlQgroupCreateArgs struct {
	Create   uint64
	Qgroupid uint64
}

// btrfsIoctlQgroupAssignArgs mirrors struct btrfs_ioctl_qgroup_assign_args
// (24 bytes):
//
//	struct btrfs_ioctl_qgroup_assign_args { __u64 assign; __u64 src; __u64 dst; };
//
// assign is 1 to assign src under dst, 0 to remove the relation.
type btrfsIoctlQgroupAssignArgs struct {
	Assign uint64
	Src    uint64
	Dst    uint64
}

// btrfsQgroupLimit mirrors struct btrfs_qgroup_limit (40 bytes): the inner
// limit spec carried by the qgroup-limit ioctl. flags is a mask of
// BTRFS_QGROUP_LIMIT_* selecting which max/rsv fields apply.
type btrfsQgroupLimit struct {
	Flags   uint64
	MaxRfer uint64
	MaxExcl uint64
	RsvRfer uint64
	RsvExcl uint64
}

// btrfsIoctlQgroupLimitArgs mirrors struct btrfs_ioctl_qgroup_limit_args
// (48 bytes):
//
//	struct btrfs_ioctl_qgroup_limit_args { __u64 qgroupid; struct btrfs_qgroup_limit lim; };
//
// qgroupid 0 means "the qgroup of the subvolume the ioctl is issued on".
type btrfsIoctlQgroupLimitArgs struct {
	Qgroupid uint64
	Lim      btrfsQgroupLimit
}

// btrfsIoctlDefragRangeArgs mirrors struct btrfs_ioctl_defrag_range_args
// (48 bytes):
//
//	struct btrfs_ioctl_defrag_range_args {
//	  __u64 start; __u64 len; __u64 flags;
//	  __u32 extent_thresh; __u32 compress_type; __u32 unused[4];
//	};
//
// start/len bound the byte range (len = ^0 means to EOF); extent_thresh is the
// max extent size below which extents are defragmented (0 = kernel default);
// compress_type forces compression when DefragRangeCompress is set in flags.
type btrfsIoctlDefragRangeArgs struct {
	Start        uint64
	Len          uint64
	Flags        uint64
	ExtentThresh uint32
	CompressType uint32
	Unused       [4]uint32
}

// btrfsQgroupInfoItem mirrors struct btrfs_qgroup_info_item (40 bytes): the
// on-disk QGROUP_INFO item body in the quota tree, carrying the referenced and
// exclusive byte usage of a qgroup. We decode it field-by-field from the raw
// item bytes (the kernel stores it little-endian) rather than overlaying.
//
//	struct btrfs_qgroup_info_item {
//	  __le64 generation; __le64 rfer; __le64 rfer_cmpr; __le64 excl; __le64 excl_cmpr;
//	} __packed;
const btrfsQgroupInfoItemSize = 40

// btrfsQgroupLimitItem mirrors struct btrfs_qgroup_limit_item (40 bytes): the
// on-disk QGROUP_LIMIT item body in the quota tree.
//
//	struct btrfs_qgroup_limit_item {
//	  __le64 flags; __le64 max_rfer; __le64 max_excl; __le64 rsv_rfer; __le64 rsv_excl;
//	} __packed;
const btrfsQgroupLimitItemSize = 40

// BTRFS_IOC_* request numbers for quota/qgroup/defrag, derived from
// linux/btrfs.h. Trailing hex comments are the values produced by the C
// preprocessor on a 6.12 kernel and pinned in abi_quota_test.go.
var (
	// _IO(0x94, 2, struct btrfs_ioctl_vol_args) — DEFRAG reuses the vol_args
	// shape (the kernel ignores all but the implicit fd); encoded _IOW.
	BTRFS_IOC_DEFRAG = iow(btrfsIoctlMagic, 2, unsafe.Sizeof(btrfsIoctlVolArgs{})) // 0x50009402
	// _IOW(0x94, 16, struct btrfs_ioctl_defrag_range_args)
	BTRFS_IOC_DEFRAG_RANGE = iow(btrfsIoctlMagic, 16, unsafe.Sizeof(btrfsIoctlDefragRangeArgs{})) // 0x40309410
	// _IOWR(0x94, 40, struct btrfs_ioctl_quota_ctl_args)
	BTRFS_IOC_QUOTA_CTL = iowr(btrfsIoctlMagic, 40, unsafe.Sizeof(btrfsIoctlQuotaCtlArgs{})) // 0xc0109428
	// _IOW(0x94, 41, struct btrfs_ioctl_qgroup_assign_args)
	BTRFS_IOC_QGROUP_ASSIGN = iow(btrfsIoctlMagic, 41, unsafe.Sizeof(btrfsIoctlQgroupAssignArgs{})) // 0x40189429
	// _IOW(0x94, 42, struct btrfs_ioctl_qgroup_create_args)
	BTRFS_IOC_QGROUP_CREATE = iow(btrfsIoctlMagic, 42, unsafe.Sizeof(btrfsIoctlQgroupCreateArgs{})) // 0x4010942a
	// _IOR(0x94, 43, struct btrfs_ioctl_qgroup_limit_args)
	BTRFS_IOC_QGROUP_LIMIT = ior(btrfsIoctlMagic, 43, unsafe.Sizeof(btrfsIoctlQgroupLimitArgs{})) // 0x8030942b
)
