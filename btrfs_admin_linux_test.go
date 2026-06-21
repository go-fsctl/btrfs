// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

//go:build linux

package btrfs

import "testing"

// TestAdvanceKey checks the search-range advancement used by the root-tree
// walk: it must move strictly past the last item and terminate at the top of
// the 136-bit key space. (advanceKey lives in the linux-only implementation
// file, so this test is linux-tagged.)
func TestAdvanceKey(t *testing.T) {
	key := btrfsIoctlSearchKey{MinType: btrfsRootRefKey, MaxType: btrfsRootRefKey}
	last := btrfsIoctlSearchHeader{Objectid: 5, Type: btrfsRootRefKey, Offset: 256}
	if !advanceKey(&key, last) {
		t.Fatal("advanceKey returned false mid-range")
	}
	if key.MinObjectid != 5 || key.MinType != btrfsRootRefKey || key.MinOffset != 257 {
		t.Errorf("advanceKey = {obj=%d type=%d off=%d}, want {5,%d,257}",
			key.MinObjectid, key.MinType, key.MinOffset, btrfsRootRefKey)
	}
	// At the very top of the space it must report exhaustion.
	top := btrfsIoctlSearchHeader{Objectid: ^uint64(0), Type: ^uint32(0), Offset: ^uint64(0)}
	if advanceKey(&key, top) {
		t.Error("advanceKey at top of key space returned true, want false")
	}
}
