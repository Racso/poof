package cmd

import (
	"net"
	"testing"
)

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("bad CIDR %q: %v", s, err)
	}
	return n
}

func TestNetsOverlap(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"172.16.0.0/19", "172.16.0.0/28", true},  // b inside a
		{"172.16.0.0/28", "172.16.0.0/19", true},  // a inside b
		{"172.16.0.0/19", "172.16.16.0/24", true}, // partial
		{"172.16.0.0/19", "172.16.32.0/19", false},
		{"172.16.0.0/19", "172.17.0.0/16", false},
		{"10.210.0.0/19", "10.108.0.0/20", false}, // DO VPC range stays clear
		{"172.16.0.0/19", "192.168.0.0/20", false},
	}
	for _, c := range cases {
		got := netsOverlap(mustCIDR(t, c.a), mustCIDR(t, c.b))
		if got != c.want {
			t.Errorf("netsOverlap(%s, %s) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// Every advertised candidate must parse and be the documented /19.
func TestPoolCandidatesAreWellFormed(t *testing.T) {
	if len(poolCandidates) == 0 {
		t.Fatal("no pool candidates defined")
	}
	seen := map[string]bool{}
	for _, c := range poolCandidates {
		ip, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Errorf("candidate %q does not parse: %v", c, err)
			continue
		}
		if ones, _ := n.Mask.Size(); ones != poolPrefixLen {
			t.Errorf("candidate %q is /%d, want /%d", c, ones, poolPrefixLen)
		}
		if !ip.IsPrivate() {
			t.Errorf("candidate %q is not a private range", c)
		}
		if seen[c] {
			t.Errorf("duplicate candidate %q", c)
		}
		seen[c] = true
	}
}

// Candidates must not overlap each other — otherwise a "free" pick could
// still collide with a range we'd have offered.
func TestPoolCandidatesDoNotOverlapEachOther(t *testing.T) {
	for i := 0; i < len(poolCandidates); i++ {
		for j := i + 1; j < len(poolCandidates); j++ {
			a, b := mustCIDR(t, poolCandidates[i]), mustCIDR(t, poolCandidates[j])
			if netsOverlap(a, b) {
				t.Errorf("candidates %s and %s overlap", poolCandidates[i], poolCandidates[j])
			}
		}
	}
}

// A /19 sliced into /28s must yield the 512 networks the docs promise.
func TestPoolCapacity(t *testing.T) {
	if got := 1 << (poolChunkSize - poolPrefixLen); got != 512 {
		t.Errorf("pool capacity = %d networks, want 512", got)
	}
}
