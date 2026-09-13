# CLAUDE.md - template-go

The full agent guide is maintained in AGENTS.md (tool-agnostic). It is imported here so
Claude Code always loads it:

@AGENTS.md

## Directory-scoped guides (read BEFORE editing files there)

- `internal/AGENTS.md` - vertical slice patterns: service/repository/handler/queue
  messages/websockets/testing + change checklists
- `internal/platform/AGENTS.md` - infrastructure packages and their usage rules
- `cmd/AGENTS.md` - Fx wiring, env->Conf mapping, lifecycle hooks, the triple-wiring pitfall

## Quick reminders (details in AGENTS.md)

- Vertical slices under `internal/<domain>/`; design order: model -> interfaces ->
  service -> repository -> handler. `internal/platform` never imports a slice.
- Build/test/migrate ONLY via `make` targets (`make build`, `make test`,
  `make migration-*`) - they load `.env` and use the right containers/flags.
- Definition of done = `make check` green (fmt + vet + build + test). Never improvise
  ad-hoc validation command mixes.
- `ctx context.Context` first param end-to-end (handler -> service -> repository -> I/O);
  single-input DTOs with `Validate()`; raw SQL with `$n` placeholders; error wrapping
  with `%w` + sentinel errors; async side effects via the transactional outbox.
- Env vars are read only in `cmd/*/main.go` (`newXConf` functions) and every new one goes
  to `.env.dist`. Never hardcode secrets.
- New/changed providers must be wired in EVERY `cmd/*/main.go` that needs them.
- Docs ship in the same change as the code they describe: apply every matching row of
  the sync matrix in AGENTS.md before finishing. No emojis in docs.
