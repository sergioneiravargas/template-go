# AGENTS.md - cmd (entry points and DI wiring)

Read the root [`AGENTS.md`](../AGENTS.md) first.

Each binary is a single `main.go` that wires the app with **Uber Fx**. There are no other
files here - configuration mapping, DI registration, routing, and lifecycle management all
live in the entry points, on purpose (packages under `internal/` stay wiring-free).

## Binaries

| Dir | Purpose | Serves | Lifecycle specifics |
|---|---|---|---|
| `server` | REST API | chi router on `:3000`; routes under `/api/v1` in public + auth-middleware groups; `/hello-world` web route | starts HTTP server; graceful stop: server -> hub -> queue pool -> AMQP -> DB |
| `socket-server` | Websockets | `/ws/rooms/{room}` on `:3000` (host `3100`) behind `auth.Middleware`; `/ws-client` demo page (no auth) | two hooks: `configureServerLifecycleHooks` (HTTP server; stop: server -> pool -> AMQP -> DB) and `configureBroadcastLifecycleHooks` (`hub.ConsumeMessages` with concurrency 4 -> `hub.BroadcastMessage` per message as it arrives, resubscribe with 1s backoff when the subscription is lost; stop: cancel, wait, `hub.Close()`) - the hub is closed exactly once, in the broadcast hook, which runs first because fx stops hooks in reverse registration order |
| `worker` | Async jobs | nothing (no HTTP) | `go pool.Work(workCtx)` (outbox listener + consumers, push queue workers); stop: `pool.Shutdown(ctx)` drains handlers, then `cancelWork()` stops the outbox consumers, then hub -> AMQP -> DB |

All three binaries start the pprof server on `:6060` when `APP_PROFILER_ENABLED=true`.

## The structure of a main.go

```go
func main() {
    app := fx.New(
        fx.Provide(
            newAppConf, newLogger, newHTTPFetcher,           // app-level
            newSQLConf, newSQLDB, newAMQPConn, newQueuePool, // infra
            newWebsocketHub,                                 // hub: producer everywhere, consumer in socket-server
            newAuthConf, newAuthService,                     // per-slice: Conf -> service
            example.NewRepository, newExampleService,        // repo -> service
            newHTTPHandler, newHTTPServer,                   // server/socket-server only
        ),
        fx.Invoke(setupQueuePool),            // attach example.MessageHandlers to the queue
        fx.Invoke(configureLifecycleHooks),   // start/stop ordering
        fx.NopLogger,
    )
    app.Run()
}
```

Conventions (all enforced by existing code - follow them exactly):

- **`newXConf` functions** are the ONLY place `os.Getenv` appears. They convert types
  (`strconv.Atoi`...) and **panic on missing critical config** (fail fast at boot).
  Every variable they read must exist in `.env.dist`.
- Repositories with a plain `(db *sql.DB)` constructor are registered directly
  (`fx.Provide(example.NewRepository)`); services with options or extra deps get a local
  `newXService` provider.
- Caches (auth user-info) are built in the `newXService` providers with explicit TTLs.
- `newAMQPConn` registers `amqpx.WithOnGiveUp` -> `fx.Shutdowner` exit(1) so the container
  restart policy recovers a dead broker connection.
- `setupQueuePool` is an `fx.Invoke`, not a provider: the queue is built empty by
  `newQueuePool` and the handlers are attached afterwards with
  `queue.WithMessageHandlers(...)`. This breaks the service <-> queue construction cycle.
  All three mains declare the queue topology; only the worker calls `pool.Work`.
- Shutdown order in `OnStop` matters: HTTP server first, then websocket hub, queue pool,
  AMQP connection, DB - keep it when editing hooks. `OnStop` hooks name and use their
  `ctx` parameter (it carries the fx stop deadline): `server.Shutdown(ctx)`,
  `pool.Shutdown(ctx)`, and every wait on a background goroutine selects on `ctx.Done()`.
  Long-lived goroutines started in `OnStart` (broadcast loop, pool work) own a
  cancellable context created in the hook's closure plus a `done` channel that `OnStop`
  waits on. `context.Background()` appears only there and in `newAuthConf`.

## The #1 pitfall: triple wiring

`server`, `socket-server`, and `worker` each maintain **their own copy** of the provider
list and `newXConf`/`newXService` functions. When you:

- add a service/repository/conf a shared component needs,
- change a constructor signature,
- add a queue or message handler,

...you MUST update **every main.go that builds that object**. Grep for the constructor
name across `cmd/` before finishing. A missed main compiles fine with fx and **fails at
startup** with a dependency-resolution error.

## Adding routes (server)

- Public endpoints -> the outer `/api/v1` group (no auth middleware).
- Authenticated endpoints -> the inner group with `r.Use(auth.Middleware(authService))`.
- Handler registration style: `r.Post("/messages", example.CreateMessageAPIHandler(logger, service))`.
- Keep REST-ish naming: plural resources, verbs as sub-resources
  (`/rooms/{room}/broadcast`).

## Adding a websocket endpoint (socket-server)

Register under the `/ws` route group (behind `auth.Middleware`) a slice-provided factory
such as `example.NewRoomWebsocketHandler(upgrader, hub, service, logger)`. The factory
builds a `websocket.ConnectionHandler` and a `userContextProvider` that reads
`auth.UserInfoFromRequest`, then delegates to `websocket.GenericHandler`.

## Adding a new binary

1. `cmd/<name>/main.go` copying the smallest relevant main (`worker`) as a template.
2. Add `build-<name>` to the Makefile (mirroring the arch-switch pattern) and hook it
   into `build`.
3. `docker/<name>/Dockerfile` (COPY `bin/<name>`) + a service in `docker-compose.yaml`
   (+ local overrides in `docker-compose.yaml.local.dist`).
4. Root `AGENTS.md` section 2 and the binaries table above.
