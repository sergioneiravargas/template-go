# AGENTS.md - internal/platform (infrastructure layer)

Read the root [`AGENTS.md`](../../AGENTS.md) first.

`internal/platform` contains **domain-agnostic** infrastructure wrappers. The single hard
rule: **platform packages must never import a slice under `internal/`** (the reverse is
expected). Keep each package generic - if a change needs domain knowledge, it belongs in
the owning slice, not here. Constructors here may `panic` on unrecoverable setup errors (they run at
process startup); runtime paths return wrapped errors.

## Package inventory & usage rules

### `sql` — database access
Type aliases over `database/sql` (`DB`, `Tx`, `Row`, `Rows`, `NullString`, `NullTime`,
`NullFloat64`, `ErrNoRows`, `ErrTxDone`) with the pgx stdlib driver registered, plus
`Conf`/`NewDB` (connection string, `sslmode=disable`, optional max pool conns), the
transaction helper `WithTx(ctx, db, fn)` (begins with `BeginTx(ctx, nil)`, commits when
`fn` returns nil, rolls back otherwise), `Listen(ctx, db, channel, onNotify)` (dedicated
`LISTEN` connection, discarded on return, used by the outbox consumers), and the generic
list primitives
`Filter{Column, Operator, Value}`, `Sorting{Column, Direction}` and operator/direction
constants.
**Rule:** slices import THIS, never `database/sql` or pgx directly. Repositories take
`ctx` as first parameter, use the `*Context` query/exec variants, and wrap
multi-statement writes in `sql.WithTx` (see `internal/AGENTS.md` section 5). Dynamic
filters must go through a hard-coded column whitelist built on `sql.Filter`/`sql.Sorting`.

### `log` — structured logging
`Logger` wraps `log/slog` with a JSON handler. API: `Debug/Info/Warn/Error(msg string,
ctx log.Context)` where `log.Context = map[string]any`. Output keys: `level`, `message`,
`timestamp`, `producer` (app name), `context`. Level derives from `APP_ENV`
(prod → Info, dev → Debug; anything else panics).
**Rule:** always pass a `log.Context` (or `nil`); put the error under key `"error"` as a
string (`err.Error()`). Never use `fmt.Println`/stdlib `log` in application code.

### `amqpx` — self-healing RabbitMQ connection
`ConnectionManager` owns the AMQP connection, watches for closure, re-dials with
exponential backoff, and re-runs registered topology on every reconnect. Key surface:
- `Conn` interface (`Channel()`) — code against this, satisfied by both the manager and a
  raw `*amqp.Connection` (tests).
- `TopologyRegistrar.RegisterTopology(fn)` — register exchange/queue declarations so they
  survive broker restarts (both `queue` and `websocket` do this via their `setupTopology`
  helper; copy that helper pattern for new consumers).
- `WithOnGiveUp(fn)` — invoked when the reconnect window is exhausted; the mains use it
  to `fx.Shutdowner`-exit so the orchestrator restarts the process.

### `queue` — job queue over RabbitMQ + transactional outbox
- `Message{ID, Name, Body(JSON), Delay, RetryCount, MaxRetries}`; build with
  `NewMessage(name, body, opts...)`; decode with `queue.DecodeMessage[T](m)`.
- `Queue` (one per slice, e.g. `example.NewQueue`) declares a durable queue bound to
  the direct exchange, the **delayed** exchange (`x-delayed-message` plugin — REQUIRED,
  used for `MessageWithDelay` and retries), and a `.deadletter` companion queue.
- `MessageHandler{CanHandleFunc, HandlerFunc}` with
  `HandlerFunc: func(ctx context.Context, msg *Message) error`: the ctx derives from the
  worker lifecycle and is cancelled only after the graceful drain window. Handler errors
  trigger retry with exponential backoff `30s·2^(n−1)` up to `MaxRetries` (default 5),
  then dead-lettering with an `x-death-reason` header. Messages are ACKed after handling
  either way (retries are app-level re-publishes, not broker redeliveries).
- `Pool` groups queues; `Pool.Work(ctx)` (worker binary only) runs one consumer loop per
  queue (up to `workerCount` handlers in parallel each)
  plus the **outbox consumer**: `CreateOutboxMessage(ctx, tx, queueName, msgs...)`
  inserts into `queue_outbox` inside the caller's DB transaction and raises
  `pg_notify('queue_outbox')` on commit; consumers claim rows one at a time with
  `FOR UPDATE SKIP LOCKED`, publish, and delete/retry in the same transaction
  (at-least-once semantics — handlers must be idempotent). Shutdown is two-stage:
  `Pool.Shutdown(ctx)` stops fetching and waits for in-flight handlers (bounded by ctx);
  cancelling the ctx passed to `Work` then stops the outbox consumers.
- **No polling while idle.** Each queue holds one long-lived push subscription
  (`channel.Consume`, manual ack) with prefetch = `workerCount` and blocks on the broker;
  every delivery is handed to a handler goroutine the moment it arrives, bounded by a
  `workerCount` semaphore — the same pattern as `websocket.Hub.ConsumeMessages`. The
  subscription is re-established with a 1 s backoff when it is lost. Outbox consumers block on a
  wake-up channel fed by a dedicated `LISTEN queue_outbox` connection (`sql.Listen`),
  with a 5 s safety poll while listening and a 100 ms poll only while the listener is
  reconnecting. `FetchMessage(ctx)` (tests, tooling) also blocks on a temporary push
  consumer.
**Rule:** producers in request paths ALWAYS go through the outbox; direct
`queue.Dispatch` is for the framework itself (retries) and tooling.

### `websocket` — hub + AMQP broadcast
`Hub` tracks `topic → clients`; `NewHub(amqpConn)` declares a topic exchange +
non-durable broadcast queue. Two halves:
- **Producers** (server/worker): `hub.Publish(Message{Topic, Body})` → AMQP.
- **Consumer** (socket-server): `hub.ConsumeMessages(ctx, concurrency, logger, handle)`
  blocks on the broker while idle (no polling), delivers each message to `handle` the
  moment it arrives, and runs at most `concurrency` handlers in parallel with the same
  value as broker prefetch (manual ack after handling). `handle` calls
  `hub.BroadcastMessage(msg)` to write to local clients (bounded fan-out, per-connection
  write lock + deadlines, dead clients pruned). It returns when the subscription is
  lost; the caller resubscribes with a backoff. This consumer diverges from
  coordinando-backend, whose `FetchMessages` batches up to 4 messages or 2 seconds.

All writes to a client connection (`WriteMessage`, `SendSuccessMessage`,
`SendErrorMessage`, `SendJSONMessage`, `Client.SafeWriteMessage`, the ping keepalive and
`BroadcastMessage`) are serialized by one lock per connection (`connLocks`), because
gorilla/websocket allows a single concurrent writer and handler replies, pings and
broadcasts run in different goroutines. Never call `conn.WriteMessage` directly. This
lock also diverges from coordinando-backend, which only guards broadcast writes.

`GenericHandler(topic, upgrader, hub, connectionHandler, userContextProvider, logger)`
builds an HTTP handler that authenticates (via the provider), upgrades, registers the
client, runs ping/pong keepalive (60s pong wait), and dispatches messages to a
`ConnectionHandler{OnConnect, OnDisconnect, OnMessage}` - implement that interface in the
owning slice (reference: `internal/example/websocket_handler.go`).

### `cache` — in-memory generic TTL cache
`cache.New[K, V](cache.WithTTL[K, V](d), cache.WithCleanupInterval[K, V](d))` →
mutex-guarded map with background eviction. Used for the auth user-info cache.
Per-process only - do not treat as shared state across binaries.

### `validation` — shared validators
`ValidateEmail`, `ValidatePhone`, `ValidateRUT` (Chilean tax ID, mod-11). Add new
cross-slice validators here WITH table-driven tests; slice-specific rules stay in the
slice's `Validate()` methods.

### `httpfetch` — outbound HTTP
`Fetcher` interface (`Get`, `Do` returning `*Response`) with a retrying client
(`hashicorp/go-retryablehttp`) behind `NewClient(logger)`.
**Rule:** services depend on the `Fetcher` interface (injected), never on `http.Client`,
so tests can fake HTTP.

### `debug` — pprof
`StartPProfServer(":6060")` - started by every binary when `APP_PROFILER_ENABLED=true`.
Collected via `make profile-local` (`scripts/pprof-report.sh`).

## Adding a new platform package

1. Only if it is genuinely domain-agnostic and reused (or clearly reusable) — otherwise
   put it in the owning slice.
2. Provide: a `Conf` struct (populated in `cmd/*/main.go` from env), a constructor, and
   interfaces for anything consumers need to mock.
3. Unit tests with fakes (see `amqpx_test.go`, `queue_test.go`, `httpfetch_test.go` for
   the house style — interface-based fakes, no live infrastructure).
4. Document the env vars in `.env.dist` and register providers in the relevant mains.
