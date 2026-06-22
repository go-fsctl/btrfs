// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

//go:build linux

package btrfs

import (
	"bytes"
	"encoding/binary"
	"errors"
	goio "io"
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// These tests drive every branch of the *_linux.go kernel paths through the
// indirection seams in seams_linux.go, fault-injecting both success and each
// errno without needing root or a real btrfs filesystem. The root-only
// integration_*_linux_test.go files exercise the genuine ioctls end to end.

var errInjected = errors.New("injected")

// snapshotSeams captures the production seam values and returns a restore func.
func snapshotSeams() func() {
	a := osOpen
	b := doIoctl
	c := unixStatfs
	d := unixPipe2
	e := unixClose
	f := unixRead
	return func() {
		osOpen = a
		doIoctl = b
		unixStatfs = c
		unixPipe2 = d
		unixClose = e
		unixRead = f
	}
}

// openOK installs an osOpen seam that hands back a fresh real temp file (so
// .Fd()/.Close() work) regardless of the requested path.
func openOK(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	osOpen = func(string) (*os.File, error) {
		f, err := os.CreateTemp(dir, "fd")
		if err != nil {
			t.Fatalf("temp: %v", err)
		}
		return f, nil
	}
}

// openErr installs an osOpen seam that always fails.
func openErr() {
	osOpen = func(string) (*os.File, error) { return nil, errInjected }
}

// ioctlFunc is the shape of the doIoctl seam fake's per-call hook: it receives
// the ioctl request number and the kernel-arg pointer and returns the errno to
// report. A hook that fills the arg buffer models a kernel that writes data
// back to userspace.
type ioctlFunc func(req uintptr, arg unsafe.Pointer) unix.Errno

// installIoctl points the doIoctl seam at fn, ignoring the fd.
func installIoctl(fn ioctlFunc) {
	doIoctl = func(_ uintptr, req uintptr, arg unsafe.Pointer) unix.Errno {
		return fn(req, arg)
	}
}

// ioctlOK is an ioctlFunc that always succeeds and writes nothing.
func ioctlOK(uintptr, unsafe.Pointer) unix.Errno { return 0 }

// ioctlErrno returns an ioctlFunc that always fails with the given errno.
func ioctlErrno(e unix.Errno) ioctlFunc {
	return func(uintptr, unsafe.Pointer) unix.Errno { return e }
}

// ---- seams_linux.go ----

// TestRealIoctl exercises the production doIoctl seam (realIoctl) directly: a
// bogus ioctl request on a regular file fd yields a non-zero errno (ENOTTY),
// covering the real SYS_IOCTL path that the fault-injecting tests bypass. The
// genuine success path is exercised by the root integration tests.
func TestRealIoctl(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "fd")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if errno := realIoctl(f.Fd(), 0xDEAD, nil); errno == 0 {
		t.Fatal("want errno from bogus ioctl request")
	}
}

// ---- btrfs_linux.go ----

func TestPutName(t *testing.T) {
	var buf [8]byte
	if err := putName(buf[:], "abc"); err != nil {
		t.Fatalf("putName: %v", err)
	}
	if cstr(buf[:]) != "abc" {
		t.Fatalf("got %q", cstr(buf[:]))
	}
	// One byte too long (needs room for the NUL terminator).
	if err := putName(buf[:], "12345678"); err == nil {
		t.Fatal("want too-long error")
	}
}

func TestCstr(t *testing.T) {
	if got := cstr([]byte{'a', 'b', 0, 'c'}); got != "ab" {
		t.Fatalf("got %q", got)
	}
	// No NUL: whole slice.
	if got := cstr([]byte("abcd")); got != "abcd" {
		t.Fatalf("got %q", got)
	}
}

func TestIoctlDirOpenError(t *testing.T) {
	defer snapshotSeams()()
	openErr()
	if err := ioctlDir("x", BTRFS_IOC_SYNC, nil); err == nil {
		t.Fatal("want open error")
	}
}

func TestSubvolCreate(t *testing.T) {
	defer snapshotSeams()()
	if err := SubvolCreate("p", ""); err == nil {
		t.Fatal("want empty-name error")
	}
	// Name too long.
	long := string(bytes.Repeat([]byte{'a'}, btrfsPathNameMax+1))
	if err := SubvolCreate("p", long); err == nil {
		t.Fatal("want too-long error")
	}
	openOK(t)
	installIoctl(ioctlErrno(unix.EPERM))
	if err := SubvolCreate("p", "name"); err == nil {
		t.Fatal("want ioctl error")
	}
	installIoctl(ioctlOK)
	if err := SubvolCreate("p", "name"); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestSnapshotCreate(t *testing.T) {
	defer snapshotSeams()()
	if err := SnapshotCreate("src", "dst", "", false); err == nil {
		t.Fatal("want empty-name error")
	}
	openErr()
	if err := SnapshotCreate("src", "dst", "snap", true); err == nil {
		t.Fatal("want src open error")
	}
	// Name too long: src opens fine but putName rejects.
	openOK(t)
	long := string(bytes.Repeat([]byte{'a'}, btrfsSubvolNameMax+1))
	if err := SnapshotCreate("src", "dst", long, true); err == nil {
		t.Fatal("want too-long error")
	}
	// readonly=true path + ioctl error.
	installIoctl(ioctlErrno(unix.EPERM))
	if err := SnapshotCreate("src", "dst", "snap", true); err == nil {
		t.Fatal("want ioctl error")
	}
	// readonly=false success path.
	installIoctl(ioctlOK)
	if err := SnapshotCreate("src", "dst", "snap", false); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestSubvolDelete(t *testing.T) {
	defer snapshotSeams()()
	if err := SubvolDelete("p", ""); err == nil {
		t.Fatal("want empty-name error")
	}
	long := string(bytes.Repeat([]byte{'a'}, btrfsPathNameMax+1))
	if err := SubvolDelete("p", long); err == nil {
		t.Fatal("want too-long error")
	}
	openOK(t)
	installIoctl(ioctlErrno(unix.ENOENT))
	if err := SubvolDelete("p", "name"); err == nil {
		t.Fatal("want ioctl error")
	}
	installIoctl(ioctlOK)
	if err := SubvolDelete("p", "name"); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestSubvolGetSetFlags(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)

	installIoctl(ioctlErrno(unix.EPERM))
	if _, err := SubvolGetFlags("p"); err == nil {
		t.Fatal("want getflags error")
	}
	if err := SubvolSetFlags("p", SubvolRDONLY); err == nil {
		t.Fatal("want setflags error")
	}

	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		*(*uint64)(arg) = SubvolRDONLY
		return 0
	})
	got, err := SubvolGetFlags("p")
	if err != nil || got != SubvolRDONLY {
		t.Fatalf("got=%#x err=%v", got, err)
	}
	if err := SubvolSetFlags("p", 0); err != nil {
		t.Fatalf("setflags: %v", err)
	}
}

func TestSetReadonly(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)

	// Get fails -> propagate.
	installIoctl(ioctlErrno(unix.EPERM))
	if err := SetReadonly("p", true); err == nil {
		t.Fatal("want get error")
	}

	// ro=true: read 0, write succeeds.
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		if req == BTRFS_IOC_SUBVOL_GETFLAGS {
			*(*uint64)(arg) = 0
		}
		return 0
	})
	if err := SetReadonly("p", true); err != nil {
		t.Fatalf("ro=true: %v", err)
	}

	// ro=false: read RDONLY|other, clear it.
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		if req == BTRFS_IOC_SUBVOL_GETFLAGS {
			*(*uint64)(arg) = SubvolRDONLY | SubvolQGroupInherit
		}
		return 0
	})
	if err := SetReadonly("p", false); err != nil {
		t.Fatalf("ro=false: %v", err)
	}
}

func TestSubvolID(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)

	installIoctl(ioctlErrno(unix.EPERM))
	if _, err := SubvolID("p"); err == nil {
		t.Fatal("want ioctl error")
	}
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		a := (*btrfsIoctlInoLookupArgs)(arg)
		a.Treeid = 5
		return 0
	})
	id, err := SubvolID("p")
	if err != nil || id != 5 {
		t.Fatalf("id=%d err=%v", id, err)
	}
}

func TestGetSubvolInfo(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)

	installIoctl(ioctlErrno(unix.EPERM))
	if _, err := GetSubvolInfo("p"); err == nil {
		t.Fatal("want ioctl error")
	}
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		a := (*btrfsIoctlGetSubvolInfoArgs)(arg)
		a.Treeid = 257
		copy(a.Name[:], "snap")
		a.ParentID = 5
		a.Dirid = 256
		a.Generation = 99
		a.Flags = RootSubvolRDONLY
		a.UUID[0] = 0xaa
		a.ParentUUID[0] = 0xbb
		return 0
	})
	info, err := GetSubvolInfo("p")
	if err != nil {
		t.Fatalf("GetSubvolInfo: %v", err)
	}
	if info.ID != 257 || info.Name != "snap" || info.ParentID != 5 || info.Dirid != 256 ||
		info.Generation != 99 || info.Flags != RootSubvolRDONLY || info.UUID[0] != 0xaa || info.ParentUUID[0] != 0xbb {
		t.Fatalf("decoded wrong: %+v", info)
	}
}

func TestIsReadonly(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)

	installIoctl(ioctlErrno(unix.EPERM))
	if _, err := IsReadonly("p"); err == nil {
		t.Fatal("want error")
	}
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		*(*uint64)(arg) = SubvolRDONLY
		return 0
	})
	ro, err := IsReadonly("p")
	if err != nil || !ro {
		t.Fatalf("ro=%v err=%v", ro, err)
	}
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		*(*uint64)(arg) = 0
		return 0
	})
	ro, err = IsReadonly("p")
	if err != nil || ro {
		t.Fatalf("ro=%v err=%v", ro, err)
	}
}

func TestSync(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(ioctlErrno(unix.EIO))
	if err := Sync("p"); err == nil {
		t.Fatal("want ioctl error")
	}
	installIoctl(ioctlOK)
	if err := Sync("p"); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestAvailable(t *testing.T) {
	defer snapshotSeams()()
	unixStatfs = func(string, *unix.Statfs_t) error { return errInjected }
	if Available("p") {
		t.Fatal("want false on statfs error")
	}
	unixStatfs = func(_ string, st *unix.Statfs_t) error {
		st.Type = int64(unix.BTRFS_SUPER_MAGIC)
		return nil
	}
	if !Available("p") {
		t.Fatal("want true on btrfs magic")
	}
	unixStatfs = func(_ string, st *unix.Statfs_t) error {
		st.Type = 0x1234
		return nil
	}
	if Available("p") {
		t.Fatal("want false on non-btrfs magic")
	}
}

// ---- btrfs_admin_linux.go ----

func TestIsUnsupportedIoctl(t *testing.T) {
	if !isUnsupportedIoctl(unix.ENOTTY) {
		t.Fatal("ENOTTY should be unsupported")
	}
	if isUnsupportedIoctl(unix.EPERM) {
		t.Fatal("EPERM should not be unsupported")
	}
}

func TestJoinPartsAndResolvePath(t *testing.T) {
	if joinParts(nil) != "" {
		t.Fatal("empty join")
	}
	if joinParts([]string{"a", "b", "c"}) != "a/b/c" {
		t.Fatal("join")
	}
	byID := map[uint64]subvolRef{
		300: {parent: 256, name: "leaf"},
		256: {parent: 5, name: "mid"},
	}
	if got := resolvePath(300, byID); got != "mid/leaf" {
		t.Fatalf("resolvePath=%q", got)
	}
	// Missing entry: break immediately.
	if got := resolvePath(999, byID); got != "" {
		t.Fatalf("resolvePath(missing)=%q", got)
	}
	// Cyclic: visited guard terminates.
	cyc := map[uint64]subvolRef{
		300: {parent: 301, name: "a"},
		301: {parent: 300, name: "b"},
	}
	_ = resolvePath(300, cyc) // must not loop forever
}

// makeRootRefBody builds a ROOT_REF item body: 18-byte btrfs_root_ref header
// (dirid, sequence, name_len) followed by the name.
func makeRootRefBody(name string) []byte {
	b := make([]byte, btrfsRootRefSize+len(name))
	binary.LittleEndian.PutUint16(b[16:18], uint16(len(name)))
	copy(b[btrfsRootRefSize:], name)
	return b
}

// v2Result fills a TREE_SEARCH_V2 buffer (header overlay + items) with the
// given (header, body) items. It writes h.Key.NrItems and the packed items.
func writeV2Result(arg unsafe.Pointer, items []struct {
	hdr  btrfsIoctlSearchHeader
	body []byte
}) {
	h := (*btrfsIoctlSearchArgsV2Hdr)(arg)
	hdrSize := int(unsafe.Sizeof(btrfsIoctlSearchArgsV2Hdr{}))
	shSize := int(unsafe.Sizeof(btrfsIoctlSearchHeader{}))
	base := arg
	off := hdrSize
	for i := range items {
		sh := (*btrfsIoctlSearchHeader)(unsafe.Add(base, off))
		items[i].hdr.Len = uint32(len(items[i].body))
		*sh = items[i].hdr
		off += shSize
		dst := unsafe.Slice((*byte)(unsafe.Add(base, off)), len(items[i].body))
		copy(dst, items[i].body)
		off += len(items[i].body)
	}
	h.Key.NrItems = uint32(len(items))
}

func TestListSubvolumes(t *testing.T) {
	defer snapshotSeams()()

	// open error
	openErr()
	if _, err := ListSubvolumes("p"); err == nil {
		t.Fatal("want open error")
	}

	openOK(t)
	// V2 ioctl error (non-ENOTTY) propagates.
	installIoctl(ioctlErrno(unix.EPERM))
	if _, err := ListSubvolumes("p"); err == nil {
		t.Fatal("want V2 ioctl error")
	}

	// One ROOT_REF item then an empty second round (found==0 => done).
	round := 0
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		round++
		if round == 1 {
			writeV2Result(arg, []struct {
				hdr  btrfsIoctlSearchHeader
				body []byte
			}{
				{btrfsIoctlSearchHeader{Objectid: 5, Offset: 256, Type: btrfsRootRefKey}, makeRootRefBody("child")},
				// A non-ROOT_REF item (filtered out by emit) and a too-short body.
				{btrfsIoctlSearchHeader{Objectid: 5, Offset: 257, Type: btrfsRootItemKey}, makeRootRefBody("ignored")},
				{btrfsIoctlSearchHeader{Objectid: 5, Offset: 258, Type: btrfsRootRefKey}, []byte{1, 2, 3}}, // short
			})
			return 0
		}
		// Second round: no items.
		h := (*btrfsIoctlSearchArgsV2Hdr)(arg)
		h.Key.NrItems = 0
		return 0
	})
	subs, err := ListSubvolumes("p")
	if err != nil {
		t.Fatalf("ListSubvolumes: %v", err)
	}
	if len(subs) != 1 || subs[0].ID != 256 || subs[0].Name != "child" || subs[0].ParentID != 5 {
		t.Fatalf("subs=%+v", subs)
	}
}

// TestEmitNameTruncation feeds a ROOT_REF whose declared name_len overruns the
// body so emit must clamp it.
func TestListSubvolumesNameClamp(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	body := make([]byte, btrfsRootRefSize+2)
	binary.LittleEndian.PutUint16(body[16:18], 100) // claims 100 bytes, only 2 present
	body[btrfsRootRefSize] = 'x'
	body[btrfsRootRefSize+1] = 'y'
	round := 0
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		round++
		if round == 1 {
			writeV2Result(arg, []struct {
				hdr  btrfsIoctlSearchHeader
				body []byte
			}{
				{btrfsIoctlSearchHeader{Objectid: 5, Offset: 256, Type: btrfsRootRefKey}, body},
			})
			return 0
		}
		h := (*btrfsIoctlSearchArgsV2Hdr)(arg)
		h.Key.NrItems = 0
		return 0
	})
	subs, err := ListSubvolumes("p")
	if err != nil {
		t.Fatalf("ListSubvolumes: %v", err)
	}
	if len(subs) != 1 || subs[0].Name != "xy" {
		t.Fatalf("subs=%+v", subs)
	}
}

func TestSearchTreeV2FnError(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		writeV2Result(arg, []struct {
			hdr  btrfsIoctlSearchHeader
			body []byte
		}{
			{btrfsIoctlSearchHeader{Objectid: 5, Offset: 256, Type: btrfsRootRefKey}, makeRootRefBody("child")},
		})
		return 0
	})
	// fn returns an error -> treeSearchV2 surfaces it.
	boom := errors.New("fn boom")
	f, _ := os.CreateTemp(t.TempDir(), "fd")
	defer f.Close()
	err := searchTreeTypeRange(f.Fd(), btrfsRootTreeObjectID, btrfsRootRefKey, btrfsRootRefKey,
		func(*btrfsIoctlSearchHeader, []byte) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("want fn error, got %v", err)
	}
}

// TestTreeSearchV2Truncation feeds an item count larger than the buffer can
// hold and a header whose Len overruns, hitting both off+shSize and end>len
// break paths.
func TestTreeSearchV2Truncation(t *testing.T) {
	defer snapshotSeams()()
	f, _ := os.CreateTemp(t.TempDir(), "fd")
	defer f.Close()

	t.Run("body overruns buffer", func(t *testing.T) {
		defer snapshotSeams()()
		installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
			h := (*btrfsIoctlSearchArgsV2Hdr)(arg)
			hdrSize := int(unsafe.Sizeof(btrfsIoctlSearchArgsV2Hdr{}))
			sh := (*btrfsIoctlSearchHeader)(unsafe.Add(arg, hdrSize))
			*sh = btrfsIoctlSearchHeader{Objectid: 5, Type: btrfsRootRefKey, Len: 1 << 20} // overruns 256K buf
			h.Key.NrItems = 1
			return 0
		})
		found, _, err := treeSearchV2(f.Fd(), &btrfsIoctlSearchKey{}, func(*btrfsIoctlSearchHeader, []byte) error { return nil })
		if err != nil || found != 0 {
			t.Fatalf("found=%d err=%v", found, err)
		}
	})

	t.Run("header count exceeds buffer", func(t *testing.T) {
		defer snapshotSeams()()
		installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
			h := (*btrfsIoctlSearchArgsV2Hdr)(arg)
			h.Key.NrItems = 1 << 20 // claims a million items; buffer can't hold their headers
			return 0
		})
		found, _, err := treeSearchV2(f.Fd(), &btrfsIoctlSearchKey{}, func(*btrfsIoctlSearchHeader, []byte) error { return nil })
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		_ = found
	})
}

// TestSearchTreeV1Fallback drives the v1 path: the V2 ioctl returns ENOTTY so
// searchTreeTypeRange falls back to searchRootTreeV1.
func TestSearchTreeV1Fallback(t *testing.T) {
	defer snapshotSeams()()
	f, _ := os.CreateTemp(t.TempDir(), "fd")
	defer f.Close()

	round := 0
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		if req == BTRFS_IOC_TREE_SEARCH_V2 {
			return unix.ENOTTY
		}
		// v1 TREE_SEARCH.
		a := (*btrfsIoctlSearchArgs)(arg)
		round++
		if round == 1 {
			// one item, then advanceKey continues
			shSize := int(unsafe.Sizeof(btrfsIoctlSearchHeader{}))
			sh := (*btrfsIoctlSearchHeader)(unsafe.Pointer(&a.Buf[0]))
			body := makeRootRefBody("v1child")
			*sh = btrfsIoctlSearchHeader{Objectid: 5, Offset: 256, Type: btrfsRootRefKey, Len: uint32(len(body))}
			copy(a.Buf[shSize:], body)
			a.Key.NrItems = 1
			return 0
		}
		// second round: zero items -> done.
		a.Key.NrItems = 0
		return 0
	})

	var got []string
	err := searchTreeTypeRange(f.Fd(), btrfsRootTreeObjectID, btrfsRootRefKey, btrfsRootRefKey,
		func(h *btrfsIoctlSearchHeader, body []byte) error {
			got = append(got, "ok")
			return nil
		})
	if err != nil {
		t.Fatalf("v1 fallback: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d items", len(got))
	}
}

// TestSearchTreeV2RangeExhausted feeds a single item whose key sits at the very
// top of the 136-bit key space, so advanceKey returns false and the V2 walk
// terminates via the !advanceKey branch rather than a found==0 round.
func TestSearchTreeV2RangeExhausted(t *testing.T) {
	defer snapshotSeams()()
	f, _ := os.CreateTemp(t.TempDir(), "fd")
	defer f.Close()
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		writeV2Result(arg, []struct {
			hdr  btrfsIoctlSearchHeader
			body []byte
		}{
			{btrfsIoctlSearchHeader{Objectid: ^uint64(0), Type: ^uint32(0), Offset: ^uint64(0)}, makeRootRefBody("top")},
		})
		return 0
	})
	got := 0
	err := searchTreeTypeRange(f.Fd(), btrfsRootTreeObjectID, 0, ^uint32(0),
		func(*btrfsIoctlSearchHeader, []byte) error { got++; return nil })
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if got != 1 {
		t.Fatalf("got %d items", got)
	}
}

// TestSearchRootTreeV1RangeExhausted is the v1 analogue: a top-of-space item
// makes advanceKey return false so the v1 walk exits via !advanceKey.
func TestSearchRootTreeV1RangeExhausted(t *testing.T) {
	defer snapshotSeams()()
	f, _ := os.CreateTemp(t.TempDir(), "fd")
	defer f.Close()
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		a := (*btrfsIoctlSearchArgs)(arg)
		shSize := int(unsafe.Sizeof(btrfsIoctlSearchHeader{}))
		sh := (*btrfsIoctlSearchHeader)(unsafe.Pointer(&a.Buf[0]))
		body := makeRootRefBody("top")
		*sh = btrfsIoctlSearchHeader{Objectid: ^uint64(0), Type: ^uint32(0), Offset: ^uint64(0), Len: uint32(len(body))}
		copy(a.Buf[shSize:], body)
		a.Key.NrItems = 1
		return 0
	})
	got := 0
	err := searchRootTreeV1(f.Fd(), &btrfsIoctlSearchKey{}, func(*btrfsIoctlSearchHeader, []byte) error { got++; return nil })
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if got != 1 {
		t.Fatalf("got %d items", got)
	}
}

func TestSearchRootTreeV1IoctlError(t *testing.T) {
	defer snapshotSeams()()
	f, _ := os.CreateTemp(t.TempDir(), "fd")
	defer f.Close()
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno { return unix.EPERM })
	err := searchRootTreeV1(f.Fd(), &btrfsIoctlSearchKey{}, func(*btrfsIoctlSearchHeader, []byte) error { return nil })
	if err == nil {
		t.Fatal("want v1 ioctl error")
	}
}

func TestSearchRootTreeV1FnError(t *testing.T) {
	defer snapshotSeams()()
	f, _ := os.CreateTemp(t.TempDir(), "fd")
	defer f.Close()
	boom := errors.New("v1 fn boom")
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		a := (*btrfsIoctlSearchArgs)(arg)
		shSize := int(unsafe.Sizeof(btrfsIoctlSearchHeader{}))
		sh := (*btrfsIoctlSearchHeader)(unsafe.Pointer(&a.Buf[0]))
		body := makeRootRefBody("x")
		*sh = btrfsIoctlSearchHeader{Objectid: 5, Offset: 256, Type: btrfsRootRefKey, Len: uint32(len(body))}
		copy(a.Buf[shSize:], body)
		a.Key.NrItems = 1
		return 0
	})
	err := searchRootTreeV1(f.Fd(), &btrfsIoctlSearchKey{}, func(*btrfsIoctlSearchHeader, []byte) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("want fn error, got %v", err)
	}
}

// TestSearchRootTreeV1Truncation hits the two break branches in the v1 inner
// loop (off+shSize > len, end > len).
func TestSearchRootTreeV1Truncation(t *testing.T) {
	defer snapshotSeams()()
	f, _ := os.CreateTemp(t.TempDir(), "fd")
	defer f.Close()

	t.Run("body overruns", func(t *testing.T) {
		defer snapshotSeams()()
		round := 0
		installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
			a := (*btrfsIoctlSearchArgs)(arg)
			round++
			if round == 1 {
				sh := (*btrfsIoctlSearchHeader)(unsafe.Pointer(&a.Buf[0]))
				*sh = btrfsIoctlSearchHeader{Type: btrfsRootRefKey, Len: 1 << 20} // overruns the fixed buf
				a.Key.NrItems = 1
				return 0
			}
			a.Key.NrItems = 0 // next round: no items -> walk terminates
			return 0
		})
		if err := searchRootTreeV1(f.Fd(), &btrfsIoctlSearchKey{}, func(*btrfsIoctlSearchHeader, []byte) error { return nil }); err != nil {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("count exceeds buffer", func(t *testing.T) {
		defer snapshotSeams()()
		round := 0
		installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
			a := (*btrfsIoctlSearchArgs)(arg)
			round++
			if round == 1 {
				a.Key.NrItems = 1 << 20 // millions of headers; buffer can't hold them all
				return 0
			}
			a.Key.NrItems = 0
			return 0
		})
		if err := searchRootTreeV1(f.Fd(), &btrfsIoctlSearchKey{}, func(*btrfsIoctlSearchHeader, []byte) error { return nil }); err != nil {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestAdvanceKeyWraps(t *testing.T) {
	// offset at max wraps to type increment.
	key := btrfsIoctlSearchKey{}
	last := btrfsIoctlSearchHeader{Objectid: 1, Type: 5, Offset: ^uint64(0)}
	if !advanceKey(&key, last) {
		t.Fatal("want true: type can still advance")
	}
	if key.MinOffset != 0 || key.MinType != 6 {
		t.Fatalf("key=%+v", key)
	}
	// offset max, type max -> objectid increment.
	key = btrfsIoctlSearchKey{}
	last = btrfsIoctlSearchHeader{Objectid: 1, Type: ^uint32(0), Offset: ^uint64(0)}
	if !advanceKey(&key, last) {
		t.Fatal("want true: objectid can still advance")
	}
	if key.MinObjectid != 2 || key.MinType != 0 {
		t.Fatalf("key=%+v", key)
	}
}

func TestDeviceAdd(t *testing.T) {
	defer snapshotSeams()()
	if err := DeviceAdd("m", ""); err == nil {
		t.Fatal("want empty error")
	}
	long := string(bytes.Repeat([]byte{'a'}, btrfsPathNameMax+1))
	if err := DeviceAdd("m", long); err == nil {
		t.Fatal("want too-long error")
	}
	openOK(t)
	installIoctl(ioctlErrno(unix.EPERM))
	if err := DeviceAdd("m", "/dev/sdb"); err == nil {
		t.Fatal("want ioctl error")
	}
	installIoctl(ioctlOK)
	if err := DeviceAdd("m", "/dev/sdb"); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestDeviceRemove(t *testing.T) {
	defer snapshotSeams()()
	if err := DeviceRemove("m", ""); err == nil {
		t.Fatal("want empty error")
	}
	long := string(bytes.Repeat([]byte{'a'}, btrfsSubvolNameMax+1))
	if err := DeviceRemove("m", long); err == nil {
		t.Fatal("want too-long error")
	}
	openOK(t)

	// V2 succeeds.
	installIoctl(ioctlOK)
	if err := DeviceRemove("m", "/dev/sdb"); err != nil {
		t.Fatalf("v2 success: %v", err)
	}

	// V2 non-ENOTTY error.
	installIoctl(ioctlErrno(unix.EPERM))
	if err := DeviceRemove("m", "/dev/sdb"); err == nil {
		t.Fatal("want v2 error")
	}

	// V2 ENOTTY -> v1 fallback success.
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		if req == BTRFS_IOC_RM_DEV_V2 {
			return unix.ENOTTY
		}
		return 0
	})
	if err := DeviceRemove("m", "/dev/sdb"); err != nil {
		t.Fatalf("v1 fallback success: %v", err)
	}

	// V2 ENOTTY -> v1 fallback ioctl error.
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		if req == BTRFS_IOC_RM_DEV_V2 {
			return unix.ENOTTY
		}
		return unix.EPERM
	})
	if err := DeviceRemove("m", "/dev/sdb"); err == nil {
		t.Fatal("want v1 fallback error")
	}

	// V2 ENOTTY -> v1 putName too long. The v1 name field is the larger
	// path-name field, so a name that fit V2's subvol field still fits v1; we
	// can't reach the v1 putName error via a real path. Skip that branch here;
	// it is defensively coded and unreachable given V2 accepted the same name.
}

func TestDeviceRemoveByID(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(ioctlErrno(unix.EPERM))
	if err := DeviceRemoveByID("m", 2); err == nil {
		t.Fatal("want ioctl error")
	}
	installIoctl(ioctlOK)
	if err := DeviceRemoveByID("m", 2); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestGetDeviceInfo(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(ioctlErrno(unix.ENODEV))
	if _, err := GetDeviceInfo("p", 1); err == nil {
		t.Fatal("want ioctl error")
	}
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		a := (*btrfsIoctlDevInfoArgs)(arg)
		a.Devid = 1
		a.BytesUsed = 100
		a.TotalBytes = 200
		copy(a.Path[:], "/dev/sda")
		a.UUID[0] = 0xcc
		return 0
	})
	di, err := GetDeviceInfo("p", 1)
	if err != nil {
		t.Fatalf("GetDeviceInfo: %v", err)
	}
	if di.Devid != 1 || di.BytesUsed != 100 || di.TotalBytes != 200 || di.Path != "/dev/sda" || di.UUID[0] != 0xcc {
		t.Fatalf("decoded wrong: %+v", di)
	}
}

func TestGetFsInfo(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(ioctlErrno(unix.EPERM))
	if _, err := GetFsInfo("p"); err == nil {
		t.Fatal("want ioctl error")
	}
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		a := (*btrfsIoctlFsInfoArgs)(arg)
		a.MaxID = 2
		a.NumDevices = 2
		a.Nodesize = 16384
		a.Sectorsize = 4096
		a.Generation = 42
		a.FSID[0] = 0xdd
		return 0
	})
	fi, err := GetFsInfo("p")
	if err != nil {
		t.Fatalf("GetFsInfo: %v", err)
	}
	if fi.MaxID != 2 || fi.NumDevices != 2 || fi.Nodesize != 16384 || fi.Sectorsize != 4096 || fi.Generation != 42 || fi.FSID[0] != 0xdd {
		t.Fatalf("decoded wrong: %+v", fi)
	}
}

func TestDecodeScrubProgress(t *testing.T) {
	p := &btrfsScrubProgress{
		DataBytesScrubbed:   1,
		TreeBytesScrubbed:   2,
		ReadErrors:          3,
		CsumErrors:          4,
		VerifyErrors:        5,
		SuperErrors:         6,
		UncorrectableErrors: 7,
		CorrectedErrors:     8,
	}
	sp := decodeScrubProgress(p)
	if sp.DataBytesScrubbed != 1 || sp.TreeBytesScrubbed != 2 || sp.ReadErrors != 3 || sp.CsumErrors != 4 ||
		sp.VerifyErrors != 5 || sp.SuperErrors != 6 || sp.UncorrectableErrors != 7 || sp.CorrectedErrors != 8 {
		t.Fatalf("decode wrong: %+v", sp)
	}
}

func TestScrubStart(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(ioctlErrno(unix.EPERM))
	if _, err := ScrubStart("p", 1, ScrubOptions{}); err == nil {
		t.Fatal("want ioctl error")
	}
	// readonly option + success, with progress filled.
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		a := (*btrfsIoctlScrubArgs)(arg)
		a.Progress.DataBytesScrubbed = 4096
		return 0
	})
	sp, err := ScrubStart("p", 1, ScrubOptions{Readonly: true})
	if err != nil || sp.DataBytesScrubbed != 4096 {
		t.Fatalf("sp=%+v err=%v", sp, err)
	}
}

func TestScrubProgressFor(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(ioctlErrno(unix.ENOTCONN))
	if _, err := ScrubProgressFor("p", 1); err == nil {
		t.Fatal("want ioctl error")
	}
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		a := (*btrfsIoctlScrubArgs)(arg)
		a.Progress.CsumErrors = 9
		return 0
	})
	sp, err := ScrubProgressFor("p", 1)
	if err != nil || sp.CsumErrors != 9 {
		t.Fatalf("sp=%+v err=%v", sp, err)
	}
}

func TestScrubCancel(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(ioctlErrno(unix.ENOTCONN))
	if err := ScrubCancel("p"); err == nil {
		t.Fatal("want ioctl error")
	}
	installIoctl(ioctlOK)
	if err := ScrubCancel("p"); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestToKernelBalArgs(t *testing.T) {
	if (toKernelBalArgs(nil)) != (btrfsBalanceArgs{}) {
		t.Fatal("nil should give zero")
	}
	k := toKernelBalArgs(&BalanceFilter{Flags: 1, Usage: 50, Profiles: 2, Devid: 3, Target: 4})
	if k.Flags != 1 || k.Usage != 50 || k.Profiles != 2 || k.Devid != 3 || k.Target != 4 {
		t.Fatalf("k=%+v", k)
	}
}

func TestBalanceStart(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(ioctlErrno(unix.EPERM))
	if _, err := BalanceStart("p", BalanceArgs{}); err == nil {
		t.Fatal("want ioctl error")
	}
	// All filters set + force; success with running state.
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		a := (*btrfsIoctlBalanceArgs)(arg)
		a.State = BalanceStateRunning
		a.Stat.Expected = 10
		a.Stat.Considered = 5
		a.Stat.Completed = 3
		return 0
	})
	bp, err := BalanceStart("p", BalanceArgs{
		Data:  &BalanceFilter{Flags: BalanceArgsUsage, Usage: 10},
		Meta:  &BalanceFilter{},
		Sys:   &BalanceFilter{},
		Force: true,
	})
	if err != nil || !bp.Running || bp.Expected != 10 || bp.Considered != 5 || bp.Completed != 3 {
		t.Fatalf("bp=%+v err=%v", bp, err)
	}
}

func TestBalanceProgressFor(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(ioctlErrno(unix.ENOTCONN))
	if _, err := BalanceProgressFor("p"); err == nil {
		t.Fatal("want ioctl error")
	}
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		a := (*btrfsIoctlBalanceArgs)(arg)
		a.State = 0
		a.Stat.Completed = 7
		return 0
	})
	bp, err := BalanceProgressFor("p")
	if err != nil || bp.Running || bp.Completed != 7 {
		t.Fatalf("bp=%+v err=%v", bp, err)
	}
}

func TestBalanceCancelPause(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(ioctlErrno(unix.ENOTCONN))
	if err := BalanceCancel("p"); err == nil {
		t.Fatal("want cancel error")
	}
	if err := BalancePause("p"); err == nil {
		t.Fatal("want pause error")
	}
	installIoctl(ioctlOK)
	if err := BalanceCancel("p"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := BalancePause("p"); err != nil {
		t.Fatalf("pause: %v", err)
	}
}

// ---- btrfs_quota_linux.go ----

func TestQuotaEnableDisable(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(ioctlErrno(unix.EPERM))
	if err := QuotaEnable("p"); err == nil {
		t.Fatal("want enable error")
	}
	if err := QuotaDisable("p"); err == nil {
		t.Fatal("want disable error")
	}
	installIoctl(ioctlOK)
	if err := QuotaEnable("p"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := QuotaDisable("p"); err != nil {
		t.Fatalf("disable: %v", err)
	}
}

func TestQgroupCreateDestroy(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(ioctlErrno(unix.EPERM))
	if err := QgroupCreate("p", 1); err == nil {
		t.Fatal("want create error")
	}
	if err := QgroupDestroy("p", 1); err == nil {
		t.Fatal("want destroy error")
	}
	installIoctl(ioctlOK)
	if err := QgroupCreate("p", 1); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := QgroupDestroy("p", 1); err != nil {
		t.Fatalf("destroy: %v", err)
	}
}

func TestQgroupAssignRemove(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(ioctlErrno(unix.EPERM))
	if err := QgroupAssign("p", 1, 2); err == nil {
		t.Fatal("want assign error")
	}
	if err := QgroupRemove("p", 1, 2); err == nil {
		t.Fatal("want remove error")
	}
	installIoctl(ioctlOK)
	if err := QgroupAssign("p", 1, 2); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if err := QgroupRemove("p", 1, 2); err != nil {
		t.Fatalf("remove: %v", err)
	}
}

func TestQgroupLimit(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(ioctlErrno(unix.EPERM))
	if err := QgroupLimit("p", 0, QgroupLimits{Flags: QgroupLimitMaxRfer, MaxRfer: 1 << 20}); err == nil {
		t.Fatal("want ioctl error")
	}
	installIoctl(ioctlOK)
	if err := QgroupLimit("p", 0, QgroupLimits{Flags: QgroupLimitMaxRfer, MaxRfer: 1 << 20}); err != nil {
		t.Fatalf("limit: %v", err)
	}
}

func TestQgroupHasLimit(t *testing.T) {
	if (Qgroup{}).HasLimit() {
		t.Fatal("zero should have no limit")
	}
	if !(Qgroup{LimFlags: QgroupLimitMaxRfer}).HasLimit() {
		t.Fatal("non-zero should have limit")
	}
}

func TestListQgroups(t *testing.T) {
	defer snapshotSeams()()

	openErr()
	if _, err := ListQgroups("p"); err == nil {
		t.Fatal("want open error")
	}

	openOK(t)
	installIoctl(ioctlErrno(unix.EPERM))
	if _, err := ListQgroups("p"); err == nil {
		t.Fatal("want ioctl error")
	}

	// INFO + LIMIT items for the same id, plus short bodies (skipped).
	info := make([]byte, btrfsQgroupInfoItemSize)
	binary.LittleEndian.PutUint64(info[8:16], 4096)  // rfer
	binary.LittleEndian.PutUint64(info[24:32], 2048) // excl
	limit := make([]byte, btrfsQgroupLimitItemSize)
	binary.LittleEndian.PutUint64(limit[0:8], QgroupLimitMaxRfer)
	binary.LittleEndian.PutUint64(limit[8:16], 1<<30)
	binary.LittleEndian.PutUint64(limit[16:24], 1<<20)

	round := 0
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		round++
		if round == 1 {
			id := (uint64(0) << 48) | 256
			writeV2Result(arg, []struct {
				hdr  btrfsIoctlSearchHeader
				body []byte
			}{
				{btrfsIoctlSearchHeader{Offset: id, Type: btrfsQgroupInfoKey}, info},
				{btrfsIoctlSearchHeader{Offset: id, Type: btrfsQgroupLimitKey}, limit},
				{btrfsIoctlSearchHeader{Offset: id, Type: btrfsQgroupInfoKey}, []byte{1}},  // short, skipped
				{btrfsIoctlSearchHeader{Offset: id, Type: btrfsQgroupLimitKey}, []byte{1}}, // short, skipped
				{btrfsIoctlSearchHeader{Offset: id, Type: btrfsQgroupStatusKey}, info},     // unrelated type
			})
			return 0
		}
		h := (*btrfsIoctlSearchArgsV2Hdr)(arg)
		h.Key.NrItems = 0
		return 0
	})
	qgs, err := ListQgroups("p")
	if err != nil {
		t.Fatalf("ListQgroups: %v", err)
	}
	if len(qgs) != 1 {
		t.Fatalf("want 1 qgroup, got %d: %+v", len(qgs), qgs)
	}
	q := qgs[0]
	if q.SubvolID != 256 || q.Rfer != 4096 || q.Excl != 2048 || q.MaxRfer != 1<<30 || q.MaxExcl != 1<<20 || q.LimFlags != QgroupLimitMaxRfer {
		t.Fatalf("decoded wrong: %+v", q)
	}
}

func TestDefrag(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(ioctlErrno(unix.EPERM))
	if err := Defrag("p"); err == nil {
		t.Fatal("want ioctl error")
	}
	installIoctl(ioctlOK)
	if err := Defrag("p"); err != nil {
		t.Fatalf("defrag: %v", err)
	}
}

func TestDefragRange(t *testing.T) {
	defer snapshotSeams()()
	openOK(t)
	installIoctl(ioctlErrno(unix.EPERM))
	if err := DefragRange("p", DefragRangeOptions{}); err == nil {
		t.Fatal("want ioctl error")
	}
	// Zero Len => EOF sentinel branch + success.
	installIoctl(ioctlOK)
	if err := DefragRange("p", DefragRangeOptions{}); err != nil {
		t.Fatalf("defragrange zero len: %v", err)
	}
	// Explicit Len branch.
	if err := DefragRange("p", DefragRangeOptions{Start: 4096, Len: 8192, Flags: DefragRangeCompress, ExtentThresh: 1, CompressType: 2}); err != nil {
		t.Fatalf("defragrange explicit: %v", err)
	}
}

// ---- btrfs_send_linux.go ----

func TestSendPipeError(t *testing.T) {
	defer snapshotSeams()()
	unixPipe2 = func([]int, int) error { return errInjected }
	if err := Send(0, goio.Discard, SendOpts{}); err == nil {
		t.Fatal("want pipe2 error")
	}
}

func TestSendIoctlError(t *testing.T) {
	defer snapshotSeams()()
	// Real pipe; ioctl reports an errno; close write end so reader gets EOF.
	installIoctl(ioctlErrno(unix.EPERM))
	if err := Send(0, goio.Discard, SendOpts{NoData: true, CloneSources: []uint64{1, 2}}); err == nil {
		t.Fatal("want ioctl error")
	}
}

func TestSendSuccess(t *testing.T) {
	defer snapshotSeams()()
	// ioctl "writes" the stream by pushing bytes into the pipe write end, then
	// succeeds. We model that by capturing the write fd from pipe2 and writing
	// to it inside the ioctl hook.
	var wfd int
	realPipe := unixPipe2
	unixPipe2 = func(fds []int, flags int) error {
		err := realPipe(fds, flags)
		wfd = fds[1]
		return err
	}
	payload := []byte("hello-send-stream")
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		_, _ = unix.Write(wfd, payload)
		return 0
	})
	var buf bytes.Buffer
	if err := Send(0, &buf, SendOpts{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), payload) {
		t.Fatalf("got %q want %q", buf.Bytes(), payload)
	}
}

// TestSendCopyError makes io.Copy fail by giving a writer that errors, so the
// copyErr branch is taken while the ioctl succeeds.
func TestSendCopyError(t *testing.T) {
	defer snapshotSeams()()
	var wfd int
	realPipe := unixPipe2
	unixPipe2 = func(fds []int, flags int) error {
		err := realPipe(fds, flags)
		wfd = fds[1]
		return err
	}
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		_, _ = unix.Write(wfd, []byte("data"))
		return 0
	})
	if err := Send(0, errWriter{}, SendOpts{}); err == nil {
		t.Fatal("want copy error")
	}
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errInjected }

func TestFdReader(t *testing.T) {
	defer snapshotSeams()()

	// EINTR then a real read.
	calls := 0
	unixRead = func(fd int, p []byte) (int, error) {
		calls++
		if calls == 1 {
			return 0, unix.EINTR
		}
		copy(p, "z")
		return 1, nil
	}
	r := newFdReader(5)
	buf := make([]byte, 4)
	n, err := r.Read(buf)
	if err != nil || n != 1 || buf[0] != 'z' {
		t.Fatalf("n=%d err=%v buf=%q", n, err, buf)
	}

	// n==0, err==nil -> EOF.
	unixRead = func(int, []byte) (int, error) { return 0, nil }
	if _, err := r.Read(buf); err != goio.EOF {
		t.Fatalf("want EOF, got %v", err)
	}

	// genuine error.
	unixRead = func(int, []byte) (int, error) { return 0, errInjected }
	if _, err := r.Read(buf); err != errInjected {
		t.Fatalf("want injected, got %v", err)
	}

	// Close routes through unixClose.
	closed := false
	unixClose = func(int) error { closed = true; return nil }
	if err := r.Close(); err != nil || !closed {
		t.Fatalf("close err=%v closed=%v", err, closed)
	}
}

func TestSetReceivedSubvol(t *testing.T) {
	defer snapshotSeams()()
	installIoctl(ioctlErrno(unix.EPERM))
	if _, err := SetReceivedSubvol(3, [16]byte{}, 1, SetReceivedTimes{}); err == nil {
		t.Fatal("want ioctl error")
	}
	installIoctl(func(req uintptr, arg unsafe.Pointer) unix.Errno {
		a := (*btrfsIoctlReceivedSubvolArgs)(arg)
		a.Rtransid = 77
		a.Rtime = btrfsIoctlTimespec{Sec: 5, Nsec: 6}
		return 0
	})
	res, err := SetReceivedSubvol(3, [16]byte{0xaa}, 1, SetReceivedTimes{
		Stime: ReceivedTimespec{Sec: 1, Nsec: 2},
		Rtime: ReceivedTimespec{Sec: 3, Nsec: 4},
	})
	if err != nil || res.Rtransid != 77 || res.Rtime.Sec != 5 || res.Rtime.Nsec != 6 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}
