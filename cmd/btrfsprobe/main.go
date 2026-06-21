// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl
//
// btrfsprobe is a live demonstration of github.com/go-fsctl/btrfs driving the
// btrfs kernel module purely via BTRFS_IOC_* ioctls (no cgo, no btrfs CLI). On
// a mounted btrfs filesystem it creates a subvolume, takes a read-only
// snapshot, toggles the read-only flag, inspects metadata, and deletes both.
package main

import (
	"fmt"
	"os"

	"github.com/go-fsctl/btrfs"
)

func main() {
	mnt := "/mnt/bt"
	if len(os.Args) > 1 {
		mnt = os.Args[1]
	}
	if !btrfs.Available(mnt) {
		fmt.Printf("FAIL: %s is not a mounted btrfs filesystem\n", mnt)
		os.Exit(1)
	}
	fmt.Printf("btrfs mount %s (pure-Go ioctl path)\n", mnt)

	const sub, snap = "probe_sub", "probe_snap"
	subPath, snapPath := mnt+"/"+sub, mnt+"/"+snap

	// Best-effort cleanup of leftovers.
	_ = btrfs.SubvolDelete(mnt, snap)
	_ = btrfs.SubvolDelete(mnt, sub)

	check("SubvolCreate", btrfs.SubvolCreate(mnt, sub))
	id, err := btrfs.SubvolID(subPath)
	check("SubvolID", err)
	fmt.Printf("OK: BTRFS_IOC_SUBVOL_CREATE %s (id=%d via BTRFS_IOC_INO_LOOKUP)\n", subPath, id)

	check("SnapshotCreate", btrfs.SnapshotCreate(subPath, mnt, snap, true))
	ro, err := btrfs.IsReadonly(snapPath)
	check("IsReadonly", err)
	fmt.Printf("OK: BTRFS_IOC_SNAP_CREATE_V2 %s (readonly=%v via BTRFS_IOC_GET_SUBVOL_INFO)\n", snapPath, ro)

	check("SetReadonly(false)", btrfs.SetReadonly(snapPath, false))
	flags, err := btrfs.SubvolGetFlags(snapPath)
	check("SubvolGetFlags", err)
	fmt.Printf("OK: BTRFS_IOC_SUBVOL_SETFLAGS cleared RO on %s (flags=%#x via GETFLAGS)\n", snapPath, flags)

	info, err := btrfs.GetSubvolInfo(snapPath)
	check("GetSubvolInfo", err)
	fmt.Printf("OK: BTRFS_IOC_GET_SUBVOL_INFO %s name=%q id=%d parent=%d flags=%#x gen=%d\n",
		snapPath, info.Name, info.ID, info.ParentID, info.Flags, info.Generation)

	check("Sync", btrfs.Sync(mnt))
	fmt.Println("OK: BTRFS_IOC_SYNC")

	check("SubvolDelete(snap)", btrfs.SubvolDelete(mnt, snap))
	check("SubvolDelete(sub)", btrfs.SubvolDelete(mnt, sub))
	fmt.Printf("OK: BTRFS_IOC_SNAP_DESTROY removed %s and %s\n", snapPath, subPath)
}

func check(what string, err error) {
	if err != nil {
		fmt.Printf("FAIL: %s: %v\n", what, err)
		os.Exit(1)
	}
}
