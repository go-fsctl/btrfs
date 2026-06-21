// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

package btrfs

import (
	"encoding/binary"
	"errors"
	"fmt"
	goio "io"
)

// This file parses the btrfs send-stream wire format produced by Send /
// BTRFS_IOC_SEND. It is pure byte handling with no kernel calls, so it builds
// and is tested on every platform. Callers use it to sanity-check a stream
// (magic + version) and to iterate the TLV command records for introspection.
//
// Wire format (fs/btrfs/send.h):
//
//	struct btrfs_stream_header { char magic[13]; __le32 version; } __packed;  // 17 bytes
//	struct btrfs_cmd_header    { __le32 len; __le16 cmd; __le32 crc; } __packed; // 10 bytes
//	... followed by `len` bytes of TLV attribute data ...
//
// All multi-byte integers on the stream are little-endian.

// ErrBadMagic is returned when a stream does not begin with the btrfs send
// magic. ErrShortStream is returned when the stream ends mid-record.
var (
	ErrBadMagic    = errors.New("btrfs: not a btrfs send stream (bad magic)")
	ErrShortStream = errors.New("btrfs: truncated send stream")
)

// Header is the decoded btrfs_stream_header at the front of a send stream.
type Header struct {
	Magic   string // the magic string, trailing NUL trimmed ("btrfs-stream")
	Version uint32 // stream version (1 for a default kernel send)
}

// ParseHeader reads and validates the 17-byte stream header from r. It returns
// the decoded Header and an error if the magic does not match the btrfs send
// magic or the stream is too short. On success r is positioned at the first
// command header, so ParseHeader composes with NewCommandReader on the same r.
func ParseHeader(r goio.Reader) (Header, error) {
	buf := make([]byte, btrfsStreamHeaderSize)
	if _, err := goio.ReadFull(r, buf); err != nil {
		if err == goio.EOF || err == goio.ErrUnexpectedEOF {
			return Header{}, ErrShortStream
		}
		return Header{}, fmt.Errorf("ParseHeader: %w", err)
	}
	magic := trimNUL(buf[:btrfsSendStreamMagicSize])
	if magic != btrfsSendStreamMagic {
		return Header{}, fmt.Errorf("%w: got %q want %q", ErrBadMagic, magic, btrfsSendStreamMagic)
	}
	return Header{
		Magic:   magic,
		Version: binary.LittleEndian.Uint32(buf[btrfsSendStreamMagicSize:]),
	}, nil
}

// Command is one TLV record from a send stream: its numeric command id, the
// declared payload length, the recorded CRC32C, and (optionally) the raw
// attribute payload. The payload is only populated when reading with
// CommandReader.WithData; otherwise Data is nil and the reader skips over it.
type Command struct {
	Cmd  uint16 // btrfs_send_cmd id (e.g. BTRFS_SEND_C_SUBVOL=1, C_END=20)
	Len  uint32 // declared payload length in bytes
	CRC  uint32 // recorded crc32c of the record (header crc field zeroed)
	Data []byte // raw TLV payload, nil unless reading with data
}

// IsEnd reports whether this is the stream-terminating END command.
func (c Command) IsEnd() bool { return c.Cmd == btrfsSendCmdEnd }

// CommandReader iterates the TLV command records following a stream header.
// Construct it with NewCommandReader after ParseHeader has consumed the header.
type CommandReader struct {
	r        goio.Reader
	withData bool
	done     bool
}

// NewCommandReader returns a CommandReader over r, which must be positioned
// immediately after the 17-byte stream header (e.g. right after ParseHeader).
// By default Next skips each record's payload and leaves Command.Data nil; call
// WithData to retain payloads.
func NewCommandReader(r goio.Reader) *CommandReader { return &CommandReader{r: r} }

// WithData makes subsequent Next calls read and retain each record's payload in
// Command.Data instead of skipping it. It returns the receiver for chaining.
func (cr *CommandReader) WithData() *CommandReader { cr.withData = true; return cr }

// Next reads the next command record. It returns io.EOF after the END command
// (or at a clean end of stream), ErrShortStream if the stream is truncated, and
// the decoded Command otherwise. After the END command Next reports io.EOF on
// every subsequent call.
func (cr *CommandReader) Next() (Command, error) {
	if cr.done {
		return Command{}, goio.EOF
	}
	hdr := make([]byte, btrfsCmdHeaderSize)
	n, err := goio.ReadFull(cr.r, hdr)
	if err != nil {
		if err == goio.EOF && n == 0 {
			// Clean end of stream with no END command (e.g. OMIT_END_CMD).
			cr.done = true
			return Command{}, goio.EOF
		}
		return Command{}, ErrShortStream
	}
	c := Command{
		Len: binary.LittleEndian.Uint32(hdr[0:4]),
		Cmd: binary.LittleEndian.Uint16(hdr[4:6]),
		CRC: binary.LittleEndian.Uint32(hdr[6:10]),
	}
	if c.Len > 0 {
		if cr.withData {
			c.Data = make([]byte, c.Len)
			if _, err := goio.ReadFull(cr.r, c.Data); err != nil {
				return Command{}, ErrShortStream
			}
		} else {
			if _, err := goio.CopyN(goio.Discard, cr.r, int64(c.Len)); err != nil {
				return Command{}, ErrShortStream
			}
		}
	}
	if c.IsEnd() {
		cr.done = true
	}
	return c, nil
}

// trimNUL returns the bytes before the first NUL as a string (the on-stream
// magic field is NUL-padded). Cross-platform copy of cstr, which is Linux-only.
func trimNUL(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// VerifyStream reads an entire send stream from r, validating the header and
// walking every command record to confirm the framing is internally consistent
// (each record's length stays within the stream and the stream terminates
// cleanly). It returns the parsed Header and the number of command records
// (including the terminating END). It does not check CRCs or replay the stream.
func VerifyStream(r goio.Reader) (Header, int, error) {
	h, err := ParseHeader(r)
	if err != nil {
		return Header{}, 0, err
	}
	cr := NewCommandReader(r)
	count := 0
	for {
		c, err := cr.Next()
		if err == goio.EOF {
			return h, count, nil
		}
		if err != nil {
			return h, count, err
		}
		count++
		if c.IsEnd() {
			return h, count, nil
		}
	}
}
