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

func restore() func() {
	a, b, c, d := available, listSubvolumes, getFsInfo, getDeviceInfo
	e, f, g, h := deviceAdd, deviceRemove, scrubStart, balanceStart
	o, w := osExit, stdout
	return func() {
		available, listSubvolumes, getFsInfo, getDeviceInfo = a, b, c, d
		deviceAdd, deviceRemove, scrubStart, balanceStart = e, f, g, h
		osExit, stdout = o, w
	}
}

func happy() {
	available = func(string) bool { return true }
	listSubvolumes = func(string) ([]btrfs.Subvolume, error) {
		return []btrfs.Subvolume{{ID: 256, ParentID: 5, Name: "a", Path: "a"}}, nil
	}
	getFsInfo = func(string) (*btrfs.FsInfo, error) {
		return &btrfs.FsInfo{NumDevices: 1, MaxID: 1, Nodesize: 16384}, nil
	}
	getDeviceInfo = func(string, uint64) (*btrfs.DeviceInfo, error) {
		return &btrfs.DeviceInfo{Devid: 1, Path: "/dev/sda", TotalBytes: 100, BytesUsed: 50}, nil
	}
	deviceAdd = func(string, string) error { return nil }
	deviceRemove = func(string, string) error { return nil }
	scrubStart = func(string, uint64, btrfs.ScrubOptions) (btrfs.ScrubProgress, error) {
		return btrfs.ScrubProgress{}, nil
	}
	balanceStart = func(string, btrfs.BalanceArgs) (btrfs.BalanceProgress, error) { return btrfs.BalanceProgress{}, nil }
}

func runWith(args ...string) int {
	var buf bytes.Buffer
	stdout = &buf
	return run(args)
}

func TestRunUsage(t *testing.T) {
	defer restore()()
	happy()
	if rc := runWith("btrfsadmin"); rc != 2 {
		t.Fatalf("rc=%d, want 2", rc)
	}
}

func TestRunUnavailable(t *testing.T) {
	defer restore()()
	happy()
	available = func(string) bool { return false }
	if rc := runWith("btrfsadmin", "/mnt/x"); rc != 1 {
		t.Fatalf("rc=%d, want 1", rc)
	}
}

func TestRunSuccessNoDev(t *testing.T) {
	defer restore()()
	happy()
	if rc := runWith("btrfsadmin", "/mnt/x"); rc != 0 {
		t.Fatalf("rc=%d, want 0", rc)
	}
}

// TestRunSuccessWithDev drives the DeviceAdd/DeviceRemove block, including the
// explicit rm-dev arg branch, and the scrub-error WARN line.
func TestRunSuccessWithDev(t *testing.T) {
	defer restore()()
	happy()
	// Scrub reports an error so the WARN branch is taken.
	scrubStart = func(string, uint64, btrfs.ScrubOptions) (btrfs.ScrubProgress, error) {
		return btrfs.ScrubProgress{ReadErrors: 1}, nil
	}
	if rc := runWith("btrfsadmin", "/mnt/x", "/dev/sdb", "/dev/sdb"); rc != 0 {
		t.Fatalf("rc=%d, want 0", rc)
	}
}

// TestRunWithDevDefaultRm covers the rmDev = addDev default (no third arg, or
// empty third arg).
func TestRunWithDevDefaultRm(t *testing.T) {
	defer restore()()
	happy()
	if rc := runWith("btrfsadmin", "/mnt/x", "/dev/sdb"); rc != 0 {
		t.Fatalf("rc=%d, want 0", rc)
	}
	// Empty third arg also defaults rmDev to addDev.
	if rc := runWith("btrfsadmin", "/mnt/x", "/dev/sdb", ""); rc != 0 {
		t.Fatalf("rc=%d (empty rm), want 0", rc)
	}
}

func TestRunErrorBranches(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		break_ func()
	}{
		{"ListSubvolumes", []string{"btrfsadmin", "/mnt/x"}, func() {
			listSubvolumes = func(string) ([]btrfs.Subvolume, error) { return nil, errBoom }
		}},
		{"GetFsInfo", []string{"btrfsadmin", "/mnt/x"}, func() {
			getFsInfo = func(string) (*btrfs.FsInfo, error) { return nil, errBoom }
		}},
		{"GetDeviceInfo", []string{"btrfsadmin", "/mnt/x"}, func() {
			getDeviceInfo = func(string, uint64) (*btrfs.DeviceInfo, error) { return nil, errBoom }
		}},
		{"DeviceAdd", []string{"btrfsadmin", "/mnt/x", "/dev/sdb"}, func() {
			deviceAdd = func(string, string) error { return errBoom }
		}},
		{"GetFsInfoAfterAdd", []string{"btrfsadmin", "/mnt/x", "/dev/sdb"}, func() {
			calls := 0
			getFsInfo = func(string) (*btrfs.FsInfo, error) {
				calls++
				if calls == 2 {
					return nil, errBoom
				}
				return &btrfs.FsInfo{NumDevices: 1}, nil
			}
		}},
		{"DeviceRemove", []string{"btrfsadmin", "/mnt/x", "/dev/sdb"}, func() {
			deviceRemove = func(string, string) error { return errBoom }
		}},
		{"GetFsInfoAfterRemove", []string{"btrfsadmin", "/mnt/x", "/dev/sdb"}, func() {
			calls := 0
			getFsInfo = func(string) (*btrfs.FsInfo, error) {
				calls++
				if calls == 3 {
					return nil, errBoom
				}
				return &btrfs.FsInfo{NumDevices: 1}, nil
			}
		}},
		{"ScrubStart", []string{"btrfsadmin", "/mnt/x"}, func() {
			scrubStart = func(string, uint64, btrfs.ScrubOptions) (btrfs.ScrubProgress, error) {
				return btrfs.ScrubProgress{}, errBoom
			}
		}},
		{"BalanceStart", []string{"btrfsadmin", "/mnt/x"}, func() {
			balanceStart = func(string, btrfs.BalanceArgs) (btrfs.BalanceProgress, error) {
				return btrfs.BalanceProgress{}, errBoom
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer restore()()
			happy()
			tc.break_()
			if rc := runWith(tc.args...); rc != 1 {
				t.Fatalf("rc=%d, want 1", rc)
			}
		})
	}
}

func TestMainInvokesRun(t *testing.T) {
	defer restore()()
	happy()
	var buf bytes.Buffer
	stdout = &buf
	code := -1
	osExit = func(c int) { code = c }
	// main() forwards os.Args (the test binary's args) into run(); with happy
	// seams the run completes and osExit is invoked with run()'s return code.
	main()
	if code == -1 {
		t.Fatal("main did not invoke osExit")
	}
}
