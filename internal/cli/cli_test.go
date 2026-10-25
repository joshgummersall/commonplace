package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDBPathPrecedence(t *testing.T) {
	tests := []struct {
		name string
		flag string
		env  string
		xdg  string
		want string
	}{
		{name: "default", want: filepath.Join("/home/me", ".local", "share", "commonplace", "commonplace.db")},
		{name: "xdg", xdg: "/xdg", want: filepath.Join("/xdg", "commonplace", "commonplace.db")},
		{name: "relative xdg ignored", xdg: "rel", want: filepath.Join("/home/me", ".local", "share", "commonplace", "commonplace.db")},
		{name: "env", env: "/env/c.db", xdg: "/xdg", want: "/env/c.db"},
		{name: "flag beats env", flag: "/flag/c.db", env: "/env/c.db", want: "/flag/c.db"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewApp(nil, &bytes.Buffer{}, &bytes.Buffer{})
			a.dbFlag = tt.flag
			a.getenv = func(k string) string {
				switch k {
				case dbEnvVar:
					return tt.env
				case "XDG_DATA_HOME":
					return tt.xdg
				}
				return ""
			}
			a.home = func() (string, error) { return "/home/me", nil }
			got, err := a.dbPath()
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("dbPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A database at the old default, ~/.commonplace, is used until one exists
// at the new default.
func TestDBPathLegacy(t *testing.T) {
	home := t.TempDir()
	touch := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var errOut bytes.Buffer
	a := NewApp(nil, &bytes.Buffer{}, &errOut)
	a.getenv = func(string) string { return "" }
	a.home = func() (string, error) { return home, nil }

	legacy := filepath.Join(home, ".commonplace", "commonplace.db")
	touch(legacy)
	if got, _ := a.dbPath(); got != legacy {
		t.Errorf("with only the old database, dbPath() = %q, want %q", got, legacy)
	}
	if _, err := a.openStore(); err != nil {
		t.Fatal(err)
	}
	a.closeStore()
	if !strings.Contains(errOut.String(), "old location") {
		t.Errorf("stderr missing the move note:\n%s", errOut.String())
	}

	current := filepath.Join(home, ".local", "share", "commonplace", "commonplace.db")
	touch(current)
	if got, _ := a.dbPath(); got != current {
		t.Errorf("with both databases, dbPath() = %q, want %q", got, current)
	}
}

func TestDBPathHomeError(t *testing.T) {
	a := NewApp(nil, &bytes.Buffer{}, &bytes.Buffer{})
	a.getenv = func(string) string { return "" }
	a.home = func() (string, error) { return "", errors.New("no home") }
	if _, err := a.dbPath(); err == nil {
		t.Fatal("dbPath() succeeded without a home dir, want error")
	}
}

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	a := NewApp(nil, &out, &bytes.Buffer{})
	if err := a.run([]string{"version"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "cmpl ") {
		t.Errorf("version output = %q", out.String())
	}
}
