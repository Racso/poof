package docker

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseFlags(t *testing.T) {
	content := `# TAP service: public queries on a shared host
--memory=1g --cpus=1

--read-only
--tmpfs=/tmp:rw,size=64m
--security-opt=no-new-privileges
--label="note=two words"
-m1g
`
	got, err := ParseFlags(content)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{
		"--memory=1g", "--cpus=1", "--read-only", "--tmpfs=/tmp:rw,size=64m",
		"--security-opt=no-new-privileges", "--label=note=two words", "-m1g",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestParseFlagsEmpty(t *testing.T) {
	got, err := ParseFlags("# nothing yet\n\n")
	if err != nil || len(got) != 0 {
		t.Errorf("got %q, %v; want no flags", got, err)
	}
}

func TestParseFlagsRejects(t *testing.T) {
	cases := map[string]string{
		"--memory 1g":          `"1g" is not a flag`,
		"--name=other":         "--name is set by Poof",
		"--network=host":       "--network is set by Poof",
		"--restart=no":         "--restart is set by Poof",
		"--rm":                 "--rm is set by Poof",
		"-e=A=1":               "-e is set by Poof",
		"-v/:/host":            "-v is set by Poof",
		"--env-file=/x":        "--env-file is set by Poof",
		"--label='unclosed":    "unterminated ' quote",
		"--read-only\n--ok \\": "line 2: trailing backslash",
	}
	for content, want := range cases {
		_, err := ParseFlags(content)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want error containing %q", content, err, want)
		}
	}
}
