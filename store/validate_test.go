package store_test

import (
	"strings"
	"testing"

	"github.com/racso/poof/store"
)

func TestValidateProjectName(t *testing.T) {
	valid := []string{
		"web", "bbsf-api", "z0", "microDocs", "racso.co",
		"poesias-infantiles", "a", "a1", "super-simple-sender-engine",
	}
	for _, n := range valid {
		if err := store.ValidateProjectName(n); err != nil {
			t.Errorf("ValidateProjectName(%q) = %v, want nil", n, err)
		}
	}

	invalid := []struct{ name, because string }{
		{"", "empty"},
		{".z0", "leading dot makes the container label \"poof-\" end in a hyphen"},
		{"web_api", "underscores are not valid in DNS hostnames"},
		{"web..api", "consecutive dots produce an empty label"},
		{"web.", "trailing dot produces an empty label"},
		{"web api", "space"},
		{"owner/web", "slash"},
		{"web:1", "colon"},
		{strings.Repeat("a", 64), "label longer than 63 characters"},
	}
	for _, tc := range invalid {
		if err := store.ValidateProjectName(tc.name); err == nil {
			t.Errorf("ValidateProjectName(%q) = nil, want error (%s)", tc.name, tc.because)
		}
	}
}

// The real-world regression: ".z0" produced container "poof-.z0", whose first
// DNS label "poof-" ends in a hyphen. Docker's DNS resolved it; Go's did not,
// so Caddy returned 502 for every request.
func TestValidateProjectNameRejectsLeadingDot(t *testing.T) {
	err := store.ValidateProjectName(".z0")
	if err == nil {
		t.Fatal("expected .z0 to be rejected")
	}
	if !strings.Contains(err.Error(), "poof-.z0") {
		t.Errorf("error should name the offending container, got: %v", err)
	}
}
