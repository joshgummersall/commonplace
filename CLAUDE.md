# Instructions

- Always run `go fmt` when making changes
- Run tasks through mise (`mise run test`, `mise run build`); there is no Makefile
- Schema changes go in a new `internal/store/migrations/NNNN_description.sql`; never edit an applied migration
