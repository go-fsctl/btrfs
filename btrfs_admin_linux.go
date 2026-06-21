// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

//go:build linux

package btrfs

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// isUnsupportedIoctl reports whether err indicates the kernel does not
// implement the issued ioctl (ENOTTY) or rejects its argument shape (EINVAL on
// some old kernels for the V2 variants), so the caller can fall back.
func isUnsupportedIoctl(err error) bool {
	return errors.Is(err, unix.ENOTTY)
}

// This file implements the btrfs admin operations beyond the subvolume set,
// all via BTRFS_IOC_* ioctls on a directory fd (the mount point or any path on
// the filesystem): subvolume listing, device management, scrub, and balance.
// As with the rest of the package it is pure Go and never shells out to the
// btrfs CLI.

// Subvolume is one entry returned by ListSubvolumes: a subvolume (or snapshot)
// with its tree id, the id of the subvolume that contains it, and its name as
// recorded in the parent. Path is the name joined onto the parent's path,
// giving the full path of the subvolume relative to the top-level (id 5)
// subvolume.
type Subvolume struct {
	ID       uint64 // this subvolume's tree id (objectid in the root tree)
	ParentID uint64 // id of the containing subvolume
	Name     string // name within the parent subvolume
	Path     string // full path relative to the top-level subvolume
}

// ListSubvolumes enumerates every subvolume on the btrfs filesystem containing
// path. It walks the root tree (BTRFS_ROOT_TREE_OBJECTID) via the TREE_SEARCH
// ioctl, collecting BTRFS_ROOT_REF items: each ROOT_REF maps a child subvolume
// id (the item offset) to its parent subvolume id (the item objectid) and
// carries the child's name. The returned slice is keyed and de-duplicated by
// subvolume id; Path is resolved by chaining names up to the top-level (id 5)
// subvolume.
//
// It prefers BTRFS_IOC_TREE_SEARCH_V2 (larger result buffers, EOVERFLOW
// signalling) and transparently falls back to BTRFS_IOC_TREE_SEARCH on kernels
// that do not implement V2. The ioctl is privileged (the root tree is not
// otherwise visible), so this typically requires root.
func ListSubvolumes(path string) ([]Subvolume, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("ListSubvolumes: open %q: %w", path, err)
	}
	defer f.Close()

	// Collect ROOT_REF items from the root tree. ROOT_REF keys are
	// (objectid=parent_id, type=ROOT_REF_KEY, offset=child_id); the item body
	// is a btrfs_root_ref followed by the child's name.
	byID := map[uint64]subvolRef{}

	emit := func(hdr *btrfsIoctlSearchHeader, body []byte) error {
		if hdr.Type != btrfsRootRefKey {
			return nil
		}
		if len(body) < btrfsRootRefSize {
			return nil
		}
		nameLen := int(binary.LittleEndian.Uint16(body[16:18]))
		nameStart := btrfsRootRefSize
		if nameStart+nameLen > len(body) {
			nameLen = len(body) - nameStart
		}
		name := string(body[nameStart : nameStart+nameLen])
		// offset of a ROOT_REF key is the child subvolume id; objectid is the
		// parent subvolume id.
		byID[hdr.Offset] = subvolRef{parent: hdr.Objectid, name: name}
		return nil
	}

	if err := searchRootTree(f.Fd(), btrfsRootRefKey, emit); err != nil {
		return nil, fmt.Errorf("ListSubvolumes: %w", err)
	}

	out := make([]Subvolume, 0, len(byID))
	for id, r := range byID {
		out = append(out, Subvolume{
			ID:       id,
			ParentID: r.parent,
			Name:     r.name,
			Path:     resolvePath(id, byID),
		})
	}
	return out, nil
}

// subvolRef is a ROOT_REF item reduced to the parent subvolume id and the
// child's name within it.
type subvolRef struct {
	parent uint64
	name   string
}

// resolvePath builds the full path of subvolume id by chaining the ROOT_REF
// names up toward the top-level subvolume (id 5 / FS_TREE). A small visited
// guard prevents an infinite loop on a cyclic/corrupt tree.
func resolvePath(id uint64, byID map[uint64]subvolRef) string {
	var parts []string
	seen := map[uint64]bool{}
	for cur := id; cur >= btrfsFirstFreeObjectID && !seen[cur]; {
		seen[cur] = true
		r, ok := byID[cur]
		if !ok {
			break
		}
		parts = append([]string{r.name}, parts...)
		cur = r.parent
	}
	return joinParts(parts)
}

func joinParts(parts []string) string {
	s := ""
	for i, p := range parts {
		if i > 0 {
			s += "/"
		}
		s += p
	}
	return s
}

// searchRootTree issues TREE_SEARCH(_V2) over the root tree filtering for items
// of the given key type, calling fn for each matching item with its header and
// body. It iterates in batches, advancing the search range past the last item
// each round until the kernel returns fewer items than requested.
func searchRootTree(fd uintptr, keyType uint32, fn func(*btrfsIoctlSearchHeader, []byte) error) error {
	return searchTreeTypeRange(fd, btrfsRootTreeObjectID, keyType, keyType, fn)
}

// searchTreeTypeRange issues TREE_SEARCH(_V2) over an arbitrary tree (treeID)
// filtering for items whose key type lies in [minType, maxType], calling fn for
// each with its header and body. It is the generalisation of searchRootTree
// used by both the subvolume listing (root tree, ROOT_REF) and the qgroup
// listing (quota tree, QGROUP_INFO/QGROUP_LIMIT). It iterates in batches,
// advancing the search range past the last item each round until the kernel
// returns fewer items than requested.
func searchTreeTypeRange(fd uintptr, treeID uint64, minType, maxType uint32, fn func(*btrfsIoctlSearchHeader, []byte) error) error {
	key := btrfsIoctlSearchKey{
		TreeID:      treeID,
		MinObjectid: 0,
		MaxObjectid: ^uint64(0),
		MinOffset:   0,
		MaxOffset:   ^uint64(0),
		MinTransid:  0,
		MaxTransid:  ^uint64(0),
		MinType:     minType,
		MaxType:     maxType,
	}

	for {
		key.NrItems = 4096
		found, last, err := treeSearchV2(fd, &key, fn)
		if err != nil {
			if err == errTreeSearchV2Unsupported {
				// Restart the walk from the current range using the v1 ioctl.
				return searchRootTreeV1(fd, &key, fn)
			}
			return err
		}
		if found == 0 {
			return nil
		}
		if !advanceKey(&key, last) {
			return nil
		}
	}
}

// errTreeSearchV2Unsupported signals that BTRFS_IOC_TREE_SEARCH_V2 is not
// available on this kernel so the caller should fall back to v1.
var errTreeSearchV2Unsupported = fmt.Errorf("TREE_SEARCH_V2 unsupported")

// treeSearchV2 performs one BTRFS_IOC_TREE_SEARCH_V2 call. It allocates a
// header + buffer block, copies the key in, issues the ioctl, then parses the
// returned items. It returns the number of items found and the header of the
// last item (for advancing the range). On ENOTTY it returns
// errTreeSearchV2Unsupported.
func treeSearchV2(fd uintptr, key *btrfsIoctlSearchKey, fn func(*btrfsIoctlSearchHeader, []byte) error) (found int, last btrfsIoctlSearchHeader, err error) {
	const bufSize = 1 << 18 // 256 KiB result buffer
	hdrSize := int(unsafe.Sizeof(btrfsIoctlSearchArgsV2Hdr{}))
	raw := make([]byte, hdrSize+bufSize)

	h := (*btrfsIoctlSearchArgsV2Hdr)(unsafe.Pointer(&raw[0]))
	h.Key = *key
	h.BufSize = bufSize

	if e := ioctlFd(fd, BTRFS_IOC_TREE_SEARCH_V2, unsafe.Pointer(&raw[0])); e != nil {
		if isUnsupportedIoctl(e) {
			return 0, last, errTreeSearchV2Unsupported
		}
		return 0, last, fmt.Errorf("BTRFS_IOC_TREE_SEARCH_V2: %w", e)
	}
	n := int(h.Key.NrItems)
	buf := raw[hdrSize:]

	off := 0
	shSize := int(unsafe.Sizeof(btrfsIoctlSearchHeader{}))
	for i := 0; i < n; i++ {
		if off+shSize > len(buf) {
			break
		}
		sh := (*btrfsIoctlSearchHeader)(unsafe.Pointer(&buf[off]))
		hdr := *sh
		off += shSize
		end := off + int(hdr.Len)
		if end > len(buf) {
			break
		}
		body := buf[off:end]
		if e := fn(&hdr, body); e != nil {
			return found, last, e
		}
		off = end
		last = hdr
		found++
	}
	runtime.KeepAlive(raw)
	return found, last, nil
}

// searchRootTreeV1 is the BTRFS_IOC_TREE_SEARCH (v1) fallback: same semantics
// but a fixed 3992-byte in-struct buffer, so it iterates more times.
func searchRootTreeV1(fd uintptr, key *btrfsIoctlSearchKey, fn func(*btrfsIoctlSearchHeader, []byte) error) error {
	shSize := int(unsafe.Sizeof(btrfsIoctlSearchHeader{}))
	for {
		var args btrfsIoctlSearchArgs
		args.Key = *key
		args.Key.NrItems = 512
		if err := ioctlFd(fd, BTRFS_IOC_TREE_SEARCH, unsafe.Pointer(&args)); err != nil {
			return fmt.Errorf("BTRFS_IOC_TREE_SEARCH: %w", err)
		}
		n := int(args.Key.NrItems)
		if n == 0 {
			return nil
		}
		buf := args.Buf[:]
		off := 0
		var last btrfsIoctlSearchHeader
		for i := 0; i < n; i++ {
			if off+shSize > len(buf) {
				break
			}
			sh := (*btrfsIoctlSearchHeader)(unsafe.Pointer(&buf[off]))
			hdr := *sh
			off += shSize
			end := off + int(hdr.Len)
			if end > len(buf) {
				break
			}
			if err := fn(&hdr, buf[off:end]); err != nil {
				return err
			}
			off = end
			last = hdr
		}
		*key = args.Key
		if !advanceKey(key, last) {
			return nil
		}
	}
}

// advanceKey moves the search range just past the last returned item so the
// next batch continues from there. It increments the (objectid, type, offset)
// tuple in that priority order, returning false when the range is exhausted.
func advanceKey(key *btrfsIoctlSearchKey, last btrfsIoctlSearchHeader) bool {
	key.MinObjectid = last.Objectid
	key.MinType = last.Type
	key.MinOffset = last.Offset
	if key.MinOffset < ^uint64(0) {
		key.MinOffset++
		return true
	}
	key.MinOffset = 0
	if uint64(key.MinType) < uint64(^uint32(0)) {
		key.MinType++
		return true
	}
	key.MinType = 0
	if key.MinObjectid < ^uint64(0) {
		key.MinObjectid++
		return true
	}
	return false
}

// DeviceAdd adds the block device at devPath to the btrfs filesystem mounted at
// mountPath, via BTRFS_IOC_ADD_DEV (btrfs_ioctl_vol_args; the device path goes
// in args.name). The device must not be in use and is typically wiped of any
// existing signature by the caller. Requires root.
func DeviceAdd(mountPath, devPath string) error {
	if devPath == "" {
		return fmt.Errorf("DeviceAdd: empty device path")
	}
	var args btrfsIoctlVolArgs
	if err := putName(args.Name[:], devPath); err != nil {
		return fmt.Errorf("DeviceAdd: %w", err)
	}
	err := ioctlDir(mountPath, BTRFS_IOC_ADD_DEV, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err != nil {
		return fmt.Errorf("BTRFS_IOC_ADD_DEV %s -> %s: %w", devPath, mountPath, err)
	}
	return nil
}

// DeviceRemove removes a device from the btrfs filesystem mounted at mountPath
// via BTRFS_IOC_RM_DEV_V2 (btrfs_ioctl_vol_args_v2), falling back to
// BTRFS_IOC_RM_DEV on kernels without V2. The device is identified by path;
// use DeviceRemoveByID to remove by devid. Requires root. The kernel relocates
// any chunks off the device before detaching it, so this can block.
func DeviceRemove(mountPath, devPath string) error {
	if devPath == "" {
		return fmt.Errorf("DeviceRemove: empty device path")
	}
	var args btrfsIoctlVolArgsV2
	if err := putName(args.Name[:], devPath); err != nil {
		return fmt.Errorf("DeviceRemove: %w", err)
	}
	err := ioctlDir(mountPath, BTRFS_IOC_RM_DEV_V2, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err == nil {
		return nil
	}
	if isUnsupportedIoctl(err) {
		// Fall back to the v1 ioctl (path in btrfs_ioctl_vol_args.name).
		var v1 btrfsIoctlVolArgs
		if e := putName(v1.Name[:], devPath); e != nil {
			return fmt.Errorf("DeviceRemove: %w", e)
		}
		e := ioctlDir(mountPath, BTRFS_IOC_RM_DEV, unsafe.Pointer(&v1))
		runtime.KeepAlive(&v1)
		if e != nil {
			return fmt.Errorf("BTRFS_IOC_RM_DEV %s: %w", devPath, e)
		}
		return nil
	}
	return fmt.Errorf("BTRFS_IOC_RM_DEV_V2 %s: %w", devPath, err)
}

// BTRFS_IOC_RM_DEV_V2 flag: identify the device by devid rather than path.
const btrfsDeviceSpecByID = 1 << 3 // BTRFS_DEVICE_SPEC_BY_ID

// DeviceRemoveByID removes the device with the given devid from the filesystem
// mounted at mountPath, via BTRFS_IOC_RM_DEV_V2 with the BY_ID flag set and the
// devid placed in the args.devid union member (which overlays the name field).
// Requires root. There is no v1 fallback for by-id removal.
func DeviceRemoveByID(mountPath string, devid uint64) error {
	var args btrfsIoctlVolArgsV2
	args.Flags = btrfsDeviceSpecByID
	// The devid shares storage with the name[] union at the same offset; write
	// it into the first 8 bytes of Name.
	binary.LittleEndian.PutUint64(args.Name[:8], devid)
	err := ioctlDir(mountPath, BTRFS_IOC_RM_DEV_V2, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err != nil {
		return fmt.Errorf("BTRFS_IOC_RM_DEV_V2 devid=%d: %w", devid, err)
	}
	return nil
}

// DeviceInfo is the decoded result of BTRFS_IOC_DEV_INFO for a single device.
type DeviceInfo struct {
	Devid      uint64
	UUID       [16]byte
	BytesUsed  uint64
	TotalBytes uint64
	Path       string
}

// GetDeviceInfo returns information about the device with the given devid on
// the filesystem containing path, via BTRFS_IOC_DEV_INFO. Use FsInfo first to
// learn the valid devids (1..max_id).
func GetDeviceInfo(path string, devid uint64) (*DeviceInfo, error) {
	var args btrfsIoctlDevInfoArgs
	args.Devid = devid
	err := ioctlDir(path, BTRFS_IOC_DEV_INFO, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err != nil {
		return nil, fmt.Errorf("BTRFS_IOC_DEV_INFO devid=%d: %w", devid, err)
	}
	return &DeviceInfo{
		Devid:      args.Devid,
		UUID:       args.UUID,
		BytesUsed:  args.BytesUsed,
		TotalBytes: args.TotalBytes,
		Path:       cstr(args.Path[:]),
	}, nil
}

// FsInfo is the decoded result of BTRFS_IOC_FS_INFO.
type FsInfo struct {
	MaxID      uint64   // highest device id; devids 1..MaxID may be valid
	NumDevices uint64   // number of devices in the filesystem
	FSID       [16]byte // filesystem UUID
	Nodesize   uint32
	Sectorsize uint32
	Generation uint64
}

// GetFsInfo returns filesystem-wide information via BTRFS_IOC_FS_INFO,
// including the device count and the highest device id. It requests the
// optional generation field via the FS_INFO_FLAG_GENERATION flag.
func GetFsInfo(path string) (*FsInfo, error) {
	const flagGeneration = 1 << 1 // BTRFS_FS_INFO_FLAG_GENERATION
	var args btrfsIoctlFsInfoArgs
	args.Flags = flagGeneration
	err := ioctlDir(path, BTRFS_IOC_FS_INFO, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err != nil {
		return nil, fmt.Errorf("BTRFS_IOC_FS_INFO %s: %w", path, err)
	}
	return &FsInfo{
		MaxID:      args.MaxID,
		NumDevices: args.NumDevices,
		FSID:       args.FSID,
		Nodesize:   args.Nodesize,
		Sectorsize: args.Sectorsize,
		Generation: args.Generation,
	}, nil
}

// ScrubProgress is the decoded subset of btrfs_scrub_progress most callers
// care about: how much was scrubbed and how many errors were seen. A healthy
// scrub reports all error counters as zero.
type ScrubProgress struct {
	DataBytesScrubbed   uint64
	TreeBytesScrubbed   uint64
	ReadErrors          uint64
	CsumErrors          uint64
	VerifyErrors        uint64
	SuperErrors         uint64
	UncorrectableErrors uint64
	CorrectedErrors     uint64
}

func decodeScrubProgress(p *btrfsScrubProgress) ScrubProgress {
	return ScrubProgress{
		DataBytesScrubbed:   p.DataBytesScrubbed,
		TreeBytesScrubbed:   p.TreeBytesScrubbed,
		ReadErrors:          p.ReadErrors,
		CsumErrors:          p.CsumErrors,
		VerifyErrors:        p.VerifyErrors,
		SuperErrors:         p.SuperErrors,
		UncorrectableErrors: p.UncorrectableErrors,
		CorrectedErrors:     p.CorrectedErrors,
	}
}

// ScrubOptions controls a scrub started with ScrubStart.
type ScrubOptions struct {
	// Readonly runs the scrub in read-only mode (BTRFS_SCRUB_READONLY): it
	// reports errors but does not attempt to repair them.
	Readonly bool
}

// ScrubStart runs a synchronous scrub of the device devid on the filesystem
// containing path, via BTRFS_IOC_SCRUB (scanning the whole device: start=0,
// end=^0). The ioctl blocks until the scrub finishes (or is cancelled from
// another fd) and returns the final progress. devid 0 is not valid; pass a
// real device id from FsInfo/GetDeviceInfo. Requires root.
//
// Note that BTRFS_IOC_SCRUB is synchronous per device; for asynchronous
// progress polling, start the scrub from one goroutine/fd and call
// ScrubProgressFor from another against the same devid.
func ScrubStart(path string, devid uint64, opts ScrubOptions) (ScrubProgress, error) {
	var args btrfsIoctlScrubArgs
	args.Devid = devid
	args.Start = 0
	args.End = ^uint64(0)
	if opts.Readonly {
		args.Flags = ScrubReadonly
	}
	err := ioctlDir(path, BTRFS_IOC_SCRUB, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err != nil {
		return ScrubProgress{}, fmt.Errorf("BTRFS_IOC_SCRUB devid=%d: %w", devid, err)
	}
	return decodeScrubProgress(&args.Progress), nil
}

// ScrubProgressFor queries the progress of an in-flight scrub of device devid
// on the filesystem containing path, via BTRFS_IOC_SCRUB_PROGRESS. It returns
// an error if no scrub is running for that device (the kernel reports ENOTCONN).
func ScrubProgressFor(path string, devid uint64) (ScrubProgress, error) {
	var args btrfsIoctlScrubArgs
	args.Devid = devid
	err := ioctlDir(path, BTRFS_IOC_SCRUB_PROGRESS, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err != nil {
		return ScrubProgress{}, fmt.Errorf("BTRFS_IOC_SCRUB_PROGRESS devid=%d: %w", devid, err)
	}
	return decodeScrubProgress(&args.Progress), nil
}

// ScrubCancel cancels any running scrub on the filesystem containing path, via
// BTRFS_IOC_SCRUB_CANCEL. It is a no-op-style error if no scrub is running.
func ScrubCancel(path string) error {
	if err := ioctlDir(path, BTRFS_IOC_SCRUB_CANCEL, nil); err != nil {
		return fmt.Errorf("BTRFS_IOC_SCRUB_CANCEL %s: %w", path, err)
	}
	return nil
}

// BalanceProgress is the decoded result of a balance progress query: the run
// state and the chunk counts.
type BalanceProgress struct {
	Running    bool   // a balance is currently running
	State      uint64 // raw BTRFS_BALANCE_STATE_* bits
	Expected   uint64 // estimated chunks to relocate
	Considered uint64 // chunks considered so far
	Completed  uint64 // chunks relocated so far
}

// BalanceFilter is the per-chunk-type filter for a balance run. The zero value
// (no Flags set) relocates every chunk of that type. Set the Usage filter (and
// BalanceArgsUsage in Flags) to only relocate chunks below a usage percentage,
// the btrfs CLI's -dusage=/-musage= equivalent.
type BalanceFilter struct {
	Flags    uint64 // BTRFS_BALANCE_ARGS_* bits selecting which fields apply
	Usage    uint64 // usage percent threshold (with BalanceArgsUsage)
	Profiles uint64 // source profile mask (with BalanceArgsProfiles)
	Devid    uint64 // device id (with BalanceArgsDevid)
	Target   uint64 // target profile (with BalanceArgsConvert)
}

// BalanceArgs configures a balance started with BalanceStart. Setting Data,
// Meta, or Sys (with a corresponding type bit, applied automatically) restricts
// the balance to that chunk type with the given filter. With all three nil the
// balance covers data, metadata and system chunks (a full balance).
type BalanceArgs struct {
	Data  *BalanceFilter
	Meta  *BalanceFilter
	Sys   *BalanceFilter
	Force bool // BTRFS_BALANCE_FORCE: proceed even when reducing redundancy
}

func toKernelBalArgs(f *BalanceFilter) btrfsBalanceArgs {
	if f == nil {
		return btrfsBalanceArgs{}
	}
	return btrfsBalanceArgs{
		Profiles: f.Profiles,
		Usage:    f.Usage,
		Devid:    f.Devid,
		Target:   f.Target,
		Flags:    f.Flags,
	}
}

// BalanceStart runs a synchronous balance on the filesystem containing path,
// via BTRFS_IOC_BALANCE_V2. The ioctl blocks until the balance completes (or is
// cancelled from another fd) and returns the final progress. With a zero-value
// BalanceArgs it performs a full balance of all chunk types. Requires root.
//
// For asynchronous progress, start the balance from one goroutine/fd and call
// BalanceProgressFor from another.
func BalanceStart(path string, ba BalanceArgs) (BalanceProgress, error) {
	var args btrfsIoctlBalanceArgs

	if ba.Data != nil {
		args.Flags |= BalanceData
		args.Data = toKernelBalArgs(ba.Data)
	}
	if ba.Meta != nil {
		args.Flags |= BalanceMetadata
		args.Meta = toKernelBalArgs(ba.Meta)
	}
	if ba.Sys != nil {
		args.Flags |= BalanceSystem
		args.Sys = toKernelBalArgs(ba.Sys)
	}
	if ba.Force {
		args.Flags |= BalanceForce
	}
	// With no type selected the kernel balances everything; that matches a
	// "full balance" and is the documented default for an all-zero flags word.

	err := ioctlDir(path, BTRFS_IOC_BALANCE_V2, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err != nil {
		return BalanceProgress{}, fmt.Errorf("BTRFS_IOC_BALANCE_V2 %s: %w", path, err)
	}
	return BalanceProgress{
		State:      args.State,
		Running:    args.State&BalanceStateRunning != 0,
		Expected:   args.Stat.Expected,
		Considered: args.Stat.Considered,
		Completed:  args.Stat.Completed,
	}, nil
}

// BalanceProgressFor queries the progress of an in-flight balance on the
// filesystem containing path, via BTRFS_IOC_BALANCE_PROGRESS. The kernel
// returns ENOTCONN when no balance is running.
func BalanceProgressFor(path string) (BalanceProgress, error) {
	var args btrfsIoctlBalanceArgs
	err := ioctlDir(path, BTRFS_IOC_BALANCE_PROGRESS, unsafe.Pointer(&args))
	runtime.KeepAlive(&args)
	if err != nil {
		return BalanceProgress{}, fmt.Errorf("BTRFS_IOC_BALANCE_PROGRESS %s: %w", path, err)
	}
	return BalanceProgress{
		State:      args.State,
		Running:    args.State&BalanceStateRunning != 0,
		Expected:   args.Stat.Expected,
		Considered: args.Stat.Considered,
		Completed:  args.Stat.Completed,
	}, nil
}

// BalanceCancel cancels any running balance on the filesystem containing path,
// via BTRFS_IOC_BALANCE_CTL with BTRFS_BALANCE_CTL_CANCEL.
func BalanceCancel(path string) error {
	mode := int32(balanceCtlCancel)
	if err := ioctlDir(path, BTRFS_IOC_BALANCE_CTL, unsafe.Pointer(&mode)); err != nil {
		return fmt.Errorf("BTRFS_IOC_BALANCE_CTL(cancel) %s: %w", path, err)
	}
	return nil
}

// BalancePause pauses any running balance on the filesystem containing path,
// via BTRFS_IOC_BALANCE_CTL with BTRFS_BALANCE_CTL_PAUSE.
func BalancePause(path string) error {
	mode := int32(balanceCtlPause)
	if err := ioctlDir(path, BTRFS_IOC_BALANCE_CTL, unsafe.Pointer(&mode)); err != nil {
		return fmt.Errorf("BTRFS_IOC_BALANCE_CTL(pause) %s: %w", path, err)
	}
	return nil
}
