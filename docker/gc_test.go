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

	del, keep := selectForRemoval(images, "", 3, 0, now)

	wantDel := []string{"v1", "v2"}        // oldest two
	wantKeep := []string{"v3", "v4", "v5"} // newest three
	if !equalRefs(refs(del), wantDel) {
		t.Errorf("delete: got %v, want %v", refs(del), wantDel)
	}
	if !equalRefs(refs(keep), wantKeep) {
		t.Errorf("keep: got %v, want %v", refs(keep), wantKeep)
	}
}

func TestSelectForRemoval_OlderThanOnly(t *testing.T) {
	now := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)
	images := []LocalImage{
		mkImage("ancient", 30, now),
		mkImage("oldish", 15, now),
		mkImage("recent", 5, now),
		mkImage("fresh", 1, now),
	}

	del, keep := selectForRemoval(images, "", 0, 14, now)

	wantDel := []string{"ancient", "oldish"}
	wantKeep := []string{"fresh", "recent"}
	if !equalRefs(refs(del), wantDel) {
		t.Errorf("delete: got %v, want %v", refs(del), wantDel)
	}
	if !equalRefs(refs(keep), wantKeep) {
		t.Errorf("keep: got %v, want %v", refs(keep), wantKeep)
	}
}

func TestSelectForRemoval_BothFiltersAreANDed(t *testing.T) {
	// keep=3 + older-than=14: an image must be BOTH outside the keep window
	// AND older than 14 days to get deleted. Layout (sorted newest-first):
	//   idx 0  1d   inside-keep  recent   → keep
	//   idx 1  3d   inside-keep  recent   → keep
	//   idx 2  5d   inside-keep  recent   → keep
	//   idx 3  7d   OUTSIDE      recent   → keep (age saves it)
	//   idx 4  30d  OUTSIDE      OLD      → delete
	// The idx-3 entry is the AND-discriminator: under OR semantics it would be
	// deleted (outside the keep window), under AND it must survive.
	now := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)
	images := []LocalImage{
		mkImage("newest", 1, now),
		mkImage("near-newest", 3, now),
		mkImage("inside-edge", 5, now),
		mkImage("outside-but-recent", 7, now),
		mkImage("outside-and-old", 30, now),
	}

	del, keep := selectForRemoval(images, "", 3, 14, now)

	if !equalRefs(refs(del), []string{"outside-and-old"}) {
		t.Errorf("delete: got %v, want [outside-and-old]", refs(del))
	}
	if !equalRefs(refs(keep), []string{"inside-edge", "near-newest", "newest", "outside-but-recent"}) {
		t.Errorf("keep: got %v", refs(keep))
	}

	// Sanity: the same image set under OR (separate keep + age passes) DOES
	// kill outside-but-recent. Confirms the test exercises the AND/OR boundary.
	delKeep, _ := selectForRemoval(images, "", 3, 0, now)
	if len(delKeep) != 2 {
		t.Errorf("keep-only pass should delete 2 (outside the window), got %d", len(delKeep))
	}
}

func TestSelectForRemoval_NeverDeletesRunningImage(t *testing.T) {
	now := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)
	images := []LocalImage{
		mkImage("ancient", 100, now), // would normally be deleted
		mkImage("recent", 1, now),
	}
	// The running image is the ancient one; even with keep=1 it must survive.
	del, keep := selectForRemoval(images, "sha256:ancient", 1, 0, now)

	if !equalRefs(refs(del), nil) {
		t.Errorf("delete: got %v, want nothing (running image protected)", refs(del))
	}
	if !equalRefs(refs(keep), []string{"ancient", "recent"}) {
		t.Errorf("keep: got %v", refs(keep))
	}
}

func TestSelectForRemoval_NoRulesDeletesNothing(t *testing.T) {
	now := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)
	images := []LocalImage{
		mkImage("a", 100, now),
		mkImage("b", 1, now),
	}
	del, keep := selectForRemoval(images, "", 0, 0, now)
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
	del, keep := selectForRemoval(images, "", 1, 0, now)

	if !equalRefs(refs(del), []string{"middle", "oldest"}) {
		t.Errorf("delete: got %v", refs(del))
	}
	if !equalRefs(refs(keep), []string{"newest"}) {
		t.Errorf("keep: got %v", refs(keep))
	}
}

func TestSelectForRemoval_EquivalentToOR_WhenAppliedSequentially(t *testing.T) {
	// Doc says: OR semantics can be achieved by running --keep then --older-than
	// sequentially. Verify that property holds: anything killed by either pass
	// in isolation is also killed by chaining the two passes in order.
	now := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)
	images := []LocalImage{
		mkImage("v1", 30, now),
		mkImage("v2", 20, now),
		mkImage("v3", 10, now),
		mkImage("v4", 5, now),
		mkImage("v5", 1, now),
	}

	delA, _ := selectForRemoval(images, "", 3, 0, now)        // outside keep
	delB, _ := selectForRemoval(images, "", 0, 14, now)       // older than 14d
	union := map[string]bool{}
	for _, img := range delA {
		union[img.Reference] = true
	}
	for _, img := range delB {
		union[img.Reference] = true
	}

	// Now simulate chaining: pass 1 (--keep 3), then pass 2 (--older-than 14)
	// against the survivors of pass 1.
	pass1Del, pass1Keep := selectForRemoval(images, "", 3, 0, now)
	pass2Del, _ := selectForRemoval(pass1Keep, "", 0, 14, now)
	chained := map[string]bool{}
	for _, img := range pass1Del {
		chained[img.Reference] = true
	}
	for _, img := range pass2Del {
		chained[img.Reference] = true
	}

	if len(union) != len(chained) {
		t.Fatalf("union=%v, chained=%v", union, chained)
	}
	for k := range union {
		if !chained[k] {
			t.Errorf("chained pass missed %q (which OR would delete)", k)
		}
	}
}
