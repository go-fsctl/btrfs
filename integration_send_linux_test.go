// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

//go:build linux

package btrfs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	goio "io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// These send integration tests drive BTRFS_IOC_SEND against a live btrfs mount
// and cross-check the produced stream against the real `btrfs receive` tool
// (the library never shells out; the test does, only to validate interop). They
// need two btrfs mounts: a source (where snapshots are sent from) and a
// destination (where `btrfs receive` materialises them). Set:
//
//	BTRFS_SEND_SRC=/mnt/bt1 BTRFS_SEND_DST=/mnt/bt2 sudo -E go test -run IntegrationSend -v ./...
//
// They are skipped unless both are mounted btrfs filesystems. `btrfs receive`
// must be on PATH. Requires root.

func sendMounts(t *testing.T) (src, dst string) {
	t.Helper()
	src = os.Getenv("BTRFS_SEND_SRC")
	dst = os.Getenv("BTRFS_SEND_DST")
	if src == "" || dst == "" {
		t.Skip("BTRFS_SEND_SRC and BTRFS_SEND_DST not both set; skipping send integration test")
	}
	if !Available(src) {
		t.Skipf("%s is not a mounted btrfs filesystem; skipping", src)
	}
	if !Available(dst) {
		t.Skipf("%s is not a mounted btrfs filesystem; skipping", dst)
	}
	if _, err := exec.LookPath("btrfs"); err != nil {
		t.Skip("btrfs(8) not on PATH; skipping send interop test")
	}
	return src, dst
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := goio.Copy(h, f); err != nil {
		t.Fatalf("hash %s: %v", path, err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// sendToFile runs our Send on the read-only subvolume at snapPath and writes the
// stream to a file, returning its path.
func sendToFile(t *testing.T, snapPath string, opts SendOpts) string {
	t.Helper()
	sf, err := os.Open(snapPath)
	if err != nil {
		t.Fatalf("open snapshot %s: %v", snapPath, err)
	}
	defer sf.Close()

	out := filepath.Join(t.TempDir(), "stream.bin")
	of, err := os.Create(out)
	if err != nil {
		t.Fatalf("create stream file: %v", err)
	}
	defer of.Close()
	if err := Send(int(sf.Fd()), of, opts); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := of.Sync(); err != nil {
		t.Fatalf("sync stream file: %v", err)
	}
	return out
}

// receiveStream pipes a stream file into `btrfs receive -e <dst>` and returns
// the combined output for diagnostics.
func receiveStream(t *testing.T, streamPath, dst string) {
	t.Helper()
	sf, err := os.Open(streamPath)
	if err != nil {
		t.Fatalf("open stream %s: %v", streamPath, err)
	}
	defer sf.Close()
	cmd := exec.Command("btrfs", "receive", dst)
	cmd.Stdin = sf
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("btrfs receive %s: %v\n%s", dst, err, out)
	}
	t.Logf("btrfs receive output: %s", bytes.TrimSpace(out))
}

// TestIntegrationSendFull proves OUR Send (full) produces a stream that real
// `btrfs receive` applies to reproduce the exact file tree: every file's
// sha256 in the received subvolume matches the source.
func TestIntegrationSendFull(t *testing.T) {
	src, dst := sendMounts(t)

	const sub = "send_src"
	const snap = "send_src_snap"
	subPath := filepath.Join(src, sub)
	snapPath := filepath.Join(src, snap)

	// Clean any leftovers, and the received copy on dst.
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
	// Populate with a few files of varying sizes.
	want := map[string]string{}
	files := map[string][]byte{
		"hello.txt":     []byte("hello btrfs send\n"),
		"zeros.bin":     make([]byte, 1<<20), // 1 MiB of zeros
		"sub/nested.go": bytes.Repeat([]byte("go-fsctl "), 4096),
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
	if err := Sync(src); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// A read-only snapshot is required to send.
	if err := SnapshotCreate(subPath, src, snap, true); err != nil {
		t.Fatalf("SnapshotCreate(ro): %v", err)
	}
	if err := Sync(src); err != nil {
		t.Fatalf("Sync after snapshot: %v", err)
	}
	for name := range files {
		want[name] = sha256File(t, filepath.Join(snapPath, name))
	}

	// OUR Send(full) -> stream file -> real btrfs receive on dst.
	stream := sendToFile(t, snapPath, SendOpts{})

	// Parse OUR stream header and confirm it matches what btrfs send produces.
	h, n, err := func() (Header, int, error) {
		f, err := os.Open(stream)
		if err != nil {
			return Header{}, 0, err
		}
		defer f.Close()
		return VerifyStream(f)
	}()
	if err != nil {
		t.Fatalf("VerifyStream(our stream): %v", err)
	}
	t.Logf("our stream: magic=%q version=%d records=%d", h.Magic, h.Version, n)
	if h.Magic != btrfsSendStreamMagic {
		t.Errorf("our stream magic = %q, want %q", h.Magic, btrfsSendStreamMagic)
	}

	receiveStream(t, stream, dst)

	// Cross-check every file's contents match the originals.
	recvPath := filepath.Join(dst, snap)
	for name, wantSum := range want {
		gotSum := sha256File(t, filepath.Join(recvPath, name))
		if gotSum != wantSum {
			t.Errorf("file %s sha256 mismatch: received %s want %s", name, gotSum, wantSum)
		} else {
			t.Logf("file %s sha256 OK (%s)", name, gotSum)
		}
	}
}

// TestIntegrationSendIncremental proves OUR incremental Send (parent_root set)
// applies on top of an already-received parent snapshot and reproduces the
// modified tree.
func TestIntegrationSendIncremental(t *testing.T) {
	src, dst := sendMounts(t)

	const sub = "send_inc_src"
	const snap1 = "send_inc_snap1"
	const snap2 = "send_inc_snap2"
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

	// Send snap1 in full and receive it first (the incremental parent).
	full := sendToFile(t, snap1Path, SendOpts{})
	receiveStream(t, full, dst)

	// The kernel needs snap1 stamped as "received" on dst for the incremental
	// receive to resolve the parent. `btrfs receive` already did that via
	// SET_RECEIVED_SUBVOL when it materialised snap1.

	// Modify the source, snapshot again.
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

	// Capture expected hashes from snap2.
	want := map[string]string{}
	for _, name := range []string{"file", "keep", "added"} {
		want[name] = sha256File(t, filepath.Join(snap2Path, name))
	}

	// snap1's root id is the parent for the incremental send.
	parentID, err := SubvolID(snap1Path)
	if err != nil {
		t.Fatalf("SubvolID(snap1): %v", err)
	}
	t.Logf("incremental parent root id = %d", parentID)

	inc := sendToFile(t, snap2Path, SendOpts{ParentRoot: parentID})

	h, n, err := func() (Header, int, error) {
		f, err := os.Open(inc)
		if err != nil {
			return Header{}, 0, err
		}
		defer f.Close()
		return VerifyStream(f)
	}()
	if err != nil {
		t.Fatalf("VerifyStream(incremental): %v", err)
	}
	t.Logf("incremental stream: version=%d records=%d", h.Version, n)

	receiveStream(t, inc, dst)

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

// TestIntegrationSendNoData proves the metadata-only flag produces a valid,
// shorter stream whose header still parses.
func TestIntegrationSendNoData(t *testing.T) {
	src, _ := sendMounts(t)

	const sub = "send_nodata_src"
	const snap = "send_nodata_snap"
	subPath := filepath.Join(src, sub)
	snapPath := filepath.Join(src, snap)

	cleanup := func() {
		_ = SubvolDelete(src, snap)
		_ = SubvolDelete(src, sub)
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := SubvolCreate(src, sub); err != nil {
		t.Fatalf("SubvolCreate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subPath, "big.bin"), make([]byte, 4<<20), 0644); err != nil {
		t.Fatalf("write big: %v", err)
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

	fullStream := sendToFile(t, snapPath, SendOpts{})
	nodataStream := sendToFile(t, snapPath, SendOpts{NoData: true})

	fi1, _ := os.Stat(fullStream)
	fi2, _ := os.Stat(nodataStream)
	t.Logf("full stream = %d bytes, no-data stream = %d bytes", fi1.Size(), fi2.Size())
	if fi2.Size() >= fi1.Size() {
		t.Errorf("no-data stream (%d) not smaller than full stream (%d)", fi2.Size(), fi1.Size())
	}
	// Both must parse as valid streams.
	for _, p := range []string{fullStream, nodataStream} {
		f, err := os.Open(p)
		if err != nil {
			t.Fatalf("open %s: %v", p, err)
		}
		_, _, err = VerifyStream(f)
		f.Close()
		if err != nil {
			t.Errorf("VerifyStream(%s): %v", p, err)
		}
	}
}
