// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

package btrfs

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// This file decodes the TLV attribute payload carried by each send-stream
// command record, and derives the o<ino>-<gen> temporary name scheme that
// `btrfs receive` uses when materialising a freshly-created inode. It is pure
// byte handling with no kernel calls, so it builds and is unit-tested on every
// platform (the Receive replay that consumes it is Linux-only).
//
// Each attribute is laid out on the wire as:
//
//	__le16 type; __le16 len; u8 data[len];
//
// (struct btrfs_tlv_header in fs/btrfs/send.h, followed by the value). A command
// record's payload is a concatenation of these TLVs.

const tlvHeaderSize = 4 // __le16 type + __le16 len

// ErrBadTLV is returned when an attribute payload is malformed: a TLV header or
// value runs past the end of the record, or a fixed-width attribute (an integer
// or a UUID) has the wrong length.
var ErrBadTLV = errors.New("btrfs: malformed send-stream TLV attribute")

// sendAttrs holds the decoded attributes of one command record. Only the
// attributes present in the record are set; absent ones keep their zero value
// and their "present" bool stays false. Receive reads the fields relevant to
// each command from here.
type sendAttrs struct {
	path     string // BTRFS_SEND_A_PATH
	pathTo   string // BTRFS_SEND_A_PATH_TO (rename destination)
	pathLink string // BTRFS_SEND_A_PATH_LINK (symlink/link target)

	clonePath string // BTRFS_SEND_A_CLONE_PATH

	ino  uint64 // BTRFS_SEND_A_INO
	size uint64 // BTRFS_SEND_A_SIZE
	mode uint64 // BTRFS_SEND_A_MODE
	uid  uint64 // BTRFS_SEND_A_UID
	gid  uint64 // BTRFS_SEND_A_GID
	rdev uint64 // BTRFS_SEND_A_RDEV

	fileOffset uint64 // BTRFS_SEND_A_FILE_OFFSET
	data       []byte // BTRFS_SEND_A_DATA (sub-slice of the record payload)

	xattrName string // BTRFS_SEND_A_XATTR_NAME
	xattrData []byte // BTRFS_SEND_A_XATTR_DATA

	uuid     [16]byte // BTRFS_SEND_A_UUID (subvol/snapshot)
	ctransid uint64   // BTRFS_SEND_A_CTRANSID

	cloneUUID     [16]byte // BTRFS_SEND_A_CLONE_UUID
	cloneCtransid uint64   // BTRFS_SEND_A_CLONE_CTRANSID
	cloneOffset   uint64   // BTRFS_SEND_A_CLONE_OFFSET
	cloneLen      uint64   // BTRFS_SEND_A_CLONE_LEN

	atime sendTimespec // BTRFS_SEND_A_ATIME
	mtime sendTimespec // BTRFS_SEND_A_MTIME
	ctime sendTimespec // BTRFS_SEND_A_CTIME
	otime sendTimespec // BTRFS_SEND_A_OTIME

	// present records which attribute types were seen, so callers can tell an
	// explicit zero (e.g. mode 0) from an absent attribute.
	present map[uint16]bool
}

func (a *sendAttrs) has(typ uint16) bool { return a.present != nil && a.present[typ] }

// sendTimespec is an on-stream timestamp: __le64 sec + __le32 nsec (12 bytes,
// struct btrfs_timespec in the stream — note this is NOT the 16-byte
// btrfs_ioctl_timespec; the wire form is packed to 12 bytes).
type sendTimespec struct {
	Sec  uint64
	Nsec uint32
}

const sendTimespecSize = 12

// decodeAttrs walks the TLV attributes in a command record's payload and
// returns them decoded. It validates that every TLV stays within the payload
// and that fixed-width attributes have the expected size. Byte-valued
// attributes (data/xattr data) are returned as sub-slices of payload, so the
// caller must not retain them past the lifetime of payload.
func decodeAttrs(payload []byte) (sendAttrs, error) {
	a := sendAttrs{present: make(map[uint16]bool)}
	for off := 0; off < len(payload); {
		if off+tlvHeaderSize > len(payload) {
			return a, fmt.Errorf("%w: TLV header at %d runs past end (%d)", ErrBadTLV, off, len(payload))
		}
		typ := binary.LittleEndian.Uint16(payload[off : off+2])
		l := int(binary.LittleEndian.Uint16(payload[off+2 : off+4]))
		off += tlvHeaderSize
		if off+l > len(payload) {
			return a, fmt.Errorf("%w: attr %d len %d at %d runs past end (%d)", ErrBadTLV, typ, l, off, len(payload))
		}
		val := payload[off : off+l]
		off += l
		a.present[typ] = true

		switch typ {
		case sendAPath:
			a.path = string(val)
		case sendAPathTo:
			a.pathTo = string(val)
		case sendAPathLink:
			a.pathLink = string(val)
		case sendAClonePath:
			a.clonePath = string(val)
		case sendAXattrName:
			a.xattrName = string(val)
		case sendAXattrData:
			a.xattrData = val
		case sendAData:
			a.data = val
		case sendAUUID:
			u, err := asUUID(val)
			if err != nil {
				return a, fmt.Errorf("UUID: %w", err)
			}
			a.uuid = u
		case sendACloneUUID:
			u, err := asUUID(val)
			if err != nil {
				return a, fmt.Errorf("CLONE_UUID: %w", err)
			}
			a.cloneUUID = u
		case sendAIno, sendASize, sendAMode, sendAUID, sendAGID, sendARdev,
			sendAFileOffset, sendACtransid, sendACloneCtransid, sendACloneOffset, sendACloneLen:
			n, err := asU64(val)
			if err != nil {
				return a, fmt.Errorf("attr %d: %w", typ, err)
			}
			switch typ {
			case sendAIno:
				a.ino = n
			case sendASize:
				a.size = n
			case sendAMode:
				a.mode = n
			case sendAUID:
				a.uid = n
			case sendAGID:
				a.gid = n
			case sendARdev:
				a.rdev = n
			case sendAFileOffset:
				a.fileOffset = n
			case sendACtransid:
				a.ctransid = n
			case sendACloneCtransid:
				a.cloneCtransid = n
			case sendACloneOffset:
				a.cloneOffset = n
			case sendACloneLen:
				a.cloneLen = n
			}
		case sendAAtime, sendAMtime, sendACtime, sendAOtime:
			ts, err := asTimespec(val)
			if err != nil {
				return a, fmt.Errorf("attr %d: %w", typ, err)
			}
			switch typ {
			case sendAAtime:
				a.atime = ts
			case sendAMtime:
				a.mtime = ts
			case sendACtime:
				a.ctime = ts
			case sendAOtime:
				a.otime = ts
			}
		default:
			// Unknown / unneeded attribute: skip it, already advanced.
		}
	}
	return a, nil
}

func asU64(val []byte) (uint64, error) {
	if len(val) != 8 {
		return 0, fmt.Errorf("%w: expected 8-byte integer, got %d", ErrBadTLV, len(val))
	}
	return binary.LittleEndian.Uint64(val), nil
}

func asUUID(val []byte) ([16]byte, error) {
	var u [16]byte
	if len(val) != 16 {
		return u, fmt.Errorf("%w: expected 16-byte UUID, got %d", ErrBadTLV, len(val))
	}
	copy(u[:], val)
	return u, nil
}

func asTimespec(val []byte) (sendTimespec, error) {
	if len(val) != sendTimespecSize {
		return sendTimespec{}, fmt.Errorf("%w: expected %d-byte timespec, got %d", ErrBadTLV, sendTimespecSize, len(val))
	}
	return sendTimespec{
		Sec:  binary.LittleEndian.Uint64(val[0:8]),
		Nsec: binary.LittleEndian.Uint32(val[8:12]),
	}, nil
}

// tmpName returns the temporary name `btrfs receive` gives a freshly-created
// inode before renaming it into place: "o<ino>-<gen>-0". The receiver creates
// every inode under this name first (so the create order is independent of the
// final directory tree, which may not exist yet) and a later RENAME moves it to
// its real path. We mirror the exact scheme so that intermediate paths the
// stream references (e.g. a CLONE source, or a child created before its parent
// is renamed) resolve identically to the real tool.
//
// The kernel's send code (get_cur_path / gen_unique_name) uses
// "o%llu-%llu-%llu" with the third field a per-name disambiguation counter that
// is 0 for the common case; receive reconstructs the same name from ino and the
// send "gen" (carried as the stream's clone/ctransid generation). We use the
// two-field-plus-zero form that btrfs-progs' process_snapshot/finish path emits.
func tmpName(ino, gen uint64) string {
	return fmt.Sprintf("o%d-%d-0", ino, gen)
}
