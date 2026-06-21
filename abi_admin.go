// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

package btrfs

import "unsafe"

// This file extends abi.go with the ABI for the admin operations beyond the
// subvolume set: subvolume listing (TREE_SEARCH), device management
// (ADD_DEV/RM_DEV/DEV_INFO/FS_INFO), scrub, and balance.
//
// As in abi.go the BTRFS_IOC_* numbers are recomputed in Go from the
// _IO/_IOR/_IOW/_IOWR encoding rather than hard-coded, and the C struct
// layouts are mirrored as Go structs whose sizes and offsets are pinned in
// abi_admin_test.go against the values produced by linux/btrfs.h on a 6.12
// kernel (captured with a C offsetof/sizeof probe on dc3).

// Additional uapi sizes from linux/btrfs.h.
const (
	btrfsDevicePathNameMax = 1024 // BTRFS_DEVICE_PATH_NAME_MAX
	btrfsFSIDSize          = 16   // BTRFS_FSID_SIZE
)

// Well-known tree object ids (linux/btrfs_tree.h) used by the search-based
// subvolume listing.
const (
	btrfsRootTreeObjectID = 1 // BTRFS_ROOT_TREE_OBJECTID (tree of tree roots)
)

// btrfs_tree.h item key types used when walking the root tree.
const (
	btrfsRootItemKey    = 132 // BTRFS_ROOT_ITEM_KEY
	btrfsRootBackrefKey = 144 // BTRFS_ROOT_BACKREF_KEY
	btrfsRootRefKey     = 156 // BTRFS_ROOT_REF_KEY
)

// Scrub flags (linux/btrfs.h).
const (
	ScrubReadonly = 1 // BTRFS_SCRUB_READONLY
)

// Balance type/control flags (linux/btrfs.h), exported for BalanceStart.
const (
	BalanceData     = 1 << 0 // BTRFS_BALANCE_DATA
	BalanceSystem   = 1 << 1 // BTRFS_BALANCE_SYSTEM
	BalanceMetadata = 1 << 2 // BTRFS_BALANCE_METADATA
	BalanceForce    = 1 << 3 // BTRFS_BALANCE_FORCE
	BalanceResume   = 1 << 4 // BTRFS_BALANCE_RESUME
)

// Per-type balance args filter flags (linux/btrfs.h, btrfs_balance_args.flags).
const (
	BalanceArgsProfiles   = 1 << 0  // BTRFS_BALANCE_ARGS_PROFILES
	BalanceArgsUsage      = 1 << 1  // BTRFS_BALANCE_ARGS_USAGE
	BalanceArgsDevid      = 1 << 2  // BTRFS_BALANCE_ARGS_DEVID
	BalanceArgsConvert    = 1 << 8  // BTRFS_BALANCE_ARGS_CONVERT
	BalanceArgsSoft       = 1 << 9  // BTRFS_BALANCE_ARGS_SOFT
	BalanceArgsUsageRange = 1 << 10 // BTRFS_BALANCE_ARGS_USAGE_RANGE
)

// Balance control modes for BTRFS_IOC_BALANCE_CTL (linux/btrfs.h).
const (
	balanceCtlPause  = 1 // BTRFS_BALANCE_CTL_PAUSE
	balanceCtlCancel = 2 // BTRFS_BALANCE_CTL_CANCEL
)

// Balance run-state bits reported in btrfs_ioctl_balance_args.state.
const (
	BalanceStateRunning   = 1 << 0 // BTRFS_BALANCE_STATE_RUNNING
	BalanceStatePauseReq  = 1 << 1 // BTRFS_BALANCE_STATE_PAUSE_REQ
	BalanceStateCancelReq = 1 << 2 // BTRFS_BALANCE_STATE_CANCEL_REQ
)

// btrfsIoctlSearchKey mirrors struct btrfs_ioctl_search_key (104 bytes). It is
// the search range for the TREE_SEARCH ioctl family: a slice of the linear
// 136-bit key space (objectid<<72 | type<<64 | offset) bounded by the min/max
// fields, optionally filtered by transid. nr_items is in/out (max desired /
// actual returned).
type btrfsIoctlSearchKey struct {
	TreeID      uint64
	MinObjectid uint64
	MaxObjectid uint64
	MinOffset   uint64
	MaxOffset   uint64
	MinTransid  uint64
	MaxTransid  uint64
	MinType     uint32
	MaxType     uint32
	NrItems     uint32
	Unused      uint32
	Unused1     uint64
	Unused2     uint64
	Unused3     uint64
	Unused4     uint64
}

// btrfsIoctlSearchHeader mirrors struct btrfs_ioctl_search_header (32 bytes).
// Each item returned in the search buffer is preceded by one of these; the
// item body of Len bytes follows immediately.
type btrfsIoctlSearchHeader struct {
	Transid  uint64
	Objectid uint64
	Offset   uint64
	Type     uint32
	Len      uint32
}

// btrfsIoctlSearchArgsV2Hdr mirrors the fixed head of struct
// btrfs_ioctl_search_args_v2 (112 bytes): the search key followed by buf_size.
// The variable-length buf[] is appended in a single allocation by
// treeSearchV2; this struct is only the header overlay.
type btrfsIoctlSearchArgsV2Hdr struct {
	Key     btrfsIoctlSearchKey
	BufSize uint64
}

// btrfsRootRef mirrors struct btrfs_root_ref (18 bytes, packed): the on-disk
// payload of a ROOT_REF/ROOT_BACKREF item. The subvolume name (name_len bytes)
// follows immediately after the struct in the item body. Because it is packed
// we decode it field-by-field from the raw bytes rather than overlaying a Go
// struct (Go cannot express the C packing and the trailing name is variable).
//
//	struct btrfs_root_ref { __le64 dirid; __le64 sequence; __le16 name_len; } __packed;
const btrfsRootRefSize = 18

// btrfsIoctlDevInfoArgs mirrors struct btrfs_ioctl_dev_info_args (4096 bytes).
// devid is in/out (set it to query a specific device); the kernel fills the
// rest. path is the device's path as the kernel knows it.
type btrfsIoctlDevInfoArgs struct {
	Devid      uint64
	UUID       [btrfsUUIDSize]byte
	BytesUsed  uint64
	TotalBytes uint64
	FSID       [btrfsUUIDSize]byte
	Unused     [377]uint64 // pad to 4k
	Path       [btrfsDevicePathNameMax]byte
}

// btrfsIoctlFsInfoArgs mirrors struct btrfs_ioctl_fs_info_args (1024 bytes).
// All fields are out except flags (in/out: requests optional generation /
// metadata-uuid fields). num_devices and max_id reflect the device count.
type btrfsIoctlFsInfoArgs struct {
	MaxID          uint64
	NumDevices     uint64
	FSID           [btrfsFSIDSize]byte
	Nodesize       uint32
	Sectorsize     uint32
	CloneAlignment uint32
	CsumType       uint16
	CsumSize       uint16
	Flags          uint64
	Generation     uint64
	MetadataUUID   [btrfsFSIDSize]byte
	Reserved       [944]byte // pad to 1k
}

// btrfsScrubProgress mirrors struct btrfs_scrub_progress (120 bytes): the
// cumulative scrub counters the kernel reports. The error counters
// (read/csum/verify/super/uncorrectable) being zero is the health signal.
type btrfsScrubProgress struct {
	DataExtentsScrubbed uint64
	TreeExtentsScrubbed uint64
	DataBytesScrubbed   uint64
	TreeBytesScrubbed   uint64
	ReadErrors          uint64
	CsumErrors          uint64
	VerifyErrors        uint64
	NoCsum              uint64
	CsumDiscards        uint64
	SuperErrors         uint64
	MallocErrors        uint64
	UncorrectableErrors uint64
	CorrectedErrors     uint64
	LastPhysical        uint64
	UnverifiedErrors    uint64
}

// btrfsIoctlScrubArgs mirrors struct btrfs_ioctl_scrub_args (1024 bytes).
// devid/start/end/flags are in; progress is out. The unused tail pads to 1k.
type btrfsIoctlScrubArgs struct {
	Devid    uint64
	Start    uint64
	End      uint64
	Flags    uint64
	Progress btrfsScrubProgress
	Unused   [(1024 - 32 - 120) / 8]uint64
}

// btrfsBalanceArgs mirrors struct btrfs_balance_args (136 bytes, packed): the
// per-chunk-type filter/target spec. We expose the commonly used subset
// (profiles, usage, devid, target, flags) and model the C anonymous unions as
// the plain u64 they overlay.
//
// The C struct is __packed; on this layout every field is naturally aligned so
// the Go struct (no explicit packing) has the same size and offsets, which the
// unit tests pin.
type btrfsBalanceArgs struct {
	Profiles uint64
	Usage    uint64 // union { usage; { usage_min; usage_max } }
	Devid    uint64
	Pstart   uint64
	Pend     uint64
	Vstart   uint64
	Vend     uint64
	Target   uint64
	Flags    uint64
	Limit    uint64 // union { limit; { limit_min; limit_max } }
	Stripes  uint64 // stripes_min(u32) + stripes_max(u32)
	Unused   [6]uint64
}

// btrfsBalanceProgress mirrors struct btrfs_balance_progress (24 bytes).
type btrfsBalanceProgress struct {
	Expected   uint64 // estimated chunks to relocate
	Considered uint64 // chunks considered so far
	Completed  uint64 // chunks relocated so far
}

// btrfsIoctlBalanceArgs mirrors struct btrfs_ioctl_balance_args (1024 bytes).
// flags is in/out, state is out, the three per-type args are in/out, stat is
// out. The unused tail pads to 1k.
type btrfsIoctlBalanceArgs struct {
	Flags  uint64
	State  uint64
	Data   btrfsBalanceArgs
	Meta   btrfsBalanceArgs
	Sys    btrfsBalanceArgs
	Stat   btrfsBalanceProgress
	Unused [72]uint64
}

// BTRFS_IOC_* request numbers for the admin operations, derived from
// linux/btrfs.h. Trailing hex comments are the values produced by the C
// preprocessor on a 6.12 kernel and pinned in abi_admin_test.go.
var (
	// _IOW(0x94, 10, struct btrfs_ioctl_vol_args)
	BTRFS_IOC_ADD_DEV = iow(btrfsIoctlMagic, 10, unsafe.Sizeof(btrfsIoctlVolArgs{})) // 0x5000940a
	// _IOW(0x94, 11, struct btrfs_ioctl_vol_args)
	BTRFS_IOC_RM_DEV = iow(btrfsIoctlMagic, 11, unsafe.Sizeof(btrfsIoctlVolArgs{})) // 0x5000940b
	// _IOW(0x94, 58, struct btrfs_ioctl_vol_args_v2)
	BTRFS_IOC_RM_DEV_V2 = iow(btrfsIoctlMagic, 58, unsafe.Sizeof(btrfsIoctlVolArgsV2{})) // 0x5000943a
	// _IOWR(0x94, 30, struct btrfs_ioctl_dev_info_args)
	BTRFS_IOC_DEV_INFO = iowr(btrfsIoctlMagic, 30, unsafe.Sizeof(btrfsIoctlDevInfoArgs{})) // 0xd000941e
	// _IOR(0x94, 31, struct btrfs_ioctl_fs_info_args)
	BTRFS_IOC_FS_INFO = ior(btrfsIoctlMagic, 31, unsafe.Sizeof(btrfsIoctlFsInfoArgs{})) // 0x8400941f
	// _IOWR(0x94, 27, struct btrfs_ioctl_scrub_args)
	BTRFS_IOC_SCRUB = iowr(btrfsIoctlMagic, 27, unsafe.Sizeof(btrfsIoctlScrubArgs{})) // 0xc400941b
	// _IO(0x94, 28)
	BTRFS_IOC_SCRUB_CANCEL = io(btrfsIoctlMagic, 28) // 0x941c
	// _IOWR(0x94, 29, struct btrfs_ioctl_scrub_args)
	BTRFS_IOC_SCRUB_PROGRESS = iowr(btrfsIoctlMagic, 29, unsafe.Sizeof(btrfsIoctlScrubArgs{})) // 0xc400941d
	// _IOWR(0x94, 32, struct btrfs_ioctl_balance_args)
	BTRFS_IOC_BALANCE_V2 = iowr(btrfsIoctlMagic, 32, unsafe.Sizeof(btrfsIoctlBalanceArgs{})) // 0xc4009420
	// _IOW(0x94, 33, int)
	BTRFS_IOC_BALANCE_CTL = iow(btrfsIoctlMagic, 33, 4) // 0x40049421
	// _IOR(0x94, 34, struct btrfs_ioctl_balance_args)
	BTRFS_IOC_BALANCE_PROGRESS = ior(btrfsIoctlMagic, 34, unsafe.Sizeof(btrfsIoctlBalanceArgs{})) // 0x84009422
	// _IOWR(0x94, 17, struct btrfs_ioctl_search_args)
	BTRFS_IOC_TREE_SEARCH = iowr(btrfsIoctlMagic, 17, unsafe.Sizeof(btrfsIoctlSearchArgs{})) // 0xd0009411
	// _IOWR(0x94, 17, struct btrfs_ioctl_search_args_v2)
	BTRFS_IOC_TREE_SEARCH_V2 = iowr(btrfsIoctlMagic, 17, unsafe.Sizeof(btrfsIoctlSearchArgsV2Hdr{})) // 0xc0709411
)

// btrfsIoctlSearchArgs mirrors struct btrfs_ioctl_search_args (4096 bytes):
// the search key followed by a fixed 3992-byte result buffer. Used as the
// TREE_SEARCH (v1) fallback when TREE_SEARCH_V2 is unavailable.
type btrfsIoctlSearchArgs struct {
	Key btrfsIoctlSearchKey
	Buf [4096 - 104]byte
}
