# Instructions

- Always run `go fmt` when making changes
- Run tasks through mise (`mise run test`, `mise run build`); there is no Makefile
- Schema changes go in a new `internal/store/migrations/NNNN_description.sql`; never edit an applied migration
- `mise run dev` serves with live reload: templates and static files reload per request (`--dev`), Go and SQL changes rebuild and restart via watchexec
- Tests never call a real Ollama: CLI tests inject a fake through `App.newEmbedder`
- Full personal exports live in `testdata/*/*-full.txt` (gitignored); tests that use them skip when absent
