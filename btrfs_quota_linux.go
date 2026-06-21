// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

//go:build linux

package btrfs

import (
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"unsafe"
)

// This file implements btrfs quota-group (qgroup) management and
// defragmentation via BTRFS_IOC_* ioctls on a directory/file fd. As with the
// rest of the package it is pure Go and never shells out to the btrfs CLI.

// QuotaEnable turns on quota accounting on the btrfs filesystem containing
// path, via BTRFS_IOC_QUOTA_CTL with BTRFS_QUOTA_CTL_ENABLE. Enabling quotas
// kicks off an asynchronous rescan to populate qgroup usage; callers that need
// accurate numbers immediately should write/Sync and allow the rescan to
// settle. path is typically the mount point. Requires root.
func QuotaEnable(path string) error {
	var args btrfsIoctlQuotaCtlArgs
	args.Cmd = quotaCtlEnable
	err := ioctlDir(path, BTRFS_IOC_QUOTA_CTL, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err != nil {
		return fmt.Errorf("BTRFS_IOC_QUOTA_CTL(enable) %s: %w", path, err)
	}
	return nil
}

// QuotaDisable turns off quota accounting on the btrfs filesystem containing
// path, via BTRFS_IOC_QUOTA_CTL with BTRFS_QUOTA_CTL_DISABLE. Requires root.
func QuotaDisable(path string) error {
	var args btrfsIoctlQuotaCtlArgs
	args.Cmd = quotaCtlDisable
	err := ioctlDir(path, BTRFS_IOC_QUOTA_CTL, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err != nil {
		return fmt.Errorf("BTRFS_IOC_QUOTA_CTL(disable) %s: %w", path, err)
	}
	return nil
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

const qgroupIDSubvolMask = (uint64(1) << 48) - 1

// ListQgroups enumerates every qgroup on the btrfs filesystem containing path.
// It walks the quota tree (BTRFS_QUOTA_TREE_OBJECTID) via TREE_SEARCH(_V2),
// collecting QGROUP_INFO items (referenced/exclusive usage) and QGROUP_LIMIT
// items (limits), keyed by qgroup id (the item offset). Quotas must be enabled
// or the quota tree does not exist (the kernel returns ENOENT, surfaced as an
// error). The walk reuses the same tree-search helper as ListSubvolumes and is
// privileged (typically root).
func ListQgroups(path string) ([]Qgroup, error) {
	f, err := os.Open(path)
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
