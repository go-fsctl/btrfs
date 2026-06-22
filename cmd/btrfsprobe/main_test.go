// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/go-fsctl/btrfs"
)

var errBoom = errors.New("boom")

// restore snapshots every seam and returns a deferred restore.
func restore() func() {
	a, b, c, d := available, subvolCreate, subvolID, snapshotCreate
	e, f, g, h := isReadonly, setReadonly, subvolGetFlags, getSubvolInfo
	i, j := syncFS, subvolDelete
	o, w := osExit, stdout
	return func() {
		available, subvolCreate, subvolID, snapshotCreate = a, b, c, d
		isReadonly, setReadonly, subvolGetFlags, getSubvolInfo = e, f, g, h
		syncFS, subvolDelete = i, j
		osExit, stdout = o, w
	}
}

// happy installs an all-succeeding set of seams; individual tests then break one.
func happy() {
	available = func(string) bool { return true }
	subvolCreate = func(string, string) error { return nil }
	subvolID = func(string) (uint64, error) { return 256, nil }
	snapshotCreate = func(string, string, string, bool) error { return nil }
	isReadonly = func(string) (bool, error) { return true, nil }
	setReadonly = func(string, bool) error { return nil }
	subvolGetFlags = func(string) (uint64, error) { return 0, nil }
	getSubvolInfo = func(string) (*btrfs.SubvolInfo, error) { return &btrfs.SubvolInfo{ID: 256}, nil }
	syncFS = func(string) error { return nil }
	subvolDelete = func(string, string) error { return nil }
}

func runWith(args ...string) int {
	var buf bytes.Buffer
	stdout = &buf
	return run(args)
}

func TestRunSuccess(t *testing.T) {
	defer restore()()
	happy()
	if rc := runWith("btrfsprobe", "/mnt/x"); rc != 0 {
		t.Fatalf("rc=%d, want 0", rc)
	}
}

func TestRunDefaultMount(t *testing.T) {
	defer restore()()
	happy()
	// No arg: default mount path, still succeeds.
	if rc := runWith("btrfsprobe"); rc != 0 {
		t.Fatalf("rc=%d, want 0", rc)
	}
}

func TestRunUnavailable(t *testing.T) {
	defer restore()()
	happy()
	available = func(string) bool { return false }
	if rc := runWith("btrfsprobe", "/mnt/x"); rc != 1 {
		t.Fatalf("rc=%d, want 1", rc)
	}
}

func TestRunErrorBranches(t *testing.T) {
	cases := []struct {
		name   string
		break_ func()
	}{
		{"SubvolCreate", func() { subvolCreate = func(string, string) error { return errBoom } }},
		{"SubvolID", func() { subvolID = func(string) (uint64, error) { return 0, errBoom } }},
		{"SnapshotCreate", func() { snapshotCreate = func(string, string, string, bool) error { return errBoom } }},
		{"IsReadonly", func() { isReadonly = func(string) (bool, error) { return false, errBoom } }},
		{"SetReadonly", func() { setReadonly = func(string, bool) error { return errBoom } }},
		{"SubvolGetFlags", func() { subvolGetFlags = func(string) (uint64, error) { return 0, errBoom } }},
		{"GetSubvolInfo", func() { getSubvolInfo = func(string) (*btrfs.SubvolInfo, error) { return nil, errBoom } }},
		{"Sync", func() { syncFS = func(string) error { return errBoom } }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer restore()()
			happy()
			tc.break_()
			if rc := runWith("btrfsprobe", "/mnt/x"); rc != 1 {
				t.Fatalf("rc=%d, want 1", rc)
			}
		})
	}
}

// TestRunDeleteErrors breaks the final SubvolDelete calls. The two best-effort
// pre-cleanup deletes ignore errors, so we only fail the post-create deletes by
// counting calls.
func TestRunDeleteSnapError(t *testing.T) {
	defer restore()()
	happy()
	calls := 0
	subvolDelete = func(string, string) error {
		calls++
		if calls == 3 { // 1,2 = pre-cleanup (ignored); 3 = delete snap
			return errBoom
		}
		return nil
	}
	if rc := runWith("btrfsprobe", "/mnt/x"); rc != 1 {
		t.Fatalf("rc=%d, want 1", rc)
	}
}

func TestRunDeleteSubError(t *testing.T) {
	defer restore()()
	happy()
	calls := 0
	subvolDelete = func(string, string) error {
		calls++
		if calls == 4 { // 4 = delete sub
			return errBoom
		}
		return nil
	}
	if rc := runWith("btrfsprobe", "/mnt/x"); rc != 1 {
		t.Fatalf("rc=%d, want 1", rc)
	}
}

// TestDefaultSeams touches the real default seam closures so no statement is
// left uncovered. They run against a path that is not btrfs, so Available
// returns false and the rest are not invoked; we call them directly instead.
func TestDefaultSeams(t *testing.T) {
	defer restore()()
	// Available on a non-btrfs path returns false without error.
	if available("/") {
		t.Skip("/ is btrfs on this host; skipping default-seam smoke")
	}
	// Exercise the remaining real seams: they will error on a non-btrfs path,
	// but invoking them runs the production closures.
	_ = subvolCreate("/nonexistent-btrfs-xyz", "x")
	_, _ = subvolID("/nonexistent-btrfs-xyz")
	_ = snapshotCreate("/nonexistent-btrfs-xyz", "/nonexistent-btrfs-xyz", "x", true)
	_, _ = isReadonly("/nonexistent-btrfs-xyz")
	_ = setReadonly("/nonexistent-btrfs-xyz", false)
	_, _ = subvolGetFlags("/nonexistent-btrfs-xyz")
	_, _ = getSubvolInfo("/nonexistent-btrfs-xyz")
	_ = syncFS("/nonexistent-btrfs-xyz")
	_ = subvolDelete("/nonexistent-btrfs-xyz", "x")
}

// TestMainInvokesRun drives the thin main() wrapper through the osExit seam.
func TestMainInvokesRun(t *testing.T) {
	defer restore()()
	happy()
	var buf bytes.Buffer
	stdout = &buf
	code := -1
	osExit = func(c int) { code = c }
	main()
	if code == -1 {
		t.Fatal("main did not invoke osExit")
	}
}
