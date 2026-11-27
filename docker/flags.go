package docker

import (
	"fmt"
	"strings"
)

// reservedFlags are the `docker create` flags Poof sets itself. A flags file
// repeating one would conflict with Poof's own value (two names, a second
// restart policy) or split a setting between two sources of truth (env vars
// and volumes, which Poof stores and re-applies on every deploy).
var reservedFlags = map[string]string{
	"--name":     "Poof names the container",
	"--network":  "use `poof net`",
	"--net":      "use `poof net`",
	"--restart":  "Poof owns the restart policy (pause/resume flip it)",
	"--rm":       "a pause stops the container, which would delete it",
	"--env":      "use `poof env`",
	"--env-file": "use `poof env`",
	"--volume":   "use `poof volume`",
	"-e":         "use `poof env`",
	"-v":         "use `poof volume`",
}

// ParseFlags reads a project's container flags file: extra `docker create`
// flags, in Docker's own CLI syntax, added to every container Poof creates for
// the project. Lines starting with # are comments; flags may span several
// lines; single and double quotes group a value containing spaces.
//
// Every token must be a flag, with its value attached by `=`
// (`--memory=1g`, not `--memory 1g`). That is what lets Poof tell a flag from
// a stray word, which `docker create` would otherwise take as the image name.
func ParseFlags(content string) ([]string, error) {
	var tokens []string
	for i, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		words, err := splitWords(trimmed)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		for _, w := range words {
			if err := checkFlag(w); err != nil {
				return nil, fmt.Errorf("line %d: %w", i+1, err)
			}
		}
		tokens = append(tokens, words...)
	}
	return tokens, nil
}

func checkFlag(token string) error {
	if !strings.HasPrefix(token, "-") || token == "-" || token == "--" {
		return fmt.Errorf("%q is not a flag: attach values with '=' (--memory=1g)", token)
	}
	name, _, _ := strings.Cut(token, "=")
	if !strings.HasPrefix(name, "--") {
		// A short flag, possibly with its value glued on (-m1g): the flag
		// is the first letter.
		name = name[:2]
	}
	if why, reserved := reservedFlags[name]; reserved {
		return fmt.Errorf("%s is set by Poof: %s", name, why)
	}
	return nil
}

// splitWords splits a line on whitespace, honouring single quotes (literal),
// double quotes (backslash escapes \" and \\) and backslash outside quotes.
func splitWords(line string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	var quote rune
	escaped := false

	for _, r := range line {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			default:
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			inWord = true
		case r == '\\':
			escaped = true
			inWord = true
		case r == ' ' || r == '\t':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c quote", quote)
	}
	if escaped {
		return nil, fmt.Errorf("trailing backslash")
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}
