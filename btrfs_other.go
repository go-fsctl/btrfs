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
