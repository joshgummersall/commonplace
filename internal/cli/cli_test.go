package cli

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestDBPathPrecedence(t *testing.T) {
	tests := []struct {
		name string
		flag string
		env  string
		want string
	}{
		{name: "default", want: filepath.Join("/home/me", ".commonplace", "commonplace.db")},
		{name: "env", env: "/env/c.db", want: "/env/c.db"},
		{name: "flag beats env", flag: "/flag/c.db", env: "/env/c.db", want: "/flag/c.db"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewApp(nil, &bytes.Buffer{}, &bytes.Buffer{})
			a.dbFlag = tt.flag
			a.getenv = func(k string) string {
				if k == dbEnvVar {
					return tt.env
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
