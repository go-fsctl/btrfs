// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

//go:build linux

package btrfs

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// TestIntegrationQuota drives the quota/qgroup ioctls against a live btrfs
// mount: it enables quotas, creates a subvolume, writes data, syncs, and
// asserts ListQgroups reports the subvolume's qgroup with non-zero referenced
// usage. It then creates a higher-level qgroup, assigns the subvolume's qgroup
// to it, applies a max_rfer limit, and confirms writing past the limit returns
// EDQUOT. Skipped unless a btrfs mount is available; requires root.
//
//	BTRFS_PATH=/mnt/bt sudo -E go test -run IntegrationQuota -v ./...
func TestIntegrationQuota(t *testing.T) {
	mnt := requireBtrfs(t)

	const sub = "quota_sub"
	subPath := mnt + "/" + sub
	cleanup := func() { _ = SubvolDelete(mnt, sub) }
	cleanup()
	t.Cleanup(cleanup)
	t.Cleanup(func() { _ = QuotaDisable(mnt) })

	if err := QuotaEnable(mnt); err != nil {
		t.Fatalf("QuotaEnable: %v", err)
	}
	t.Logf("QuotaEnable OK")

	if err := SubvolCreate(mnt, sub); err != nil {
		t.Fatalf("SubvolCreate: %v", err)
	}
	subID, err := SubvolID(subPath)
	if err != nil {
		t.Fatalf("SubvolID: %v", err)
	}

	// Write ~8 MiB into the subvolume and force a commit so qgroup accounting
	// reflects the usage.
	if err := os.WriteFile(subPath+"/data", make([]byte, 8<<20), 0644); err != nil {
		t.Fatalf("write data: %v", err)
	}
	if err := Sync(mnt); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	qgs, err := ListQgroups(mnt)
	if err != nil {
		t.Fatalf("ListQgroups: %v", err)
	}
	var subQ *Qgroup
	for i := range qgs {
		t.Logf("qgroup %d/%d rfer=%d excl=%d maxRfer=%d limFlags=%#x",
			qgs[i].Level, qgs[i].SubvolID, qgs[i].Rfer, qgs[i].Excl, qgs[i].MaxRfer, qgs[i].LimFlags)
		if qgs[i].Level == 0 && qgs[i].SubvolID == subID {
			subQ = &qgs[i]
		}
	}
	if subQ == nil {
		t.Fatalf("ListQgroups: missing level-0 qgroup for subvol id %d", subID)
	}
	if subQ.Rfer == 0 {
		t.Errorf("subvol qgroup 0/%d reports zero rfer after writing 8 MiB", subID)
	}

	// Create a level-1 aggregation qgroup, assign the subvolume's qgroup to it,
	// and apply a max_rfer limit, then verify the limit shows up in listing.
	const parentQ = (uint64(1) << 48) | 100 // 1/100
	if err := QgroupCreate(mnt, parentQ); err != nil {
		t.Fatalf("QgroupCreate(1/100): %v", err)
	}
	if err := QgroupAssign(mnt, subQ.ID, parentQ); err != nil {
		t.Fatalf("QgroupAssign(0/%d -> 1/100): %v", subID, err)
	}

	const limit = 16 << 20 // 16 MiB referenced limit on the subvolume qgroup
	if err := QgroupLimit(mnt, subQ.ID, QgroupLimits{Flags: QgroupLimitMaxRfer, MaxRfer: limit}); err != nil {
		t.Fatalf("QgroupLimit: %v", err)
	}
	if err := Sync(mnt); err != nil {
		t.Fatalf("Sync after limit: %v", err)
	}

	qgs, err = ListQgroups(mnt)
	if err != nil {
		t.Fatalf("ListQgroups (after limit): %v", err)
	}
	sawLimit := false
	for _, q := range qgs {
		if q.Level == 0 && q.SubvolID == subID {
			if q.MaxRfer == limit && q.LimFlags&QgroupLimitMaxRfer != 0 {
				sawLimit = true
			}
			t.Logf("post-limit qgroup 0/%d maxRfer=%d limFlags=%#x", subID, q.MaxRfer, q.LimFlags)
		}
	}
	if !sawLimit {
		t.Errorf("ListQgroups did not report max_rfer=%d on qgroup 0/%d", limit, subID)
	}

	// Writing past the 16 MiB limit must fail with EDQUOT. Append enough to
	// exceed it (already wrote 8 MiB; add 16 MiB more).
	if err := writeMoreExpectEDQUOT(t, subPath+"/big", 16<<20); err != nil {
		t.Errorf("expected EDQUOT writing past limit, got: %v", err)
	} else {
		t.Logf("write past max_rfer limit correctly returned EDQUOT")
	}
}

// writeMoreExpectEDQUOT writes size bytes to path and returns nil if (and only
// if) the write (or the forced commit) fails with EDQUOT. btrfs may surface the
// over-quota condition either on write or at commit, so it syncs and inspects
// both.
func writeMoreExpectEDQUOT(t *testing.T, path string, size int) error {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, werr := f.Write(make([]byte, size))
	serr := Sync(path)
	if errors.Is(werr, unix.EDQUOT) || errors.Is(serr, unix.EDQUOT) {
		return nil
	}
	if werr != nil {
		return werr
	}
	if serr != nil {
		return serr
	}
	return errors.New("write and sync both succeeded")
}

// TestIntegrationDefrag drives the defrag ioctls against a live btrfs mount: it
// writes a fragmented file (many small writes with syncs to force separate
// extents), then defragments it with DefragRange and Defrag, asserting both
// complete without error. Skipped unless a btrfs mount is available.
//
//	BTRFS_PATH=/mnt/bt sudo -E go test -run IntegrationDefrag -v ./...
func TestIntegrationDefrag(t *testing.T) {
	mnt := requireBtrfs(t)

	path := mnt + "/defrag_target"
	t.Cleanup(func() { _ = os.Remove(path) })

	// Create a fragmented file: write 256 KiB in 4 KiB chunks, fsync between
	// chunks so the allocator scatters extents.
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	chunk := make([]byte, 4096)
	for i := 0; i < 64; i++ {
		for j := range chunk {
			chunk[j] = byte(i)
		}
		if _, err := f.Write(chunk); err != nil {
			f.Close()
			t.Fatalf("write chunk %d: %v", i, err)
		}
		if err := f.Sync(); err != nil {
			f.Close()
			t.Fatalf("fsync chunk %d: %v", i, err)
		}
	}
	f.Close()

	if err := DefragRange(path, DefragRangeOptions{Start: 0, Len: 0, ExtentThresh: 0}); err != nil {
		t.Fatalf("DefragRange: %v", err)
	}
	t.Logf("DefragRange OK")

	if err := Defrag(path); err != nil {
		t.Fatalf("Defrag: %v", err)
	}
	t.Logf("Defrag OK")
}
