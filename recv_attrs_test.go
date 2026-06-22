// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

package btrfs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

// tlv encodes a single send-stream TLV attribute: __le16 type, __le16 len,
// value. Used to synthesise command payloads for the decoder tests.
func tlv(typ uint16, val []byte) []byte {
	b := make([]byte, tlvHeaderSize+len(val))
	binary.LittleEndian.PutUint16(b[0:2], typ)
	binary.LittleEndian.PutUint16(b[2:4], uint16(len(val)))
	copy(b[tlvHeaderSize:], val)
	return b
}

func u64le(v uint64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, v)
	return b
}

func tsle(sec uint64, nsec uint32) []byte {
	b := make([]byte, sendTimespecSize)
	binary.LittleEndian.PutUint64(b[0:8], sec)
	binary.LittleEndian.PutUint32(b[8:12], nsec)
	return b
}

func concat(parts ...[]byte) []byte {
	var b bytes.Buffer
	for _, p := range parts {
		b.Write(p)
	}
	return b.Bytes()
}

// TestDecodeAttrsMkfile decodes a MKFILE-shaped payload (PATH + INO) the way the
// receiver does, checking the string and integer attributes round-trip.
func TestDecodeAttrsMkfile(t *testing.T) {
	payload := concat(
		tlv(sendAPath, []byte("o257-30-0")),
		tlv(sendAIno, u64le(257)),
	)
	a, err := decodeAttrs(payload)
	if err != nil {
		t.Fatalf("decodeAttrs: %v", err)
	}
	if a.path != "o257-30-0" {
		t.Errorf("path = %q, want %q", a.path, "o257-30-0")
	}
	if a.ino != 257 {
		t.Errorf("ino = %d, want 257", a.ino)
	}
	if !a.has(sendAPath) || !a.has(sendAIno) {
		t.Errorf("expected PATH and INO present")
	}
	if a.has(sendAMode) {
		t.Errorf("MODE should be absent")
	}
}

// TestDecodeAttrsRename checks the two-path attributes (RENAME: PATH + PATH_TO).
func TestDecodeAttrsRename(t *testing.T) {
	payload := concat(
		tlv(sendAPath, []byte("o257-30-0")),
		tlv(sendAPathTo, []byte("dir/final.txt")),
	)
	a, err := decodeAttrs(payload)
	if err != nil {
		t.Fatalf("decodeAttrs: %v", err)
	}
	if a.path != "o257-30-0" || a.pathTo != "dir/final.txt" {
		t.Errorf("path=%q pathTo=%q, want o257-30-0 / dir/final.txt", a.path, a.pathTo)
	}
}

// TestDecodeAttrsWrite checks WRITE: PATH + FILE_OFFSET + DATA, including that
// the data sub-slice matches and an explicit zero offset is recorded present.
func TestDecodeAttrsWrite(t *testing.T) {
	data := bytes.Repeat([]byte("go-fsctl "), 1000) // > 8 KiB
	payload := concat(
		tlv(sendAPath, []byte("file.bin")),
		tlv(sendAFileOffset, u64le(0)),
		tlv(sendAData, data),
	)
	a, err := decodeAttrs(payload)
	if err != nil {
		t.Fatalf("decodeAttrs: %v", err)
	}
	if a.fileOffset != 0 || !a.has(sendAFileOffset) {
		t.Errorf("fileOffset = %d present=%v, want 0/true", a.fileOffset, a.has(sendAFileOffset))
	}
	if !bytes.Equal(a.data, data) {
		t.Errorf("data mismatch: got %d bytes want %d", len(a.data), len(data))
	}
}

// TestDecodeAttrsChownChmod checks the metadata integer attributes.
func TestDecodeAttrsChownChmod(t *testing.T) {
	payload := concat(
		tlv(sendAPath, []byte("f")),
		tlv(sendAMode, u64le(0o644)),
		tlv(sendAUID, u64le(1000)),
		tlv(sendAGID, u64le(1001)),
	)
	a, err := decodeAttrs(payload)
	if err != nil {
		t.Fatalf("decodeAttrs: %v", err)
	}
	if a.mode != 0o644 || a.uid != 1000 || a.gid != 1001 {
		t.Errorf("mode=%#o uid=%d gid=%d, want 0644/1000/1001", a.mode, a.uid, a.gid)
	}
}

// TestDecodeAttrsUtimes checks the packed 12-byte timespec attributes.
func TestDecodeAttrsUtimes(t *testing.T) {
	payload := concat(
		tlv(sendAPath, []byte("f")),
		tlv(sendAAtime, tsle(1700000000, 123)),
		tlv(sendAMtime, tsle(1700000001, 456)),
		tlv(sendACtime, tsle(1700000002, 789)),
	)
	a, err := decodeAttrs(payload)
	if err != nil {
		t.Fatalf("decodeAttrs: %v", err)
	}
	if a.atime.Sec != 1700000000 || a.atime.Nsec != 123 {
		t.Errorf("atime = %+v, want {1700000000,123}", a.atime)
	}
	if a.mtime.Sec != 1700000001 || a.mtime.Nsec != 456 {
		t.Errorf("mtime = %+v, want {1700000001,456}", a.mtime)
	}
	if a.ctime.Sec != 1700000002 || a.ctime.Nsec != 789 {
		t.Errorf("ctime = %+v, want {1700000002,789}", a.ctime)
	}
}

// TestDecodeAttrsXattr checks XATTR_NAME (string) + XATTR_DATA (bytes).
func TestDecodeAttrsXattr(t *testing.T) {
	payload := concat(
		tlv(sendAPath, []byte("f")),
		tlv(sendAXattrName, []byte("user.comment")),
		tlv(sendAXattrData, []byte("hello-value")),
	)
	a, err := decodeAttrs(payload)
	if err != nil {
		t.Fatalf("decodeAttrs: %v", err)
	}
	if a.xattrName != "user.comment" {
		t.Errorf("xattrName = %q, want user.comment", a.xattrName)
	}
	if string(a.xattrData) != "hello-value" {
		t.Errorf("xattrData = %q, want hello-value", a.xattrData)
	}
}

// TestDecodeAttrsSubvol checks the SUBVOL identity attributes: UUID + CTRANSID,
// the fixed-width 16-byte and 8-byte decodes used to stamp the received subvol.
func TestDecodeAttrsSubvol(t *testing.T) {
	var uuid [16]byte
	for i := range uuid {
		uuid[i] = byte(i + 1)
	}
	payload := concat(
		tlv(sendAPath, []byte("recv_subvol")),
		tlv(sendAUUID, uuid[:]),
		tlv(sendACtransid, u64le(0xdeadbeef)),
		tlv(sendAOtime, tsle(1600000000, 0)),
	)
	a, err := decodeAttrs(payload)
	if err != nil {
		t.Fatalf("decodeAttrs: %v", err)
	}
	if a.uuid != uuid {
		t.Errorf("uuid = %x, want %x", a.uuid, uuid)
	}
	if a.ctransid != 0xdeadbeef {
		t.Errorf("ctransid = %#x, want 0xdeadbeef", a.ctransid)
	}
	if a.otime.Sec != 1600000000 {
		t.Errorf("otime.Sec = %d, want 1600000000", a.otime.Sec)
	}
}

// TestDecodeAttrsClone checks the CLONE attribute set: CLONE_UUID/CTRANSID,
// CLONE_PATH, CLONE_OFFSET/LEN plus the destination PATH/FILE_OFFSET.
func TestDecodeAttrsClone(t *testing.T) {
	var cuuid [16]byte
	for i := range cuuid {
		cuuid[i] = byte(0xa0 + i)
	}
	payload := concat(
		tlv(sendAPath, []byte("dst.bin")),
		tlv(sendAFileOffset, u64le(4096)),
		tlv(sendACloneLen, u64le(8192)),
		tlv(sendACloneUUID, cuuid[:]),
		tlv(sendACloneCtransid, u64le(42)),
		tlv(sendAClonePath, []byte("src.bin")),
		tlv(sendACloneOffset, u64le(0)),
	)
	a, err := decodeAttrs(payload)
	if err != nil {
		t.Fatalf("decodeAttrs: %v", err)
	}
	if a.fileOffset != 4096 || a.cloneLen != 8192 || a.cloneOffset != 0 {
		t.Errorf("offsets: file=%d len=%d cloneOff=%d, want 4096/8192/0", a.fileOffset, a.cloneLen, a.cloneOffset)
	}
	if a.cloneUUID != cuuid || a.cloneCtransid != 42 || a.clonePath != "src.bin" {
		t.Errorf("clone src: uuid=%x ctransid=%d path=%q", a.cloneUUID, a.cloneCtransid, a.clonePath)
	}
}

// TestDecodeAttrsTruncated rejects a TLV whose declared length runs off the end.
func TestDecodeAttrsTruncated(t *testing.T) {
	good := tlv(sendAPath, []byte("hello"))
	// Chop the last 2 bytes of the value: header still claims 5.
	bad := good[:len(good)-2]
	if _, err := decodeAttrs(bad); !errors.Is(err, ErrBadTLV) {
		t.Errorf("err = %v, want ErrBadTLV", err)
	}
	// A header that itself runs past the end.
	if _, err := decodeAttrs([]byte{0x01, 0x00}); !errors.Is(err, ErrBadTLV) {
		t.Errorf("short header err = %v, want ErrBadTLV", err)
	}
}

// TestDecodeAttrsBadWidth rejects a fixed-width integer/UUID of the wrong size.
func TestDecodeAttrsBadWidth(t *testing.T) {
	// INO with 4 bytes instead of 8.
	if _, err := decodeAttrs(tlv(sendAIno, []byte{1, 2, 3, 4})); !errors.Is(err, ErrBadTLV) {
		t.Errorf("bad int width err = %v, want ErrBadTLV", err)
	}
	// UUID with 8 bytes instead of 16.
	if _, err := decodeAttrs(tlv(sendAUUID, make([]byte, 8))); !errors.Is(err, ErrBadTLV) {
		t.Errorf("bad uuid width err = %v, want ErrBadTLV", err)
	}
	// Timespec with 8 bytes instead of 12.
	if _, err := decodeAttrs(tlv(sendAMtime, make([]byte, 8))); !errors.Is(err, ErrBadTLV) {
		t.Errorf("bad timespec width err = %v, want ErrBadTLV", err)
	}
}

// TestDecodeAttrsEmpty: an empty payload (e.g. a bare command) decodes to no
// attributes present, no error.
func TestDecodeAttrsEmpty(t *testing.T) {
	a, err := decodeAttrs(nil)
	if err != nil {
		t.Fatalf("decodeAttrs(nil): %v", err)
	}
	if a.has(sendAPath) {
		t.Errorf("empty payload should have no attributes")
	}
}

// TestTmpName pins the orphan/temp-name scheme the sender uses for a
// freshly-created inode (o<ino>-<gen>-0), which the receiver reproduces when it
// resolves intermediate paths.
func TestTmpName(t *testing.T) {
	for _, c := range []struct {
		ino, gen uint64
		want     string
	}{
		{257, 30, "o257-30-0"},
		{1, 1, "o1-1-0"},
		{1048576, 9999, "o1048576-9999-0"},
	} {
		if got := tmpName(c.ino, c.gen); got != c.want {
			t.Errorf("tmpName(%d,%d) = %q, want %q", c.ino, c.gen, got, c.want)
		}
	}
}
