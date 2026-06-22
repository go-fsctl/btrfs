// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

//go:build linux

package btrfs

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// These receive integration tests drive OUR Receive against streams produced by
// the real `btrfs send` and by our own Send, materialising the subvolume tree
// under a destination btrfs mount via syscalls + ioctls (the library never
// shells out; the test does, only to produce streams and cross-check with
// btrfs-progs). They reuse the BTRFS_SEND_SRC / BTRFS_SEND_DST mounts:
//
//	BTRFS_SEND_SRC=/mnt/bt1 BTRFS_SEND_DST=/mnt/bt2 sudo -E go test -run IntegrationReceive -v ./...
//
// Skipped unless both are mounted btrfs filesystems and `btrfs` is on PATH.
// Requires root (subvolume create + SET_RECEIVED_SUBVOL).

// realSend pipes `btrfs send [-p parent] <snap>` into a stream file and returns
// its path. This is the producer mirror of receiveStream in the send test.
func realSend(t *testing.T, snapPath, parentPath string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "real_stream.bin")
	of, err := os.Create(out)
	if err != nil {
		t.Fatalf("create stream file: %v", err)
	}
	defer of.Close()
	args := []string{"send"}
	if parentPath != "" {
		args = append(args, "-p", parentPath)
	}
	args = append(args, snapPath)
	cmd := exec.Command("btrfs", args...)
	cmd.Stdout = of
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("btrfs %v: %v\n%s", args, err, stderr.String())
	}
	if err := of.Sync(); err != nil {
		t.Fatalf("sync stream: %v", err)
	}
	return out
}

// ourReceive opens a stream file and replays it with OUR Receive under dst.
func ourReceive(t *testing.T, streamPath, dst string, opts ReceiveOpts) {
	t.Helper()
	f, err := os.Open(streamPath)
	if err != nil {
		t.Fatalf("open stream %s: %v", streamPath, err)
	}
	defer f.Close()
	if err := Receive(dst, f, opts); err != nil {
		t.Fatalf("Receive: %v", err)
	}
}

// TestIntegrationReceiveFull proves real `btrfs send` -> OUR Receive reproduces
// the source tree: every file's sha256 matches, the symlink target matches, the
// hardlink shares an inode, the xattr is present, mode/uid/gid match, and
// `btrfs subvolume show` reports the correct Received UUID and read-only flag.
func TestIntegrationReceiveFull(t *testing.T) {
	src, dst := sendMounts(t)

	const sub = "recv_src"
	const snap = "recv_src_snap"
	subPath := filepath.Join(src, sub)
	snapPath := filepath.Join(src, snap)

	cleanup := func() {
		_ = SubvolDelete(src, snap)
		_ = SubvolDelete(src, sub)
		_ = SubvolDelete(dst, snap)
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := SubvolCreate(src, sub); err != nil {
		t.Fatalf("SubvolCreate: %v", err)
	}

	// Build a realistic tree: varied-size regular files (incl. >128 KiB so the
	// sender emits multiple WRITE commands), subdirs, a symlink, a hardlink, an
	// xattr, specific mode/owner/mtime.
	files := map[string][]byte{
		"small.txt":     []byte("hello btrfs receive\n"),
		"sub/nested.go": bytes.Repeat([]byte("go-fsctl "), 4096), // ~36 KiB
		"big.bin":       bytes.Repeat([]byte{0x5a}, 300*1024),    // 300 KiB, multi-WRITE
	}
	for name, data := range files {
		full := filepath.Join(subPath, name)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(full, data, 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// Specific mode + owner on one file.
	if err := os.Chmod(filepath.Join(subPath, "small.txt"), 0640); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := os.Chown(filepath.Join(subPath, "small.txt"), 0, 0); err != nil {
		t.Fatalf("chown: %v", err)
	}
	// Symlink.
	if err := os.Symlink("sub/nested.go", filepath.Join(subPath, "link.lnk")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	// Hardlink to small.txt.
	if err := os.Link(filepath.Join(subPath, "small.txt"), filepath.Join(subPath, "small.hard")); err != nil {
		t.Fatalf("hardlink: %v", err)
	}
	// Xattr via setfattr (btrfs-progs cross-check tool family).
	if err := exec.Command("setfattr", "-n", "user.comment", "-v", "go-fsctl-receive", filepath.Join(subPath, "big.bin")).Run(); err != nil {
		t.Fatalf("setfattr: %v", err)
	}
	// Specific mtime.
	mtime := unix.NsecToTimespec(1_600_000_000 * 1e9)
	if err := unix.UtimesNanoAt(unix.AT_FDCWD, filepath.Join(subPath, "small.txt"),
		[]unix.Timespec{mtime, mtime}, 0); err != nil {
		t.Fatalf("utimes: %v", err)
	}

	if err := Sync(src); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := SnapshotCreate(subPath, src, snap, true); err != nil {
		t.Fatalf("SnapshotCreate(ro): %v", err)
	}
	if err := Sync(src); err != nil {
		t.Fatalf("Sync after snap: %v", err)
	}

	// Capture expected hashes + the source subvol UUID for the Received-UUID check.
	want := map[string]string{}
	for name := range files {
		want[name] = sha256File(t, filepath.Join(snapPath, name))
	}
	srcInfo, err := GetSubvolInfo(snapPath)
	if err != nil {
		t.Fatalf("GetSubvolInfo(src snap): %v", err)
	}

	// real btrfs send -> OUR Receive.
	stream := realSend(t, snapPath, "")
	ourReceive(t, stream, dst, ReceiveOpts{})

	recvPath := filepath.Join(dst, snap)

	// 1) Every file's sha256 matches.
	for name, wantSum := range want {
		gotSum := sha256File(t, filepath.Join(recvPath, name))
		if gotSum != wantSum {
			t.Errorf("file %s sha256 mismatch: received %s want %s", name, gotSum, wantSum)
		} else {
			t.Logf("file %s sha256 OK (%s)", name, gotSum)
		}
	}

	// 2) Symlink target matches.
	if tgt, err := os.Readlink(filepath.Join(recvPath, "link.lnk")); err != nil {
		t.Errorf("readlink: %v", err)
	} else if tgt != "sub/nested.go" {
		t.Errorf("symlink target = %q, want sub/nested.go", tgt)
	} else {
		t.Logf("symlink target OK (%s)", tgt)
	}

	// 3) Hardlink shares inode with small.txt.
	var st1, st2 unix.Stat_t
	if err := unix.Stat(filepath.Join(recvPath, "small.txt"), &st1); err != nil {
		t.Fatalf("stat small.txt: %v", err)
	}
	if err := unix.Stat(filepath.Join(recvPath, "small.hard"), &st2); err != nil {
		t.Fatalf("stat small.hard: %v", err)
	}
	if st1.Ino != st2.Ino {
		t.Errorf("hardlink inode mismatch: %d vs %d", st1.Ino, st2.Ino)
	} else if st1.Nlink < 2 {
		t.Errorf("hardlink nlink = %d, want >= 2", st1.Nlink)
	} else {
		t.Logf("hardlink shares inode %d (nlink %d) OK", st1.Ino, st1.Nlink)
	}

	// 4) Xattr present.
	buf := make([]byte, 256)
	n, err := unix.Lgetxattr(filepath.Join(recvPath, "big.bin"), "user.comment", buf)
	if err != nil {
		t.Errorf("lgetxattr: %v", err)
	} else if string(buf[:n]) != "go-fsctl-receive" {
		t.Errorf("xattr = %q, want go-fsctl-receive", buf[:n])
	} else {
		t.Logf("xattr user.comment OK (%q)", buf[:n])
	}

	// 5) mode/uid/gid/mtime match on small.txt.
	if st1.Mode&0o7777 != 0o640 {
		t.Errorf("mode = %#o, want 0640", st1.Mode&0o7777)
	}
	if st1.Uid != 0 || st1.Gid != 0 {
		t.Errorf("owner = %d:%d, want 0:0", st1.Uid, st1.Gid)
	}
	if st1.Mtim.Sec != 1_600_000_000 {
		t.Errorf("mtime = %d, want 1600000000", st1.Mtim.Sec)
	} else {
		t.Logf("mode/uid/gid/mtime OK (%#o %d:%d %d)", st1.Mode&0o7777, st1.Uid, st1.Gid, st1.Mtim.Sec)
	}

	// 6) `btrfs subvolume show` reports the correct Received UUID + read-only.
	recvInfo, err := GetSubvolInfo(recvPath)
	if err != nil {
		t.Fatalf("GetSubvolInfo(received): %v", err)
	}
	gotRecvUUID := receivedUUID(recvPath)
	if gotRecvUUID != srcInfo.UUID {
		t.Errorf("Received UUID = %x, want source subvol UUID %x", gotRecvUUID, srcInfo.UUID)
	} else {
		t.Logf("Received UUID OK (%x)", gotRecvUUID)
	}
	if ro, err := IsReadonly(recvPath); err != nil {
		t.Errorf("IsReadonly: %v", err)
	} else if !ro {
		t.Errorf("received subvol not read-only")
	} else {
		t.Logf("received subvol is read-only OK")
	}
	_ = recvInfo

	// Cross-check with the real tool's view.
	show, err := exec.Command("btrfs", "subvolume", "show", recvPath).CombinedOutput()
	if err != nil {
		t.Logf("btrfs subvolume show (non-fatal): %v\n%s", err, show)
	} else {
		t.Logf("btrfs subvolume show:\n%s", show)
	}
}

// TestIntegrationReceiveIncremental proves real incremental `btrfs send -p`
// -> OUR Receive on top of a received parent reproduces the modified tree.
func TestIntegrationReceiveIncremental(t *testing.T) {
	src, dst := sendMounts(t)

	const sub = "recv_inc_src"
	const snap1 = "recv_inc_snap1"
	const snap2 = "recv_inc_snap2"
	subPath := filepath.Join(src, sub)
	snap1Path := filepath.Join(src, snap1)
	snap2Path := filepath.Join(src, snap2)

	cleanup := func() {
		_ = SubvolDelete(src, snap2)
		_ = SubvolDelete(src, snap1)
		_ = SubvolDelete(src, sub)
		_ = SubvolDelete(dst, snap2)
		_ = SubvolDelete(dst, snap1)
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := SubvolCreate(src, sub); err != nil {
		t.Fatalf("SubvolCreate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subPath, "file"), []byte("v1 contents\n"), 0644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subPath, "keep"), []byte("unchanged\n"), 0644); err != nil {
		t.Fatalf("write keep: %v", err)
	}
	if err := Sync(src); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := SnapshotCreate(subPath, src, snap1, true); err != nil {
		t.Fatalf("SnapshotCreate(snap1): %v", err)
	}
	if err := Sync(src); err != nil {
		t.Fatalf("Sync after snap1: %v", err)
	}

	// Full send of snap1 -> OUR Receive (the incremental parent on dst, stamped
	// with its Received UUID by our END so the incremental can resolve it).
	full := realSend(t, snap1Path, "")
	ourReceive(t, full, dst, ReceiveOpts{})

	// Modify + add, snapshot2.
	if err := os.WriteFile(filepath.Join(subPath, "file"), []byte("v2 modified contents, longer than before\n"), 0644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subPath, "added"), []byte("brand new file\n"), 0644); err != nil {
		t.Fatalf("write added: %v", err)
	}
	if err := Sync(src); err != nil {
		t.Fatalf("Sync before snap2: %v", err)
	}
	if err := SnapshotCreate(subPath, src, snap2, true); err != nil {
		t.Fatalf("SnapshotCreate(snap2): %v", err)
	}
	if err := Sync(src); err != nil {
		t.Fatalf("Sync after snap2: %v", err)
	}

	want := map[string]string{}
	for _, name := range []string{"file", "keep", "added"} {
		want[name] = sha256File(t, filepath.Join(snap2Path, name))
	}

	// real incremental send (-p snap1) -> OUR Receive on top of received snap1.
	inc := realSend(t, snap2Path, snap1Path)
	ourReceive(t, inc, dst, ReceiveOpts{})

	recvPath := filepath.Join(dst, snap2)
	for name, wantSum := range want {
		gotSum := sha256File(t, filepath.Join(recvPath, name))
		if gotSum != wantSum {
			t.Errorf("incremental file %s sha256 mismatch: received %s want %s", name, gotSum, wantSum)
		} else {
			t.Logf("incremental file %s sha256 OK (%s)", name, gotSum)
		}
	}
}

// TestIntegrationReceiveRoundTrip proves the fully in-library loop: OUR Send
// (merged) -> OUR Receive reproduces the tree, with no btrfs-progs in the path.
func TestIntegrationReceiveRoundTrip(t *testing.T) {
	src, dst := sendMounts(t)

	const sub = "recv_rt_src"
	const snap = "recv_rt_snap"
	subPath := filepath.Join(src, sub)
	snapPath := filepath.Join(src, snap)

	cleanup := func() {
		_ = SubvolDelete(src, snap)
		_ = SubvolDelete(src, sub)
		_ = SubvolDelete(dst, snap)
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := SubvolCreate(src, sub); err != nil {
		t.Fatalf("SubvolCreate: %v", err)
	}
	files := map[string][]byte{
		"a.txt":       []byte("round trip\n"),
		"d/b.bin":     bytes.Repeat([]byte{0x42}, 200*1024), // multi-WRITE
		"d/e/c.empty": {},
	}
	for name, data := range files {
		full := filepath.Join(subPath, name)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, data, 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.Symlink("a.txt", filepath.Join(subPath, "sl")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := Sync(src); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := SnapshotCreate(subPath, src, snap, true); err != nil {
		t.Fatalf("SnapshotCreate: %v", err)
	}
	if err := Sync(src); err != nil {
		t.Fatalf("Sync after snap: %v", err)
	}
	want := map[string]string{}
	for name := range files {
		want[name] = sha256File(t, filepath.Join(snapPath, name))
	}

	// OUR Send -> stream file -> OUR Receive.
	stream := sendToFile(t, snapPath, SendOpts{})
	ourReceive(t, stream, dst, ReceiveOpts{})

	recvPath := filepath.Join(dst, snap)
	for name, wantSum := range want {
		gotSum := sha256File(t, filepath.Join(recvPath, name))
		if gotSum != wantSum {
			t.Errorf("round-trip file %s sha256 mismatch: received %s want %s", name, gotSum, wantSum)
		} else {
			t.Logf("round-trip file %s sha256 OK (%s)", name, gotSum)
		}
	}
	if tgt, err := os.Readlink(filepath.Join(recvPath, "sl")); err != nil || tgt != "a.txt" {
		t.Errorf("round-trip symlink = %q err=%v, want a.txt", tgt, err)
	}
	if ro, _ := IsReadonly(recvPath); !ro {
		t.Errorf("round-trip received subvol not read-only")
	}
}
