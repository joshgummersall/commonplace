# Instructions

- Always run `go fmt` when making changes
- Run tasks through mise (`mise run test`, `mise run build`); there is no Makefile
- Schema changes go in a new `internal/store/migrations/NNNN_description.sql`; never edit an applied migration
- `mise run dev` serves with live reload: templates and static files reload per request (`--dev`), Go and SQL changes rebuild and restart via watchexec
- Tests never call a real Ollama: CLI tests inject a fake through `App.newEmbedder`
- Full personal exports live in `testdata/*/*-full.txt` (gitignored); tests that use them skip when absent

## Agent skills

### Issue tracker

Issues live in GitHub Issues on joshgummersall/commonplace, through the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Triage labels

Default labels: `needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: one root `CONTEXT.md` and `docs/adr/`. See `docs/agents/domain.md`.
