// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

package btrfs

import "unsafe"

// This file extends abi.go / abi_send.go with the ABI needed to *replay* a
// btrfs send stream in userspace (receive-apply): the full set of send-stream
// command and attribute numbers (btrfs_send_cmd / btrfs_send_attr from the
// kernel's fs/btrfs/send.h), and the FICLONERANGE ioctl used to satisfy the
// stream's CLONE command.
//
// The command/attribute numbers are part of the on-stream wire contract (not
// the uapi linux/btrfs.h header); they are stable kernel constants. They are
// pinned in abi_receive_test.go. As elsewhere FICLONERANGE's number is
// recomputed from its _IOW encoding rather than hard-coded.

// Send-stream command numbers (enum btrfs_send_cmd, fs/btrfs/send.h). The
// boundary commands (UNSPEC/SUBVOL/SNAPSHOT/END) are already named in
// abi_send.go for the introspection iterator; the remainder are the filesystem
// operations a receiver must apply. v1 streams (the only version a default
// `btrfs send` emits) use commands 0..22 (END=21 is the terminator, with UTIMES
// at 20 and UPDATE_EXTENT at 22); commands 23..26 are the v2 encoded-write
// additions, named here so the decoder can recognise and refuse them explicitly.
const (
	sendCmdMkfile       = 3  // BTRFS_SEND_C_MKFILE
	sendCmdMkdir        = 4  // BTRFS_SEND_C_MKDIR
	sendCmdMknod        = 5  // BTRFS_SEND_C_MKNOD
	sendCmdMkfifo       = 6  // BTRFS_SEND_C_MKFIFO
	sendCmdMksock       = 7  // BTRFS_SEND_C_MKSOCK
	sendCmdSymlink      = 8  // BTRFS_SEND_C_SYMLINK
	sendCmdRename       = 9  // BTRFS_SEND_C_RENAME
	sendCmdLink         = 10 // BTRFS_SEND_C_LINK
	sendCmdUnlink       = 11 // BTRFS_SEND_C_UNLINK
	sendCmdRmdir        = 12 // BTRFS_SEND_C_RMDIR
	sendCmdSetXattr     = 13 // BTRFS_SEND_C_SET_XATTR
	sendCmdRemoveXattr  = 14 // BTRFS_SEND_C_REMOVE_XATTR
	sendCmdWrite        = 15 // BTRFS_SEND_C_WRITE
	sendCmdClone        = 16 // BTRFS_SEND_C_CLONE
	sendCmdTruncate     = 17 // BTRFS_SEND_C_TRUNCATE
	sendCmdChmod        = 18 // BTRFS_SEND_C_CHMOD
	sendCmdChown        = 19 // BTRFS_SEND_C_CHOWN
	sendCmdUtimes       = 20 // BTRFS_SEND_C_UTIMES (END=21 follows in the enum)
	sendCmdUpdateExtent = 22 // BTRFS_SEND_C_UPDATE_EXTENT

	// v2 (encoded/compressed) write commands; recognised but not applied.
	sendCmdEncodedWrite = 23 // BTRFS_SEND_C_ENCODED_WRITE
	sendCmdFallocate    = 24 // BTRFS_SEND_C_FALLOCATE
	sendCmdSetFileXattr = 25 // BTRFS_SEND_C_SETFLAGS (v2, fileattr)
	sendCmdEnableVerity = 26 // BTRFS_SEND_C_ENABLE_VERITY (v2)
)

// Send-stream attribute numbers (enum btrfs_send_attr, fs/btrfs/send.h). Each
// TLV inside a command record is keyed by one of these. We name every
// attribute a v1 stream can carry; the decoder ignores any it does not need.
const (
	sendAUUID          = 1  // BTRFS_SEND_A_UUID
	sendACtransid      = 2  // BTRFS_SEND_A_CTRANSID
	sendAIno           = 3  // BTRFS_SEND_A_INO
	sendASize          = 4  // BTRFS_SEND_A_SIZE
	sendAMode          = 5  // BTRFS_SEND_A_MODE
	sendAUID           = 6  // BTRFS_SEND_A_UID
	sendAGID           = 7  // BTRFS_SEND_A_GID
	sendARdev          = 8  // BTRFS_SEND_A_RDEV
	sendACtime         = 9  // BTRFS_SEND_A_CTIME
	sendAMtime         = 10 // BTRFS_SEND_A_MTIME
	sendAAtime         = 11 // BTRFS_SEND_A_ATIME
	sendAOtime         = 12 // BTRFS_SEND_A_OTIME
	sendAXattrName     = 13 // BTRFS_SEND_A_XATTR_NAME
	sendAXattrData     = 14 // BTRFS_SEND_A_XATTR_DATA
	sendAPath          = 15 // BTRFS_SEND_A_PATH
	sendAPathTo        = 16 // BTRFS_SEND_A_PATH_TO
	sendAPathLink      = 17 // BTRFS_SEND_A_PATH_LINK
	sendAFileOffset    = 18 // BTRFS_SEND_A_FILE_OFFSET
	sendAData          = 19 // BTRFS_SEND_A_DATA
	sendACloneUUID     = 20 // BTRFS_SEND_A_CLONE_UUID
	sendACloneCtransid = 21 // BTRFS_SEND_A_CLONE_CTRANSID
	sendAClonePath     = 22 // BTRFS_SEND_A_CLONE_PATH
	sendACloneOffset   = 23 // BTRFS_SEND_A_CLONE_OFFSET
	sendACloneLen      = 24 // BTRFS_SEND_A_CLONE_LEN
)

// fileCloneRange mirrors struct file_clone_range (linux/fs.h, 32 bytes), the
// argument to FICLONERANGE:
//
//	struct file_clone_range { __s64 src_fd; __u64 src_offset;
//	                          __u64 src_length; __u64 dest_offset; };
//
// FICLONERANGE reflinks src_length bytes from src_fd@src_offset into the
// destination fd at dest_offset (a reference-counted copy: no data is moved).
// btrfs receive uses it to satisfy the stream's CLONE command.
type fileCloneRange struct {
	SrcFd     int64
	SrcOffset uint64
	SrcLength uint64
	DestOff   uint64
}

// FICLONERANGE is the generic VFS reflink ioctl, _IOW(0x94, 13,
// struct file_clone_range). Despite sharing magic 0x94 with btrfs it is a
// filesystem-independent VFS ioctl. Recomputed from its encoding here.
var FICLONERANGE = iow(btrfsIoctlMagic, 13, unsafe.Sizeof(fileCloneRange{})) // 0x4020940d
