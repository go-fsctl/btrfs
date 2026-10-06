// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

//go:build linux

package btrfs

import (
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// This file implements btrfs quota-group (qgroup) management and
// defragmentation via BTRFS_IOC_* ioctls on a directory/file fd. As with the
// rest of the package it is pure Go and never shells out to the btrfs CLI.

// QuotaEnable turns on quota accounting on the btrfs filesystem containing
// path, via BTRFS_IOC_QUOTA_CTL with BTRFS_QUOTA_CTL_ENABLE. Enabling quotas
// kicks off an asynchronous rescan to populate qgroup usage
// (fs/btrfs/qgroup.c btrfs_quota_enable() queues the rescan worker itself), so
// a QuotaRescan issued straight afterwards fails with EINPROGRESS; call
// QuotaRescanWait to block until the numbers are populated. path is typically
// the mount point. Requires CAP_SYS_ADMIN.
func QuotaEnable(path string) error {
	return quotaCtl(path, quotaCtlEnable, "enable")
}

// QuotaEnableSimple turns on simple quota accounting (squotas) on the btrfs
// filesystem containing path, via BTRFS_IOC_QUOTA_CTL with
// BTRFS_QUOTA_CTL_ENABLE_SIMPLE_QUOTA. Simple quotas appeared in Linux 6.7
// (btrfs-progs Documentation/Kernel-by-version.rst, 6.7: "simple quota
// accounting (squota)") and set the SIMPLE_QUOTA incompat feature on the
// filesystem, which older kernels then refuse to mount. A kernel that does not
// know the command answers EINVAL (fs/btrfs/ioctl.c btrfs_ioctl_quota_ctl():
// an unlisted cmd falls to the switch's default case, ret = -EINVAL).
//
// Squotas share the qgroup API and limits, but "do not track shared vs.
// exclusive usage. Instead, they account all extents to the subvolume that
// first allocated it" (btrfs-progs Documentation/ch-quota-intro.rst). An
// extent shared with a snapshot therefore stays charged to its first owner even
// after that owner deletes it. There is no rescan in simple mode:
// fs/btrfs/qgroup.c qgroup_rescan_init() returns EINVAL ("running in simple
// mode"), so QuotaRescan fails with EINVAL and QuotaRescanWait returns at once.
//
// Enabling is a no-op once quotas are on in either mode: fs/btrfs/qgroup.c
// btrfs_quota_enable() returns success early when a quota root already exists,
// so this call on a filesystem with full qgroups returns nil and leaves it in
// full mode. To switch, QuotaDisable first. Requires CAP_SYS_ADMIN.
func QuotaEnableSimple(path string) error {
	return quotaCtl(path, quotaCtlEnableSimple, "enable-simple")
}

// QuotaDisable turns off quota accounting on the btrfs filesystem containing
// path, via BTRFS_IOC_QUOTA_CTL with BTRFS_QUOTA_CTL_DISABLE. Requires root.
func QuotaDisable(path string) error {
	return quotaCtl(path, quotaCtlDisable, "disable")
}

func quotaCtl(path string, cmd uint64, verb string) error {
	var args btrfsIoctlQuotaCtlArgs
	args.Cmd = cmd
	err := ioctlDir(path, BTRFS_IOC_QUOTA_CTL, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err != nil {
		return fmt.Errorf("BTRFS_IOC_QUOTA_CTL(%s) %s: %w", verb, path, err)
	}
	return nil
}

// QuotaRescan starts a full rescan of qgroup accounting on the btrfs filesystem
// containing path, via BTRFS_IOC_QUOTA_RESCAN, and returns as soon as the
// kernel has queued it; the rescan itself runs asynchronously (use
// QuotaRescanStatus to poll, QuotaRescanWait to block, or QuotaRescanAndWait
// for both steps). The kernel trashes the current numbers and recomputes them,
// which is what makes usage correct after qgroups were assigned to a parent
// after the fact.
//
// The errno is wrapped, so callers can test it with errors.Is:
//
//   - EINPROGRESS: a rescan is already running (fs/btrfs/qgroup.c
//     qgroup_rescan_init() sees BTRFS_QGROUP_STATUS_FLAG_RESCAN). This is
//     always the case right after QuotaEnable, which starts one itself.
//   - ENOTCONN: quotas are not enabled (fs/btrfs/ioctl.c
//     btrfs_ioctl_quota_rescan() checks btrfs_qgroup_enabled() first).
//   - EINVAL: the filesystem is in simple-quota mode, which has no rescan.
//   - EBUSY: quotas are being disabled concurrently.
//   - EPERM: the caller lacks CAP_SYS_ADMIN.
func QuotaRescan(path string) error {
	var args btrfsIoctlQuotaRescanArgs // flags must be 0 or the kernel says EINVAL
	err := ioctlDir(path, BTRFS_IOC_QUOTA_RESCAN, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err != nil {
		return fmt.Errorf("BTRFS_IOC_QUOTA_RESCAN %s: %w", path, err)
	}
	return nil
}

// RescanStatus is the state of a qgroup rescan reported by QuotaRescanStatus.
type RescanStatus struct {
	// Running is true while a rescan is in progress (the kernel's flags = 1).
	Running bool
	// Progress is the objectid the rescan has reached; 0 when not running.
	Progress uint64
}

// QuotaRescanStatus reports whether a qgroup rescan is running on the btrfs
// filesystem containing path, and how far it has got, via
// BTRFS_IOC_QUOTA_RESCAN_STATUS. fs/btrfs/ioctl.c
// btrfs_ioctl_quota_rescan_status() answers even when quotas are off (it then
// reports not running). Requires CAP_SYS_ADMIN.
func QuotaRescanStatus(path string) (RescanStatus, error) {
	var args btrfsIoctlQuotaRescanArgs
	err := ioctlDir(path, BTRFS_IOC_QUOTA_RESCAN_STATUS, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err != nil {
		return RescanStatus{}, fmt.Errorf("BTRFS_IOC_QUOTA_RESCAN_STATUS %s: %w", path, err)
	}
	return RescanStatus{Running: args.Flags != 0, Progress: args.Progress}, nil
}

// QuotaRescanWait blocks until the qgroup rescan running on the btrfs
// filesystem containing path has finished, via BTRFS_IOC_QUOTA_RESCAN_WAIT. It
// returns immediately when no rescan is running (fs/btrfs/qgroup.c
// btrfs_qgroup_wait_for_completion() checks qgroup_rescan_running first), so it
// is safe to call unconditionally after QuotaEnable or QuotaRescan. The wait is
// interruptible; the Go runtime installs its signal handlers with SA_RESTART,
// so a signal restarts it rather than surfacing EINTR. Requires CAP_SYS_ADMIN.
func QuotaRescanWait(path string) error {
	err := ioctlDir(path, BTRFS_IOC_QUOTA_RESCAN_WAIT, nil)
	if err != nil {
		return fmt.Errorf("BTRFS_IOC_QUOTA_RESCAN_WAIT %s: %w", path, err)
	}
	return nil
}

// QuotaRescanAndWait starts a rescan and waits for it to finish, accepting a
// rescan that is already running — the behaviour of `btrfs quota rescan -w`
// (btrfs-progs cmds/quota.c, which tolerates EINPROGRESS only when asked to
// wait). Any other QuotaRescan error is returned without waiting.
func QuotaRescanAndWait(path string) error {
	if err := QuotaRescan(path); err != nil && !errors.Is(err, unix.EINPROGRESS) {
		return err
	}
	return QuotaRescanWait(path)
}

// QgroupCreate creates the qgroup with the given id on the filesystem
// containing path, via BTRFS_IOC_QGROUP_CREATE (create=1). Qgroup ids are
// level/subvolid pairs encoded as level<<48 | subvolid; the per-subvolume
// qgroups at level 0 are created automatically, so this is primarily for
// higher-level aggregation qgroups (e.g. 1/100). Quotas must be enabled first.
// Requires root.
func QgroupCreate(path string, qgroupid uint64) error {
	return qgroupCreateDestroy(path, qgroupid, true)
}

// QgroupDestroy removes the qgroup with the given id on the filesystem
// containing path, via BTRFS_IOC_QGROUP_CREATE (create=0). Requires root.
func QgroupDestroy(path string, qgroupid uint64) error {
	return qgroupCreateDestroy(path, qgroupid, false)
}

func qgroupCreateDestroy(path string, qgroupid uint64, create bool) error {
	var args btrfsIoctlQgroupCreateArgs
	if create {
		args.Create = 1
	}
	args.Qgroupid = qgroupid
	verb := "create"
	if !create {
		verb = "destroy"
	}
	err := ioctlDir(path, BTRFS_IOC_QGROUP_CREATE, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err != nil {
		return fmt.Errorf("BTRFS_IOC_QGROUP_CREATE(%s id=%d) %s: %w", verb, qgroupid, path, err)
	}
	return nil
}

// QgroupAssign makes the qgroup src a member of the qgroup dst on the
// filesystem containing path, via BTRFS_IOC_QGROUP_ASSIGN (assign=1). dst's
// accounting then includes src's. Requires root.
func QgroupAssign(path string, src, dst uint64) error {
	return qgroupAssignRemove(path, src, dst, true)
}

// QgroupRemove removes the membership relation making src a member of dst on
// the filesystem containing path, via BTRFS_IOC_QGROUP_ASSIGN (assign=0).
// Requires root.
func QgroupRemove(path string, src, dst uint64) error {
	return qgroupAssignRemove(path, src, dst, false)
}

func qgroupAssignRemove(path string, src, dst uint64, assign bool) error {
	var args btrfsIoctlQgroupAssignArgs
	if assign {
		args.Assign = 1
	}
	args.Src = src
	args.Dst = dst
	verb := "assign"
	if !assign {
		verb = "remove"
	}
	// QGROUP_ASSIGN can return a positive value meaning "quota rescan needed";
	// the kernel signals that as a normal return, not an error, so we treat any
	// non-error result as success.
	err := ioctlDir(path, BTRFS_IOC_QGROUP_ASSIGN, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err != nil {
		return fmt.Errorf("BTRFS_IOC_QGROUP_ASSIGN(%s src=%d dst=%d) %s: %w", verb, src, dst, path, err)
	}
	return nil
}

// QgroupLimits is the set of limits applied by QgroupLimit. A zero field with
// its corresponding flag unset leaves that limit unchanged/unlimited; set the
// matching QgroupLimit* flag in Flags for each field the kernel should enforce.
type QgroupLimits struct {
	Flags   uint64 // QgroupLimit* mask selecting which fields apply
	MaxRfer uint64 // max referenced bytes (with QgroupLimitMaxRfer)
	MaxExcl uint64 // max exclusive bytes (with QgroupLimitMaxExcl)
	RsvRfer uint64 // referenced reservation limit (with QgroupLimitRsvRfer)
	RsvExcl uint64 // exclusive reservation limit (with QgroupLimitRsvExcl)
}

// QgroupLimit sets usage limits on the qgroup with the given id on the
// filesystem containing path, via BTRFS_IOC_QGROUP_LIMIT. qgroupid 0 targets
// the qgroup of the subvolume the ioctl is issued on. Set lim.Flags to a mask
// of QgroupLimit* bits selecting which of MaxRfer/MaxExcl/RsvRfer/RsvExcl the
// kernel should enforce; once a max_rfer limit is in force, writes that would
// exceed it fail with EDQUOT. Requires root.
func QgroupLimit(path string, qgroupid uint64, lim QgroupLimits) error {
	var args btrfsIoctlQgroupLimitArgs
	args.Qgroupid = qgroupid
	args.Lim = btrfsQgroupLimit{
		Flags:   lim.Flags,
		MaxRfer: lim.MaxRfer,
		MaxExcl: lim.MaxExcl,
		RsvRfer: lim.RsvRfer,
		RsvExcl: lim.RsvExcl,
	}
	err := ioctlDir(path, BTRFS_IOC_QGROUP_LIMIT, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err != nil {
		return fmt.Errorf("BTRFS_IOC_QGROUP_LIMIT(id=%d) %s: %w", qgroupid, path, err)
	}
	return nil
}

// SubvolQgroupID returns the id of the level-0 qgroup of the subvolume
// containing path: QgroupID(0, subvolume id). Every subvolume gets that qgroup
// automatically when quotas are on — btrfs-progs
// Documentation/ch-quota-intro.rst: "Qgroups of level 0 get created
// automatically when a subvolume/snapshot gets created. The ID of the qgroup
// corresponds to the ID of the subvolume". The lookup itself is SubvolID and
// does not need quotas enabled.
func SubvolQgroupID(path string) (uint64, error) {
	id, err := SubvolID(path)
	if err != nil {
		return 0, err
	}
	return QgroupID(0, id), nil
}

// SubvolLimit caps the referenced bytes of the subvolume at path (its level-0
// qgroup), via BTRFS_IOC_QGROUP_LIMIT issued on the subvolume itself with
// qgroupid 0: fs/btrfs/ioctl.c btrfs_ioctl_qgroup_limit() substitutes the
// root of the fd ("take the current subvol as qgroup"), so path must lie inside
// the subvolume to limit. maxRfer 0 removes the limit (it is sent as the
// kernel's (u64)-1 "clear" value). Once a limit is in force, writes that would
// exceed it fail with EDQUOT. Quotas must be enabled (else ENOTCONN). Requires
// CAP_SYS_ADMIN.
func SubvolLimit(path string, maxRfer uint64) error {
	if maxRfer == 0 {
		maxRfer = qgroupLimitClear
	}
	return QgroupLimit(path, 0, QgroupLimits{Flags: QgroupLimitMaxRfer, MaxRfer: maxRfer})
}

// Qgroup is one entry returned by ListQgroups: a quota group with its decoded
// id (level/subvolume), its referenced/exclusive byte usage, and its limits.
// HasLimit reports whether any limit is in force (lim_flags non-zero).
type Qgroup struct {
	ID       uint64 // raw qgroup id (level<<48 | subvolid)
	Level    uint64 // qgroup level (id >> 48)
	SubvolID uint64 // subvolume id component (id & ((1<<48)-1))
	Rfer     uint64 // referenced bytes (QGROUP_INFO.rfer)
	Excl     uint64 // exclusive bytes (QGROUP_INFO.excl)
	MaxRfer  uint64 // max referenced limit (QGROUP_LIMIT.max_rfer), 0 = unlimited
	MaxExcl  uint64 // max exclusive limit (QGROUP_LIMIT.max_excl), 0 = unlimited
	LimFlags uint64 // QGROUP_LIMIT.flags (mask of QgroupLimit* bits)
}

// HasLimit reports whether any usage limit is in force on this qgroup.
func (q Qgroup) HasLimit() bool { return q.LimFlags != 0 }

// ListQgroups enumerates every qgroup on the btrfs filesystem containing path.
// It walks the quota tree (BTRFS_QUOTA_TREE_OBJECTID) via TREE_SEARCH(_V2),
// collecting QGROUP_INFO items (referenced/exclusive usage) and QGROUP_LIMIT
// items (limits), keyed by qgroup id (the item offset). Quotas must be enabled
// or the quota tree does not exist (the kernel returns ENOENT, surfaced as an
// error). The walk reuses the same tree-search helper as ListSubvolumes and is
// privileged (typically root).
func ListQgroups(path string) ([]Qgroup, error) {
	f, err := osOpen(path)
	if err != nil {
		return nil, fmt.Errorf("ListQgroups: open %q: %w", path, err)
	}
	defer f.Close()

	byID := map[uint64]*Qgroup{}
	get := func(id uint64) *Qgroup {
		q, ok := byID[id]
		if !ok {
			q = &Qgroup{
				ID:       id,
				Level:    id >> 48,
				SubvolID: id & qgroupIDSubvolMask,
			}
			byID[id] = q
		}
		return q
	}

	// QGROUP_INFO and QGROUP_LIMIT keys are (objectid=0, type, offset=qgroupid);
	// the item bodies are packed little-endian and we decode them by field.
	emit := func(hdr *btrfsIoctlSearchHeader, body []byte) error {
		switch hdr.Type {
		case btrfsQgroupInfoKey:
			if len(body) < btrfsQgroupInfoItemSize {
				return nil
			}
			q := get(hdr.Offset)
			q.Rfer = binary.LittleEndian.Uint64(body[8:16])
			q.Excl = binary.LittleEndian.Uint64(body[24:32])
		case btrfsQgroupLimitKey:
			if len(body) < btrfsQgroupLimitItemSize {
				return nil
			}
			q := get(hdr.Offset)
			q.LimFlags = binary.LittleEndian.Uint64(body[0:8])
			q.MaxRfer = binary.LittleEndian.Uint64(body[8:16])
			q.MaxExcl = binary.LittleEndian.Uint64(body[16:24])
		}
		return nil
	}

	// Search the whole quota tree across both item types in one walk: the type
	// range [QGROUP_INFO_KEY, QGROUP_LIMIT_KEY] also spans QGROUP_LIMIT_KEY's
	// neighbours, but emit filters by exact type so unrelated items are ignored.
	if err := searchTreeTypeRange(f.Fd(), btrfsQuotaTreeObjectID, btrfsQgroupInfoKey, btrfsQgroupLimitKey, emit); err != nil {
		return nil, fmt.Errorf("ListQgroups: %w", err)
	}

	out := make([]Qgroup, 0, len(byID))
	for _, q := range byID {
		out = append(out, *q)
	}
	return out, nil
}

// Defrag defragments the file or directory at path via BTRFS_IOC_DEFRAG. When
// path is a regular file the whole file is defragmented; when it is a directory
// the kernel defragments that directory's b-tree (it does not recurse into the
// directory's files — use DefragRange per file or walk the tree yourself). This
// is the simple, range-less variant; use DefragRange for byte-range control,
// extent thresholds, or forced compression.
func Defrag(path string) error {
	// BTRFS_IOC_DEFRAG takes a btrfs_ioctl_vol_args by ABI but the kernel
	// operates on the fd the ioctl is issued against and ignores the contents.
	var args btrfsIoctlVolArgs
	err := ioctlDir(path, BTRFS_IOC_DEFRAG, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err != nil {
		return fmt.Errorf("BTRFS_IOC_DEFRAG %s: %w", path, err)
	}
	return nil
}

// DefragRangeOptions controls a ranged defragmentation issued by DefragRange.
// The zero value defragments the whole file ([0, ^0)) with kernel-default
// behaviour.
type DefragRangeOptions struct {
	// Start is the first byte of the range to defragment.
	Start uint64
	// Len is the length of the range in bytes; 0 is treated as "to end of file"
	// (the kernel uses ^0 as the sentinel, which DefragRange substitutes for 0).
	Len uint64
	// Flags is a mask of DefragRange* bits (e.g. DefragRangeCompress to force
	// the CompressType compression, DefragRangeStartIO to flush after queuing).
	Flags uint64
	// ExtentThresh is the maximum extent size (bytes) considered fragmented; 0
	// selects the kernel default.
	ExtentThresh uint32
	// CompressType selects the compression algorithm when DefragRangeCompress is
	// set in Flags (0 = no/zlib default per kernel).
	CompressType uint32
}

// DefragRange defragments a byte range of the file at path via
// BTRFS_IOC_DEFRAG_RANGE, giving control over the range, extent threshold, and
// optional forced compression. path must be a regular file. A zero Len is
// converted to the kernel's "to EOF" sentinel so the zero-value options
// defragment the entire file.
func DefragRange(path string, opts DefragRangeOptions) error {
	var args btrfsIoctlDefragRangeArgs
	args.Start = opts.Start
	if opts.Len == 0 {
		args.Len = ^uint64(0)
	} else {
		args.Len = opts.Len
	}
	args.Flags = opts.Flags
	args.ExtentThresh = opts.ExtentThresh
	args.CompressType = opts.CompressType
	err := ioctlDir(path, BTRFS_IOC_DEFRAG_RANGE, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err != nil {
		return fmt.Errorf("BTRFS_IOC_DEFRAG_RANGE %s: %w", path, err)
	}
	return nil
}
