// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

package btrfs

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// TestIocAgainstXSys judges the _IO/_IOR/_IOW/_IOWR helpers against a source
// that does not come from this package: golang.org/x/sys/unix, whose
// zerrors_linux_<arch>.go constants are generated per architecture from the
// kernel headers by a C compiler. The tables pinned in abi_*_test.go were
// written in the asm-generic layout, so a helper that ignored powerpc's and
// mips' own layout agreed with them on every lane while the kernel answered
// ENOTTY; x/sys has the real per-architecture numbers.
//
// One request of each direction, plus FICLONERANGE, which this package defines
// itself. It is a pure computation, so it runs under -test.short on the
// emulated lanes.
func TestIocAgainstXSys(t *testing.T) {
	sizeofLong := unsafe.Sizeof(uintptr(0)) // C long and size_t: pointer-sized on every Linux ABI Go has
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"_IO(0x12, 97) BLKFLSBUF", io(0x12, 97), unix.BLKFLSBUF},
		{"_IOR(0x12, 114, size_t) BLKGETSIZE64", ior(0x12, 114, sizeofLong), unix.BLKGETSIZE64},
		{"_IOW(0x94, 9, int) FICLONE", iow(btrfsIoctlMagic, 9, 4), unix.FICLONE},
		{"_IOW('f', 2, long) FS_IOC_SETFLAGS", iow('f', 2, sizeofLong), unix.FS_IOC_SETFLAGS},
		{"_IOWR(0x94, 54, struct file_dedupe_range) FIDEDUPERANGE", iowr(btrfsIoctlMagic, 54, 24), unix.FIDEDUPERANGE},
		{"FICLONERANGE", FICLONERANGE, unix.FICLONERANGE},
		// The test's own re-encoder must agree with x/sys too, or the pinned
		// tables it feeds would be judged by something unchecked.
		{"kernelIOC(_IO BLKFLSBUF)", kernelIOC(0x1261), unix.BLKFLSBUF},
		{"kernelIOC(_IOW FICLONE)", kernelIOC(0x40049409), unix.FICLONE},
		{"kernelIOC(_IOW FICLONERANGE)", kernelIOC(0x4020940d), unix.FICLONERANGE},
		{"kernelIOC(_IOWR FIDEDUPERANGE)", kernelIOC(0xc0189436), unix.FIDEDUPERANGE},
	} {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x (x/sys/unix)", c.name, c.got, c.want)
		}
	}
}
