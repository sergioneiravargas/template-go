# AGENTS.md - template-go

Guidance for AI coding agents and new contributors. This is the **root** guide.
Directory-scoped guides exist and MUST be read before editing files under their directory:

| Scope | Guide |
|---|---|
| Domain slices (business logic) | [`internal/AGENTS.md`](internal/AGENTS.md) |
| Platform packages (infrastructure) | [`internal/platform/AGENTS.md`](internal/platform/AGENTS.md) |
| Entry points / DI wiring | [`cmd/AGENTS.md`](cmd/AGENTS.md) |

---

## 1. What this project is

A **starter template** for Go web services with OIDC/JWT authentication. It ships the
wiring, lifecycle, messaging and websocket plumbing of a production service, plus one
reference slice (`internal/example`) that exercises every path end to end:

- REST endpoint that writes a row and enqueues a job through the transactional outbox.
- Worker queue handler that consumes the job and pushes a websocket event.
- Bidirectional websocket room endpoint (ping / echo / broadcast) with a browser client.

Replace `internal/example` with real slices; keep the patterns.

## 2. Runtime topology

One Go module (`github.com/sergioneiravargas/template-go`) produces **three long-running
binaries**, all wired with Uber Fx from `cmd/*/main.go`:

| Binary | Role | Listens |
|---|---|---|
| `cmd/server` | REST API (`/api/v1/...`, chi router) | `:3000` (host `3000`) |
| `cmd/socket-server` | Websocket endpoints (`/ws/...`) + AMQP broadcast fan-out + `/ws-client` demo page | `:3000` (host `3100`) |
| `cmd/worker` | Queue consumers (outbox relay + AMQP message handlers) | no HTTP port |

Every binary starts pprof on `:6060` when `APP_PROFILER_ENABLED=true` (host ports
`6060` / `6070` / `6080` in the local compose overlay).

Shared infrastructure: **PostgreSQL 16** (raw SQL via the pgx stdlib driver, no ORM) and
**RabbitMQ** with the *delayed message exchange* plugin (required, see
`docker/rabbitmq/Dockerfile`).

Event flow between processes:

- Repositories write **outbox messages** (`queue_outbox`) in the same DB transaction as the
  domain change and raise a `NOTIFY` on commit; the **worker**'s pool wakes up, claims rows
  (`FOR UPDATE SKIP LOCKED`) and publishes them to RabbitMQ; each queue holds one push
  subscription (prefetch = concurrency, same model as the websocket broadcast consumer)
  and queue handlers (`example.MessageHandlers`) process messages in parallel goroutines
  with retries (exponential backoff, max 5) and a dead-letter queue. Nothing
  polls while idle.
- Services publish websocket events via `websocket.Hub.Publish` (AMQP broadcast exchange);
  the **socket-server** keeps one long-lived subscription (`hub.ConsumeMessages`, prefetch
  and concurrency 4) and writes each event to the subscribed clients by topic
  (`example:{room}`) the moment it arrives. All writes to a client connection are
  serialized by a per-connection lock.

### Timing reference

Every interval below is fixed in code; none is configurable through env.

| Component | Behavior | Value | Where |
|---|---|---|---|
| socket-server broadcast consumer | blocks on the broker while idle; delivers per message; prefetch = concurrency | 4 in flight; 1 s backoff on lost subscription | `cmd/socket-server/main.go`, `websocket.go` `ConsumeMessages` |
| websocket keepalive | ping period / pong wait / write deadline | 54 s / 60 s / 10 s | `websocket/handler.go`, `websocket.go` |
| socket-server HTTP | handshake read and write timeout; no idle timeout | 5 s | `cmd/socket-server/main.go` |
| worker queue consumers | one push subscription per queue, blocks while idle; prefetch = concurrency; resubscribe backoff | 4 in flight; 1 s | `queue.go` `consume` |
| worker outbox relay | woken by `NOTIFY queue_outbox` on commit; safety poll while listening; poll while listener reconnects | 10 consumers; 5 s; 100 ms | `outbox.go`, `sql.go` `Listen` |
| message retries (queue and outbox) | exponential backoff, then dead-letter | 30 s, 60 s, 120 s, 240 s, 480 s | `queue.go` `calculateExponentialBackoff` |
| AMQP connection | heartbeat / dial timeout / reconnect backoff / give-up window | 10 s / 30 s / 1 s to 30 s with jitter / 2 min then exit(1) | `amqpx.go` |
| auth user-info cache | TTL / cleanup interval | 10 min / 30 s | `cmd/*/main.go` `newAuthService` |
| outbound HTTP (`httpfetch`) | timeout / attempts / backoff | 10 s / 3 / 200 ms to 10 s | `httpfetch.go` |
| fx start and stop | timeouts | 15 s each | fx defaults |
| Docker stop grace | before SIGKILL | 10 s | compose default |

## 3. Tech stack

| Concern | Choice | Notes |
|---|---|---|
| Language | Go 1.25 (`go.mod`), tests run in `golang:1.25-alpine` | via `make test` container |
| HTTP router | `go-chi/chi/v5` | `chi` v1 is ALSO imported, only for `chi/middleware` in `cmd/*` - keep that split as-is |
| DI | `go.uber.org/fx` | providers + lifecycle hooks in `cmd/*/main.go` only |
| DB | `jackc/pgx/v5` through `database/sql` (`internal/platform/sql` aliases) | raw SQL, `$1..$n` placeholders |
| Messaging | `rabbitmq/amqp091-go` wrapped by `internal/platform/amqpx` (self-healing) + `internal/platform/queue` | delayed exchange plugin required |
| Websockets | `gorilla/websocket` wrapped by `internal/platform/websocket` | |
| Auth | `golang-jwt/jwt/v5`, RS256, PEM keys on disk + external JWKS | no sessions, no cookies |
| Outbound HTTP | `hashicorp/go-retryablehttp` wrapped by `internal/platform/httpfetch` | injected as `httpfetch.Fetcher` |
| Logging | stdlib `log/slog` wrapped by `internal/platform/log` | JSON, `producer` + `context` keys |
| IDs | `google/uuid` -> `uuid.NewString()` | |

## 4. Repository map

```
cmd/                  Entry points (fx wiring, env->Conf mapping, routes)  -> cmd/AGENTS.md
internal/             Vertical slices: auth, example                        -> internal/AGENTS.md
internal/platform/    Domain-agnostic infra: amqpx, cache, debug, httpfetch,
                      log, queue, sql, validation, websocket               -> internal/platform/AGENTS.md
migrations/           golang-migrate SQL pairs (NNNNNN_domain_desc)
docker/               Dockerfiles (server, socket-server, worker, rabbitmq+plugin)
scripts/              pprof-report.sh (profiling), check-gate.sh (validation gate),
                      mint-token.sh (local test JWT)
bin/                  Build output (gitignored)
.claude/              Claude Code settings (Stop hook -> scripts/check-gate.sh)
Makefile              The ONLY sanctioned way to build/test/migrate
docker-compose.yaml   Base services (server, socket-server, worker)
docker-compose.yaml.local[.dist]  Local overrides (rabbitmq, postgres, adminer, pprof ports)
.env[.dist]           All configuration (never commit .env)
```

## 5. Golden rules (non-negotiable)

1. **Vertical slice architecture.** All logic for a domain lives in one package under
   `internal/<domain>/`. Never create generic `controllers/`, `services/`, `models/` dirs.
2. **`internal/platform` never imports a slice.** Platform code is domain-agnostic.
3. **Raw SQL only** with `$1..$n` placeholders, through `internal/platform/sql` aliases.
   No ORM, no query builders.
4. **Every service method takes `ctx context.Context` first, then a single input DTO**
   (`CreateXInput`) with a `Validate()` method, and the service calls `Validate()` first.
   `ctx` flows request -> handler (`r.Context()`) -> service -> repository -> I/O; it is
   never stored in structs, and `context.Background()` appears only at program roots
   (`cmd/*/main.go` lifecycle hooks and DI factories, websocket connection callbacks,
   tests). No `context.TODO()` in production code.
5. **Error handling:** wrap with `fmt.Errorf("context: %w", err)`; define package-level
   sentinel errors (`var ErrMessageNotFound = errors.New(...)`); handlers map sentinels via
   `errors.Is` to HTTP statuses. Repository `Get*` returns `(nil, nil)` on no rows.
6. **Async side effects go through the outbox** in the same transaction as the domain
   write (`queue.CreateOutboxMessage(ctx, tx, QueueName, msgs...)`). Never publish
   directly to AMQP from a request path when a DB write is involved.
7. **Configuration only via env vars**, read with `os.Getenv` **only in `cmd/*/main.go`**,
   mapped into per-package `Conf` structs and injected. Every new variable is added to
   `.env.dist` (placeholder, no real value). Never hardcode secrets. Fail fast (panic)
   on missing critical config at startup.
8. **Use Makefile targets** for build, test, and migrations - never raw `go build`,
   `go test`, or `migrate` (they depend on `.env` loading and Docker context). See section 6.
9. **Migrations**: sequential 6-digit prefix, domain-prefixed name, `.up.sql` AND
   `.down.sql`, created with `make migration-create`. Never edit or renumber an applied
   migration.
10. **Definition of done: `make check` green** (fmt -> vet -> build -> test). Run it before
    declaring any change finished; never improvise ad-hoc validation command mixes.
    Match the existing style of the file you edit, including its comment density (low).
11. **A new provider/service must be wired into EVERY `cmd/*/main.go` that needs it.**
    The three mains duplicate DI wiring on purpose; forgetting one is the most common
    integration bug. See `cmd/AGENTS.md`.

## 6. Everyday workflows (Makefile)

`make` loads `.env` automatically (`include .env`). Interactive targets prompt on stdin.

| Task | Command | Notes |
|---|---|---|
| Build all binaries | `make build` | cross-compiles to `bin/` (arm64 when `APP_ENV=prod`, else amd64); required BEFORE `make up` since Docker images COPY `bin/*` |
| Build one binary | `make build-server` / `build-socket-server` / `build-worker` | |
| Run full local stack | `make run` (= build + up) | `docker compose` with base + local overlay |
| Stop / teardown | `make stop` / `make down` | |
| Tests | `make test` | runs `go test -count=1 ./...` inside a `golang:1.25-alpine` container with `.env`; requires Docker; reuses the `${APP_NAME}.test` container (after `.env` changes, `docker rm ${APP_NAME}.test` first) |
| Full validation suite | `make check` | fmt -> vet -> build -> test; the definition-of-done gate; on success records the `bin/.last-check` fingerprint via `scripts/check-gate.sh record`. Stop the `worker` container first when the local stack is running (see section 10) |
| Static analysis / format | `make vet` / `make fmt` | on the host |
| New migration | `make migration-create` | prompts for name - use `domain_snake_case_description` |
| Apply / rollback | `make migration-up` / `make migration-down` | prompts for count (empty = all up) |
| Fix dirty migration state | `make migration-fix` | prompts for last known good version |
| Generate JWT keys | `make gen-keys` | writes `private.pem` / `public.pem` |
| Logs / shell / stats for a service | `make logs` / `make exec` / `make stats` | prompts for service name (`server`, `socket-server`, `worker`, ...) |
| CPU/mem profiling | `make profile-local` | pprof via `scripts/pprof-report.sh`; needs `APP_PROFILER_ENABLED=true` |
| Rename the Go module | `make init` | one-shot; rewrites the module path everywhere and removes itself from the Makefile |

Unit tests are pure (hand-rolled mocks, no DB/broker needed), so during iteration
`go test ./internal/<pkg>/...` also works - but run `make check` before declaring done.

## 7. Configuration reference (`.env.dist`)

| Prefix | Variables | Consumed by |
|---|---|---|
| `APP_` | `NAME`, `ENV` (`prod`\|`dev` only), `PROFILER_ENABLED` | all binaries |
| `SQL_` | `USER, PASSWORD, HOST, PORT, DATABASE, MAX_POOL_CONN` | `sql.Conf` |
| `AMQP_` | `USER, PASSWORD, HOST, PORT` | `amqpx.Config` (also compose rabbitmq) |
| `AUTH_` | `KEYSET_URL` (JWKS, fetched at boot), `USERINFO_URL`, `PRIVATE_KEY_FILE`, `PUBLIC_KEY_FILE` | `auth.Conf` |

Secret **files** mounted into containers locally (gitignored): `private.pem`, `public.pem`.

## 8. Authentication model

- `auth.Middleware` extracts a JWT from `Authorization: Bearer` or the `?access_token=`
  query param (the latter exists for websocket handshakes), validates it against the
  local PEM public key first and the JWKS second, then puts token, claims and `UserInfo`
  in the request context.
- Handlers read identity with `auth.UserInfoFromRequest(r)` - **never re-parse tokens**.
- `UserInfo` is built from the token claims; the OIDC userinfo endpoint
  (`AUTH_USERINFO_URL`, via `httpfetch.Fetcher`) is a fallback only.
- There are no roles in the template. Add them to `auth.UserInfo` and gate in handlers
  AND services when you need them.
- Local development: a token signed with `private.pem` is accepted, so test tokens can be
  minted without an identity provider: `export TOKEN=$(./scripts/mint-token.sh)` (optional
  args: key file, `sub`, TTL in seconds). `AUTH_KEYSET_URL` must still be a reachable JWKS
  because `newAuthConf` fetches it at boot.

## 9. Maintaining these docs (sync matrix)

Documentation ships **in the same change** as the code it describes.

| Code change | Docs to update in the same change |
|---|---|
| Env var added / renamed / removed | `.env.dist` + this file section 7 |
| Makefile target added / changed | This file section 6 |
| New slice | `internal/AGENTS.md` section 1 inventory (+ dependency rules) |
| New platform package | `internal/platform/AGENTS.md` inventory |
| New binary | `cmd/AGENTS.md` binaries table, this file section 2, Dockerfile + compose services, Makefile `build-<name>` |
| Queue retry / backoff / DLQ semantics | `internal/platform/AGENTS.md` (queue), `internal/AGENTS.md` section 7 |
| Auth token flow | This file section 8 |
| Renaming any symbol, table, file, or endpoint cited in docs | `grep -rn '<old-name>' --include='*.md' .` and fix every hit |
| Fixing a known quirk | Delete its entry from section 10 below |

Doc style: no emojis; tables for enumerable facts, prose for reasoning; cite real paths
and identifiers from the codebase, never invented examples.

## 10. Known quirks and pitfalls (verified against the current tree)

- **`cmd/*` imports both chi v1 (`chi/middleware`) and chi v5** - intentional; do not
  "fix" imports unless migrating all middleware usage at once.
- **`APP_ENV` accepts only `prod` or `dev`** - anything else panics at startup (and the
  log level derives from it: prod=Info, dev=Debug).
- **`newAuthConf` fetches the JWKS at boot** (`AUTH_KEYSET_URL`). If the identity provider
  is unreachable the binary panics and the container restarts - intended fail-fast.
- **Websocket broadcast is single-replica.** The hub's AMQP broadcast queue is shared, so
  with more than one `socket-server` replica each event reaches only one of them. Use an
  exclusive per-instance queue before scaling the socket-server horizontally.
- **Docker stop grace (10s) is shorter than the fx stop timeout (15s).** A drain longer
  than 10s gets SIGKILLed; add `stop_grace_period` to the compose services if that matters.
- **`make init` trims itself** from the Makefile with `head -n -7`: the `init` target must
  stay the last block of the file.
- **The outbox integration tests share the dev database.** `internal/platform/queue`
  runs its HA tests against the `SQL_*` database from `.env` when it is reachable, using
  the real `queue_outbox` table. A running `worker` container claims those rows and the
  tests fail with `queue not found: test-queue`. Stop the worker
  (`docker stop ${APP_NAME}.worker`) before `make check`, or point `.env` at a separate
  test database.
