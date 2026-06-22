// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

//go:build linux

package btrfs

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Indirection seams over the operating-system and ioctl primitives this package
// drives. They exist so the error branches of every kernel call — which only
// trigger on real ioctl failures that are impractical to provoke against a live
// btrfs filesystem — can be exercised deterministically by fault-injecting fakes
// in tests. Production code uses the real implementations assigned here; tests
// swap a var, run, and restore it. The root-only integration tests still drive
// the genuine BTRFS_IOC_* ioctls for end-to-end confidence.
var (
	osOpen = os.Open

	// doIoctl is the single raw-ioctl seam every ioctlFd/Send/SetReceivedSubvol
	// call funnels through. It keeps the kernel-arg pointer typed as
	// unsafe.Pointer all the way to unix.Syscall (so the only uintptr conversion
	// stays adjacent to the syscall, as the unsafe.Pointer rules require). Tests
	// swap it for a fake that fills the arg buffer and returns a chosen errno,
	// covering every BTRFS_IOC_* call without root or a real filesystem.
	doIoctl = realIoctl

	unixStatfs = unix.Statfs
	unixPipe2  = unix.Pipe2
	unixClose  = unix.Close
	unixRead   = unix.Read
)

// realIoctl is the production doIoctl: it issues the SYS_IOCTL syscall.
func realIoctl(fd uintptr, req uintptr, arg unsafe.Pointer) unix.Errno {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, req, uintptr(arg))
	return errno
}
