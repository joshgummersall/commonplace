// Package cli wires the cmpl cobra commands.
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/joshgummersall/commonplace/internal/embedding"
	"github.com/joshgummersall/commonplace/internal/store"
	"github.com/spf13/cobra"
)

var version = "dev"

// dbEnvVar overrides the default database path; --db overrides both.
const dbEnvVar = "COMMONPLACE_DB"

type ExitError struct {
	Code    int
	Message string
}

func (e ExitError) Error() string {
	return e.Message
}

type App struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	getenv func(string) string
	home   func() (string, error)
	now    func() time.Time

	dbFlag    string
	ollamaURL string
	model     string
	store     *store.Store
	// newEmbedder is replaced in tests.
	newEmbedder func() embedding.Embedder
}

func NewApp(stdin io.Reader, stdout, stderr io.Writer) *App {
	return &App{
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
		getenv: os.Getenv,
		home:   os.UserHomeDir,
		now:    time.Now,
	}
}

// embedder returns the configured embedder: --ollama-url, else
// $OLLAMA_HOST, else Ollama's default address.
func (a *App) embedder() embedding.Embedder {
	if a.newEmbedder != nil {
		return a.newEmbedder()
	}
	return embedding.NewOllama(a.ollamaBase(), a.model)
}

// ollamaBase is --ollama-url, else $OLLAMA_HOST; empty means the default.
func (a *App) ollamaBase() string {
	if a.ollamaURL != "" {
		return a.ollamaURL
	}
	return a.getenv("OLLAMA_HOST")
}

func Run(args []string, stdout io.Writer) error {
	app := NewApp(os.Stdin, stdout, os.Stderr)
	return app.run(args)
}

func (a *App) run(args []string) error {
	defer a.closeStore()
	root := a.newRootCommand()
	root.SetArgs(args)
	root.SetIn(a.stdin)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	return root.Execute()
}

func (a *App) newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "cmpl",
		Short:         "Commonplace: import, search, and resurface your reading highlights",
		SilenceUsage:  true,
		SilenceErrors: true,
		Long: `cmpl keeps e-reader highlights in a local SQLite database, searches them by
words and meaning, and resurfaces a daily set of past highlights. Read them
in the web app (cmpl serve) or with the read commands below.

The library:

  highlight  A passage marked on an e-reader, with its book, location, and
             the time it was marked. Each has a stable numeric id (#412).
  book       A title and author. Each has an id; cmpl books lists them.
  note       Text attached to a highlight, from the e-reader or written in
             the web app (source "user"). Search matches note text. cmpl
             show prints a highlight's notes; --notes adds them to daily,
             list, search, and similar.
  favorite   A highlight marked as a favorite in the web app. Favorites
             come up more often in the daily set; --favorites filters to
             them.
  buried     A highlight hidden from the daily set. It still appears in
             list and search, marked "buried".
  theme      A group of highlights that say related things, usually across
             books, found and named in the web app. Broad areas hold
             specific themes; cmpl themes lists them, and --theme filters
             to one.

Every read command prints JSON and takes ids from the others' output. A typical session:

  cmpl daily                      today's highlights
  cmpl search 'fear of failure'   find highlights about an idea
  cmpl show 412                   one highlight in full, with notes
  cmpl similar 412                highlights close in meaning to it
  cmpl books                      book ids, for --book
  cmpl themes                     theme ids, for --theme

Read commands never change highlights, notes, favorites, or burials. Run
cmpl help <command> for each command's flags, output, and examples.

The database is $XDG_DATA_HOME/commonplace/commonplace.db
(~/.local/share/commonplace/commonplace.db when $XDG_DATA_HOME is unset)
unless overridden by $` + dbEnvVar + ` or --db.`,
	}
	root.AddGroup(
		&cobra.Group{ID: "read", Title: "Read highlights:"},
		&cobra.Group{ID: "manage", Title: "Manage the library:"},
	)
	root.PersistentFlags().StringVar(&a.dbFlag, "db", "", "database path (default $"+dbEnvVar+" or ~/.local/share/commonplace/commonplace.db)")
	root.PersistentFlags().StringVar(&a.ollamaURL, "ollama-url", "", "Ollama address for embeddings (default $OLLAMA_HOST or "+embedding.DefaultOllamaURL+")")
	root.PersistentFlags().StringVar(&a.model, "embed-model", embedding.DefaultModel, "Ollama embedding model; changing it re-embeds every highlight")
	root.CompletionOptions.DisableDefaultCmd = true

	read := []*cobra.Command{
		a.newDailyCommand(),
		a.newListCommand(),
		a.newSearchCommand(),
		a.newShowCommand(),
		a.newSimilarCommand(),
		a.newBooksCommand(),
		a.newThemesCommand(),
	}
	manage := []*cobra.Command{
		a.newImportCommand(),
		a.newServeCommand(),
		a.newBackupCommand(),
	}
	for _, c := range read {
		c.GroupID = "read"
	}
	for _, c := range manage {
		c.GroupID = "manage"
	}
	root.AddCommand(read...)
	root.AddCommand(manage...)
	root.AddCommand(a.newEvalCommand(), a.newVersionCommand())
	return root
}

func (a *App) newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		Long:  "Print the CLI version, as \"cmpl 0.4.0\".",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintf(a.stdout, "cmpl %s\n", version)
			return err
		},
	}
}

// dbPath resolves the database path: --db, then $COMMONPLACE_DB, then
// commonplace/commonplace.db under the XDG data dir ($XDG_DATA_HOME, else
// ~/.local/share). Until the new default exists, a database at the old
// default, ~/.commonplace, is used instead.
func (a *App) dbPath() (string, error) {
	if a.dbFlag != "" {
		return a.dbFlag, nil
	}
	if p := a.getenv(dbEnvVar); p != "" {
		return p, nil
	}
	home, err := a.home()
	if err != nil {
		return "", fmt.Errorf("find home dir: %w", err)
	}
	path := a.defaultDBPath(home)
	if exists(path) {
		return path, nil
	}
	if legacy := legacyDBPath(home); exists(legacy) {
		return legacy, nil
	}
	return path, nil
}

func (a *App) defaultDBPath(home string) string {
	data := a.getenv("XDG_DATA_HOME")
	// The XDG spec says to ignore a relative path.
	if !filepath.IsAbs(data) {
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(data, "commonplace", "commonplace.db")
}

// legacyDBPath is the default before cmpl followed the XDG spec.
func legacyDBPath(home string) string {
	return filepath.Join(home, ".commonplace", "commonplace.db")
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// openStore opens the database on first use so commands that never touch it
// (version, help) don't create it.
func (a *App) openStore() (*store.Store, error) {
	if a.store != nil {
		return a.store, nil
	}
	path, err := a.dbPath()
	if err != nil {
		return nil, err
	}
	if home, err := a.home(); err == nil && path == legacyDBPath(home) {
		fmt.Fprintf(a.stderr, "note: using the database at its old location, %s. "+
			"To use the new default, stop cmpl and move commonplace.db and any "+
			"-wal and -shm files to %s\n", filepath.Dir(path), filepath.Dir(a.defaultDBPath(home)))
	}
	s, err := store.Open(path)
	if err != nil {
		return nil, err
	}
	a.store = s
	return s, nil
}

func (a *App) closeStore() {
	if a.store != nil {
		a.store.Close()
		a.store = nil
	}
}
