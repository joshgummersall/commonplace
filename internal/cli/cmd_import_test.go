package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	a := NewApp(nil, &out, &errOut)
	a.getenv = func(string) string { return "" }
	err = a.run(args)
	return out.String(), errOut.String(), err
}

func TestImportCommand(t *testing.T) {
	db := filepath.Join(t.TempDir(), "c.db")
	fixture := filepath.Join("..", "..", "testdata", "kindle", "edge-cases.txt")

	out, errOut, err := runCLI(t, "--db", db, "import", fixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"3 added", "1 notes attached", "3 entries skipped with warnings"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errOut, "unrecognized kindle header") {
		t.Errorf("stderr missing parse warning:\n%s", errOut)
	}

	out, _, err = runCLI(t, "--db", db, "import", fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "0 added, 0 updated to a newer version, 4 unchanged") {
		t.Errorf("re-import stdout:\n%s", out)
	}
}

func TestImportCommandErrors(t *testing.T) {
	db := filepath.Join(t.TempDir(), "c.db")
	kindle := filepath.Join("..", "..", "testdata", "kindle", "clippings.txt")
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unknown source", []string{"import", kindle, "--source", "kobo"}, `unknown source "kobo"`},
		{"wrong source", []string{"import", kindle, "--source", "crosspoint"}, "kindle entry in a crosspoint file"},
		{"missing file", []string{"import", "nope.txt"}, "no such file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCLI(t, append([]string{"--db", db}, tt.args...)...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}
