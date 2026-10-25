package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshgummersall/commonplace/internal/store"
)

func TestBackupCommand(t *testing.T) {
	tmp := t.TempDir()
	db := filepath.Join(tmp, "c.db")
	fixture := filepath.Join("..", "..", "testdata", "kindle", "edge-cases.txt")
	if _, _, err := runCLI(t, "--db", db, "import", fixture); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(tmp, "backups")
	out, _, err := runCLI(t, "--db", db, "backup", dir)
	if err != nil {
		t.Fatal(err)
	}
	snaps, _ := filepath.Glob(filepath.Join(dir, "commonplace-*.db"))
	if len(snaps) != 1 || !strings.Contains(out, "Wrote "+snaps[0]) {
		t.Fatalf("snapshots %v, stdout:\n%s", snaps, out)
	}
	if extra, _ := filepath.Glob(filepath.Join(dir, "*-*")); len(extra) != 1 {
		t.Errorf("backup left companion files: %v", extra)
	}

	s, err := store.Open(snaps[0])
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM highlights`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if n != 3 {
		t.Errorf("snapshot has %d highlights, want 3", n)
	}
}

func TestBackupKeep(t *testing.T) {
	tmp := t.TempDir()
	db := filepath.Join(tmp, "c.db")
	dir := filepath.Join(tmp, "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := []string{"commonplace-20200101T000000Z.db", "commonplace-20210101T000000Z.db", "commonplace-20220101T000000Z.db"}
	for _, n := range append(old, "notes.txt", "commonplace-copy.db") {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, _, err := runCLI(t, "--db", db, "backup", dir, "--keep", "2")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range old[:2] {
		if _, err := os.Stat(filepath.Join(dir, n)); !os.IsNotExist(err) {
			t.Errorf("%s not pruned", n)
		}
		if !strings.Contains(out, "Removed "+filepath.Join(dir, n)) {
			t.Errorf("stdout missing removal of %s:\n%s", n, out)
		}
	}
	// The newest old snapshot, the new one, and unrelated files stay.
	for _, n := range []string{old[2], "notes.txt", "commonplace-copy.db"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s removed: %v", n, err)
		}
	}

	if _, _, err := runCLI(t, "--db", db, "backup", dir, "--keep", "-1"); err == nil {
		t.Error("--keep -1 accepted")
	}
}
