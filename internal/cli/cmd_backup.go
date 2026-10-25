package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/spf13/cobra"
)

// backupName matches the files cmpl backup writes, and only those, so
// --keep never removes anything else in the folder.
var backupName = regexp.MustCompile(`^commonplace-\d{8}T\d{6}Z\.db$`)

func (a *App) newBackupCommand() *cobra.Command {
	var keep int
	cmd := &cobra.Command{
		Use:   "backup <dir>",
		Short: "Write a snapshot of the database to a folder",
		Long: `Write a consistent snapshot of the database to <dir>, named by the time in
UTC (commonplace-20261025T153000Z.db). The folder is created if needed.

A snapshot is one self-contained file, safe to take while cmpl serve runs
and safe to keep in a synced folder such as iCloud Drive. Keep the live
database out of synced folders: it is several files that must change
together.

To restore, stop cmpl and copy a snapshot to the database path.`,
		Example: `  cmpl backup ~/Library/Mobile\ Documents/com~apple~CloudDocs/commonplace
  cmpl backup ~/backups/commonplace --keep 30`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if keep < 0 {
				return fmt.Errorf("--keep must be 0 or more")
			}
			dir := args[0]
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			s, err := a.openStore()
			if err != nil {
				return err
			}

			name := "commonplace-" + time.Now().UTC().Format("20060102T150405Z") + ".db"
			path := filepath.Join(dir, name)
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s already exists", path)
			}
			// Write under a temporary name and rename, so a sync client or
			// a pruning run never sees a half-written snapshot.
			tmp := filepath.Join(dir, "."+name+".tmp")
			os.Remove(tmp)
			if err := s.Backup(tmp); err != nil {
				os.Remove(tmp)
				return err
			}
			if err := os.Rename(tmp, path); err != nil {
				os.Remove(tmp)
				return err
			}
			fmt.Fprintf(a.stdout, "Wrote %s\n", path)

			if keep == 0 {
				return nil
			}
			return a.pruneBackups(dir, keep)
		},
	}
	cmd.Flags().IntVar(&keep, "keep", 0, "keep only the newest N snapshots in <dir> (0 keeps all)")
	return cmd
}

// pruneBackups removes all but the newest keep snapshots in dir. Names
// sort by time, so the newest sort last.
func (a *App) pruneBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && backupName.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names[:max(len(names)-keep, 0)] {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "Removed %s\n", filepath.Join(dir, n))
	}
	return nil
}
