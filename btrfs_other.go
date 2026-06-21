// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

//go:build !linux

// Package btrfs drives btrfs kernel operations via BTRFS_IOC_* ioctls. The
// kernel control path only exists on Linux; on other platforms every operation
// returns ErrUnsupported. The ABI definitions and ioctl-number derivation in
// abi.go remain available everywhere for testing and tooling.
package btrfs

import "errors"

// ErrUnsupported is returned by all kernel operations on non-Linux platforms.
var ErrUnsupported = errors.New("btrfs: BTRFS_IOC_* ioctls are only supported on Linux")

// SubvolInfo is the decoded result of BTRFS_IOC_GET_SUBVOL_INFO.
type SubvolInfo struct {
	ID         uint64
	Name       string
	ParentID   uint64
	Dirid      uint64
	Generation uint64
	Flags      uint64
	UUID       [16]byte
	ParentUUID [16]byte
}

// SubvolCreate is unsupported off Linux.
func SubvolCreate(parentDir, name string) error { return ErrUnsupported }

// SnapshotCreate is unsupported off Linux.
func SnapshotCreate(srcSubvolPath, destParentDir, name string, readonly bool) error {
	return ErrUnsupported
}

// SubvolDelete is unsupported off Linux.
func SubvolDelete(parentDir, name string) error { return ErrUnsupported }

// SubvolGetFlags is unsupported off Linux.
func SubvolGetFlags(subvolPath string) (uint64, error) { return 0, ErrUnsupported }

// SubvolSetFlags is unsupported off Linux.
func SubvolSetFlags(subvolPath string, flags uint64) error { return ErrUnsupported }

// SetReadonly is unsupported off Linux.
func SetReadonly(subvolPath string, ro bool) error { return ErrUnsupported }

// SubvolID is unsupported off Linux.
func SubvolID(path string) (uint64, error) { return 0, ErrUnsupported }

// GetSubvolInfo is unsupported off Linux.
func GetSubvolInfo(subvolPath string) (*SubvolInfo, error) { return nil, ErrUnsupported }

// IsReadonly is unsupported off Linux.
func IsReadonly(subvolPath string) (bool, error) { return false, ErrUnsupported }

// Sync is unsupported off Linux.
func Sync(path string) error { return ErrUnsupported }

// Available reports false off Linux.
func Available(path string) bool { return false }

// Subvolume is one entry returned by ListSubvolumes.
type Subvolume struct {
	ID       uint64
	ParentID uint64
	Name     string
	Path     string
}

// ListSubvolumes is unsupported off Linux.
func ListSubvolumes(path string) ([]Subvolume, error) { return nil, ErrUnsupported }

// DeviceAdd is unsupported off Linux.
func DeviceAdd(mountPath, devPath string) error { return ErrUnsupported }

// DeviceRemove is unsupported off Linux.
func DeviceRemove(mountPath, devPath string) error { return ErrUnsupported }

// DeviceRemoveByID is unsupported off Linux.
func DeviceRemoveByID(mountPath string, devid uint64) error { return ErrUnsupported }

// DeviceInfo is the decoded result of BTRFS_IOC_DEV_INFO.
type DeviceInfo struct {
	Devid      uint64
	UUID       [16]byte
	BytesUsed  uint64
	TotalBytes uint64
	Path       string
}

// GetDeviceInfo is unsupported off Linux.
func GetDeviceInfo(path string, devid uint64) (*DeviceInfo, error) { return nil, ErrUnsupported }

// FsInfo is the decoded result of BTRFS_IOC_FS_INFO.
type FsInfo struct {
	MaxID      uint64
	NumDevices uint64
	FSID       [16]byte
	Nodesize   uint32
	Sectorsize uint32
	Generation uint64
}

// GetFsInfo is unsupported off Linux.
func GetFsInfo(path string) (*FsInfo, error) { return nil, ErrUnsupported }

// ScrubProgress is the decoded subset of btrfs_scrub_progress.
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

// ScrubOptions controls a scrub started with ScrubStart.
type ScrubOptions struct {
	Readonly bool
}

// ScrubStart is unsupported off Linux.
func ScrubStart(path string, devid uint64, opts ScrubOptions) (ScrubProgress, error) {
	return ScrubProgress{}, ErrUnsupported
}

// ScrubProgressFor is unsupported off Linux.
func ScrubProgressFor(path string, devid uint64) (ScrubProgress, error) {
	return ScrubProgress{}, ErrUnsupported
}

// ScrubCancel is unsupported off Linux.
func ScrubCancel(path string) error { return ErrUnsupported }

// BalanceProgress is the decoded result of a balance progress query.
type BalanceProgress struct {
	Running    bool
	State      uint64
	Expected   uint64
	Considered uint64
	Completed  uint64
}

// BalanceFilter is the per-chunk-type filter for a balance run.
type BalanceFilter struct {
	Flags    uint64
	Usage    uint64
	Profiles uint64
	Devid    uint64
	Target   uint64
}

// BalanceArgs configures a balance started with BalanceStart.
type BalanceArgs struct {
	Data  *BalanceFilter
	Meta  *BalanceFilter
	Sys   *BalanceFilter
	Force bool
}

// BalanceStart is unsupported off Linux.
func BalanceStart(path string, ba BalanceArgs) (BalanceProgress, error) {
	return BalanceProgress{}, ErrUnsupported
}

// BalanceProgressFor is unsupported off Linux.
func BalanceProgressFor(path string) (BalanceProgress, error) {
	return BalanceProgress{}, ErrUnsupported
}

// BalanceCancel is unsupported off Linux.
func BalanceCancel(path string) error { return ErrUnsupported }

// BalancePause is unsupported off Linux.
func BalancePause(path string) error { return ErrUnsupported }
