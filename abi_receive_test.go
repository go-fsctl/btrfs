// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

package btrfs

import (
	"testing"
	"unsafe"
)

// TestReceiveCmdNumbers pins the send-stream command numbers (enum
// btrfs_send_cmd, fs/btrfs/send.h) the receiver dispatches on. These are part
// of the on-stream wire contract and are stable across kernels.
func TestReceiveCmdNumbers(t *testing.T) {
	for _, c := range []struct {
		name string
		got  int
		want int
	}{
		{"SUBVOL", btrfsSendCmdSubvol, 1},
		{"SNAPSHOT", btrfsSendCmdSnapshot, 2},
		{"MKFILE", sendCmdMkfile, 3},
		{"MKDIR", sendCmdMkdir, 4},
		{"MKNOD", sendCmdMknod, 5},
		{"MKFIFO", sendCmdMkfifo, 6},
		{"MKSOCK", sendCmdMksock, 7},
		{"SYMLINK", sendCmdSymlink, 8},
		{"RENAME", sendCmdRename, 9},
		{"LINK", sendCmdLink, 10},
		{"UNLINK", sendCmdUnlink, 11},
		{"RMDIR", sendCmdRmdir, 12},
		{"SET_XATTR", sendCmdSetXattr, 13},
		{"REMOVE_XATTR", sendCmdRemoveXattr, 14},
		{"WRITE", sendCmdWrite, 15},
		{"CLONE", sendCmdClone, 16},
		{"TRUNCATE", sendCmdTruncate, 17},
		{"CHMOD", sendCmdChmod, 18},
		{"CHOWN", sendCmdChown, 19},
		{"UTIMES", sendCmdUtimes, 20},
		{"END", btrfsSendCmdEnd, 21},
		{"UPDATE_EXTENT", sendCmdUpdateExtent, 22},
		{"ENCODED_WRITE", sendCmdEncodedWrite, 23},
		{"FALLOCATE", sendCmdFallocate, 24},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestReceiveAttrNumbers pins the send-stream attribute numbers (enum
// btrfs_send_attr, fs/btrfs/send.h) the TLV decoder keys on.
func TestReceiveAttrNumbers(t *testing.T) {
	for _, c := range []struct {
		name string
		got  int
		want int
	}{
		{"UUID", sendAUUID, 1},
		{"CTRANSID", sendACtransid, 2},
		{"INO", sendAIno, 3},
		{"SIZE", sendASize, 4},
		{"MODE", sendAMode, 5},
		{"UID", sendAUID, 6},
		{"GID", sendAGID, 7},
		{"RDEV", sendARdev, 8},
		{"CTIME", sendACtime, 9},
		{"MTIME", sendAMtime, 10},
		{"ATIME", sendAAtime, 11},
		{"OTIME", sendAOtime, 12},
		{"XATTR_NAME", sendAXattrName, 13},
		{"XATTR_DATA", sendAXattrData, 14},
		{"PATH", sendAPath, 15},
		{"PATH_TO", sendAPathTo, 16},
		{"PATH_LINK", sendAPathLink, 17},
		{"FILE_OFFSET", sendAFileOffset, 18},
		{"DATA", sendAData, 19},
		{"CLONE_UUID", sendACloneUUID, 20},
		{"CLONE_CTRANSID", sendACloneCtransid, 21},
		{"CLONE_PATH", sendAClonePath, 22},
		{"CLONE_OFFSET", sendACloneOffset, 23},
		{"CLONE_LEN", sendACloneLen, 24},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestReceiveIocNumbers pins FICLONERANGE, recomputed from its _IOW encoding,
// to the value produced by the C preprocessor over linux/fs.h.
func TestReceiveIocNumbers(t *testing.T) {
	if FICLONERANGE != 0x4020940d {
		t.Errorf("FICLONERANGE = %#x, want 0x4020940d", FICLONERANGE)
	}
}

// TestReceiveStructSizes pins the file_clone_range size and the on-stream TLV
// header / timespec widths.
func TestReceiveStructSizes(t *testing.T) {
	if got := unsafe.Sizeof(fileCloneRange{}); got != 32 {
		t.Errorf("sizeof(file_clone_range) = %d, want 32", got)
	}
	if tlvHeaderSize != 4 {
		t.Errorf("tlvHeaderSize = %d, want 4", tlvHeaderSize)
	}
	if sendTimespecSize != 12 {
		t.Errorf("sendTimespecSize = %d, want 12", sendTimespecSize)
	}
}
