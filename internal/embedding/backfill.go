package embedding

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/joshgummersall/commonplace/internal/store"
)

// BatchSize is how many highlights go to the embedder per request.
const BatchSize = 32

// Backfill embeds every highlight whose embedding is missing or stale for
// e's model. progress, if set, is called after each batch. It returns how
// many highlights were embedded; on error, the batches already saved stay
// saved, so a later run picks up where this one stopped.
func Backfill(ctx context.Context, s *store.Store, e Embedder, progress func(done, total int)) (int, error) {
	model := e.Model()
	total, err := s.CountStaleEmbeddings(model)
	if err != nil {
		return 0, err
	}
	done := 0
	for done < total {
		if err := ctx.Err(); err != nil {
			return done, err
		}
		jobs, err := s.StaleEmbeddings(model, BatchSize)
		if err != nil {
			return done, err
		}
		if len(jobs) == 0 {
			break
		}
		texts := make([]string, len(jobs))
		ids := make([]int64, len(jobs))
		for i, j := range jobs {
			texts[i], ids[i] = j.Text, j.ID
		}
		vecs, err := e.EmbedDocuments(ctx, texts)
		if err != nil {
			return done, err
		}
		if err := s.SaveEmbeddings(model, ids, vecs, time.Now()); err != nil {
			return done, err
		}
		done += len(jobs)
		if progress != nil {
			progress(min(done, total), total)
		}
	}
	return done, nil
}

// KeepFresh runs Backfill now and then every interval until ctx ends, so
// highlights imported while the embedder was down, or a model change, get
// picked up once it is reachable. It logs when the embedder becomes
// unreachable or reachable again, not on every attempt.
func KeepFresh(ctx context.Context, s *store.Store, e Embedder, interval time.Duration, logger *slog.Logger) {
	unavailable := false
	run := func() {
		n, err := Backfill(ctx, s, e, nil)
		switch {
		case errors.Is(err, ErrUnavailable):
			if !unavailable {
				logger.Warn("embeddings paused; semantic search covers only embedded highlights", "err", err)
			}
			unavailable = true
		case err != nil && ctx.Err() == nil:
			logger.Error("embedding backfill failed", "err", err)
		default:
			if unavailable {
				logger.Info("embedder reachable again")
			}
			unavailable = false
			if n > 0 {
				logger.Info("embedded highlights", "count", n, "model", e.Model())
			}
		}
	}
	run()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}
