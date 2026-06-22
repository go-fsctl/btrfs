// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

package btrfs

import "unsafe"

// This file extends abi.go / abi_admin.go with the ABI for the filesystem-level
// admin operations: label get/set, online resize, the default subvolume, the
// feature-flag ioctls, and the reverse extent->inode->path mapping.
//
// As elsewhere in the package the BTRFS_IOC_* numbers are recomputed in Go from
// the _IO/_IOR/_IOW/_IOWR encoding rather than hard-coded, and the C struct
// layouts are mirrored as Go structs whose sizes and offsets are pinned in
// abi_fsadmin_test.go against the values produced by linux/btrfs.h (and
// linux/fs.h for the FSLABEL ioctls) on a 6.12 kernel, captured with a
// cpp/offsetof probe on dc3.

// uapi sizes from linux/fs.h and linux/btrfs.h for the label ioctls.
const (
	// btrfsLabelSize is BTRFS_LABEL_SIZE: the on-disk label field width,
	// including the NUL terminator. The GET/SET_FSLABEL ioctls carry a
	// char[FSLABEL_MAX] and FSLABEL_MAX == BTRFS_LABEL_SIZE == 256.
	btrfsLabelSize = 256 // BTRFS_LABEL_SIZE / FSLABEL_MAX
)

// Well-known object ids used to read the default subvolume out of the root
// tree (linux/btrfs_tree.h).
const (
	btrfsRootTreeDirObjectID = 6 // BTRFS_ROOT_TREE_DIR_OBJECTID (".." of the FS)
)

// btrfs_tree.h key type for the directory item that names the default
// subvolume ("default") in the root tree.
const (
	btrfsDirItemKey = 84 // BTRFS_DIR_ITEM_KEY
)

// Feature flag bits as they appear in btrfs_ioctl_feature_flags. These mirror
// the BTRFS_FEATURE_{COMPAT,COMPAT_RO,INCOMPAT}_* macros in linux/btrfs.h and
// are used both to decode GetFeatures/GetSupportedFeatures into typed names and
// to construct a feature set for SetFeatures.

// Compat (read-write) feature bits. None are currently defined by the kernel,
// but the field is decoded for completeness.

// Compat-RO feature bits (a filesystem with an unknown bit set here may still
// be mounted read-only).
const (
	FeatureCompatROFreeSpaceTree      = 1 << 0 // BTRFS_FEATURE_COMPAT_RO_FREE_SPACE_TREE
	FeatureCompatROFreeSpaceTreeValid = 1 << 1 // BTRFS_FEATURE_COMPAT_RO_FREE_SPACE_TREE_VALID
	FeatureCompatROVerity             = 1 << 2 // BTRFS_FEATURE_COMPAT_RO_VERITY
	FeatureCompatROBlockGroupTree     = 1 << 3 // BTRFS_FEATURE_COMPAT_RO_BLOCK_GROUP_TREE
)

// Incompat feature bits (a filesystem with an unknown bit set here cannot be
// mounted at all).
const (
	FeatureIncompatMixedBackref   = 1 << 0  // BTRFS_FEATURE_INCOMPAT_MIXED_BACKREF
	FeatureIncompatDefaultSubvol  = 1 << 1  // BTRFS_FEATURE_INCOMPAT_DEFAULT_SUBVOL
	FeatureIncompatMixedGroups    = 1 << 2  // BTRFS_FEATURE_INCOMPAT_MIXED_GROUPS
	FeatureIncompatCompressLZO    = 1 << 3  // BTRFS_FEATURE_INCOMPAT_COMPRESS_LZO
	FeatureIncompatCompressZSTD   = 1 << 4  // BTRFS_FEATURE_INCOMPAT_COMPRESS_ZSTD
	FeatureIncompatBigMetadata    = 1 << 5  // BTRFS_FEATURE_INCOMPAT_BIG_METADATA
	FeatureIncompatExtendedIref   = 1 << 6  // BTRFS_FEATURE_INCOMPAT_EXTENDED_IREF
	FeatureIncompatRAID56         = 1 << 7  // BTRFS_FEATURE_INCOMPAT_RAID56
	FeatureIncompatSkinnyMetadata = 1 << 8  // BTRFS_FEATURE_INCOMPAT_SKINNY_METADATA
	FeatureIncompatNoHoles        = 1 << 9  // BTRFS_FEATURE_INCOMPAT_NO_HOLES
	FeatureIncompatMetadataUUID   = 1 << 10 // BTRFS_FEATURE_INCOMPAT_METADATA_UUID
	FeatureIncompatRAID1C34       = 1 << 11 // BTRFS_FEATURE_INCOMPAT_RAID1C34
	FeatureIncompatZoned          = 1 << 12 // BTRFS_FEATURE_INCOMPAT_ZONED
	FeatureIncompatExtentTreeV2   = 1 << 13 // BTRFS_FEATURE_INCOMPAT_EXTENT_TREE_V2
	FeatureIncompatRAIDStripeTree = 1 << 14 // BTRFS_FEATURE_INCOMPAT_RAID_STRIPE_TREE
	FeatureIncompatSimpleQuota    = 1 << 16 // BTRFS_FEATURE_INCOMPAT_SIMPLE_QUOTA
)

// btrfsIoctlFeatureFlags mirrors struct btrfs_ioctl_feature_flags (24 bytes):
//
//	struct btrfs_ioctl_feature_flags {
//	  __u64 compat_flags; __u64 compat_ro_flags; __u64 incompat_flags;
//	};
//
// GET_FEATURES returns one of these; GET_SUPPORTED_FEATURES returns three
// (always-set / safe-to-set / safe-to-clear) and SET_FEATURES takes two
// (clear-mask then set-mask).
type btrfsIoctlFeatureFlags struct {
	Compat   uint64
	CompatRO uint64
	Incompat uint64
}

// btrfsIoctlLogicalInoArgs mirrors struct btrfs_ioctl_logical_ino_args (56
// bytes):
//
//	struct btrfs_ioctl_logical_ino_args {
//	  __u64 logical; __u64 size; __u64 reserved[3]; __u64 flags; __u64 inodes;
//	};
//
// logical/size/flags are in; inodes is a userspace pointer to a
// btrfs_data_container the kernel fills with (inode, offset, root) triples.
type btrfsIoctlLogicalInoArgs struct {
	Logical  uint64
	Size     uint64
	Reserved [3]uint64
	Flags    uint64
	Inodes   uint64 // pointer to btrfs_data_container
}

// btrfsIoctlInoPathArgs mirrors struct btrfs_ioctl_ino_path_args (56 bytes):
//
//	struct btrfs_ioctl_ino_path_args {
//	  __u64 inum; __u64 size; __u64 reserved[4]; __u64 fspath;
//	};
//
// inum/size are in; fspath is a userspace pointer to a btrfs_data_container the
// kernel fills with relative-path offsets into the same container.
type btrfsIoctlInoPathArgs struct {
	Inum     uint64
	Size     uint64
	Reserved [4]uint64
	Fspath   uint64 // pointer to btrfs_data_container
}

// btrfsDataContainerHdr mirrors the fixed head of struct btrfs_data_container
// (16 bytes): the four counters preceding the variable-length val[] array.
//
//	struct btrfs_data_container {
//	  __u32 bytes_left; __u32 bytes_missing; __u32 elem_cnt; __u32 elem_missed;
//	  __u64 val[];
//	};
type btrfsDataContainerHdr struct {
	BytesLeft    uint32
	BytesMissing uint32
	ElemCnt      uint32
	ElemMissed   uint32
}

// btrfsDataContainerHdrSize is sizeof(struct btrfs_data_container) up to but
// excluding the flexible val[] array.
const btrfsDataContainerHdrSize = 16

// btrfsDirItemHdrSize is sizeof(struct btrfs_dir_item) (packed, 30 bytes): the
// disk_key location (17), transid (8), data_len (2), name_len (2), type (1).
// The item name follows immediately in the search-result body. We decode the
// fields by hand because the struct is __packed and carries a trailing name.
//
//	struct btrfs_dir_item {
//	  struct btrfs_disk_key location; // __le64 objectid; __u8 type; __le64 offset
//	  __le64 transid; __le16 data_len; __le16 name_len; __u8 type;
//	} __packed;
const btrfsDirItemHdrSize = 30

// BTRFS_IOC_* request numbers for the filesystem-level admin operations,
// derived from linux/btrfs.h (and linux/fs.h for the FSLABEL aliases). Trailing
// hex comments are the values produced by the C preprocessor on a 6.12 kernel
// and pinned in abi_fsadmin_test.go.
var (
	// BTRFS_IOC_GET_FSLABEL == FS_IOC_GETFSLABEL == _IOR(0x94, 49, char[256])
	BTRFS_IOC_GET_FSLABEL = ior(btrfsIoctlMagic, 49, btrfsLabelSize) // 0x81009431
	// BTRFS_IOC_SET_FSLABEL == FS_IOC_SETFSLABEL == _IOW(0x94, 50, char[256])
	BTRFS_IOC_SET_FSLABEL = iow(btrfsIoctlMagic, 50, btrfsLabelSize) // 0x41009432
	// _IOW(0x94, 3, struct btrfs_ioctl_vol_args)
	BTRFS_IOC_RESIZE = iow(btrfsIoctlMagic, 3, unsafe.Sizeof(btrfsIoctlVolArgs{})) // 0x50009403
	// _IOW(0x94, 19, __u64)
	BTRFS_IOC_DEFAULT_SUBVOL = iow(btrfsIoctlMagic, 19, 8) // 0x40089413
	// _IOR(0x94, 57, struct btrfs_ioctl_feature_flags)
	BTRFS_IOC_GET_FEATURES = ior(btrfsIoctlMagic, 57, unsafe.Sizeof(btrfsIoctlFeatureFlags{})) // 0x80189439
	// _IOW(0x94, 57, struct btrfs_ioctl_feature_flags[2])
	BTRFS_IOC_SET_FEATURES = iow(btrfsIoctlMagic, 57, 2*unsafe.Sizeof(btrfsIoctlFeatureFlags{})) // 0x40309439
	// _IOR(0x94, 57, struct btrfs_ioctl_feature_flags[3])
	BTRFS_IOC_GET_SUPPORTED_FEATURES = ior(btrfsIoctlMagic, 57, 3*unsafe.Sizeof(btrfsIoctlFeatureFlags{})) // 0x80489439
	// _IOWR(0x94, 36, struct btrfs_ioctl_logical_ino_args)
	BTRFS_IOC_LOGICAL_INO = iowr(btrfsIoctlMagic, 36, unsafe.Sizeof(btrfsIoctlLogicalInoArgs{})) // 0xc0389424
	// _IOWR(0x94, 35, struct btrfs_ioctl_ino_path_args)
	BTRFS_IOC_INO_PATHS = iowr(btrfsIoctlMagic, 35, unsafe.Sizeof(btrfsIoctlInoPathArgs{})) // 0xc0389423
)
