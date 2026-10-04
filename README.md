# Go starter template
A quickstart for web services in Go with internal email/password authentication
(argon2id password hashes, JWT access tokens and rotating refresh tokens). It ships three
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
- Auth demo page on `http://localhost:3000/auth-client` (register, log in and copy the
  access token from the browser).
- Create an account and log in (after `make migration-up`):
  ```
  curl -X POST http://localhost:3000/api/v1/auth/register \
    -d '{"email":"ada@example.com","password":"correct horse battery","given_name":"Ada"}'
  curl -X POST http://localhost:3000/api/v1/auth/login \
    -d '{"email":"ada@example.com","password":"correct horse battery"}'
  ```
  Login returns `access_token` (15 min JWT for `Authorization: Bearer`) and
  `refresh_token` (30 days, rotate it via `/api/v1/auth/refresh`, revoke it via
  `/api/v1/auth/logout`).
- Password recovery (reset links go out via AWS SES, see the `MAILER_*` vars; the
  emailed link's target is `AUTH_PASSWORD_RESET_URL?token=...`):
  ```
  curl -X POST http://localhost:3000/api/v1/auth/forgot-password \
    -d '{"email":"ada@example.com"}'
  curl -X POST http://localhost:3000/api/v1/auth/reset-password \
    -d '{"token":"<token-from-the-email>","password":"brand new password"}'
  ```
  Forgot-password always answers 202 (unknown emails included); reset-password consumes
  the single-use token (1 h TTL) and revokes every active session.
- Alternatively mint an access token for an existing user directly:
  `export TOKEN=$(./scripts/mint-token.sh private.pem <user-uuid>)` - the `sub` must be
  a real `auth_user.id` or requests get a 401.
- `make check` runs the outbox integration tests against the database in `.env`. Stop
  the worker first (`docker stop <APP_NAME>.worker`) so it does not consume the test rows.
