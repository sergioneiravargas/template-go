# Go starter template
A quickstart for web services in Go with OIDC based authentication. It ships three
binaries (`server`, `socket-server`, `worker`) wired with Uber Fx, PostgreSQL, RabbitMQ
with a transactional outbox, websockets, and one reference slice (`internal/example`)
exercising every path end to end. The architecture and conventions are documented in
`AGENTS.md`.

## Requirements
- **GNU Make:** *optional, enables some useful commands*
- **Docker**: *required*
- **Docker Compose:** *required*

## Setup
Execute the following steps to set the project's configuration for the first time.

### Step 1
Create the **.env** file in the **project's root** directory.

**Note:** *use the .env.dist file as template.*

### Step 2
Create the **PEM key** files in the **project's root** directory.

**Note:** *use `make gen-keys` to quickly generate the credentials.*

### Step 3
Create the **docker-compose.yaml.local** file in the **project's root** directory.

**Note:** *use the docker-compose.yaml.local.dist file as template.*

### Step 4
Run the following command from the **project's root** directory:
```
make init
```
**Note:** *this will set the name for the project's Go module.*

## Running
```
make run            # build the binaries and start the stack
make migration-up   # apply migrations (empty count = all)
make logs           # tail a service (server, socket-server, worker, ...)
make check          # fmt + vet + build + test: the definition of done
```

- REST API on `http://localhost:3000/api/v1` (JWT in `Authorization: Bearer`).
- Websocket client page on `http://localhost:3100/ws-client` (JWT pasted into the page;
  room `messages` receives the events produced by the worker).
- A token signed with `private.pem` is accepted, so you can mint test tokens yourself:
  `export TOKEN=$(./scripts/mint-token.sh)`. `AUTH_KEYSET_URL` must still point at a
  reachable JWKS because the binaries fetch it at boot.
- `make check` runs the outbox integration tests against the database in `.env`. Stop
  the worker first (`docker stop <APP_NAME>.worker`) so it does not consume the test rows.
