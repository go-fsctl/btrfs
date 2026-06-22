// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

//go:build linux

package btrfs

import (
	"encoding/binary"
	"fmt"
	"runtime"
	"strings"
	"unsafe"
)

// This file implements the filesystem-level admin operations via BTRFS_IOC_*
// ioctls on a directory fd: label get/set, online resize, the default
// subvolume, the feature-flag ioctls, and the reverse extent->inode->path
// mapping. As with the rest of the package it is pure Go and never shells out
// to the btrfs CLI.

// GetLabel returns the filesystem label of the btrfs filesystem containing the
// open fd, via BTRFS_IOC_GET_FSLABEL (the generic FS_IOC_GETFSLABEL). The label
// is at most BTRFS_LABEL_SIZE-1 (255) bytes. fd may refer to the mount point or
// any file/directory on the filesystem.
func GetLabel(fd uintptr) (string, error) {
	var buf [btrfsLabelSize]byte
	if err := ioctlFd(fd, BTRFS_IOC_GET_FSLABEL, unsafe.Pointer(&buf[0])); err != nil {
		return "", fmt.Errorf("BTRFS_IOC_GET_FSLABEL: %w", err)
	}
	runtime.KeepAlive(&buf)
	return cstr(buf[:]), nil
}

// SetLabel sets the filesystem label of the btrfs filesystem containing the
// open fd, via BTRFS_IOC_SET_FSLABEL (the generic FS_IOC_SETFSLABEL). The label
// must fit in BTRFS_LABEL_SIZE-1 (255) bytes including no embedded NUL. The
// change is online and persisted; requires CAP_SYS_ADMIN (root).
func SetLabel(fd uintptr, label string) error {
	if strings.IndexByte(label, 0) >= 0 {
		return fmt.Errorf("SetLabel: label contains NUL")
	}
	var buf [btrfsLabelSize]byte
	if err := putName(buf[:], label); err != nil {
		return fmt.Errorf("SetLabel: %w", err)
	}
	if err := ioctlFd(fd, BTRFS_IOC_SET_FSLABEL, unsafe.Pointer(&buf[0])); err != nil {
		return fmt.Errorf("BTRFS_IOC_SET_FSLABEL: %w", err)
	}
	runtime.KeepAlive(&buf)
	return nil
}

// Resize grows or shrinks the btrfs filesystem containing the open fd, via
// BTRFS_IOC_RESIZE (btrfs_ioctl_vol_args; the size spec goes in args.name as a
// string the kernel parses). size accepts the same forms as `btrfs filesystem
// resize`:
//
//	"+1G"        grow by one gibibyte
//	"-512M"      shrink by 512 mebibytes
//	"max"        grow to fill the underlying device
//	"2:+1G"      apply the change to device id 2 (devid:size)
//
// A bare number is interpreted as an absolute byte size. Shrinking relocates
// any data living past the new boundary first, so this can block. Requires
// CAP_SYS_ADMIN (root).
func Resize(fd uintptr, size string) error {
	if size == "" {
		return fmt.Errorf("Resize: empty size")
	}
	var args btrfsIoctlVolArgs
	if err := putName(args.Name[:], size); err != nil {
		return fmt.Errorf("Resize: %w", err)
	}
	if err := ioctlFd(fd, BTRFS_IOC_RESIZE, unsafe.Pointer(&args)); err != nil {
		return fmt.Errorf("BTRFS_IOC_RESIZE %q: %w", size, err)
	}
	runtime.KeepAlive(&args)
	return nil
}

// SetDefaultSubvol makes the subvolume with id subvolid the default subvolume
// of the filesystem containing the open fd, via BTRFS_IOC_DEFAULT_SUBVOL (a
// single __u64). The default subvolume is the one mounted when no subvol=/
// subvolid= mount option is given. Requires CAP_SYS_ADMIN (root).
func SetDefaultSubvol(fd uintptr, subvolid uint64) error {
	id := subvolid
	if err := ioctlFd(fd, BTRFS_IOC_DEFAULT_SUBVOL, unsafe.Pointer(&id)); err != nil {
		return fmt.Errorf("BTRFS_IOC_DEFAULT_SUBVOL id=%d: %w", subvolid, err)
	}
	runtime.KeepAlive(&id)
	return nil
}

// GetDefaultSubvol returns the id of the default subvolume of the filesystem
// containing the open fd. There is no dedicated read ioctl, so it reads the
// "default" DIR_ITEM out of the root tree (objectid = ROOT_TREE_DIR_OBJECTID,
// type = DIR_ITEM_KEY) via the existing TREE_SEARCH helper and returns the
// subvolume id recorded in the directory item's location key. When no explicit
// default has been set the kernel points "default" at the FS tree, so this
// returns BTRFS_FS_TREE_OBJECTID (5). Requires privileges to read the root tree
// (typically root).
func GetDefaultSubvol(fd uintptr) (uint64, error) {
	var (
		found bool
		id    uint64
	)
	emit := func(hdr *btrfsIoctlSearchHeader, body []byte) error {
		if hdr.Type != btrfsDirItemKey || hdr.Objectid != btrfsRootTreeDirObjectID {
			return nil
		}
		if len(body) < btrfsDirItemHdrSize {
			return nil
		}
		// btrfs_dir_item (packed): disk_key.location is the first field:
		//   __le64 objectid (the target subvolume id) at [0:8],
		//   __u8 type at [8], __le64 offset at [9:17].
		// name_len lives at [27:29]; the name follows the 30-byte header.
		nameLen := int(binary.LittleEndian.Uint16(body[27:29]))
		nameStart := btrfsDirItemHdrSize
		if nameStart+nameLen > len(body) {
			nameLen = len(body) - nameStart
		}
		if string(body[nameStart:nameStart+nameLen]) != "default" {
			return nil
		}
		id = binary.LittleEndian.Uint64(body[0:8])
		found = true
		return nil
	}
	if err := searchTreeTypeRange(fd, btrfsRootTreeObjectID, btrfsDirItemKey, btrfsDirItemKey, emit); err != nil {
		return 0, fmt.Errorf("GetDefaultSubvol: %w", err)
	}
	if !found {
		// No explicit "default" dir item: the kernel falls back to the FS tree.
		return btrfsFSTreeObjectID, nil
	}
	return id, nil
}

// Features is the decoded set of btrfs feature flags reported by GetFeatures
// and GetSupportedFeatures. The three masks correspond to the three feature
// classes the kernel tracks. Names lists the symbolic BTRFS_FEATURE_* names of
// the known bits that are set, across all three masks, for convenient logging.
type Features struct {
	Compat   uint64   // compat_flags (no bits currently defined)
	CompatRO uint64   // compat_ro_flags
	Incompat uint64   // incompat_flags
	Names    []string // symbolic names of the known set bits
}

// featureName tables map a single set bit in each mask to its symbolic name.
var (
	featureCompatRONames = []struct {
		bit  uint64
		name string
	}{
		{FeatureCompatROFreeSpaceTree, "FREE_SPACE_TREE"},
		{FeatureCompatROFreeSpaceTreeValid, "FREE_SPACE_TREE_VALID"},
		{FeatureCompatROVerity, "VERITY"},
		{FeatureCompatROBlockGroupTree, "BLOCK_GROUP_TREE"},
	}
	featureIncompatNames = []struct {
		bit  uint64
		name string
	}{
		{FeatureIncompatMixedBackref, "MIXED_BACKREF"},
		{FeatureIncompatDefaultSubvol, "DEFAULT_SUBVOL"},
		{FeatureIncompatMixedGroups, "MIXED_GROUPS"},
		{FeatureIncompatCompressLZO, "COMPRESS_LZO"},
		{FeatureIncompatCompressZSTD, "COMPRESS_ZSTD"},
		{FeatureIncompatBigMetadata, "BIG_METADATA"},
		{FeatureIncompatExtendedIref, "EXTENDED_IREF"},
		{FeatureIncompatRAID56, "RAID56"},
		{FeatureIncompatSkinnyMetadata, "SKINNY_METADATA"},
		{FeatureIncompatNoHoles, "NO_HOLES"},
		{FeatureIncompatMetadataUUID, "METADATA_UUID"},
		{FeatureIncompatRAID1C34, "RAID1C34"},
		{FeatureIncompatZoned, "ZONED"},
		{FeatureIncompatExtentTreeV2, "EXTENT_TREE_V2"},
		{FeatureIncompatRAIDStripeTree, "RAID_STRIPE_TREE"},
		{FeatureIncompatSimpleQuota, "SIMPLE_QUOTA"},
	}
)

func decodeFeatures(ff *btrfsIoctlFeatureFlags) Features {
	out := Features{
		Compat:   ff.Compat,
		CompatRO: ff.CompatRO,
		Incompat: ff.Incompat,
	}
	for _, f := range featureCompatRONames {
		if out.CompatRO&f.bit != 0 {
			out.Names = append(out.Names, "COMPAT_RO_"+f.name)
		}
	}
	for _, f := range featureIncompatNames {
		if out.Incompat&f.bit != 0 {
			out.Names = append(out.Names, "INCOMPAT_"+f.name)
		}
	}
	return out
}

// GetFeatures returns the feature flags currently enabled on the filesystem
// containing the open fd, via BTRFS_IOC_GET_FEATURES.
func GetFeatures(fd uintptr) (Features, error) {
	var ff btrfsIoctlFeatureFlags
	if err := ioctlFd(fd, BTRFS_IOC_GET_FEATURES, unsafe.Pointer(&ff)); err != nil {
		return Features{}, fmt.Errorf("BTRFS_IOC_GET_FEATURES: %w", err)
	}
	runtime.KeepAlive(&ff)
	return decodeFeatures(&ff), nil
}

// SupportedFeatures is the result of GetSupportedFeatures: the three feature
// masks the kernel reports for what it understands. Supported is the set of
// features this kernel knows about at all; SafeSet and SafeClear are the
// subsets that may be toggled on a mounted filesystem via SetFeatures.
type SupportedFeatures struct {
	Supported Features // all features this kernel understands
	SafeSet   Features // features that can be turned on online
	SafeClear Features // features that can be turned off online
}

// GetSupportedFeatures returns the feature flags this kernel understands, via
// BTRFS_IOC_GET_SUPPORTED_FEATURES. The ioctl fills an array of three
// btrfs_ioctl_feature_flags: [0] every feature the kernel supports, [1] the
// features that are safe to set online, [2] the features that are safe to clear
// online. The SafeSet/SafeClear masks drive what SetFeatures can change.
func GetSupportedFeatures(fd uintptr) (SupportedFeatures, error) {
	var arr [3]btrfsIoctlFeatureFlags
	if err := ioctlFd(fd, BTRFS_IOC_GET_SUPPORTED_FEATURES, unsafe.Pointer(&arr[0])); err != nil {
		return SupportedFeatures{}, fmt.Errorf("BTRFS_IOC_GET_SUPPORTED_FEATURES: %w", err)
	}
	runtime.KeepAlive(&arr)
	return SupportedFeatures{
		Supported: decodeFeatures(&arr[0]),
		SafeSet:   decodeFeatures(&arr[1]),
		SafeClear: decodeFeatures(&arr[2]),
	}, nil
}

// FeatureChange describes an online feature-flag change for SetFeatures: bits
// to clear and bits to set in each of the three masks. The kernel applies the
// clear mask before the set mask. Only features reported in the SafeSet /
// SafeClear masks of GetSupportedFeatures may be changed online; others are
// rejected with EPERM.
type FeatureChange struct {
	ClearCompat   uint64
	SetCompat     uint64
	ClearCompatRO uint64
	SetCompatRO   uint64
	ClearIncompat uint64
	SetIncompat   uint64
}

// SetFeatures applies an online feature-flag change to the filesystem
// containing the open fd, via BTRFS_IOC_SET_FEATURES. The ioctl takes an array
// of two btrfs_ioctl_feature_flags: [0] the bits to clear, [1] the bits to set.
// Only the subset of features the kernel reports as safe to toggle online (see
// GetSupportedFeatures) can be changed; others fail. Requires CAP_SYS_ADMIN
// (root).
func SetFeatures(fd uintptr, change FeatureChange) error {
	var arr [2]btrfsIoctlFeatureFlags
	arr[0] = btrfsIoctlFeatureFlags{
		Compat:   change.ClearCompat,
		CompatRO: change.ClearCompatRO,
		Incompat: change.ClearIncompat,
	}
	arr[1] = btrfsIoctlFeatureFlags{
		Compat:   change.SetCompat,
		CompatRO: change.SetCompatRO,
		Incompat: change.SetIncompat,
	}
	if err := ioctlFd(fd, BTRFS_IOC_SET_FEATURES, unsafe.Pointer(&arr[0])); err != nil {
		return fmt.Errorf("BTRFS_IOC_SET_FEATURES: %w", err)
	}
	runtime.KeepAlive(&arr)
	return nil
}

// InodeOwner is one (inode, root) pair returned by LogicalToIno: an inode
// number and the subvolume (tree) root that owns it. Offset is the offset
// within the inode's file at which the queried extent appears.
type InodeOwner struct {
	Inode  uint64
	Offset uint64
	Root   uint64
}

// LogicalToIno resolves a logical (filesystem byte) address to the inodes that
// reference an extent at that address, via BTRFS_IOC_LOGICAL_INO. The kernel
// fills a btrfs_data_container with (inode, offset, root) triples; this is the
// reverse of the usual inode->extent mapping and is the building block for
// turning a scrub/csum error at a physical location back into the affected
// files. fd must refer to the filesystem (e.g. the mount point). Requires
// CAP_SYS_ADMIN (root).
func LogicalToIno(fd uintptr, logical uint64) ([]InodeOwner, error) {
	// btrfs_data_container with room for the triples. 64 KiB holds ~2730
	// triples, plenty for a single extent; the kernel reports elem_missed /
	// bytes_missing if the buffer is too small, which we surface as an error.
	const bufSize = 64 << 10
	container := make([]byte, bufSize)

	var args btrfsIoctlLogicalInoArgs
	args.Logical = logical
	args.Size = bufSize
	args.Inodes = uint64(uintptr(unsafe.Pointer(&container[0])))

	if err := ioctlFd(fd, BTRFS_IOC_LOGICAL_INO, unsafe.Pointer(&args)); err != nil {
		runtime.KeepAlive(container)
		return nil, fmt.Errorf("BTRFS_IOC_LOGICAL_INO logical=%d: %w", logical, err)
	}
	hdr := (*btrfsDataContainerHdr)(unsafe.Pointer(&container[0]))
	if hdr.ElemMissed != 0 {
		runtime.KeepAlive(container)
		return nil, fmt.Errorf("BTRFS_IOC_LOGICAL_INO logical=%d: result truncated (%d elements missed, %d bytes short)", logical, hdr.ElemMissed, hdr.BytesMissing)
	}
	// val[] holds elem_cnt entries of three u64 each: inode, offset, root.
	const triple = 3
	out := make([]InodeOwner, 0, hdr.ElemCnt/triple)
	val := container[btrfsDataContainerHdrSize:]
	n := int(hdr.ElemCnt)
	for i := 0; i+triple <= n; i += triple {
		base := i * 8
		if base+triple*8 > len(val) {
			break
		}
		out = append(out, InodeOwner{
			Inode:  binary.LittleEndian.Uint64(val[base : base+8]),
			Offset: binary.LittleEndian.Uint64(val[base+8 : base+16]),
			Root:   binary.LittleEndian.Uint64(val[base+16 : base+24]),
		})
	}
	runtime.KeepAlive(container)
	runtime.KeepAlive(&args)
	return out, nil
}

// InoToPath resolves an inode number to the path(s) that reference it within
// its subvolume, via BTRFS_IOC_INO_PATHS. The returned paths are relative to
// the root of the subvolume the inode lives in (the ioctl is issued against an
// fd on that subvolume). An inode may have more than one path when it is
// hard-linked. Requires CAP_SYS_ADMIN (root).
func InoToPath(fd uintptr, inode uint64) ([]string, error) {
	const bufSize = 64 << 10
	container := make([]byte, bufSize)

	var args btrfsIoctlInoPathArgs
	args.Inum = inode
	args.Size = bufSize
	args.Fspath = uint64(uintptr(unsafe.Pointer(&container[0])))

	if err := ioctlFd(fd, BTRFS_IOC_INO_PATHS, unsafe.Pointer(&args)); err != nil {
		runtime.KeepAlive(container)
		return nil, fmt.Errorf("BTRFS_IOC_INO_PATHS inode=%d: %w", inode, err)
	}
	hdr := (*btrfsDataContainerHdr)(unsafe.Pointer(&container[0]))
	if hdr.ElemMissed != 0 {
		runtime.KeepAlive(container)
		return nil, fmt.Errorf("BTRFS_IOC_INO_PATHS inode=%d: result truncated (%d paths missed, %d bytes short)", inode, hdr.ElemMissed, hdr.BytesMissing)
	}
	// val[] holds elem_cnt u64 byte-offsets, each pointing at a NUL-terminated
	// path string elsewhere in the container (relative to the start of val[]).
	val := container[btrfsDataContainerHdrSize:]
	n := int(hdr.ElemCnt)
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		base := i * 8
		if base+8 > len(val) {
			break
		}
		rel := binary.LittleEndian.Uint64(val[base : base+8])
		if int(rel) >= len(val) {
			break
		}
		out = append(out, cstr(val[rel:]))
	}
	runtime.KeepAlive(container)
	runtime.KeepAlive(&args)
	return out, nil
}
