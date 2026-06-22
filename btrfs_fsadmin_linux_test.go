// SPDX-License-Identifier: BSD-3-Clause
//
// Copyright (c) 2026, go-fsctl

//go:build linux

package btrfs

import "testing"

// TestDecodeFeaturesNames checks the symbolic-name decoding of a feature mask:
// a typical default mkfs.btrfs profile sets MIXED_BACKREF, EXTENDED_IREF,
// SKINNY_METADATA and NO_HOLES (incompat) plus FREE_SPACE_TREE (compat_ro).
// decodeFeatures lives in the linux-only implementation file, so this test is
// linux-tagged.
func TestDecodeFeaturesNames(t *testing.T) {
	ff := btrfsIoctlFeatureFlags{
		CompatRO: FeatureCompatROFreeSpaceTree | FeatureCompatROFreeSpaceTreeValid,
		Incompat: FeatureIncompatExtendedIref | FeatureIncompatSkinnyMetadata | FeatureIncompatNoHoles | FeatureIncompatMixedBackref,
	}
	f := decodeFeatures(&ff)
	want := map[string]bool{
		"COMPAT_RO_FREE_SPACE_TREE":       true,
		"COMPAT_RO_FREE_SPACE_TREE_VALID": true,
		"INCOMPAT_MIXED_BACKREF":          true,
		"INCOMPAT_EXTENDED_IREF":          true,
		"INCOMPAT_SKINNY_METADATA":        true,
		"INCOMPAT_NO_HOLES":               true,
	}
	got := map[string]bool{}
	for _, n := range f.Names {
		got[n] = true
	}
	for n := range want {
		if !got[n] {
			t.Errorf("decodeFeatures missing name %q (got %v)", n, f.Names)
		}
	}
	if len(f.Names) != len(want) {
		t.Errorf("decodeFeatures names = %v, want %d entries", f.Names, len(want))
	}
}
