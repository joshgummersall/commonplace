// Package cli wires the cmpl cobra commands.
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

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
	}
}

// embedder returns the configured embedder: --ollama-url, else
// $OLLAMA_HOST, else Ollama's default address.
func (a *App) embedder() embedding.Embedder {
	if a.newEmbedder != nil {
		return a.newEmbedder()
	}
	url := a.ollamaURL
	if url == "" {
		url = a.getenv("OLLAMA_HOST")
	}
	return embedding.NewOllama(url, a.model)
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
keyword and meaning, and serves a daily shuffle of past highlights.

Semantic search uses embeddings from a local Ollama model. Without Ollama,
everything else works and search falls back to keywords.

The database lives at ~/.commonplace/commonplace.db unless overridden by
$` + dbEnvVar + ` or --db.`,
	}
	root.PersistentFlags().StringVar(&a.dbFlag, "db", "", "database path (default $"+dbEnvVar+" or ~/.commonplace/commonplace.db)")
	root.PersistentFlags().StringVar(&a.ollamaURL, "ollama-url", "", "Ollama address for embeddings (default $OLLAMA_HOST or "+embedding.DefaultOllamaURL+")")
	root.PersistentFlags().StringVar(&a.model, "embed-model", embedding.DefaultModel, "Ollama embedding model; changing it re-embeds every highlight")
	root.CompletionOptions.DisableDefaultCmd = true

	root.AddCommand(
		a.newImportCommand(),
		a.newEvalCommand(),
		a.newServeCommand(),
		a.newVersionCommand(),
	)
	return root
}

func (a *App) newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintf(a.stdout, "cmpl %s\n", version)
			return err
		},
	}
}

// dbPath resolves the database path: --db, then $COMMONPLACE_DB, then the
// default under the home directory.
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
	return filepath.Join(home, ".commonplace", "commonplace.db"), nil
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
