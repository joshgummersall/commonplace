package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/joshgummersall/commonplace/internal/autotag"
	"github.com/joshgummersall/commonplace/internal/clippings"
	"github.com/joshgummersall/commonplace/internal/embedding"
	"github.com/joshgummersall/commonplace/internal/importer"
	"github.com/joshgummersall/commonplace/internal/store"
	"github.com/spf13/cobra"
)

func (a *App) newImportCommand() *cobra.Command {
	var source string
	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Import highlights from a My Clippings.txt file",
		Long: `Import highlights from a Kindle or CrossPoint "My Clippings.txt" file.

The format is detected from the first entry unless --source is given.
Re-importing a file is safe: highlights already stored are skipped, and an
edited highlight (extended, trimmed, or shifted) replaces its older version,
keeping its notes and favorite. Bookmarks are skipped.

Entries that can't be parsed are reported as warnings and skipped.

New and changed highlights are then embedded for search by meaning. If
Ollama isn't running, the import still succeeds; cmpl serve embeds them
later.

Output: counts on stdout, such as "12 added, 1 updated to a newer version,
1533 unchanged", plus lines for notes, bookmarks, and skipped entries when
there are any. Parse warnings and embedding problems go to stderr. It
exits non-zero only when the file can't be read or stored.

Safe to run while cmpl serve is running.`,
		Example: `  cmpl import "/Volumes/Kindle/documents/My Clippings.txt"
  cmpl import clippings.txt --source crosspoint`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var dialect clippings.Dialect
			if source != "" {
				d, err := clippings.ParseDialect(source)
				if err != nil {
					return err
				}
				dialect = d
			}

			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer f.Close()
			clips, warnings, err := clippings.Parse(f, dialect, time.Local)
			if err != nil {
				return fmt.Errorf("%s: %w", args[0], err)
			}
			for _, w := range warnings {
				fmt.Fprintf(a.stderr, "warning: %s: %s\n", args[0], w)
			}

			s, err := a.openStore()
			if err != nil {
				return err
			}
			r, err := importer.Import(s, clips, a.now())
			if err != nil {
				return err
			}
			a.printImportResult(r, len(warnings))
			a.embedAfterImport(cmd.Context(), s)
			return nil
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "file format: kindle or crosspoint (default: detect)")
	return cmd
}

func (a *App) printImportResult(r importer.Result, warnings int) {
	fmt.Fprintf(a.stdout, "%d added, %d updated to a newer version, %d unchanged\n", r.Added, r.Replaced, r.Unchanged)
	if r.Merged > 0 || r.Older > 0 {
		fmt.Fprintf(a.stdout, "%d older versions in the file merged, %d skipped (newer version stored)\n", r.Merged, r.Older)
	}
	if r.Notes > 0 || r.Orphans > 0 {
		fmt.Fprintf(a.stdout, "%d notes attached, %d notes stored on their own\n", r.Notes, r.Orphans)
	}
	if r.Bookmarks > 0 {
		fmt.Fprintf(a.stdout, "%d bookmarks skipped\n", r.Bookmarks)
	}
	if warnings > 0 {
		fmt.Fprintf(a.stdout, "%d entries skipped with warnings (see above)\n", warnings)
	}
}

// embedAfterImport embeds whatever is missing, then suggests tags for the
// new highlights. Failing to embed never fails the import: the highlights
// are stored, and serve retries.
func (a *App) embedAfterImport(ctx context.Context, s *store.Store) {
	e := a.embedder()
	progress := func(done, total int) {
		fmt.Fprintf(a.stderr, "\rEmbedding highlights: %d/%d", done, total)
	}
	n, err := embedding.Backfill(ctx, s, e, progress)
	if n > 0 {
		fmt.Fprintln(a.stderr)
		fmt.Fprintf(a.stdout, "%d highlights embedded with %s\n", n, e.Model())
		if added, err := autotag.Suggest(s, e.Model()); err != nil {
			fmt.Fprintf(a.stderr, "warning: suggesting tags failed: %v\n", err)
		} else if added > 0 {
			fmt.Fprintf(a.stdout, "%d tags suggested\n", added)
		}
	}
	if err != nil {
		left, _ := s.CountStaleEmbeddings(e.Model())
		fmt.Fprintf(a.stderr, "warning: %d highlights not embedded: %v\n", left, err)
		fmt.Fprintln(a.stderr, "Keyword search works now; cmpl serve embeds them once the embedder is reachable.")
	}
}
