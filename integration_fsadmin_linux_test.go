// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

//go:build linux

package btrfs

import (
	"encoding/binary"
	"os"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// btrfs_tree.h EXTENT_DATA key type and file_extent_item REG type, used by the
// reverse-mapping integration test to locate a file's on-disk extent.
const (
	testExtentDataKey     = 108 // BTRFS_EXTENT_DATA_KEY
	testFileExtentRegType = 1   // BTRFS_FILE_EXTENT_REG
)

// fileInode returns the inode number of the file at path via unix.Stat.
func fileInode(path string) (uint64, bool) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, false
	}
	return uint64(st.Ino), true
}

// hasSuffix is strings.HasSuffix, named locally to keep the test self-contained.
func hasSuffix(s, suffix string) bool { return strings.HasSuffix(s, suffix) }

// firstFileExtentLogical walks the subvolume tree containing the ioctl fd for
// the EXTENT_DATA item of the given inode and returns the logical (filesystem
// byte) address of its first non-inline data extent. found is false when the
// file has only inline data (no separate extent to reverse-map).
func firstFileExtentLogical(fd uintptr, inode uint64) (logical uint64, found bool, err error) {
	// The default-mounted subvolume is normally the FS tree (id 5); use the
	// id the kernel reports for the fd to be safe.
	var lk btrfsIoctlInoLookupArgs
	lk.Objectid = btrfsFirstFreeObjectID
	if e := ioctlFd(fd, BTRFS_IOC_INO_LOOKUP, unsafe.Pointer(&lk)); e != nil {
		return 0, false, e
	}
	treeID := lk.Treeid

	emit := func(hdr *btrfsIoctlSearchHeader, body []byte) error {
		if found || hdr.Type != testExtentDataKey || hdr.Objectid != inode {
			return nil
		}
		// file_extent_item (packed): generation(8) ram_bytes(8) compression(1)
		// encryption(1) other_encoding(2) type(1) disk_bytenr(8) ...
		if len(body) < 29 {
			return nil
		}
		if body[20] != testFileExtentRegType {
			return nil // inline or prealloc
		}
		diskBytenr := binary.LittleEndian.Uint64(body[21:29])
		if diskBytenr == 0 {
			return nil // a hole
		}
		logical = diskBytenr
		found = true
		return nil
	}
	if e := searchTreeTypeRange(fd, treeID, testExtentDataKey, testExtentDataKey, emit); e != nil {
		return 0, false, e
	}
	return logical, found, nil
}

// These integration tests drive the filesystem-level admin ioctls against a
// live btrfs mount. They are skipped unless a mounted btrfs filesystem is
// available (BTRFS_PATH or /mnt/bt) and require privileges (root) for the
// privileged ioctls (label set, resize, default subvol, feature toggle, the
// reverse extent mapping).
//
// Run on the validation host as root:
//
//	BTRFS_PATH=/mnt/bt sudo -E go test -run IntegrationFsAdmin -v ./...

// openMount opens the mount point and returns its fd for the fd-based ioctls.
func openMount(t *testing.T, mnt string) *os.File {
	t.Helper()
	f, err := os.Open(mnt)
	if err != nil {
		t.Fatalf("open %q: %v", mnt, err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// TestIntegrationFsAdminLabel round-trips the filesystem label: read the
// current label, set a new one, read it back, then restore the original.
func TestIntegrationFsAdminLabel(t *testing.T) {
	mnt := requireBtrfs(t)
	f := openMount(t, mnt)

	orig, err := GetLabel(f.Fd())
	if err != nil {
		t.Fatalf("GetLabel: %v", err)
	}
	t.Logf("original label = %q", orig)

	const want = "gofsctl_lbl"
	if err := SetLabel(f.Fd(), want); err != nil {
		t.Fatalf("SetLabel(%q): %v", want, err)
	}
	t.Cleanup(func() { _ = SetLabel(f.Fd(), orig) })

	got, err := GetLabel(f.Fd())
	if err != nil {
		t.Fatalf("GetLabel after set: %v", err)
	}
	if got != want {
		t.Errorf("GetLabel after SetLabel = %q, want %q", got, want)
	}
}

// TestIntegrationFsAdminResize shrinks then re-grows the filesystem and checks
// the reported device size moves accordingly via FS_INFO/DEV_INFO.
func TestIntegrationFsAdminResize(t *testing.T) {
	mnt := requireBtrfs(t)
	f := openMount(t, mnt)

	before, err := GetDeviceInfo(mnt, 1)
	if err != nil {
		t.Fatalf("GetDeviceInfo(1) before: %v", err)
	}
	t.Logf("device 1 total before = %d", before.TotalBytes)

	if err := Resize(f.Fd(), "-256M"); err != nil {
		t.Fatalf("Resize(-256M): %v", err)
	}
	shrunk, err := GetDeviceInfo(mnt, 1)
	if err != nil {
		t.Fatalf("GetDeviceInfo(1) after shrink: %v", err)
	}
	t.Logf("device 1 total after shrink = %d", shrunk.TotalBytes)
	if shrunk.TotalBytes >= before.TotalBytes {
		t.Errorf("after -256M total = %d, want < %d", shrunk.TotalBytes, before.TotalBytes)
	}

	if err := Resize(f.Fd(), "max"); err != nil {
		t.Fatalf("Resize(max): %v", err)
	}
	grown, err := GetDeviceInfo(mnt, 1)
	if err != nil {
		t.Fatalf("GetDeviceInfo(1) after grow: %v", err)
	}
	t.Logf("device 1 total after max = %d", grown.TotalBytes)
	if grown.TotalBytes <= shrunk.TotalBytes {
		t.Errorf("after max total = %d, want > %d", grown.TotalBytes, shrunk.TotalBytes)
	}
}

// TestIntegrationFsAdminDefaultSubvol creates a subvolume, makes it the default,
// reads the default back, then restores the original default.
func TestIntegrationFsAdminDefaultSubvol(t *testing.T) {
	mnt := requireBtrfs(t)
	f := openMount(t, mnt)

	orig, err := GetDefaultSubvol(f.Fd())
	if err != nil {
		t.Fatalf("GetDefaultSubvol: %v", err)
	}
	t.Logf("original default subvol id = %d", orig)
	t.Cleanup(func() { _ = SetDefaultSubvol(f.Fd(), orig) })

	const sub = "gofsctl_def"
	_ = SubvolDelete(mnt, sub)
	if err := SubvolCreate(mnt, sub); err != nil {
		t.Fatalf("SubvolCreate(%s): %v", sub, err)
	}
	t.Cleanup(func() { _ = SubvolDelete(mnt, sub) })

	id, err := SubvolID(mnt + "/" + sub)
	if err != nil {
		t.Fatalf("SubvolID(%s): %v", sub, err)
	}
	if err := SetDefaultSubvol(f.Fd(), id); err != nil {
		t.Fatalf("SetDefaultSubvol(%d): %v", id, err)
	}
	got, err := GetDefaultSubvol(f.Fd())
	if err != nil {
		t.Fatalf("GetDefaultSubvol after set: %v", err)
	}
	if got != id {
		t.Errorf("GetDefaultSubvol = %d, want %d", got, id)
	}
}

// TestIntegrationFsAdminFeatures checks GetFeatures/GetSupportedFeatures against
// a live mount: a default mkfs sets at least one incompat feature, and every
// enabled feature must be in the kernel's supported set.
func TestIntegrationFsAdminFeatures(t *testing.T) {
	mnt := requireBtrfs(t)
	f := openMount(t, mnt)

	feat, err := GetFeatures(f.Fd())
	if err != nil {
		t.Fatalf("GetFeatures: %v", err)
	}
	t.Logf("features: compat=%#x compat_ro=%#x incompat=%#x names=%v",
		feat.Compat, feat.CompatRO, feat.Incompat, feat.Names)

	sup, err := GetSupportedFeatures(f.Fd())
	if err != nil {
		t.Fatalf("GetSupportedFeatures: %v", err)
	}
	t.Logf("supported incompat=%#x safe_set incompat=%#x safe_clear incompat=%#x",
		sup.Supported.Incompat, sup.SafeSet.Incompat, sup.SafeClear.Incompat)

	if feat.Incompat == 0 {
		t.Error("GetFeatures reports no incompat features on a default mkfs")
	}
	// Every enabled incompat feature must be one the kernel understands.
	if feat.Incompat&^sup.Supported.Incompat != 0 {
		t.Errorf("enabled incompat %#x not all in supported %#x",
			feat.Incompat, sup.Supported.Incompat)
	}
}

// TestIntegrationFsAdminLogicalIno exercises the reverse extent->inode->path
// mapping: it writes a file, finds the file's first data extent via TREE_SEARCH
// over the subvolume's FS tree (EXTENT_DATA item), then checks LogicalToIno maps
// the extent back to the file's inode and InoToPath maps the inode back to a
// path that ends with the file name.
func TestIntegrationFsAdminLogicalIno(t *testing.T) {
	mnt := requireBtrfs(t)
	f := openMount(t, mnt)

	const name = "gofsctl_lino.dat"
	p := mnt + "/" + name
	_ = os.Remove(p)
	data := make([]byte, 1<<20) // 1 MiB to force a real (non-inline) extent
	for i := range data {
		data[i] = byte(i)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write %q: %v", p, err)
	}
	t.Cleanup(func() { _ = os.Remove(p) })
	if err := Sync(mnt); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	sysStat, ok := fileInode(p)
	if !ok {
		t.Skip("cannot stat the test file for its inode")
	}
	t.Logf("file inode = %d", sysStat)

	logical, found, err := firstFileExtentLogical(f.Fd(), sysStat)
	if err != nil {
		t.Fatalf("firstFileExtentLogical: %v", err)
	}
	if !found {
		t.Skip("no non-inline data extent found for the test file; skipping reverse map")
	}
	t.Logf("file extent logical = %d", logical)

	owners, err := LogicalToIno(f.Fd(), logical)
	if err != nil {
		t.Fatalf("LogicalToIno(%d): %v", logical, err)
	}
	t.Logf("LogicalToIno -> %+v", owners)
	var sawInode bool
	for _, o := range owners {
		if o.Inode == sysStat {
			sawInode = true
		}
	}
	if !sawInode {
		t.Errorf("LogicalToIno did not return inode %d (got %+v)", sysStat, owners)
	}

	paths, err := InoToPath(f.Fd(), sysStat)
	if err != nil {
		t.Fatalf("InoToPath(%d): %v", sysStat, err)
	}
	t.Logf("InoToPath -> %v", paths)
	var sawName bool
	for _, pp := range paths {
		if pp == name || hasSuffix(pp, "/"+name) {
			sawName = true
		}
	}
	if !sawName {
		t.Errorf("InoToPath did not return a path ending in %q (got %v)", name, paths)
	}
}
