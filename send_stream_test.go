// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

package btrfs

import (
	"bytes"
	"encoding/binary"
	"errors"
	goio "io"
	"testing"
)

// errReader returns errBoom after handing back prefix (if any).
type errReader struct {
	prefix []byte
	off    int
}

func (r *errReader) Read(p []byte) (int, error) {
	if r.off < len(r.prefix) {
		n := copy(p, r.prefix[r.off:])
		r.off += n
		return n, nil
	}
	return 0, errBoom
}

var errBoom = errors.New("boom")

// buildStream synthesises a minimal but well-formed send stream: the 17-byte
// header followed by the given command records and a terminating END command.
// Each record is (len u32, cmd u16, crc u32) + payload. The CRC is not
// validated by the parser, so we leave it zero.
func buildStream(version uint32, cmds []Command) []byte {
	var b bytes.Buffer
	// Header: magic[13] + version u32.
	magic := make([]byte, btrfsSendStreamMagicSize)
	copy(magic, btrfsSendStreamMagic)
	b.Write(magic)
	var v [4]byte
	binary.LittleEndian.PutUint32(v[:], version)
	b.Write(v[:])
	// Command records.
	writeCmd := func(c Command) {
		var hdr [btrfsCmdHeaderSize]byte
		binary.LittleEndian.PutUint32(hdr[0:4], uint32(len(c.Data)))
		binary.LittleEndian.PutUint16(hdr[4:6], c.Cmd)
		binary.LittleEndian.PutUint32(hdr[6:10], c.CRC)
		b.Write(hdr[:])
		b.Write(c.Data)
	}
	for _, c := range cmds {
		writeCmd(c)
	}
	writeCmd(Command{Cmd: btrfsSendCmdEnd})
	return b.Bytes()
}

func TestParseHeaderOK(t *testing.T) {
	stream := buildStream(btrfsSendStreamVersion, nil)
	h, err := ParseHeader(bytes.NewReader(stream))
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	if h.Magic != btrfsSendStreamMagic {
		t.Errorf("magic = %q, want %q", h.Magic, btrfsSendStreamMagic)
	}
	if h.Version != btrfsSendStreamVersion {
		t.Errorf("version = %d, want %d", h.Version, btrfsSendStreamVersion)
	}
}

func TestParseHeaderBadMagic(t *testing.T) {
	bad := make([]byte, btrfsStreamHeaderSize)
	copy(bad, "not-a-stream")
	_, err := ParseHeader(bytes.NewReader(bad))
	if !errors.Is(err, ErrBadMagic) {
		t.Errorf("err = %v, want ErrBadMagic", err)
	}
}

func TestParseHeaderShort(t *testing.T) {
	_, err := ParseHeader(bytes.NewReader([]byte("btrfs")))
	if !errors.Is(err, ErrShortStream) {
		t.Errorf("err = %v, want ErrShortStream", err)
	}
}

func TestCommandReaderSkipsData(t *testing.T) {
	stream := buildStream(1, []Command{
		{Cmd: btrfsSendCmdSubvol, Data: []byte("subvol-attrs")},
		{Cmd: btrfsSendCmdSnapshot, Data: bytes.Repeat([]byte{0xab}, 64)},
	})
	r := bytes.NewReader(stream)
	if _, err := ParseHeader(r); err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	cr := NewCommandReader(r)
	var cmds []uint16
	for {
		c, err := cr.Next()
		if err == goio.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if c.Data != nil {
			t.Errorf("cmd %d retained data without WithData", c.Cmd)
		}
		cmds = append(cmds, c.Cmd)
	}
	want := []uint16{btrfsSendCmdSubvol, btrfsSendCmdSnapshot, btrfsSendCmdEnd}
	if len(cmds) != len(want) {
		t.Fatalf("got %v cmds, want %v", cmds, want)
	}
	for i := range want {
		if cmds[i] != want[i] {
			t.Errorf("cmd[%d] = %d, want %d", i, cmds[i], want[i])
		}
	}
}

func TestCommandReaderWithData(t *testing.T) {
	payload := []byte("hello-payload")
	stream := buildStream(1, []Command{{Cmd: btrfsSendCmdSubvol, Data: payload}})
	r := bytes.NewReader(stream)
	if _, err := ParseHeader(r); err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	cr := NewCommandReader(r).WithData()
	c, err := cr.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !bytes.Equal(c.Data, payload) {
		t.Errorf("Data = %q, want %q", c.Data, payload)
	}
	if c.Len != uint32(len(payload)) {
		t.Errorf("Len = %d, want %d", c.Len, len(payload))
	}
}

func TestVerifyStream(t *testing.T) {
	stream := buildStream(1, []Command{
		{Cmd: btrfsSendCmdSubvol, Data: []byte("a")},
		{Cmd: 15, Data: bytes.Repeat([]byte{1}, 100)},
		{Cmd: 16, Data: nil},
	})
	h, n, err := VerifyStream(bytes.NewReader(stream))
	if err != nil {
		t.Fatalf("VerifyStream: %v", err)
	}
	if h.Version != 1 {
		t.Errorf("version = %d, want 1", h.Version)
	}
	// 3 records + END.
	if n != 4 {
		t.Errorf("record count = %d, want 4", n)
	}
}

func TestVerifyStreamTruncated(t *testing.T) {
	stream := buildStream(1, []Command{{Cmd: btrfsSendCmdSubvol, Data: bytes.Repeat([]byte{1}, 50)}})
	// Cut off in the middle of the first record's payload.
	cut := stream[:btrfsStreamHeaderSize+btrfsCmdHeaderSize+10]
	_, _, err := VerifyStream(bytes.NewReader(cut))
	if !errors.Is(err, ErrShortStream) {
		t.Errorf("err = %v, want ErrShortStream", err)
	}
}

// TestParseHeaderGenericError covers the non-EOF read-error branch of
// ParseHeader (a reader that fails partway with a custom error, not EOF).
func TestParseHeaderGenericError(t *testing.T) {
	r := &errReader{prefix: []byte("btrfs")} // 5 bytes then errBoom (not EOF)
	_, err := ParseHeader(r)
	if err == nil || errors.Is(err, ErrShortStream) {
		t.Fatalf("want generic wrapped error, got %v", err)
	}
	if !errors.Is(err, errBoom) {
		t.Fatalf("want errBoom, got %v", err)
	}
}

// TestCommandReaderCleanEOF covers Next's clean-end-of-stream branch: after the
// last full record the reader hits EOF with n==0, so Next returns io.EOF (not
// ErrShortStream). buildStream always appends END, so we feed a stream with no
// END at all and stop right at a record boundary.
func TestCommandReaderCleanEOF(t *testing.T) {
	// Header + one complete record, then EOF exactly at the next record's start.
	var b bytes.Buffer
	magic := make([]byte, btrfsSendStreamMagicSize)
	copy(magic, btrfsSendStreamMagic)
	b.Write(magic)
	var v [4]byte
	binary.LittleEndian.PutUint32(v[:], 1)
	b.Write(v[:])
	var hdr [btrfsCmdHeaderSize]byte
	binary.LittleEndian.PutUint16(hdr[4:6], 7) // some non-END command id
	b.Write(hdr[:])                            // zero-length payload

	r := bytes.NewReader(b.Bytes())
	if _, err := ParseHeader(r); err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	cr := NewCommandReader(r)
	if _, err := cr.Next(); err != nil {
		t.Fatalf("first Next: %v", err)
	}
	if _, err := cr.Next(); err != goio.EOF {
		t.Fatalf("want clean EOF, got %v", err)
	}
	// Subsequent Next still reports EOF (done flag).
	if _, err := cr.Next(); err != goio.EOF {
		t.Fatalf("want EOF after done, got %v", err)
	}
}

// TestCommandReaderPartialHeader covers Next's truncated-header branch: a
// partial command header (not a clean boundary) yields ErrShortStream.
func TestCommandReaderPartialHeader(t *testing.T) {
	stream := buildStream(1, nil) // header + END
	// Keep the header plus 3 bytes of the END record's 10-byte command header.
	cut := stream[:btrfsStreamHeaderSize+3]
	r := bytes.NewReader(cut)
	if _, err := ParseHeader(r); err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	cr := NewCommandReader(r)
	if _, err := cr.Next(); !errors.Is(err, ErrShortStream) {
		t.Fatalf("want ErrShortStream, got %v", err)
	}
}

// TestCommandReaderWithDataTruncated covers the WithData ReadFull error branch:
// a record whose declared length exceeds the available payload.
func TestCommandReaderWithDataTruncated(t *testing.T) {
	var b bytes.Buffer
	magic := make([]byte, btrfsSendStreamMagicSize)
	copy(magic, btrfsSendStreamMagic)
	b.Write(magic)
	var v [4]byte
	binary.LittleEndian.PutUint32(v[:], 1)
	b.Write(v[:])
	var hdr [btrfsCmdHeaderSize]byte
	binary.LittleEndian.PutUint32(hdr[0:4], 100) // claims 100-byte payload
	binary.LittleEndian.PutUint16(hdr[4:6], btrfsSendCmdSubvol)
	b.Write(hdr[:])
	b.Write([]byte("only-a-few-bytes")) // far short of 100

	r := bytes.NewReader(b.Bytes())
	if _, err := ParseHeader(r); err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	cr := NewCommandReader(r).WithData()
	if _, err := cr.Next(); !errors.Is(err, ErrShortStream) {
		t.Fatalf("want ErrShortStream, got %v", err)
	}
}

// TestTrimNULNoNUL covers trimNUL's no-NUL branch (returns the whole slice).
func TestTrimNULNoNUL(t *testing.T) {
	if got := trimNUL([]byte("abcd")); got != "abcd" {
		t.Fatalf("got %q", got)
	}
}

// TestVerifyStreamHeaderError covers VerifyStream's ParseHeader-error
// propagation.
func TestVerifyStreamHeaderError(t *testing.T) {
	bad := make([]byte, btrfsStreamHeaderSize)
	copy(bad, "not-a-stream")
	if _, _, err := VerifyStream(bytes.NewReader(bad)); !errors.Is(err, ErrBadMagic) {
		t.Fatalf("want ErrBadMagic, got %v", err)
	}
}

// TestVerifyStreamNextError covers VerifyStream's non-EOF cr.Next error branch:
// a truncated command header after a valid stream header.
func TestVerifyStreamNextError(t *testing.T) {
	stream := buildStream(1, nil)
	cut := stream[:btrfsStreamHeaderSize+3] // partial END command header
	if _, _, err := VerifyStream(bytes.NewReader(cut)); !errors.Is(err, ErrShortStream) {
		t.Fatalf("want ErrShortStream, got %v", err)
	}
}

// TestVerifyStreamCleanEOF covers VerifyStream's clean-EOF exit (the loop sees
// io.EOF without ever reaching an END command, e.g. an OMIT_END_CMD stream).
func TestVerifyStreamCleanEOF(t *testing.T) {
	// Header + one zero-length non-END record, then EOF at a clean boundary.
	var b bytes.Buffer
	magic := make([]byte, btrfsSendStreamMagicSize)
	copy(magic, btrfsSendStreamMagic)
	b.Write(magic)
	var v [4]byte
	binary.LittleEndian.PutUint32(v[:], 1)
	b.Write(v[:])
	var hdr [btrfsCmdHeaderSize]byte
	binary.LittleEndian.PutUint16(hdr[4:6], 7) // non-END command
	b.Write(hdr[:])

	_, n, err := VerifyStream(bytes.NewReader(b.Bytes()))
	if err != nil {
		t.Fatalf("VerifyStream: %v", err)
	}
	if n != 1 {
		t.Fatalf("record count = %d, want 1", n)
	}
}
