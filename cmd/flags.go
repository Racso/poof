package cmd

import (
	"fmt"
	"strings"
)

// parseCIFlag parses a yes/no flag value into a boolean.
// Accepts: yes, y, no, n (case-insensitive).
func parseCIFlag(val string) (bool, error) {
	switch strings.ToLower(val) {
	case "yes", "y":
		return true, nil
	case "no", "n":
		return false, nil
	default:
		return false, fmt.Errorf("invalid value %q: must be yes/y or no/n", val)
	}
}

// parseCIModeFlag parses a CI flag value into a tri-state.
// Accepts: yes, y → (true, "managed"); no, n → (false, ""); callable → (true, "callable").
//
// Used by --ci on commands that need to distinguish a callable
// (workflow_call) workflow from a regular push-triggered one.
func parseCIModeFlag(val string) (enabled bool, mode string, err error) {
	switch strings.ToLower(val) {
	case "yes", "y":
		return true, "managed", nil
	case "no", "n":
		return false, "", nil
	case "callable":
		return true, "callable", nil
	default:
		return false, "", fmt.Errorf("invalid value %q: must be yes/y, no/n, or callable", val)
	}
}
