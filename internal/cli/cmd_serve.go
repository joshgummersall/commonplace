package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/joshgummersall/commonplace/internal/web"
	"github.com/spf13/cobra"
)

func (a *App) newServeCommand() *cobra.Command {
	var (
		host   string
		port   int
		devDir string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the web app",
		Long: `Serve the Commonplace web app: the daily shuffle, search, and notes.

It listens on localhost by default. The app has no login, so only use
--host 0.0.0.0 (for example, to read on your phone) on a network you trust.`,
		Example: `  cmpl serve
  cmpl serve --port 9000`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.openStore()
			if err != nil {
				return err
			}
			logger := slog.New(slog.NewTextHandler(a.stderr, nil))
			srv, err := web.New(s, web.Options{DevDir: devDir, Logger: logger})
			if err != nil {
				return err
			}

			addr := net.JoinHostPort(host, strconv.Itoa(port))
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return err
			}
			httpSrv := &http.Server{
				Handler:           srv.Handler(),
				ReadHeaderTimeout: 10 * time.Second,
			}
			fmt.Fprintf(a.stdout, "Serving Commonplace at http://%s\n", ln.Addr())

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			errc := make(chan error, 1)
			go func() { errc <- httpSrv.Serve(ln) }()
			select {
			case err := <-errc:
				return err
			case <-ctx.Done():
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := httpSrv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "host", "127.0.0.1", "address to listen on")
	cmd.Flags().IntVar(&port, "port", 8420, "port to listen on")
	cmd.Flags().StringVar(&devDir, "dev", "", "serve templates and static files from this directory, reloading templates per request")
	cmd.Flags().MarkHidden("dev")
	return cmd
}
