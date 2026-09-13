# AGENTS.md - internal (domain slices)

Read the root [`AGENTS.md`](../AGENTS.md) first. This guide documents the **canonical
patterns of a vertical slice**. `internal/example` is the reference slice: it shows a
REST handler, a service, a repository with a transactional outbox write, a queue message
handler, and a websocket connection handler.

## 1. Slice inventory

| Package | Responsibility | Notable files |
|---|---|---|
| `auth` | JWT (RS256) validation against local PEM keys and a remote JWKS, user info from claims or the OIDC userinfo endpoint, HTTP middleware, request-context helpers | `middleware.go`, `service.go`, `token.go`, `user_info.go` |
| `example` | Reference slice: messages (REST + outbox), `message_created` queue handler, websocket rooms, embedded browser client | `model.go`, `interfaces.go`, `repository.go`, `service.go`, `handler.go`, `queue.go`, `websocket_handler.go`, `websocket_client.go` |

### Inter-package dependency rules

- `slice -> internal/platform`: always allowed.
- `slice -> slice`: only `example -> auth` (handlers read `auth.UserInfoFromRequest`).
  **Never create a cycle.** To let a "lower" package use a "higher" one, define a
  consumer-side interface in the lower package and implement it upstream, or bridge with
  a function type constructed in `cmd/*/main.go`.

## 2. Standard slice anatomy

For a slice with one resource, files are unprefixed (`model.go`, `service.go`,
`repository.go`, `handler.go`). For multiple resources, prefix per resource
(`workorder_service.go`, `contact_service.go`, ...).

| File | Contents |
|---|---|
| `model.go` | Entities, value objects, enums (typed string consts), `Conf` struct, input DTOs with `Validate()`, topic/event constants |
| `interfaces.go` | ALL interfaces the slice's services depend on (repos, publishers, other slices), hand-written, with `var _ Iface = (*Impl)(nil)` compile-time checks |
| `x_repository.go` | SQL implementation |
| `x_service.go` | Business logic; sentinel errors |
| `x_handler.go` | HTTP handler factories + the local `httpError` JSON helper |
| `x_queue.go` | Queue name, `NewQueue`, message names, payload structs, `MessageHandlers(...)` |
| `x_websocket_handler.go` | `websocket.ConnectionHandler` implementation + `NewXWebsocketHandler` factory |
| `x_test.go`, `mocks_test.go` | Table-driven tests + hand-rolled mocks |

Design order for a new slice is mandated: model -> interfaces -> service -> repository
-> handler.

## 3. Naming conventions

| Thing | Pattern | Example |
|---|---|---|
| Constructor | `NewX` / `NewService` / `NewRepository` | `example.NewService` |
| Input DTO | `<Verb><Entity>Input` | `CreateMessageInput`, `BroadcastRoomInput` |
| HTTP handler factory | `<Verb><Entity>APIHandler` | `CreateMessageAPIHandler` |
| Sentinel error | `Err<Condition>` | `ErrMessageNotFound` |
| Queue message name const | `MessageName<Action>` = `snake_case` string | `MessageNameMessageCreated = "message_created"` |
| Queue payload struct | `Message<Action>` | `MessageMessageCreated` |
| Websocket event type | `EventType<Action>` = `snake_case` string | `EventTypeMessageCreated = "message_created"` |
| Enum | typed string + `<Type><Value>` consts | |
| DB table | `<slice>_<entity>` | `example_message`, `queue_outbox` |

## 4. Service pattern

```go
var ErrMessageNotFound = errors.New("log not found") // package-level sentinels

type Service struct { repository MessageRepository; hub EventPublisher; logger *log.Logger }

func NewService(repository MessageRepository, hub EventPublisher, logger *log.Logger) *Service {
    if repository == nil { panic("repository is required") } // constructors panic on nil deps
    ...
}

func (s *Service) CreateMessage(ctx context.Context, input CreateMessageInput) (*Message, error) {
    if err := input.Validate(); err != nil { return nil, err }
    entry := &Message{ID: uuid.NewString(), Body: input.Body, CreatedAt: time.Now()}
    message, err := queue.NewMessage(MessageNameMessageCreated, MessageMessageCreated{MessageID: entry.ID})
    ...
    if err := s.repository.CreateMessage(ctx, entry, message); err != nil { return nil, err }
    return entry, nil
}
```

Rules:
- One exported method per use case; `ctx context.Context` first, then **exactly one input
  struct parameter** (plus plain `id string` args only for trivial getters). Propagate
  `ctx` to every repository and I/O call; never store it in a struct.
- `Validate()` is called **inside the service method** (mandatory) and optionally in the
  handler for early 400s.
- IDs: `uuid.NewString()`. Timestamps: `time.Now()` set in the service.
- Dependencies are interfaces declared in **this** slice's `interfaces.go`
  (`MessageRepository`, `EventPublisher`) so tests use hand-rolled mocks.
- Optional dependencies use functional options (`auth.ServiceWithUserInfoCache`,
  `auth.ServiceWithFetcher`).

## 5. Repository pattern

- Depend on `*sql.DB` from `internal/platform/sql` (aliases of `database/sql`) - never
  import `database/sql` or pgx directly in a slice.
- Raw SQL strings with `$1..$n`. Explicit column lists in SELECT/INSERT (no `SELECT *`).
- `Get*` single-row: `sql.ErrNoRows` -> return `(nil, nil)`; the **service** converts nil
  to its `ErrNotFound` sentinel.
- Nullable columns scan into `sql.NullString` / `sql.NullTime`, then map to pointer fields.
- Errors: `fmt.Errorf("failed to <action>: %w", err)` - lowercase, action-specific.

### Transactional writes with outbox

Mutations whose side effects must be published only after commit take a variadic tail of
queue messages, all persisted in **one transaction**:

```go
func (r *Repository) CreateMessage(ctx context.Context, entry *Message, queueMessages ...*queue.Message) error {
    return sql.WithTx(ctx, r.db, func(tx *sql.Tx) error {
        tx.ExecContext(ctx, "INSERT INTO example_message (id, body, created_at) VALUES ($1, $2, $3)", ...)
        queue.CreateOutboxMessage(ctx, tx, QueueName, queueMessages...) // outbox, same tx
        return nil
    })
}
```

`sql.WithTx` commits when the closure returns nil, rolls back otherwise. **Never dispatch
to AMQP directly from a repository or request path** - the outbox + worker guarantees
at-least-once delivery after commit.

## 6. Handler pattern

Handlers are **closure factories** `func XxxAPIHandler(logger *log.Logger, service *Service) http.HandlerFunc`,
registered in `cmd/server/main.go` (and `cmd/socket-server/main.go` for `/ws`). Canonical
body order:

0. `ctx := r.Context()` - first line; every service call receives it.
1. `userInfo, ok := auth.UserInfoFromRequest(r)` -> 401 if missing (skip for public routes).
2. Path params via `chi.URLParam(r, "id")` -> 400 if empty.
3. Decode body with `json.NewDecoder(r.Body).Decode(&input)` -> log + 400 on failure.
4. **Overwrite identity fields from the token / path** (`input.UserID = userInfo.ID`,
   `input.Room = chi.URLParam(r, "room")`) - never trust client-supplied identity.
5. `input.Validate()` -> 400 with the validation message.
6. Call the service; map errors: `errors.Is(err, ErrMessageNotFound)` -> 404, otherwise
   `logger.Error(...)` + generic 500. Never leak internal error strings to clients.
7. Respond with `Content-Type: application/json`; 201 for creates, 202 for accepted
   async work.

Errors are written as the JSON envelope `{"error": "..."}` through the slice-local
`httpError` helper.

Logging: `logger.Error("Human sentence", log.Context{"error": err.Error(), "id": id})` -
message in English prose, machine data in the context map.

## 7. Async jobs (queue messages)

Per message, in `x_queue.go`:

```go
const MessageNameMessageCreated = "message_created"

type MessageMessageCreated struct { MessageID string `json:"message_id"` } // IDs only, never entities

func messageCreatedMessageHandler(service *Service, logger *log.Logger) *queue.MessageHandler {
    return &queue.MessageHandler{
        CanHandleFunc: func(m *queue.Message) bool { return m.Name == MessageNameMessageCreated },
        HandlerFunc: func(ctx context.Context, m *queue.Message) error {
            msgBody, ok := queue.DecodeMessage[MessageMessageCreated](m)
            if !ok { logger.Error(...); return nil }        // malformed -> DROP (nil), never retry
            entry, err := service.repository.GetMessage(ctx, msgBody.MessageID)
            if err != nil { return fmt.Errorf(...) }         // transient -> retry with backoff, then DLQ
            if entry == nil { return nil }                   // stale -> drop
            ...
        },
    }
}
```

Rules:
- Payloads carry **IDs, not entities** - handlers re-fetch current state.
- Return `nil` to drop (malformed, stale, already handled); return an error only when a
  retry can succeed. Max 5 retries, exponential backoff (30s * 2^(n-1)), then the
  dead-letter queue. Handlers must be idempotent (at-least-once delivery).
- `ctx` comes from the worker lifecycle and is cancelled only after the graceful drain.
- Register the handler in `MessageHandlers(...)`; it is wired into the pool by
  `setupQueuePool` in **all three** mains.
- Producers enqueue via the repository transaction (outbox, section 5). Delayed messages
  use `queue.MessageWithDelay(ms)`.

## 8. Websocket pattern

- Topics: `example:{room}` (`RoomTopic(room)`); the worker publishes `message_created` events
  to `example:logs`.
- Implement `websocket.ConnectionHandler` (`OnConnect`/`OnDisconnect`/`OnMessage`) in
  `x_websocket_handler.go`; `OnConnect` is where subscription authorization belongs.
- The interface carries no request context; callbacks create their own root with
  `context.Background()` (commented as such) - this is the one sanctioned exception to
  rule 4 of the root guide.
- Incoming messages are the JSON envelope `{id, type, data}`; reply with the platform
  helpers `websocket.SendSuccessMessage(conn, type, data)` / `websocket.SendErrorMessage`.
  Never call `conn.WriteMessage` directly: the helpers take the per-connection write lock
  that also guards pings and broadcasts, and `OnMessage` runs concurrently with both.
- Server-side events: services call `hub.Publish(websocket.Message{Topic, Body})` (through
  the slice's `EventPublisher` interface) -> AMQP broadcast exchange -> socket-server fans
  out to clients. Services never write to client connections directly. The body is the
  `Event{Type, Data}` envelope.
- The HTTP factory (`NewRoomWebsocketHandler`) validates path params, builds the
  `userContextProvider` from `auth.UserInfoFromRequest`, and delegates to
  `websocket.GenericHandler`. Routes are registered only in `cmd/socket-server/main.go`
  under `/ws/...` behind `auth.Middleware`.

## 9. Testing pattern

- Stdlib `testing` only; **table-driven** tests or `t.Run` subtests.
- Mocks are **hand-rolled** in `mocks_test.go`: a struct with one `XxxFunc func(...)`
  field per interface method. No mock libraries, no codegen.
- Service tests cover validation rules, sentinel error mapping, and that the right repo
  calls / queue messages / published events were produced.
- Queue handler tests exercise the drop / retry / success branches.
- No live broker/DB in unit tests. Full suite: `make test`.

## 10. Checklists

**Add a field to an existing entity**
1. `model.go`: add the field (pointer if nullable).
2. New migration (`make migration-create`): `ALTER TABLE ... ADD COLUMN ...` + down.
3. Repository: update EVERY `INSERT`, `UPDATE`, `SELECT` column list and `Scan`.
4. Input DTOs + `Validate()` if it is writable; handlers if it is client-supplied.
5. Update mocks if an interface changed; extend tests.
6. `make check`.

**Add a new endpoint to an existing slice**
1. Input DTO + `Validate()` -> service method -> (repo method if needed).
2. Handler factory following section 6.
3. Route in `cmd/server/main.go` (correct group: public vs. auth-middleware).
4. Tests for the service method. `make check`.

**Create a new slice**
1. `mkdir internal/<domain>`; write `model.go` -> `interfaces.go` -> service ->
   repository -> handler.
2. Migration(s) creating `<domain>_<entity>` tables.
3. Wire constructors + `Conf` mapping in every `cmd/*/main.go` that needs the slice, and
   routes in `cmd/server/main.go`.
4. New env vars -> `.env.dist`.
5. Tests + mocks. Update section 1 above. `make check`.
