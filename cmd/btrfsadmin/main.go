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
	"os"

	"github.com/go-fsctl/btrfs"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: btrfsadmin <mount> [add-dev] [rm-dev]")
		os.Exit(2)
	}
	mnt := os.Args[1]
	if !btrfs.Available(mnt) {
		fmt.Printf("FAIL: %s is not a mounted btrfs filesystem\n", mnt)
		os.Exit(1)
	}
	fmt.Printf("btrfs mount %s (pure-Go ioctl admin path)\n", mnt)

	// --- ListSubvolumes (TREE_SEARCH) ---
	subs, err := btrfs.ListSubvolumes(mnt)
	check("ListSubvolumes", err)
	fmt.Printf("OK: BTRFS_IOC_TREE_SEARCH listed %d subvolume(s):\n", len(subs))
	for _, s := range subs {
		fmt.Printf("    id=%d parent=%d name=%q path=%q\n", s.ID, s.ParentID, s.Name, s.Path)
	}

	// --- FsInfo / DeviceInfo ---
	fi, err := btrfs.GetFsInfo(mnt)
	check("GetFsInfo", err)
	fmt.Printf("OK: BTRFS_IOC_FS_INFO num_devices=%d max_id=%d nodesize=%d gen=%d\n",
		fi.NumDevices, fi.MaxID, fi.Nodesize, fi.Generation)
	dev, err := btrfs.GetDeviceInfo(mnt, 1)
	check("GetDeviceInfo(1)", err)
	fmt.Printf("OK: BTRFS_IOC_DEV_INFO devid=%d path=%q total=%d used=%d\n",
		dev.Devid, dev.Path, dev.TotalBytes, dev.BytesUsed)

	// --- DeviceAdd / DeviceRemove ---
	if len(os.Args) > 2 && os.Args[2] != "" {
		addDev := os.Args[2]
		check("DeviceAdd", btrfs.DeviceAdd(mnt, addDev))
		fi2, err := btrfs.GetFsInfo(mnt)
		check("GetFsInfo(after add)", err)
		fmt.Printf("OK: BTRFS_IOC_ADD_DEV %s -> num_devices now %d\n", addDev, fi2.NumDevices)

		rmDev := addDev
		if len(os.Args) > 3 && os.Args[3] != "" {
			rmDev = os.Args[3]
		}
		check("DeviceRemove", btrfs.DeviceRemove(mnt, rmDev))
		fi3, err := btrfs.GetFsInfo(mnt)
		check("GetFsInfo(after remove)", err)
		fmt.Printf("OK: BTRFS_IOC_RM_DEV_V2 %s -> num_devices now %d\n", rmDev, fi3.NumDevices)
	}

	// --- Scrub ---
	sp, err := btrfs.ScrubStart(mnt, 1, btrfs.ScrubOptions{})
	check("ScrubStart", err)
	fmt.Printf("OK: BTRFS_IOC_SCRUB devid=1 data_scrubbed=%d tree_scrubbed=%d read_err=%d csum_err=%d uncorrectable=%d\n",
		sp.DataBytesScrubbed, sp.TreeBytesScrubbed, sp.ReadErrors, sp.CsumErrors, sp.UncorrectableErrors)
	if sp.ReadErrors+sp.CsumErrors+sp.VerifyErrors+sp.UncorrectableErrors != 0 {
		fmt.Println("WARN: scrub reported errors")
	}

	// --- Balance (full) ---
	bp, err := btrfs.BalanceStart(mnt, btrfs.BalanceArgs{})
	check("BalanceStart", err)
	fmt.Printf("OK: BTRFS_IOC_BALANCE_V2 (full) expected=%d considered=%d completed=%d running=%v\n",
		bp.Expected, bp.Considered, bp.Completed, bp.Running)

	fmt.Println("ALL OK")
}

func check(what string, err error) {
	if err != nil {
		fmt.Printf("FAIL: %s: %v\n", what, err)
		os.Exit(1)
	}
}
