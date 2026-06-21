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
