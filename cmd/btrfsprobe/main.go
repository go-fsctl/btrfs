// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, go-fsctl

// btrfsprobe is a live demonstration of github.com/go-fsctl/btrfs driving the
// btrfs kernel module purely via BTRFS_IOC_* ioctls (no cgo, no btrfs CLI). On
// a mounted btrfs filesystem it creates a subvolume, takes a read-only
// snapshot, toggles the read-only flag, inspects metadata, and deletes both.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/go-fsctl/btrfs"
)

// Seams over the btrfs package and the process, overridable in tests. Production
// code uses the real implementations assigned here.
var (
	available      = btrfs.Available
	subvolCreate   = btrfs.SubvolCreate
	subvolID       = btrfs.SubvolID
	snapshotCreate = btrfs.SnapshotCreate
	isReadonly     = btrfs.IsReadonly
	setReadonly    = btrfs.SetReadonly
	subvolGetFlags = btrfs.SubvolGetFlags
	getSubvolInfo  = btrfs.GetSubvolInfo
	syncFS         = btrfs.Sync
	subvolDelete   = btrfs.SubvolDelete

	osExit           = os.Exit
	stdout io.Writer = os.Stdout
)

func main() { osExit(run(os.Args)) }

func run(args []string) int {
	mnt := "/mnt/bt"
	if len(args) > 1 {
		mnt = args[1]
	}
	if !available(mnt) {
		fmt.Fprintf(stdout, "FAIL: %s is not a mounted btrfs filesystem\n", mnt)
		return 1
	}
	fmt.Fprintf(stdout, "btrfs mount %s (pure-Go ioctl path)\n", mnt)

	const sub, snap = "probe_sub", "probe_snap"
	subPath, snapPath := mnt+"/"+sub, mnt+"/"+snap

	// Best-effort cleanup of leftovers.
	_ = subvolDelete(mnt, snap)
	_ = subvolDelete(mnt, sub)

	if err := subvolCreate(mnt, sub); err != nil {
		return fail("SubvolCreate", err)
	}
	id, err := subvolID(subPath)
	if err != nil {
		return fail("SubvolID", err)
	}
	fmt.Fprintf(stdout, "OK: BTRFS_IOC_SUBVOL_CREATE %s (id=%d via BTRFS_IOC_INO_LOOKUP)\n", subPath, id)

	if err := snapshotCreate(subPath, mnt, snap, true); err != nil {
		return fail("SnapshotCreate", err)
	}
	ro, err := isReadonly(snapPath)
	if err != nil {
		return fail("IsReadonly", err)
	}
	fmt.Fprintf(stdout, "OK: BTRFS_IOC_SNAP_CREATE_V2 %s (readonly=%v via BTRFS_IOC_GET_SUBVOL_INFO)\n", snapPath, ro)

	if err := setReadonly(snapPath, false); err != nil {
		return fail("SetReadonly(false)", err)
	}
	flags, err := subvolGetFlags(snapPath)
	if err != nil {
		return fail("SubvolGetFlags", err)
	}
	fmt.Fprintf(stdout, "OK: BTRFS_IOC_SUBVOL_SETFLAGS cleared RO on %s (flags=%#x via GETFLAGS)\n", snapPath, flags)

	info, err := getSubvolInfo(snapPath)
	if err != nil {
		return fail("GetSubvolInfo", err)
	}
	fmt.Fprintf(stdout, "OK: BTRFS_IOC_GET_SUBVOL_INFO %s name=%q id=%d parent=%d flags=%#x gen=%d\n",
		snapPath, info.Name, info.ID, info.ParentID, info.Flags, info.Generation)

	if err := syncFS(mnt); err != nil {
		return fail("Sync", err)
	}
	fmt.Fprintln(stdout, "OK: BTRFS_IOC_SYNC")

	if err := subvolDelete(mnt, snap); err != nil {
		return fail("SubvolDelete(snap)", err)
	}
	if err := subvolDelete(mnt, sub); err != nil {
		return fail("SubvolDelete(sub)", err)
	}
	fmt.Fprintf(stdout, "OK: BTRFS_IOC_SNAP_DESTROY removed %s and %s\n", snapPath, subPath)
	return 0
}

func fail(what string, err error) int {
	fmt.Fprintf(stdout, "FAIL: %s: %v\n", what, err)
	return 1
}
