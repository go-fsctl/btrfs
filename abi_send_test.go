// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

package btrfs

import (
	"testing"
	"unsafe"
)

// TestSendIocNumbers pins the send / set-received BTRFS_IOC_* request numbers
// derived in abi_send.go to the values produced by the C preprocessor over
// linux/btrfs.h (captured on a 6.12 kernel, arm64, with a cpp/offsetof probe on
// dc3, btrfs-progs v6.14).
func TestSendIocNumbers(t *testing.T) {
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"SEND", BTRFS_IOC_SEND, 0x40489426},
		{"SET_RECEIVED_SUBVOL", BTRFS_IOC_SET_RECEIVED_SUBVOL, 0xc0c89425},
	} {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x", c.name, c.got, c.want)
		}
	}
}

// TestSendStructSizes pins the send ioctl struct sizes to the C sizeof() values
// from linux/btrfs.h on a 64-bit kernel.
func TestSendStructSizes(t *testing.T) {
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"btrfs_ioctl_send_args", unsafe.Sizeof(btrfsIoctlSendArgs{}), 72},
		{"btrfs_ioctl_received_subvol_args", unsafe.Sizeof(btrfsIoctlReceivedSubvolArgs{}), 200},
		{"btrfs_ioctl_timespec", unsafe.Sizeof(btrfsIoctlTimespec{}), 16},
	} {
		if c.got != c.want {
			t.Errorf("sizeof(%s) = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestSendStructOffsets pins the field byte offsets the kernel ABI is sensitive
// to (captured via offsetof() against linux/btrfs.h on dc3).
func TestSendStructOffsets(t *testing.T) {
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		// send_args: send_fd, clone_sources_count, clone_sources, parent_root,
		// flags, version.
		{"send.SendFd", unsafe.Offsetof(btrfsIoctlSendArgs{}.SendFd), 0},
		{"send.CloneSourcesCount", unsafe.Offsetof(btrfsIoctlSendArgs{}.CloneSourcesCount), 8},
		{"send.CloneSources", unsafe.Offsetof(btrfsIoctlSendArgs{}.CloneSources), 16},
		{"send.ParentRoot", unsafe.Offsetof(btrfsIoctlSendArgs{}.ParentRoot), 24},
		{"send.Flags", unsafe.Offsetof(btrfsIoctlSendArgs{}.Flags), 32},
		{"send.Version", unsafe.Offsetof(btrfsIoctlSendArgs{}.Version), 40},
		{"send.Reserved", unsafe.Offsetof(btrfsIoctlSendArgs{}.Reserved), 44},
		// received_subvol_args: uuid, stransid, rtransid, stime, rtime, flags.
		{"recv.UUID", unsafe.Offsetof(btrfsIoctlReceivedSubvolArgs{}.UUID), 0},
		{"recv.Stransid", unsafe.Offsetof(btrfsIoctlReceivedSubvolArgs{}.Stransid), 16},
		{"recv.Rtransid", unsafe.Offsetof(btrfsIoctlReceivedSubvolArgs{}.Rtransid), 24},
		{"recv.Stime", unsafe.Offsetof(btrfsIoctlReceivedSubvolArgs{}.Stime), 32},
		{"recv.Rtime", unsafe.Offsetof(btrfsIoctlReceivedSubvolArgs{}.Rtime), 48},
		{"recv.Flags", unsafe.Offsetof(btrfsIoctlReceivedSubvolArgs{}.Flags), 64},
		{"recv.Reserved", unsafe.Offsetof(btrfsIoctlReceivedSubvolArgs{}.Reserved), 72},
	} {
		if c.got != c.want {
			t.Errorf("offsetof(%s) = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestSendConstants pins the send flags and the stream-framing constants.
func TestSendConstants(t *testing.T) {
	for _, c := range []struct {
		name string
		got  int
		want int
	}{
		{"SendFlagNoFileData", SendFlagNoFileData, 1},
		{"SendFlagOmitStreamHeader", SendFlagOmitStreamHeader, 2},
		{"SendFlagOmitEndCmd", SendFlagOmitEndCmd, 4},
		{"SendFlagVersion", SendFlagVersion, 8},
		{"btrfsSendStreamMagicSize", btrfsSendStreamMagicSize, 13},
		{"btrfsSendStreamVersion", btrfsSendStreamVersion, 1},
		{"btrfsStreamHeaderSize", btrfsStreamHeaderSize, 17},
		{"btrfsCmdHeaderSize", btrfsCmdHeaderSize, 10},
		{"btrfsSendCmdEnd", btrfsSendCmdEnd, 20},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	if btrfsSendStreamMagic != "btrfs-stream" {
		t.Errorf("btrfsSendStreamMagic = %q, want %q", btrfsSendStreamMagic, "btrfs-stream")
	}
}
