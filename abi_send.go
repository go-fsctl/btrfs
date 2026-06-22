// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

package btrfs

import "unsafe"

// This file extends abi.go with the ABI for SEND-stream generation
// (BTRFS_IOC_SEND) and for marking a received subvolume
// (BTRFS_IOC_SET_RECEIVED_SUBVOL), plus the on-stream framing structures
// (btrfs_stream_header / btrfs_cmd_header) used to introspect a send stream.
//
// As elsewhere in the package the BTRFS_IOC_* numbers are recomputed in Go
// from the _IO/_IOR/_IOW/_IOWR encoding rather than hard-coded, and the C
// struct layouts are mirrored as Go structs whose sizes and offsets are pinned
// in abi_send_test.go against the values produced by linux/btrfs.h on a 6.12
// kernel (captured with a cpp/offsetof probe on dc3, btrfs-progs v6.14).

// Send flags (linux/btrfs.h), exported for SendOpts. NoFileData omits file
// data from the stream (metadata-only). The remaining flags are exposed for
// completeness; the kernel sets the stream version itself.
const (
	SendFlagNoFileData       = 1 << 0 // BTRFS_SEND_FLAG_NO_FILE_DATA
	SendFlagOmitStreamHeader = 1 << 1 // BTRFS_SEND_FLAG_OMIT_STREAM_HEADER
	SendFlagOmitEndCmd       = 1 << 2 // BTRFS_SEND_FLAG_OMIT_END_CMD
	SendFlagVersion          = 1 << 3 // BTRFS_SEND_FLAG_VERSION
)

// btrfsIoctlSendArgs mirrors struct btrfs_ioctl_send_args (72 bytes):
//
//	struct btrfs_ioctl_send_args {
//	  __s64 send_fd;              /* in: fd we write the stream to */
//	  __u64 clone_sources_count;  /* in */
//	  __u64 *clone_sources;       /* in: array of clone-source root ids */
//	  __u64 parent_root;          /* in: parent subvol root id (0 = full) */
//	  __u64 flags;                /* in: BTRFS_SEND_FLAG_* */
//	  __u32 version;              /* in: requested stream version (0 = kernel default) */
//	  __u8  reserved[28];         /* in: must be zero */
//	};
//
// CloneSources is a userspace pointer to a __u64[clone_sources_count]; the Go
// caller passes a *uint64 from a pinned slice's first element. Send issues the
// ioctl on the (read-only) source subvolume's fd and the kernel streams the
// send data out through send_fd.
type btrfsIoctlSendArgs struct {
	SendFd            int64
	CloneSourcesCount uint64
	CloneSources      *uint64
	ParentRoot        uint64
	Flags             uint64
	Version           uint32
	Reserved          [28]uint8
}

// btrfsIoctlReceivedSubvolArgs mirrors struct
// btrfs_ioctl_received_subvol_args (200 bytes):
//
//	struct btrfs_ioctl_received_subvol_args {
//	  char  uuid[BTRFS_UUID_SIZE];        /* in */
//	  __u64 stransid;                     /* in */
//	  __u64 rtransid;                     /* out */
//	  struct btrfs_ioctl_timespec stime;  /* in */
//	  struct btrfs_ioctl_timespec rtime;  /* out */
//	  __u64 flags;                        /* in */
//	  __u64 reserved[16];                 /* in */
//	};
//
// This is the ioctl `btrfs receive` issues last on a freshly received
// subvolume to stamp it with the sender's UUID/transid so future incremental
// receives can find it as a parent. rtransid/rtime are filled by the kernel.
type btrfsIoctlReceivedSubvolArgs struct {
	UUID     [btrfsUUIDSize]byte
	Stransid uint64
	Rtransid uint64
	Stime    btrfsIoctlTimespec
	Rtime    btrfsIoctlTimespec
	Flags    uint64
	Reserved [16]uint64
}

// Send-stream framing constants from fs/btrfs/send.h (kernel) /
// common/send.h (btrfs-progs). These are not part of the uapi header
// linux/btrfs.h; they describe the wire format the kernel writes to send_fd.
const (
	// btrfsSendStreamMagic is BTRFS_SEND_STREAM_MAGIC, the leading magic of a
	// send stream. The on-stream field is NUL-terminated and 13 bytes wide
	// (sizeof("btrfs-stream") including the terminator).
	btrfsSendStreamMagic     = "btrfs-stream"
	btrfsSendStreamMagicSize = 13 // sizeof("btrfs-stream") incl. NUL
	// btrfsSendStreamVersion is the default stream version the kernel emits.
	btrfsSendStreamVersion = 1

	// btrfsStreamHeaderSize is sizeof(struct btrfs_stream_header) packed:
	// char magic[13] + __le32 version.
	btrfsStreamHeaderSize = btrfsSendStreamMagicSize + 4 // 17
	// btrfsCmdHeaderSize is sizeof(struct btrfs_cmd_header) packed:
	// __le32 len + __le16 cmd + __le32 crc.
	btrfsCmdHeaderSize = 10
)

// Send-stream command numbers (btrfs_send_cmd) used by the lightweight TLV
// iterator for introspection. We only name the sentinel/boundary commands; the
// iterator surfaces every command's numeric id regardless.
const (
	btrfsSendCmdUnspec   = 0  // BTRFS_SEND_C_UNSPEC
	btrfsSendCmdSubvol   = 1  // BTRFS_SEND_C_SUBVOL
	btrfsSendCmdSnapshot = 2  // BTRFS_SEND_C_SNAPSHOT
	btrfsSendCmdEnd      = 21 // BTRFS_SEND_C_END (stream terminator in v1)
)

// BTRFS_IOC_* request numbers for send / set-received, derived from
// linux/btrfs.h. Trailing hex comments are the values produced by the C
// preprocessor on a 6.12 kernel and pinned in abi_send_test.go.
var (
	// _IOW(0x94, 38, struct btrfs_ioctl_send_args)
	BTRFS_IOC_SEND = iow(btrfsIoctlMagic, 38, unsafe.Sizeof(btrfsIoctlSendArgs{})) // 0x40489426
	// _IOWR(0x94, 37, struct btrfs_ioctl_received_subvol_args)
	BTRFS_IOC_SET_RECEIVED_SUBVOL = iowr(btrfsIoctlMagic, 37, unsafe.Sizeof(btrfsIoctlReceivedSubvolArgs{})) // 0xc0c89425
)
