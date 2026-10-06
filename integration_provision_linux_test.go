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

// These tests walk the path a storage provisioner takes to give a share a
// size-limited subvolume: enable quotas, wait for the accounting to settle,
// create the subvolume, find its level-0 qgroup, limit it, write until the
// kernel refuses, lift the limit, and delete it. They need root and a btrfs
// mount (BTRFS_PATH); the simple-quota variant needs a filesystem of its own
// (BTRFS_SQUOTA_PATH), because switching an existing filesystem between full
// and simple accounting means disabling quotas on it and simple mode leaves an
// incompat flag behind.
//
//	BTRFS_PATH=/mnt/bt1 BTRFS_SQUOTA_PATH=/mnt/bt3 sudo -E go test -run IntegrationProvision -v ./...

const provisionLimit = 8 << 20 // 8 MiB max_rfer on the share

// TestIntegrationProvisionFullQuota drives the rescan ioctls, SubvolQgroupID
// and SubvolLimit under full qgroup accounting.
func TestIntegrationProvisionFullQuota(t *testing.T) {
	mnt := requireBtrfs(t)

	if err := QuotaEnable(mnt); err != nil {
		t.Fatalf("QuotaEnable: %v", err)
	}
	t.Cleanup(func() { _ = QuotaDisable(mnt) })

	// QuotaEnable queued a rescan of its own; waiting is always safe.
	if err := QuotaRescanWait(mnt); err != nil {
		t.Fatalf("QuotaRescanWait after enable: %v", err)
	}
	st, err := QuotaRescanStatus(mnt)
	if err != nil {
		t.Fatalf("QuotaRescanStatus: %v", err)
	}
	t.Logf("rescan status after the enable rescan: %+v", st)

	// An explicit rescan, then the tolerant start-and-wait while one may
	// already be running.
	if err := QuotaRescan(mnt); err != nil && !errors.Is(err, unix.EINPROGRESS) {
		t.Fatalf("QuotaRescan: %v", err)
	}
	if err := QuotaRescanAndWait(mnt); err != nil {
		t.Fatalf("QuotaRescanAndWait: %v", err)
	}
	if st, err := QuotaRescanStatus(mnt); err != nil || st.Running {
		t.Fatalf("QuotaRescanStatus after wait = %+v, %v; want not running", st, err)
	}

	provisionShare(t, mnt, "provision_full")
}

// TestIntegrationProvisionSimpleQuota does the same under simple quotas
// (Linux >= 6.7), on a filesystem dedicated to it, and checks the kernel's
// refusal to rescan in that mode.
func TestIntegrationProvisionSimpleQuota(t *testing.T) {
	mnt := os.Getenv("BTRFS_SQUOTA_PATH")
	if mnt == "" {
		t.Skip("BTRFS_SQUOTA_PATH not set; skipping simple-quota integration test")
	}
	if !Available(mnt) {
		t.Skipf("%s is not a mounted btrfs filesystem; skipping kernel integration test", mnt)
	}

	if err := QuotaEnableSimple(mnt); err != nil {
		t.Fatalf("QuotaEnableSimple (needs Linux >= 6.7): %v", err)
	}
	t.Cleanup(func() { _ = QuotaDisable(mnt) })

	// fs/btrfs/qgroup.c qgroup_rescan_init(): no rescan in simple mode.
	if err := QuotaRescan(mnt); !errors.Is(err, unix.EINVAL) {
		t.Fatalf("QuotaRescan under simple quotas = %v; want EINVAL", err)
	}
	if err := QuotaRescanWait(mnt); err != nil {
		t.Fatalf("QuotaRescanWait under simple quotas: %v", err)
	}

	provisionShare(t, mnt, "provision_simple")
}

// provisionShare creates subvolume name under mnt, limits it to
// provisionLimit through SubvolLimit, fills it until EDQUOT, lifts the limit,
// proves a write now succeeds, and deletes it.
func provisionShare(t *testing.T, mnt, name string) {
	t.Helper()
	subPath := mnt + "/" + name
	_ = SubvolDelete(mnt, name)
	if err := SubvolCreate(mnt, name); err != nil {
		t.Fatalf("SubvolCreate: %v", err)
	}
	t.Cleanup(func() { _ = SubvolDelete(mnt, name) })

	subID, err := SubvolID(subPath)
	if err != nil {
		t.Fatalf("SubvolID: %v", err)
	}
	qid, err := SubvolQgroupID(subPath)
	if err != nil {
		t.Fatalf("SubvolQgroupID: %v", err)
	}
	if qid != QgroupID(0, subID) {
		t.Fatalf("SubvolQgroupID = %#x, want 0/%d", qid, subID)
	}

	if err := SubvolLimit(subPath, provisionLimit); err != nil {
		t.Fatalf("SubvolLimit: %v", err)
	}
	if err := Sync(mnt); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	q := findQgroup(t, mnt, qid)
	if q.MaxRfer != provisionLimit || q.LimFlags&QgroupLimitMaxRfer == 0 {
		t.Fatalf("qgroup 0/%d after SubvolLimit: maxRfer=%d limFlags=%#x; want %d with MAX_RFER",
			subID, q.MaxRfer, q.LimFlags, provisionLimit)
	}

	written, err := fillUntilEDQUOT(subPath+"/fill", 4*provisionLimit)
	if err != nil {
		t.Fatalf("filling a share limited to %d bytes: %v (after %d bytes)", provisionLimit, err, written)
	}
	t.Logf("0/%d: EDQUOT after %d bytes under a %d-byte limit", subID, written, provisionLimit)
	if written > provisionLimit || written < provisionLimit/4 {
		t.Errorf("EDQUOT after %d bytes; want it near the %d-byte limit", written, provisionLimit)
	}

	// maxRfer 0 lifts the limit.
	if err := SubvolLimit(subPath, 0); err != nil {
		t.Fatalf("SubvolLimit(0): %v", err)
	}
	if err := Sync(mnt); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if q := findQgroup(t, mnt, qid); q.LimFlags&QgroupLimitMaxRfer != 0 {
		t.Fatalf("qgroup 0/%d still limited after SubvolLimit(0): maxRfer=%d limFlags=%#x", subID, q.MaxRfer, q.LimFlags)
	}
	if err := os.WriteFile(subPath+"/after", make([]byte, 2<<20), 0o644); err != nil {
		t.Fatalf("write after lifting the limit: %v", err)
	}
	if err := Sync(subPath); err != nil {
		t.Fatalf("Sync after lifting the limit: %v", err)
	}

	if err := SubvolDelete(mnt, name); err != nil {
		t.Fatalf("SubvolDelete: %v", err)
	}
}

// findQgroup returns the qgroup with id qid from ListQgroups, failing the test
// when it is absent.
func findQgroup(t *testing.T, mnt string, qid uint64) Qgroup {
	t.Helper()
	qgs, err := ListQgroups(mnt)
	if err != nil {
		t.Fatalf("ListQgroups: %v", err)
	}
	for _, q := range qgs {
		if q.ID == qid {
			return q
		}
	}
	t.Fatalf("ListQgroups: no qgroup %d/%d", qid>>48, qid&qgroupIDSubvolMask)
	return Qgroup{}
}

// fillUntilEDQUOT appends 256 KiB chunks to path, fsyncing each, until the
// kernel answers EDQUOT on the write or the fsync. It returns the bytes
// accepted before that, and an error if anything else fails or max bytes go
// through without EDQUOT.
func fillUntilEDQUOT(path string, max int) (int, error) {
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	chunk := make([]byte, 256<<10)
	written := 0
	for written < max {
		n, werr := f.Write(chunk)
		if errors.Is(werr, unix.EDQUOT) {
			return written + n, nil
		}
		if werr != nil {
			return written + n, werr
		}
		if serr := f.Sync(); errors.Is(serr, unix.EDQUOT) {
			return written, nil
		} else if serr != nil {
			return written, serr
		}
		written += n
	}
	return written, errors.New("no EDQUOT")
}
