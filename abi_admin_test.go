// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

package btrfs

import (
	"encoding/binary"
	"testing"
	"unsafe"
)

// TestAdminIocNumbers pins the admin BTRFS_IOC_* request numbers derived in
// abi_admin.go to the values produced by the C preprocessor over linux/btrfs.h
// (captured on a 6.12 kernel, arm64, with a cpp/offsetof probe on dc3).
func TestAdminIocNumbers(t *testing.T) {
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"ADD_DEV", BTRFS_IOC_ADD_DEV, 0x5000940a},
		{"RM_DEV", BTRFS_IOC_RM_DEV, 0x5000940b},
		{"RM_DEV_V2", BTRFS_IOC_RM_DEV_V2, 0x5000943a},
		{"DEV_INFO", BTRFS_IOC_DEV_INFO, 0xd000941e},
		{"FS_INFO", BTRFS_IOC_FS_INFO, 0x8400941f},
		{"SCRUB", BTRFS_IOC_SCRUB, 0xc400941b},
		{"SCRUB_CANCEL", BTRFS_IOC_SCRUB_CANCEL, 0x941c},
		{"SCRUB_PROGRESS", BTRFS_IOC_SCRUB_PROGRESS, 0xc400941d},
		{"BALANCE_V2", BTRFS_IOC_BALANCE_V2, 0xc4009420},
		{"BALANCE_CTL", BTRFS_IOC_BALANCE_CTL, 0x40049421},
		{"BALANCE_PROGRESS", BTRFS_IOC_BALANCE_PROGRESS, 0x84009422},
		{"TREE_SEARCH", BTRFS_IOC_TREE_SEARCH, 0xd0009411},
		{"TREE_SEARCH_V2", BTRFS_IOC_TREE_SEARCH_V2, 0xc0709411},
	} {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x", c.name, c.got, c.want)
		}
	}
}

// TestAdminStructSizes pins the admin ioctl struct sizes to the C sizeof()
// values from linux/btrfs.h on a 64-bit kernel.
func TestAdminStructSizes(t *testing.T) {
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"btrfs_ioctl_search_key", unsafe.Sizeof(btrfsIoctlSearchKey{}), 104},
		{"btrfs_ioctl_search_header", unsafe.Sizeof(btrfsIoctlSearchHeader{}), 32},
		{"btrfs_ioctl_search_args", unsafe.Sizeof(btrfsIoctlSearchArgs{}), 4096},
		{"btrfs_ioctl_search_args_v2(head)", unsafe.Sizeof(btrfsIoctlSearchArgsV2Hdr{}), 112},
		{"btrfs_ioctl_dev_info_args", unsafe.Sizeof(btrfsIoctlDevInfoArgs{}), 4096},
		{"btrfs_ioctl_fs_info_args", unsafe.Sizeof(btrfsIoctlFsInfoArgs{}), 1024},
		{"btrfs_scrub_progress", unsafe.Sizeof(btrfsScrubProgress{}), 120},
		{"btrfs_ioctl_scrub_args", unsafe.Sizeof(btrfsIoctlScrubArgs{}), 1024},
		{"btrfs_balance_args", unsafe.Sizeof(btrfsBalanceArgs{}), 136},
		{"btrfs_balance_progress", unsafe.Sizeof(btrfsBalanceProgress{}), 24},
		{"btrfs_ioctl_balance_args", unsafe.Sizeof(btrfsIoctlBalanceArgs{}), 1024},
	} {
		if c.got != c.want {
			t.Errorf("sizeof(%s) = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestAdminStructOffsets pins the field byte offsets the kernel ABI is
// sensitive to (captured via offsetof() against linux/btrfs.h on dc3).
func TestAdminStructOffsets(t *testing.T) {
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		// search_key: the type fields and nr_items must land where the kernel
		// expects, or the search range/result count is misread.
		{"search_key.MinType", unsafe.Offsetof(btrfsIoctlSearchKey{}.MinType), 56},
		{"search_key.NrItems", unsafe.Offsetof(btrfsIoctlSearchKey{}.NrItems), 64},
		// search_args_v2 head: key then buf_size; buf[] starts at 112.
		{"search_args_v2.BufSize", unsafe.Offsetof(btrfsIoctlSearchArgsV2Hdr{}.BufSize), 104},
		// dev_info_args: bytes_used/total_bytes/fsid placement and the 4k path.
		{"dev_info.BytesUsed", unsafe.Offsetof(btrfsIoctlDevInfoArgs{}.BytesUsed), 24},
		{"dev_info.TotalBytes", unsafe.Offsetof(btrfsIoctlDevInfoArgs{}.TotalBytes), 32},
		{"dev_info.FSID", unsafe.Offsetof(btrfsIoctlDevInfoArgs{}.FSID), 40},
		{"dev_info.Path", unsafe.Offsetof(btrfsIoctlDevInfoArgs{}.Path), 3072},
		// fs_info_args: fsid/nodesize/csum_type/flags/generation/metadata_uuid.
		{"fs_info.FSID", unsafe.Offsetof(btrfsIoctlFsInfoArgs{}.FSID), 16},
		{"fs_info.Nodesize", unsafe.Offsetof(btrfsIoctlFsInfoArgs{}.Nodesize), 32},
		{"fs_info.CsumType", unsafe.Offsetof(btrfsIoctlFsInfoArgs{}.CsumType), 44},
		{"fs_info.Flags", unsafe.Offsetof(btrfsIoctlFsInfoArgs{}.Flags), 48},
		{"fs_info.Generation", unsafe.Offsetof(btrfsIoctlFsInfoArgs{}.Generation), 56},
		{"fs_info.MetadataUUID", unsafe.Offsetof(btrfsIoctlFsInfoArgs{}.MetadataUUID), 64},
		// scrub_args: progress block.
		{"scrub.Progress", unsafe.Offsetof(btrfsIoctlScrubArgs{}.Progress), 32},
		// balance_args (packed in C, naturally aligned here): flags/target.
		{"balargs.Target", unsafe.Offsetof(btrfsBalanceArgs{}.Target), 56},
		{"balargs.Flags", unsafe.Offsetof(btrfsBalanceArgs{}.Flags), 64},
		// ioctl_balance_args: the three per-type args and the progress block.
		{"ibal.Data", unsafe.Offsetof(btrfsIoctlBalanceArgs{}.Data), 16},
		{"ibal.Meta", unsafe.Offsetof(btrfsIoctlBalanceArgs{}.Meta), 152},
		{"ibal.Sys", unsafe.Offsetof(btrfsIoctlBalanceArgs{}.Sys), 288},
		{"ibal.Stat", unsafe.Offsetof(btrfsIoctlBalanceArgs{}.Stat), 424},
	} {
		if c.got != c.want {
			t.Errorf("offsetof(%s) = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestKeyTypeConstants pins the root-tree key type numbers and tree ids used
// by the subvolume-listing walk.
func TestKeyTypeConstants(t *testing.T) {
	if btrfsRootRefKey != 156 {
		t.Errorf("ROOT_REF_KEY = %d, want 156", btrfsRootRefKey)
	}
	if btrfsRootBackrefKey != 144 {
		t.Errorf("ROOT_BACKREF_KEY = %d, want 144", btrfsRootBackrefKey)
	}
	if btrfsRootItemKey != 132 {
		t.Errorf("ROOT_ITEM_KEY = %d, want 132", btrfsRootItemKey)
	}
	if btrfsRootTreeObjectID != 1 {
		t.Errorf("ROOT_TREE_OBJECTID = %d, want 1", btrfsRootTreeObjectID)
	}
	if btrfsRootRefSize != 18 {
		t.Errorf("sizeof(btrfs_root_ref) = %d, want 18", btrfsRootRefSize)
	}
}

// TestBalanceFlagBits pins the balance type/control flag values.
func TestBalanceFlagBits(t *testing.T) {
	for _, c := range []struct {
		name string
		got  int
		want int
	}{
		{"BalanceData", BalanceData, 1},
		{"BalanceSystem", BalanceSystem, 2},
		{"BalanceMetadata", BalanceMetadata, 4},
		{"BalanceForce", BalanceForce, 8},
		{"BalanceArgsUsage", BalanceArgsUsage, 2},
		{"ScrubReadonly", ScrubReadonly, 1},
		{"balanceCtlCancel", balanceCtlCancel, 2},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestSearchKeyEncoding checks that a search key constructed for the
// root-tree ROOT_REF walk lays out the type-filter fields exactly where the
// kernel reads them: min_type/max_type as 32-bit words at offsets 56/60 and
// nr_items at 64. We build the struct, then read it back through its raw bytes
// the way the kernel would.
func TestSearchKeyEncoding(t *testing.T) {
	key := btrfsIoctlSearchKey{
		TreeID:      btrfsRootTreeObjectID,
		MaxObjectid: ^uint64(0),
		MaxOffset:   ^uint64(0),
		MaxTransid:  ^uint64(0),
		MinType:     btrfsRootRefKey,
		MaxType:     btrfsRootRefKey,
		NrItems:     4096,
	}
	raw := (*[unsafe.Sizeof(btrfsIoctlSearchKey{})]byte)(unsafe.Pointer(&key))[:]

	if got := binary.LittleEndian.Uint64(raw[0:8]); got != btrfsRootTreeObjectID {
		t.Errorf("tree_id at offset 0 = %d, want %d", got, btrfsRootTreeObjectID)
	}
	if got := binary.LittleEndian.Uint32(raw[56:60]); got != btrfsRootRefKey {
		t.Errorf("min_type at offset 56 = %d, want %d", got, btrfsRootRefKey)
	}
	if got := binary.LittleEndian.Uint32(raw[60:64]); got != btrfsRootRefKey {
		t.Errorf("max_type at offset 60 = %d, want %d", got, btrfsRootRefKey)
	}
	if got := binary.LittleEndian.Uint32(raw[64:68]); got != 4096 {
		t.Errorf("nr_items at offset 64 = %d, want 4096", got)
	}
}

// TestRootRefDecode checks the manual little-endian decode of a packed
// btrfs_root_ref item body (dirid, sequence, name_len) followed by the name.
func TestRootRefDecode(t *testing.T) {
	// Build an 18-byte packed root_ref + name "child".
	name := "child"
	body := make([]byte, btrfsRootRefSize+len(name))
	binary.LittleEndian.PutUint64(body[0:8], 256) // dirid
	binary.LittleEndian.PutUint64(body[8:16], 2)  // sequence
	binary.LittleEndian.PutUint16(body[16:18], uint16(len(name)))
	copy(body[btrfsRootRefSize:], name)

	gotLen := int(binary.LittleEndian.Uint16(body[16:18]))
	if gotLen != len(name) {
		t.Fatalf("name_len = %d, want %d", gotLen, len(name))
	}
	if got := string(body[btrfsRootRefSize : btrfsRootRefSize+gotLen]); got != name {
		t.Errorf("name = %q, want %q", got, name)
	}
}
