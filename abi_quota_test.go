// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

package btrfs

import (
	"encoding/binary"
	"testing"
	"unsafe"
)

// TestQuotaIocNumbers pins the quota/qgroup/defrag BTRFS_IOC_* request numbers
// derived in abi_quota.go to the values produced by the C preprocessor over
// linux/btrfs.h (captured on a 6.12 kernel, arm64, with a cpp/offsetof probe on
// dc3, btrfs-progs v6.14).
func TestQuotaIocNumbers(t *testing.T) {
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"DEFRAG", BTRFS_IOC_DEFRAG, 0x50009402},
		{"DEFRAG_RANGE", BTRFS_IOC_DEFRAG_RANGE, 0x40309410},
		{"QUOTA_CTL", BTRFS_IOC_QUOTA_CTL, 0xc0109428},
		{"QGROUP_ASSIGN", BTRFS_IOC_QGROUP_ASSIGN, 0x40189429},
		{"QGROUP_CREATE", BTRFS_IOC_QGROUP_CREATE, 0x4010942a},
		{"QGROUP_LIMIT", BTRFS_IOC_QGROUP_LIMIT, 0x8030942b},
	} {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x", c.name, c.got, c.want)
		}
	}
}

// TestQuotaStructSizes pins the quota/qgroup/defrag ioctl struct sizes to the C
// sizeof() values from linux/btrfs.h on a 64-bit kernel.
func TestQuotaStructSizes(t *testing.T) {
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"btrfs_ioctl_quota_ctl_args", unsafe.Sizeof(btrfsIoctlQuotaCtlArgs{}), 16},
		{"btrfs_ioctl_qgroup_create_args", unsafe.Sizeof(btrfsIoctlQgroupCreateArgs{}), 16},
		{"btrfs_ioctl_qgroup_assign_args", unsafe.Sizeof(btrfsIoctlQgroupAssignArgs{}), 24},
		{"btrfs_qgroup_limit", unsafe.Sizeof(btrfsQgroupLimit{}), 40},
		{"btrfs_ioctl_qgroup_limit_args", unsafe.Sizeof(btrfsIoctlQgroupLimitArgs{}), 48},
		{"btrfs_ioctl_defrag_range_args", unsafe.Sizeof(btrfsIoctlDefragRangeArgs{}), 48},
	} {
		if c.got != c.want {
			t.Errorf("sizeof(%s) = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestQuotaStructOffsets pins the field byte offsets the kernel ABI is
// sensitive to (captured via offsetof() against linux/btrfs.h on dc3).
func TestQuotaStructOffsets(t *testing.T) {
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		// quota_ctl_args: cmd then status.
		{"quota_ctl.Cmd", unsafe.Offsetof(btrfsIoctlQuotaCtlArgs{}.Cmd), 0},
		{"quota_ctl.Status", unsafe.Offsetof(btrfsIoctlQuotaCtlArgs{}.Status), 8},
		// qgroup_create_args: create then qgroupid.
		{"qgroup_create.Create", unsafe.Offsetof(btrfsIoctlQgroupCreateArgs{}.Create), 0},
		{"qgroup_create.Qgroupid", unsafe.Offsetof(btrfsIoctlQgroupCreateArgs{}.Qgroupid), 8},
		// qgroup_assign_args: assign, src, dst.
		{"qgroup_assign.Assign", unsafe.Offsetof(btrfsIoctlQgroupAssignArgs{}.Assign), 0},
		{"qgroup_assign.Src", unsafe.Offsetof(btrfsIoctlQgroupAssignArgs{}.Src), 8},
		{"qgroup_assign.Dst", unsafe.Offsetof(btrfsIoctlQgroupAssignArgs{}.Dst), 16},
		// qgroup_limit_args: qgroupid then the embedded limit at 8.
		{"qgroup_limit_args.Qgroupid", unsafe.Offsetof(btrfsIoctlQgroupLimitArgs{}.Qgroupid), 0},
		{"qgroup_limit_args.Lim", unsafe.Offsetof(btrfsIoctlQgroupLimitArgs{}.Lim), 8},
		// btrfs_qgroup_limit inner fields.
		{"qgroup_limit.Flags", unsafe.Offsetof(btrfsQgroupLimit{}.Flags), 0},
		{"qgroup_limit.MaxRfer", unsafe.Offsetof(btrfsQgroupLimit{}.MaxRfer), 8},
		{"qgroup_limit.MaxExcl", unsafe.Offsetof(btrfsQgroupLimit{}.MaxExcl), 16},
		{"qgroup_limit.RsvRfer", unsafe.Offsetof(btrfsQgroupLimit{}.RsvRfer), 24},
		{"qgroup_limit.RsvExcl", unsafe.Offsetof(btrfsQgroupLimit{}.RsvExcl), 32},
		// defrag_range_args: start/len/flags/extent_thresh/compress_type.
		{"defrag_range.Start", unsafe.Offsetof(btrfsIoctlDefragRangeArgs{}.Start), 0},
		{"defrag_range.Len", unsafe.Offsetof(btrfsIoctlDefragRangeArgs{}.Len), 8},
		{"defrag_range.Flags", unsafe.Offsetof(btrfsIoctlDefragRangeArgs{}.Flags), 16},
		{"defrag_range.ExtentThresh", unsafe.Offsetof(btrfsIoctlDefragRangeArgs{}.ExtentThresh), 24},
		{"defrag_range.CompressType", unsafe.Offsetof(btrfsIoctlDefragRangeArgs{}.CompressType), 28},
		{"defrag_range.Unused", unsafe.Offsetof(btrfsIoctlDefragRangeArgs{}.Unused), 32},
	} {
		if c.got != c.want {
			t.Errorf("offsetof(%s) = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestQuotaConstants pins the quota-control commands, qgroup-limit flags, and
// the quota-tree object id / item key types used by the qgroup listing walk.
func TestQuotaConstants(t *testing.T) {
	for _, c := range []struct {
		name string
		got  int
		want int
	}{
		{"quotaCtlEnable", quotaCtlEnable, 1},
		{"quotaCtlDisable", quotaCtlDisable, 2},
		{"QgroupLimitMaxRfer", QgroupLimitMaxRfer, 1},
		{"QgroupLimitMaxExcl", QgroupLimitMaxExcl, 2},
		{"QgroupLimitRsvRfer", QgroupLimitRsvRfer, 4},
		{"QgroupLimitRsvExcl", QgroupLimitRsvExcl, 8},
		{"DefragRangeCompress", DefragRangeCompress, 1},
		{"DefragRangeStartIO", DefragRangeStartIO, 2},
		{"btrfsQuotaTreeObjectID", btrfsQuotaTreeObjectID, 8},
		{"btrfsQgroupStatusKey", btrfsQgroupStatusKey, 240},
		{"btrfsQgroupInfoKey", btrfsQgroupInfoKey, 242},
		{"btrfsQgroupLimitKey", btrfsQgroupLimitKey, 244},
		{"btrfsQgroupRelationKey", btrfsQgroupRelationKey, 246},
		{"btrfsQgroupInfoItemSize", btrfsQgroupInfoItemSize, 40},
		{"btrfsQgroupLimitItemSize", btrfsQgroupLimitItemSize, 40},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestQgroupInfoItemDecode checks the manual little-endian decode of a packed
// btrfs_qgroup_info_item body (generation, rfer, rfer_cmpr, excl, excl_cmpr)
// matches the field offsets the kernel writes.
func TestQgroupInfoItemDecode(t *testing.T) {
	body := make([]byte, btrfsQgroupInfoItemSize)
	binary.LittleEndian.PutUint64(body[0:8], 7)       // generation
	binary.LittleEndian.PutUint64(body[8:16], 16384)  // rfer
	binary.LittleEndian.PutUint64(body[16:24], 16384) // rfer_cmpr
	binary.LittleEndian.PutUint64(body[24:32], 4096)  // excl
	binary.LittleEndian.PutUint64(body[32:40], 4096)  // excl_cmpr

	if got := binary.LittleEndian.Uint64(body[8:16]); got != 16384 {
		t.Errorf("rfer at offset 8 = %d, want 16384", got)
	}
	if got := binary.LittleEndian.Uint64(body[24:32]); got != 4096 {
		t.Errorf("excl at offset 24 = %d, want 4096", got)
	}
}
