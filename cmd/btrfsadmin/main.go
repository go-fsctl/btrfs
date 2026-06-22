// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

// btrfsadmin is a live demonstration of the github.com/go-fsctl/btrfs admin
// operations driving the btrfs kernel module purely via BTRFS_IOC_* ioctls (no
// cgo, no btrfs CLI). It lists subvolumes, reports fs/device info, optionally
// adds and removes a second device, scrubs a device, and runs a full balance.
//
// Usage:
//
//	btrfsadmin <mount> [add-dev-path] [rm-dev-path-or-empty]
//
// With an add-dev-path it issues DeviceAdd then (if rm matches) DeviceRemove,
// so the device count round-trips. Scrub and balance always run.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/go-fsctl/btrfs"
)

// Seams over the btrfs package and the process, overridable in tests.
var (
	available      = btrfs.Available
	listSubvolumes = btrfs.ListSubvolumes
	getFsInfo      = btrfs.GetFsInfo
	getDeviceInfo  = btrfs.GetDeviceInfo
	deviceAdd      = btrfs.DeviceAdd
	deviceRemove   = btrfs.DeviceRemove
	scrubStart     = btrfs.ScrubStart
	balanceStart   = btrfs.BalanceStart

	osExit           = os.Exit
	stdout io.Writer = os.Stdout
)

func main() { osExit(run(os.Args)) }

func run(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(stdout, "usage: btrfsadmin <mount> [add-dev] [rm-dev]")
		return 2
	}
	mnt := args[1]
	if !available(mnt) {
		fmt.Fprintf(stdout, "FAIL: %s is not a mounted btrfs filesystem\n", mnt)
		return 1
	}
	fmt.Fprintf(stdout, "btrfs mount %s (pure-Go ioctl admin path)\n", mnt)

	// --- ListSubvolumes (TREE_SEARCH) ---
	subs, err := listSubvolumes(mnt)
	if err != nil {
		return fail("ListSubvolumes", err)
	}
	fmt.Fprintf(stdout, "OK: BTRFS_IOC_TREE_SEARCH listed %d subvolume(s):\n", len(subs))
	for _, s := range subs {
		fmt.Fprintf(stdout, "    id=%d parent=%d name=%q path=%q\n", s.ID, s.ParentID, s.Name, s.Path)
	}

	// --- FsInfo / DeviceInfo ---
	fi, err := getFsInfo(mnt)
	if err != nil {
		return fail("GetFsInfo", err)
	}
	fmt.Fprintf(stdout, "OK: BTRFS_IOC_FS_INFO num_devices=%d max_id=%d nodesize=%d gen=%d\n",
		fi.NumDevices, fi.MaxID, fi.Nodesize, fi.Generation)
	dev, err := getDeviceInfo(mnt, 1)
	if err != nil {
		return fail("GetDeviceInfo(1)", err)
	}
	fmt.Fprintf(stdout, "OK: BTRFS_IOC_DEV_INFO devid=%d path=%q total=%d used=%d\n",
		dev.Devid, dev.Path, dev.TotalBytes, dev.BytesUsed)

	// --- DeviceAdd / DeviceRemove ---
	if len(args) > 2 && args[2] != "" {
		addDev := args[2]
		if err := deviceAdd(mnt, addDev); err != nil {
			return fail("DeviceAdd", err)
		}
		fi2, err := getFsInfo(mnt)
		if err != nil {
			return fail("GetFsInfo(after add)", err)
		}
		fmt.Fprintf(stdout, "OK: BTRFS_IOC_ADD_DEV %s -> num_devices now %d\n", addDev, fi2.NumDevices)

		rmDev := addDev
		if len(args) > 3 && args[3] != "" {
			rmDev = args[3]
		}
		if err := deviceRemove(mnt, rmDev); err != nil {
			return fail("DeviceRemove", err)
		}
		fi3, err := getFsInfo(mnt)
		if err != nil {
			return fail("GetFsInfo(after remove)", err)
		}
		fmt.Fprintf(stdout, "OK: BTRFS_IOC_RM_DEV_V2 %s -> num_devices now %d\n", rmDev, fi3.NumDevices)
	}

	// --- Scrub ---
	sp, err := scrubStart(mnt, 1, btrfs.ScrubOptions{})
	if err != nil {
		return fail("ScrubStart", err)
	}
	fmt.Fprintf(stdout, "OK: BTRFS_IOC_SCRUB devid=1 data_scrubbed=%d tree_scrubbed=%d read_err=%d csum_err=%d uncorrectable=%d\n",
		sp.DataBytesScrubbed, sp.TreeBytesScrubbed, sp.ReadErrors, sp.CsumErrors, sp.UncorrectableErrors)
	if sp.ReadErrors+sp.CsumErrors+sp.VerifyErrors+sp.UncorrectableErrors != 0 {
		fmt.Fprintln(stdout, "WARN: scrub reported errors")
	}

	// --- Balance (full) ---
	bp, err := balanceStart(mnt, btrfs.BalanceArgs{})
	if err != nil {
		return fail("BalanceStart", err)
	}
	fmt.Fprintf(stdout, "OK: BTRFS_IOC_BALANCE_V2 (full) expected=%d considered=%d completed=%d running=%v\n",
		bp.Expected, bp.Considered, bp.Completed, bp.Running)

	fmt.Fprintln(stdout, "ALL OK")
	return 0
}

func fail(what string, err error) int {
	fmt.Fprintf(stdout, "FAIL: %s: %v\n", what, err)
	return 1
}
