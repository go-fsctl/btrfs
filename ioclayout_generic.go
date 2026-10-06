// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

//go:build !ppc64 && !ppc64le && !mips && !mipsle && !mips64 && !mips64le

package btrfs

// The asm-generic _IOC layout (include/uapi/asm-generic/ioctl.h), used by
// every Linux architecture Go supports except powerpc and mips (see
// ioclayout_ppcmips.go).
const (
	iocNone  = 0
	iocWrite = 1
	iocRead  = 2

	iocSizeBits = 14
	iocDirBits  = 2
)
