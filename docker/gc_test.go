package docker

import (
	"sort"
	"testing"
	"time"
)

// mkImage builds a LocalImage at a given age (days before "now").
// ID is auto-derived from the reference so identity matches by name.
func mkImage(ref string, daysAgo int, now time.Time) LocalImage {
	return LocalImage{
		Reference: ref,
		ID:        "sha256:" + ref,
		Created:   now.AddDate(0, 0, -daysAgo),
	}
}

// refs returns the sorted reference list of a slice of LocalImage —
// makes assertions order-independent.
func refs(imgs []LocalImage) []string {
	out := make([]string, len(imgs))
	for i, img := range imgs {
		out[i] = img.Reference
	}
	sort.Strings(out)
	return out
}

func equalRefs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestImageRepo(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"ghcr.io/foo/bar:abc", "ghcr.io/foo/bar"},
		{"ghcr.io/foo/bar", "ghcr.io/foo/bar"},
		{"localhost:5000/foo:tag", "localhost:5000/foo"},
		{"ubuntu:22.04", "ubuntu"},
		{"ubuntu", "ubuntu"},
	}
	for _, c := range cases {
		if got := imageRepo(c.in); got != c.want {
			t.Errorf("imageRepo(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseDockerTime(t *testing.T) {
	good := []string{
		"2024-01-15 10:30:00 +0000 UTC",
		"2024-01-15 10:30:00 +0000",
		"2024-01-15T10:30:00Z",
	}
	for _, s := range good {
		if _, err := parseDockerTime(s); err != nil {
			t.Errorf("parseDockerTime(%q) error: %v", s, err)
		}
	}
	if _, err := parseDockerTime("not a date"); err == nil {
		t.Error("expected error for invalid date")
	}
}

func TestParseHumanSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"0B", 0},
		{"412B", 412},
		{"1kB", 1000},
		{"1.5kB", 1500},
		{"5.2GB", 5_200_000_000},
		{"1MB", 1_000_000},
		{"  3GB  ", 3_000_000_000},
		{"2TB", 2_000_000_000_000},
	}
	for _, c := range cases {
		got, err := parseHumanSize(c.in)
		if err != nil {
			t.Errorf("parseHumanSize(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseHumanSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "GB", "1.5XB", "abc"} {
		if _, err := parseHumanSize(bad); err == nil {
			t.Errorf("parseHumanSize(%q) expected error", bad)
		}
	}
}

// --- selectForRemoval ---

func TestSelectForRemoval_KeepOnly(t *testing.T) {
	now := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)
	images := []LocalImage{
		mkImage("v1", 5, now),
		mkImage("v2", 4, now),
		mkImage("v3", 3, now),
		mkImage("v4", 2, now),
		mkImage("v5", 1, now),
	}

	del, keep := selectForRemoval(images, nil, 3)

	wantDel := []string{"v1", "v2"}        // oldest two
	wantKeep := []string{"v3", "v4", "v5"} // newest three
	if !equalRefs(refs(del), wantDel) {
		t.Errorf("delete: got %v, want %v", refs(del), wantDel)
	}
	if !equalRefs(refs(keep), wantKeep) {
		t.Errorf("keep: got %v, want %v", refs(keep), wantKeep)
	}
}

func TestSelectForRemoval_NeverDeletesRunningImage(t *testing.T) {
	now := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)
	images := []LocalImage{
		mkImage("ancient", 100, now), // would normally be deleted
		mkImage("recent", 1, now),
	}
	// The running image is the ancient one; even with keep=1 it must survive.
	del, keep := selectForRemoval(images, map[string]bool{"sha256:ancient": true}, 1)

	if !equalRefs(refs(del), nil) {
		t.Errorf("delete: got %v, want nothing (running image protected)", refs(del))
	}
	if !equalRefs(refs(keep), []string{"ancient", "recent"}) {
		t.Errorf("keep: got %v", refs(keep))
	}
}

func TestSelectForRemoval_KeepZeroDeletesNothing(t *testing.T) {
	now := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)
	images := []LocalImage{
		mkImage("a", 100, now),
		mkImage("b", 1, now),
	}
	del, keep := selectForRemoval(images, nil, 0)
	if len(del) != 0 {
		t.Errorf("delete: got %v, want none", refs(del))
	}
	if len(keep) != len(images) {
		t.Errorf("keep: got %d, want %d", len(keep), len(images))
	}
}

func TestSelectForRemoval_OutOfOrderInputStillWorks(t *testing.T) {
	// Caller passes images in arbitrary order; selection must still be by age.
	now := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)
	images := []LocalImage{
		mkImage("middle", 5, now),
		mkImage("oldest", 30, now),
		mkImage("newest", 1, now),
	}
	del, keep := selectForRemoval(images, nil, 1)

	if !equalRefs(refs(del), []string{"middle", "oldest"}) {
		t.Errorf("delete: got %v", refs(del))
	}
	if !equalRefs(refs(keep), []string{"newest"}) {
		t.Errorf("keep: got %v", refs(keep))
	}
}
