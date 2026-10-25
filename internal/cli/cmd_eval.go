package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"text/tabwriter"

	"github.com/joshgummersall/commonplace/internal/eval"
	"github.com/joshgummersall/commonplace/internal/search"
	"github.com/spf13/cobra"
)

func (a *App) newEvalCommand() *cobra.Command {
	var verbose bool
	cmd := &cobra.Command{
		Use:    "eval [file]",
		Short:  "Score search modes against queries with known answers",
		Hidden: true,
		Long: `Score keyword, semantic, and hybrid search against a file of queries
and the highlight ids each should find. Reports mean recall@k and MRR per
mode, to compare models and settings objectively.

The file defaults to eval.yaml next to the database. Format:

` + eval.Example,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 1 {
				path = args[0]
			} else {
				db, err := a.dbPath()
				if err != nil {
					return err
				}
				path = filepath.Join(filepath.Dir(db), "eval.yaml")
			}
			f, err := eval.Load(path)
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("%s does not exist; create it like this:\n\n%s", path, eval.Example)
			}
			if err != nil {
				return err
			}

			s, err := a.openStore()
			if err != nil {
				return err
			}
			sr := search.New(s, a.embedder())
			r, err := eval.Run(cmd.Context(), sr, f)
			if err != nil {
				return err
			}
			a.printEval(r, sr.Embedder.Model(), verbose)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "show each query's rank per mode")
	return cmd
}

func (a *App) printEval(r eval.Report, model string, verbose bool) {
	if len(r.Missing) > 0 {
		fmt.Fprintf(a.stderr, "warning: expected highlights not in this database: %v\n", r.Missing)
	}
	fmt.Fprintf(a.stdout, "%d queries, k=%d, model %s\n\n", len(r.Cases), r.K, model)
	tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "MODE\tRECALL@%d\tMRR\t\n", r.K)
	for _, m := range r.Modes {
		note := ""
		if m.Fallback != "" {
			note = "(keyword fallback: " + m.Fallback + ")"
		}
		fmt.Fprintf(tw, "%s\t%.3f\t%.3f\t%s\n", m.Mode, m.Recall, m.MRR, note)
	}
	tw.Flush()

	if !verbose {
		return
	}
	fmt.Fprintln(a.stdout, "\nFirst expected hit's rank per query (- = not in the top k):")
	tw = tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprint(tw, "QUERY")
	for _, m := range r.Modes {
		fmt.Fprintf(tw, "\t%s", m.Mode)
	}
	fmt.Fprintln(tw, "\t")
	for i, c := range r.Cases {
		fmt.Fprint(tw, c.Query)
		for _, m := range r.Modes {
			cr := m.Cases[i]
			rank := "-"
			if cr.FirstRank > 0 {
				rank = fmt.Sprintf("%d (%d/%d)", cr.FirstRank, cr.Found, len(c.Expect))
			}
			fmt.Fprintf(tw, "\t%s", rank)
		}
		fmt.Fprintln(tw, "\t")
	}
	tw.Flush()
}
