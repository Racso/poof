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
