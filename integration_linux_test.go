// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

//go:build linux

package btrfs

import (
	"os"
	"testing"
)

// These integration tests drive the live btrfs kernel module via BTRFS_IOC_*
// ioctls. They are skipped unless a mounted btrfs filesystem is available: set
// BTRFS_PATH to its mount point, or rely on the default /mnt/bt. They require
// privileges sufficient to create subvolumes (typically root, or an owner of
// the mount as set up by the validation harness).
//
// Run on the validation host as root:
//
//	BTRFS_PATH=/mnt/bt sudo -E go test -run Integration -v ./...

func testMount() string {
	if p := os.Getenv("BTRFS_PATH"); p != "" {
		return p
	}
	return "/mnt/bt"
}

func requireBtrfs(t *testing.T) string {
	t.Helper()
	mnt := testMount()
	if !Available(mnt) {
		t.Skipf("%s is not a mounted btrfs filesystem; skipping kernel integration test", mnt)
	}
	return mnt
}

// TestIntegrationLifecycle exercises the full create -> snapshot -> flag-toggle
// -> delete cycle entirely through our ioctls.
func TestIntegrationLifecycle(t *testing.T) {
	mnt := requireBtrfs(t)

	const sub = "gofsctl_sub"
	const snap = "gofsctl_snap"
	subPath := mnt + "/" + sub
	snapPath := mnt + "/" + snap

	// Clean up any leftovers from a previous run (best effort).
	_ = SubvolDelete(mnt, snap)
	_ = SubvolDelete(mnt, sub)

	// SUBVOL_CREATE
	if err := SubvolCreate(mnt, sub); err != nil {
		t.Fatalf("SubvolCreate: %v", err)
	}
	t.Cleanup(func() { _ = SubvolDelete(mnt, sub) })

	id, err := SubvolID(subPath)
	if err != nil {
		t.Fatalf("SubvolID: %v", err)
	}
	if id < btrfsFirstFreeObjectID {
		t.Errorf("SubvolID = %d, want >= %d", id, btrfsFirstFreeObjectID)
	}
	t.Logf("created subvolume %s (id=%d)", subPath, id)

	info, err := GetSubvolInfo(subPath)
	if err != nil {
		t.Fatalf("GetSubvolInfo: %v", err)
	}
	if info.ID != id {
		t.Errorf("GetSubvolInfo id = %d, want %d", info.ID, id)
	}
	if info.Flags&RootSubvolRDONLY != 0 {
		t.Errorf("new subvolume unexpectedly read-only")
	}

	// SNAP_CREATE_V2 (read-only)
	if err := SnapshotCreate(subPath, mnt, snap, true); err != nil {
		t.Fatalf("SnapshotCreate(ro): %v", err)
	}
	t.Cleanup(func() { _ = SubvolDelete(mnt, snap) })

	ro, err := IsReadonly(snapPath)
	if err != nil {
		t.Fatalf("IsReadonly(snap): %v", err)
	}
	if !ro {
		t.Errorf("snapshot %s not read-only (SUBVOL_GETFLAGS)", snapPath)
	}
	// Cross-check the other flag namespace: GET_SUBVOL_INFO surfaces the
	// on-disk root_item RDONLY bit (RootSubvolRDONLY), not SubvolRDONLY.
	snapInfo, err := GetSubvolInfo(snapPath)
	if err != nil {
		t.Fatalf("GetSubvolInfo(snap): %v", err)
	}
	if snapInfo.Flags&RootSubvolRDONLY == 0 {
		t.Errorf("snapshot %s GET_SUBVOL_INFO flags=%#x missing RootSubvolRDONLY", snapPath, snapInfo.Flags)
	}
	// A snapshot records the source subvolume's UUID in ParentUUID; it must be
	// non-zero (a plain subvolume would report all-zero here). ParentID, by
	// contrast, is the *containing* subvolume (here the top-level fs tree, 5),
	// not the snapshot source.
	srcInfo, err := GetSubvolInfo(subPath)
	if err != nil {
		t.Fatalf("GetSubvolInfo(src): %v", err)
	}
	if snapInfo.ParentUUID != srcInfo.UUID {
		t.Errorf("snapshot ParentUUID %x != source UUID %x", snapInfo.ParentUUID, srcInfo.UUID)
	}
	t.Logf("created read-only snapshot %s (info.flags=%#x, parentUUID matches source)", snapPath, snapInfo.Flags)

	// SUBVOL_SETFLAGS: clear RO on the snapshot, confirm via GETFLAGS.
	if err := SetReadonly(snapPath, false); err != nil {
		t.Fatalf("SetReadonly(false): %v", err)
	}
	flags, err := SubvolGetFlags(snapPath)
	if err != nil {
		t.Fatalf("SubvolGetFlags: %v", err)
	}
	if flags&SubvolRDONLY != 0 {
		t.Errorf("snapshot still RO after clearing: flags=%#x", flags)
	}
	t.Logf("toggled RO flag off on %s", snapPath)

	// SYNC
	if err := Sync(mnt); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// SNAP_DESTROY (delete snapshot then subvolume)
	if err := SubvolDelete(mnt, snap); err != nil {
		t.Fatalf("SubvolDelete(snap): %v", err)
	}
	if err := SubvolDelete(mnt, sub); err != nil {
		t.Fatalf("SubvolDelete(sub): %v", err)
	}
	t.Logf("deleted %s and %s", snapPath, subPath)
}
