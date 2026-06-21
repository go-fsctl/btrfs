// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

//go:build linux

package btrfs

import (
	"fmt"
	goio "io"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// This file implements btrfs SEND-stream generation (BTRFS_IOC_SEND) and the
// SET_RECEIVED_SUBVOL ioctl, plus parsing of the resulting send stream. As with
// the rest of the package it is pure Go and never shells out to the btrfs CLI.
//
// Receive (replaying a stream to recreate the subvolume tree) is intentionally
// out of scope here: it is a large userspace state machine. The pieces shipped
// here — full and incremental Send, SET_RECEIVED_SUBVOL, and stream parsing —
// are the producer side and the interop primitive that lets our streams be
// applied by real `btrfs receive` and lets a future receive-apply mark the
// subvolumes it creates. Receive-apply is a documented follow-up.

// SendOpts controls a Send. The zero value performs a full (non-incremental)
// send of all data in the source subvolume.
type SendOpts struct {
	// ParentRoot is the root id of a parent subvolume to diff against. When
	// non-zero the kernel emits an incremental stream carrying only the delta
	// from that parent (e.g. the changes between two snapshots). 0 means a full
	// send. The parent must be an earlier snapshot in the same lineage and must
	// already exist on the receiving side for the stream to apply.
	ParentRoot uint64
	// CloneSources lists root ids the kernel may reference with clone operations
	// instead of re-sending identical extents. These subvolumes must also exist
	// on the receiving side. Optional.
	CloneSources []uint64
	// NoData requests a metadata-only stream (BTRFS_SEND_FLAG_NO_FILE_DATA): the
	// kernel emits the file hierarchy and metadata but no file contents. Useful
	// for inspecting structure without transferring data.
	NoData bool
}

// Send generates a btrfs send stream for the subvolume open at subvolFd and
// writes it to w, via BTRFS_IOC_SEND. The subvolume MUST be read-only (the
// kernel rejects sending a writable subvolume); take a read-only snapshot
// first. With opts.ParentRoot set to a parent subvolume's root id the stream is
// incremental (the delta from that parent); otherwise it is a full send.
//
// The kernel writes the stream synchronously to a pipe while Send drains the
// read end into w in a goroutine, so arbitrarily large streams flow without
// buffering the whole thing in memory. Send blocks until the stream is fully
// written (or an error occurs) and returns the first error from either the
// ioctl or the copy. Requires privileges sufficient to send (typically root).
func Send(subvolFd int, w goio.Writer, opts SendOpts) error {
	// A pipe carries the stream from the kernel (write end handed to the ioctl
	// as send_fd) to us (read end drained into w). O_CLOEXEC on both ends.
	var fds [2]int
	if err := unix.Pipe2(fds[:], unix.O_CLOEXEC); err != nil {
		return fmt.Errorf("Send: pipe2: %w", err)
	}
	rfd, wfd := fds[0], fds[1]

	var args btrfsIoctlSendArgs
	args.SendFd = int64(wfd)
	args.ParentRoot = opts.ParentRoot
	if opts.NoData {
		args.Flags |= SendFlagNoFileData
	}

	// Pin the clone-sources slice for the duration of the ioctl and point the
	// args at its first element. nil/empty means no clone sources.
	var clone []uint64
	if len(opts.CloneSources) > 0 {
		clone = append(clone, opts.CloneSources...)
		args.CloneSources = &clone[0]
		args.CloneSourcesCount = uint64(len(clone))
	}

	// Run the ioctl in a goroutine: it blocks writing the stream into the pipe
	// while the main goroutine drains the read end, avoiding a deadlock when the
	// stream exceeds the pipe buffer.
	ioctlErr := make(chan error, 1)
	go func() {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(subvolFd), BTRFS_IOC_SEND, uintptr(unsafe.Pointer(&args)))
		runtime.KeepAlive(&args)
		runtime.KeepAlive(clone)
		// Close the write end so the reader sees EOF once the stream is done.
		_ = unix.Close(wfd)
		if errno != 0 {
			ioctlErr <- errno
		} else {
			ioctlErr <- nil
		}
	}()

	// Drain the pipe into w. read end is closed once we are done with it.
	rf := newFdReader(rfd)
	_, copyErr := goio.Copy(w, rf)
	_ = rf.Close()

	sendErr := <-ioctlErr
	if sendErr != nil {
		return fmt.Errorf("BTRFS_IOC_SEND: %w", sendErr)
	}
	if copyErr != nil {
		return fmt.Errorf("Send: draining stream: %w", copyErr)
	}
	return nil
}

// fdReader is a minimal io.ReadCloser over a raw fd, used to drain the pipe's
// read end without taking ownership semantics from os.NewFile (we close it
// ourselves exactly once).
type fdReader struct{ fd int }

func newFdReader(fd int) *fdReader { return &fdReader{fd: fd} }

func (r *fdReader) Read(p []byte) (int, error) {
	for {
		n, err := unix.Read(r.fd, p)
		if err == unix.EINTR {
			continue
		}
		if n == 0 && err == nil {
			return 0, goio.EOF
		}
		if err != nil {
			return n, err
		}
		return n, nil
	}
}

func (r *fdReader) Close() error { return unix.Close(r.fd) }

// SetReceivedTimes carries the send/receive timestamps stamped onto a received
// subvolume. Stime is the sender's ctime for the subvolume (copied from the
// stream); Rtime is the local receive time. The kernel fills the rtransid/rtime
// outputs, which SetReceivedSubvol returns.
type SetReceivedTimes struct {
	Stime ReceivedTimespec // sender-side time (in)
	Rtime ReceivedTimespec // receive-side time (in)
}

// ReceivedTimespec mirrors struct btrfs_ioctl_timespec { __u64 sec; __u32 nsec }.
type ReceivedTimespec struct {
	Sec  uint64
	Nsec uint32
}

// SetReceivedResult is what the kernel reports back from SET_RECEIVED_SUBVOL.
type SetReceivedResult struct {
	Rtransid uint64           // received transid assigned by the kernel
	Rtime    ReceivedTimespec // receive time recorded by the kernel
}

// SetReceivedSubvol marks the subvolume open at fd as a received subvolume via
// BTRFS_IOC_SET_RECEIVED_SUBVOL: it stamps the subvolume with the sending
// side's UUID and send transid (ctransid) plus the send/receive times. This is
// the final ioctl `btrfs receive` issues on each subvolume it materialises, and
// it is what makes a received subvolume discoverable as the parent of a later
// incremental receive. The subvolume must be read-only at the time of the call
// (receive sets it read-only just before stamping). Requires root.
//
// uuid is the sending subvolume's UUID (from the stream's subvol/snapshot
// command); ctransid is the sender's transid for that subvolume. The kernel
// fills rtransid/rtime, returned in SetReceivedResult.
func SetReceivedSubvol(fd int, uuid [16]byte, ctransid uint64, times SetReceivedTimes) (SetReceivedResult, error) {
	var args btrfsIoctlReceivedSubvolArgs
	args.UUID = uuid
	args.Stransid = ctransid
	args.Stime = btrfsIoctlTimespec{Sec: times.Stime.Sec, Nsec: times.Stime.Nsec}
	args.Rtime = btrfsIoctlTimespec{Sec: times.Rtime.Sec, Nsec: times.Rtime.Nsec}

	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), BTRFS_IOC_SET_RECEIVED_SUBVOL, uintptr(unsafe.Pointer(&args)))
	runtime.KeepAlive(&args)
	if errno != 0 {
		return SetReceivedResult{}, fmt.Errorf("BTRFS_IOC_SET_RECEIVED_SUBVOL: %w", errno)
	}
	return SetReceivedResult{
		Rtransid: args.Rtransid,
		Rtime:    ReceivedTimespec{Sec: args.Rtime.Sec, Nsec: args.Rtime.Nsec},
	}, nil
}
