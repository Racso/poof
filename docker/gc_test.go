package docker

import "testing"

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
