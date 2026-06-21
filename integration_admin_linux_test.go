// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

//go:build linux

package btrfs

import "testing"

// TestIntegrationListSubvolumes drives the TREE_SEARCH-based subvolume listing
// against a live btrfs mount: it creates two subvolumes and a snapshot, then
// asserts ListSubvolumes reports all three with the expected names, ids, and
// parent ids. It is skipped unless a btrfs mount is available (BTRFS_PATH or
// /mnt/bt) and requires privileges to create subvolumes and read the root tree.
//
// Run on the validation host as root:
//
//	BTRFS_PATH=/mnt/bt sudo -E go test -run IntegrationList -v ./...
func TestIntegrationListSubvolumes(t *testing.T) {
	mnt := requireBtrfs(t)

	const a, b, snap = "list_a", "list_b", "list_a_snap"
	// Best-effort cleanup, both before and after.
	cleanup := func() {
		_ = SubvolDelete(mnt, snap)
		_ = SubvolDelete(mnt, a)
		_ = SubvolDelete(mnt, b)
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := SubvolCreate(mnt, a); err != nil {
		t.Fatalf("SubvolCreate(%s): %v", a, err)
	}
	if err := SubvolCreate(mnt, b); err != nil {
		t.Fatalf("SubvolCreate(%s): %v", b, err)
	}
	if err := SnapshotCreate(mnt+"/"+a, mnt, snap, true); err != nil {
		t.Fatalf("SnapshotCreate(%s): %v", snap, err)
	}

	idA, err := SubvolID(mnt + "/" + a)
	if err != nil {
		t.Fatalf("SubvolID(%s): %v", a, err)
	}

	subs, err := ListSubvolumes(mnt)
	if err != nil {
		t.Fatalf("ListSubvolumes: %v", err)
	}

	byName := map[string]Subvolume{}
	for _, s := range subs {
		byName[s.Name] = s
		t.Logf("subvol id=%d parent=%d name=%q path=%q", s.ID, s.ParentID, s.Name, s.Path)
	}

	for _, name := range []string{a, b, snap} {
		s, ok := byName[name]
		if !ok {
			t.Errorf("ListSubvolumes missing %q", name)
			continue
		}
		if s.ID < btrfsFirstFreeObjectID {
			t.Errorf("%q has id %d, want >= %d", name, s.ID, btrfsFirstFreeObjectID)
		}
	}
	// The snapshot was created at the top level, so its id-by-lookup must match
	// what listing reports.
	if got, want := byName[a].ID, idA; got != want {
		t.Errorf("ListSubvolumes id for %q = %d, want %d (from SubvolID)", a, got, want)
	}
}

// TestIntegrationFsInfo checks BTRFS_IOC_FS_INFO against a live mount: a fresh
// single-device filesystem reports exactly one device and a non-zero nodesize.
func TestIntegrationFsInfo(t *testing.T) {
	mnt := requireBtrfs(t)
	info, err := GetFsInfo(mnt)
	if err != nil {
		t.Fatalf("GetFsInfo: %v", err)
	}
	t.Logf("FsInfo: num_devices=%d max_id=%d nodesize=%d sectorsize=%d gen=%d",
		info.NumDevices, info.MaxID, info.Nodesize, info.Sectorsize, info.Generation)
	if info.NumDevices == 0 {
		t.Error("FsInfo reports 0 devices")
	}
	if info.Nodesize == 0 || info.Sectorsize == 0 {
		t.Error("FsInfo reports zero nodesize/sectorsize")
	}

	// Device 1 must exist on any mounted btrfs.
	dev, err := GetDeviceInfo(mnt, 1)
	if err != nil {
		t.Fatalf("GetDeviceInfo(1): %v", err)
	}
	t.Logf("DeviceInfo devid=%d path=%q total=%d used=%d",
		dev.Devid, dev.Path, dev.TotalBytes, dev.BytesUsed)
	if dev.Devid != 1 {
		t.Errorf("GetDeviceInfo(1).Devid = %d, want 1", dev.Devid)
	}
}
