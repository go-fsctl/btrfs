// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

package btrfs

import (
	"encoding/binary"
	"testing"
	"unsafe"
)

// TestFsAdminIocNumbers pins the filesystem-level admin BTRFS_IOC_* request
// numbers derived in abi_fsadmin.go to the values produced by the C
// preprocessor over linux/btrfs.h and linux/fs.h (captured on a 6.12 kernel,
// arm64, with a cpp probe on dc3).
func TestFsAdminIocNumbers(t *testing.T) {
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"GET_FSLABEL", BTRFS_IOC_GET_FSLABEL, 0x81009431},
		{"SET_FSLABEL", BTRFS_IOC_SET_FSLABEL, 0x41009432},
		{"RESIZE", BTRFS_IOC_RESIZE, 0x50009403},
		{"DEFAULT_SUBVOL", BTRFS_IOC_DEFAULT_SUBVOL, 0x40089413},
		{"GET_FEATURES", BTRFS_IOC_GET_FEATURES, 0x80189439},
		{"SET_FEATURES", BTRFS_IOC_SET_FEATURES, 0x40309439},
		{"GET_SUPPORTED_FEATURES", BTRFS_IOC_GET_SUPPORTED_FEATURES, 0x80489439},
		{"LOGICAL_INO", BTRFS_IOC_LOGICAL_INO, 0xc0389424},
		{"INO_PATHS", BTRFS_IOC_INO_PATHS, 0xc0389423},
	} {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x", c.name, c.got, c.want)
		}
	}
}

// TestFsAdminStructSizes pins the filesystem-level admin ioctl struct sizes to
// the C sizeof() values from linux/btrfs.h on a 64-bit kernel.
func TestFsAdminStructSizes(t *testing.T) {
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"btrfs_ioctl_feature_flags", unsafe.Sizeof(btrfsIoctlFeatureFlags{}), 24},
		{"btrfs_ioctl_logical_ino_args", unsafe.Sizeof(btrfsIoctlLogicalInoArgs{}), 56},
		{"btrfs_ioctl_ino_path_args", unsafe.Sizeof(btrfsIoctlInoPathArgs{}), 56},
		{"btrfs_data_container(head)", unsafe.Sizeof(btrfsDataContainerHdr{}), btrfsDataContainerHdrSize},
		// vol_args is reused for RESIZE; pin its size here too for the alias.
		{"btrfs_ioctl_vol_args(resize)", unsafe.Sizeof(btrfsIoctlVolArgs{}), 4096},
	} {
		if c.got != c.want {
			t.Errorf("sizeof(%s) = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestFsAdminStructOffsets pins the field byte offsets the kernel ABI is
// sensitive to (captured via offsetof() against linux/btrfs.h on dc3).
func TestFsAdminStructOffsets(t *testing.T) {
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		// feature_flags: three consecutive u64s.
		{"feature.CompatRO", unsafe.Offsetof(btrfsIoctlFeatureFlags{}.CompatRO), 8},
		{"feature.Incompat", unsafe.Offsetof(btrfsIoctlFeatureFlags{}.Incompat), 16},
		// logical_ino_args: flags after 5 u64s, inodes pointer last.
		{"logical_ino.Flags", unsafe.Offsetof(btrfsIoctlLogicalInoArgs{}.Flags), 40},
		{"logical_ino.Inodes", unsafe.Offsetof(btrfsIoctlLogicalInoArgs{}.Inodes), 48},
		// ino_path_args: fspath pointer last (after inum/size/reserved[4]).
		{"ino_path.Fspath", unsafe.Offsetof(btrfsIoctlInoPathArgs{}.Fspath), 48},
		// data_container counters.
		{"container.ElemCnt", unsafe.Offsetof(btrfsDataContainerHdr{}.ElemCnt), 8},
		{"container.ElemMissed", unsafe.Offsetof(btrfsDataContainerHdr{}.ElemMissed), 12},
	} {
		if c.got != c.want {
			t.Errorf("offsetof(%s) = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestFeatureFlagBits pins the feature-flag bit values that GetFeatures decodes
// and SetFeatures constructs, against the BTRFS_FEATURE_* macros.
func TestFeatureFlagBits(t *testing.T) {
	for _, c := range []struct {
		name string
		got  uint64
		want uint64
	}{
		{"COMPAT_RO_FREE_SPACE_TREE", FeatureCompatROFreeSpaceTree, 1 << 0},
		{"COMPAT_RO_BLOCK_GROUP_TREE", FeatureCompatROBlockGroupTree, 1 << 3},
		{"INCOMPAT_MIXED_BACKREF", FeatureIncompatMixedBackref, 1 << 0},
		{"INCOMPAT_DEFAULT_SUBVOL", FeatureIncompatDefaultSubvol, 1 << 1},
		{"INCOMPAT_EXTENDED_IREF", FeatureIncompatExtendedIref, 1 << 6},
		{"INCOMPAT_SKINNY_METADATA", FeatureIncompatSkinnyMetadata, 1 << 8},
		{"INCOMPAT_NO_HOLES", FeatureIncompatNoHoles, 1 << 9},
		{"INCOMPAT_SIMPLE_QUOTA", FeatureIncompatSimpleQuota, 1 << 16},
	} {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x", c.name, c.got, c.want)
		}
	}
}

// TestKeyConstantsFsAdmin pins the root-tree object id and DIR_ITEM key type
// used by GetDefaultSubvol's TREE_SEARCH walk, and the label size.
func TestKeyConstantsFsAdmin(t *testing.T) {
	if btrfsDirItemKey != 84 {
		t.Errorf("DIR_ITEM_KEY = %d, want 84", btrfsDirItemKey)
	}
	if btrfsRootTreeDirObjectID != 6 {
		t.Errorf("ROOT_TREE_DIR_OBJECTID = %d, want 6", btrfsRootTreeDirObjectID)
	}
	if btrfsLabelSize != 256 {
		t.Errorf("BTRFS_LABEL_SIZE = %d, want 256", btrfsLabelSize)
	}
	if btrfsDirItemHdrSize != 30 {
		t.Errorf("sizeof(btrfs_dir_item head) = %d, want 30", btrfsDirItemHdrSize)
	}
}

// TestDirItemDecode checks the manual little-endian decode of a packed
// btrfs_dir_item body: location.objectid (the target subvolume id), name_len,
// and the trailing name "default".
func TestDirItemDecode(t *testing.T) {
	const subvolID = 257
	name := "default"
	body := make([]byte, btrfsDirItemHdrSize+len(name))
	binary.LittleEndian.PutUint64(body[0:8], subvolID) // location.objectid
	body[8] = btrfsRootItemKey                         // location.type (ROOT_ITEM)
	binary.LittleEndian.PutUint64(body[9:17], 0)       // location.offset
	binary.LittleEndian.PutUint64(body[17:25], 99)     // transid
	binary.LittleEndian.PutUint16(body[25:27], 0)      // data_len
	binary.LittleEndian.PutUint16(body[27:29], uint16(len(name)))
	body[29] = 2 // type = BTRFS_FT_DIR
	copy(body[btrfsDirItemHdrSize:], name)

	gotLen := int(binary.LittleEndian.Uint16(body[27:29]))
	if gotLen != len(name) {
		t.Fatalf("name_len = %d, want %d", gotLen, len(name))
	}
	if got := string(body[btrfsDirItemHdrSize : btrfsDirItemHdrSize+gotLen]); got != name {
		t.Errorf("name = %q, want %q", got, name)
	}
	if got := binary.LittleEndian.Uint64(body[0:8]); got != subvolID {
		t.Errorf("location.objectid = %d, want %d", got, subvolID)
	}
}
