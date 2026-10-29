# Iris: MU Proxy/Gateway

Iris is a Telnet proxy/gateway application written in Go that acts as an intermediary between Telnet clients and servers. It keeps persistent connections to upstream servers, replays recent history to clients when they reconnect, and handles Telnet option negotiation. Upstreams are managed through a REST API and the `iris upstream` command line client.

## Features

- **Persistent Upstreams**:
  - Upstream connections stay open when clients disconnect
  - Multiple clients can share the same upstream connection
  - Per-upstream passwords, stored as bcrypt hashes
  - Configurable connect script sent to the upstream on connection
- **Session History**:
  - Persistent session history stored in dated log files
  - Automatic history trimming (default 20KB)
  - History replay for new connections to existing upstreams
  - SIGHUP signal handling to reopen history files without disconnecting
- **Telnet Protocol Support**:
  - RFC-compliant option negotiation (Suppress Go Ahead, End of Record, Transmit Binary, Charset)
  - Subnegotiation handling for character set negotiation
  - Event-driven architecture for protocol handling
- **Management API**:
  - JSON REST API for adding, editing, deleting, and listing upstreams
  - Listing of active upstream sessions
  - `iris upstream` command line client
- **Observability**:
  - Structured logging with Zerolog (JSON format)
  - Request logging for the management API
  - Detailed Telnet event tracing
- **Standards Compliant**: Supports Telnet RFCs 854, 855, 856, 857, 858, 885, 930, 1073, 2066

## Getting Started

### Prerequisites

- Go 1.25 or later
- [goose](https://github.com/pressly/goose) for database migrations
- A C compiler, for the [go-sqlite3](https://github.com/mattn/go-sqlite3) driver

### Installation

```bash
# Clone the repository
git clone https://github.com/stesla/iris.git
cd iris

# Build the application
go build

# Optional: Install globally
go install
```

### Database Setup

Iris stores upstreams in a SQLite database. Create it by running the migrations:

```bash
goose -dir migrations sqlite3 ./iris.db up
```

### Running the Server

Iris requires a configuration file (see [Configuration](#configuration)). A minimal `config.yaml` in the current directory:

```yaml
db: ./iris.db
```

Then start the server:

```bash
./iris serve
```

By default this starts the Telnet proxy on port 4042 and the management API on `localhost:40042`.

### Adding an Upstream

Use the command line client to add an upstream server. The password is used both to authenticate to Iris and as the upstream account's password in the connect script:

```bash
./iris upstream add mygame secret -a game.example.com:4201 -l mycharacter
```

### Connecting a Client

Connect with any Telnet client:

```bash
telnet localhost 4042
```

The first thing the client sends must be a JSON object naming the upstream and its password:

```json
{"name": "mygame", "password": "secret"}
```

If the upstream is already connected, Iris attaches the client to it and replays recent history. Otherwise Iris connects to the upstream's address, sends the connect script, and starts recording history. From then on, everything the client sends goes to the upstream.

When opening a new upstream connection, the client can also set upstream options:

```json
{"name": "mygame", "password": "secret", "options": {"force_suppress_go_ahead": "true"}}
```

| Option | Description | Values |
|--------|-------------|--------|
| `always_allow_charset` | Allow charset negotiation without Transmit Binary | `true` or `false` |
| `force_suppress_go_ahead` | Force suppression of GA signals | `true` or `false` |

Options are ignored when joining an upstream that is already connected.

## Configuration

Iris reads `config.yaml` (or any format supported by [Viper](https://github.com/spf13/viper), e.g. `config.json` or `config.toml`) from the current directory or `$HOME/.config/iris/`. A configuration file is required, even if it only sets defaults.

Every setting can also be overridden with an environment variable: prefix the key with `IRIS_`, uppercase it, and replace `.` with `_`.

| Key | Environment Variable | Default | Description |
|-----|---------------------|---------|-------------|
| `addr` | `IRIS_ADDR` | `:4042` | Address the Telnet proxy listens on |
| `db` | `IRIS_DB` | `./iris.db` | Path to the SQLite database |
| `api.server.addr` | `IRIS_API_SERVER_ADDR` | `localhost:40042` | Address the management API listens on |
| `api.client.url` | `IRIS_API_CLIENT_URL` | `http://localhost:40042` | Base URL `iris upstream` uses to reach the API |
| `log.level` | `IRIS_LOG_LEVEL` | `info` | Log level (trace, debug, info, warn, error, fatal, panic) |
| `log.dir` | `IRIS_LOG_DIR` | `./logs` | Directory for session history logs |

Example `config.yaml`:

```yaml
addr: :4042
db: /var/lib/iris/iris.db
api.server.addr: localhost:40042
api.client.url: http://localhost:40042
log.level: info
log.dir: /var/lib/iris/logs
```

The management API has no transport security, and adding an upstream requires no authentication. Keep `api.server.addr` bound to localhost, or otherwise restrict who can reach it.

> Older versions used the keys `grpc.server.addr` and `grpc.client.addr`. These are no longer read; rename them to `api.server.addr` and `api.client.url` (note that the client setting is now a URL).

## Command Line Client

`iris upstream` manages upstreams through the management API, using `api.client.url` to find the server.

```bash
# Add an upstream (requires --address and --login)
iris upstream add NAME PASSWORD -a HOST:PORT -l LOGIN [-s SCRIPT]

# List upstreams
iris upstream list

# Edit an upstream; only the given flags are changed
iris upstream edit NAME PASSWORD [-n NEW_NAME] [-p NEW_PASSWORD] [-a HOST:PORT] [-l LOGIN] [-s SCRIPT]

# Delete an upstream
iris upstream delete NAME PASSWORD
```

| Flag | Description |
|------|-------------|
| `-a`, `--address` | Upstream server address (`host:port`) |
| `-l`, `--login` | Login used in the connect script |
| `-s`, `--script` | Connect script |
| `-n`, `--name` | New name (`edit` only) |
| `-p`, `--password` | New password (`edit` only) |

The connect script is sent to the upstream when Iris connects to it. `%LOGIN%` and `%PASSWORD%` are replaced with the upstream's login and the password the client logged in with. If no script is set, Iris sends `connect %LOGIN% %PASSWORD%`.

The client exits with status 1 if a request fails.

## Management API

The management API is a JSON REST API served at `api.server.addr`. Request and response bodies are JSON (`Content-Type: application/json`), and field names use `snake_case`.

| Method | Path | Description | Auth | Success |
|--------|------|-------------|------|---------|
| `GET` | `/sessions` | List active upstream sessions | None | `200 OK` |
| `GET` | `/upstreams` | List upstreams | None | `200 OK` |
| `POST` | `/upstreams` | Add an upstream | None | `201 Created` |
| `PATCH` | `/upstreams/{name}` | Edit an upstream | Basic | `204 No Content` |
| `DELETE` | `/upstreams/{name}` | Delete an upstream | Basic | `204 No Content` |

### Authentication

Editing and deleting an upstream require HTTP Basic authentication, with the upstream's name as the username and its password as the password. The username must match `{name}` in the path.

```bash
curl -u mygame:secret -X DELETE http://localhost:40042/upstreams/mygame
```

### `GET /sessions`

Lists upstreams that are currently connected, sorted by name. `time_started` is when the upstream connection was opened, in RFC 3339 format.

```json
{
  "sessions": [
    {"upstream_name": "mygame", "time_started": "2026-10-28T12:00:00Z"}
  ]
}
```

### `GET /upstreams`

Lists all configured upstreams, sorted by name. Passwords and connect scripts are never returned.

```json
{
  "upstreams": [
    {"name": "mygame", "address": "game.example.com:4201", "login": "mycharacter"}
  ]
}
```

### `POST /upstreams`

Adds an upstream. `name`, `address`, `login`, and `password` are required; `script` is optional.

```json
{
  "name": "mygame",
  "address": "game.example.com:4201",
  "login": "mycharacter",
  "password": "secret",
  "script": "connect %LOGIN% %PASSWORD%"
}
```

Adding an upstream does not connect to it; Iris connects the first time a client logs in to it.

### `PATCH /upstreams/{name}`

Edits an upstream. All fields are optional, and only the fields present are changed. An empty object (`{}`) changes nothing.

```json
{
  "new_name": "othergame",
  "new_password": "hunter2",
  "address": "game.example.org:23",
  "login": "othercharacter",
  "script": "connect %LOGIN% %PASSWORD%"
}
```

Changes apply to future connections; an upstream that is already connected keeps its current connection.

### `DELETE /upstreams/{name}`

Deletes an upstream. The request has no body. An upstream that is already connected stays connected, but clients can no longer log in to it once it disconnects.

### Errors

Errors return a JSON body with a message:

```json
{"error": "incorrect password"}
```

| Status | Meaning |
|--------|---------|
| `400 Bad Request` | Malformed JSON, unknown or missing fields, or Basic auth username doesn't match the path |
| `401 Unauthorized` | Missing Basic auth or incorrect password |
| `404 Not Found` | No upstream with that name |
| `405 Method Not Allowed` | Unsupported method for the path |
| `409 Conflict` | An upstream with that name already exists |
| `500 Internal Server Error` | Unexpected server error (details are logged by the server) |

Request bodies are limited to 1 MiB, and unknown fields are rejected.

### Go Client

The [`api`](api) package contains the request and response types and a Go client:

```go
c := api.NewClient("http://localhost:40042")
upstreams, err := c.ListUpstreams(ctx)
```

Errors from the server are returned as `*api.Error`, which includes the HTTP status code.

## Architecture

Iris follows a modular architecture with clear separation of concerns:

- *main.go*: Application entry point
- *cmd/root.go*: Root command and configuration loading
- *cmd/serve/*: The `iris serve` command
  - *serve.go*: Starts the Telnet proxy and management API, handles signals
  - *session.go*: Session management for downstream (client) and upstream (server) connections, and session history
  - *rest.go*: Management API handlers
  - *log.go*: Zerolog integration for Telnet event tracing
- *cmd/upstream/*: The `iris upstream` command line client
- *api/*: Management API types and Go client
- *migrations/*: Database migrations, run with goose
- *internal/event/*: Lightweight event dispatcher for decoupled protocol handling
- *internal/telnet/*: Complete Telnet protocol implementation including:
  - Option negotiation state machine
  - Character set and Transmit Binary handling
  - Subnegotiation processing
  - Protocol-level encoding transformation

### Data Flow

1. Upstreams are added to the database through the management API
2. Client connects to the Iris Telnet listener (cmd/serve/serve.go)
3. New downstream session is created (cmd/serve/session.go)
4. Client sends a JSON login naming an upstream and its password
5. Iris attaches the client to the existing upstream connection, or connects and sends the connect script
6. Upstream sessions maintain persistent history in log files, replayed to newly attached clients
7. Data flows bidirectionally between client and server with protocol translation
8. Telnet options are negotiated and handled according to RFC specifications
9. Events are logged for observability and debugging

## Development

### Project Structure
```
iris/
├── main.go              # Application entry point
├── go.mod               # Go module definition
├── LICENSE              # GPLv3.0 license
├── README.md            # This file
├── api/                 # Management API types and Go client
│   ├── client.go
│   ├── client_test.go
│   └── types.go
├── cmd/
│   ├── root.go          # Root command and configuration
│   ├── serve/           # `iris serve`
│   │   ├── log.go       # Logging integration
│   │   ├── rest.go      # Management API handlers
│   │   ├── rest_test.go
│   │   ├── serve.go     # Server startup and signal handling
│   │   └── session.go   # Session management logic
│   └── upstream/        # `iris upstream`
│       └── upstream.go
├── migrations/          # goose database migrations
└── internal/
    ├── event/           # Event system implementation
    │   ├── event.go
    │   └── event_test.go
    └── telnet/          # Telnet protocol implementation
        ├── constants.go # Protocol constants
        ├── encoding.go  # Character set handling
        ├── encoding_test.go
        ├── events.go    # Event type definitions
        ├── option.go    # Option negotiation state machine
        ├── option_test.go
        ├── telnet.go    # Telnet protocol wrapper
        └── telnet_test.go
```

### Building from Source

```bash
# Clone and enter directory
git clone https://github.com/stesla/iris.git
cd iris

# Build
go build -o iris

# Run tests
go test ./...

# Run with debug logging
IRIS_LOG_LEVEL=debug go run . serve
```

### Adding Features

1. *New Telnet Options*: 
   - Add constants to [internal/telnet/constants.go](internal/telnet/constants.go)
   - Implement handling in [internal/telnet/option.go](internal/telnet/option.go) and [internal/telnet/telnet.go](internal/telnet/telnet.go)
   - Add tests in [internal/telnet/option_test.go](internal/telnet/option_test.go) and [internal/telnet/telnet_test.go](internal/telnet/telnet_test.go)

2. *New Upstream Options*:
   - Add the option to `upstream.setOption` in [cmd/serve/session.go](cmd/serve/session.go)

3. *New API Endpoints*:
   - Add request and response types to [api/types.go](api/types.go) and a method to [api/client.go](api/client.go)
   - Add the route and handler in [cmd/serve/rest.go](cmd/serve/rest.go)
   - Add tests in [api/client_test.go](api/client_test.go) and [cmd/serve/rest_test.go](cmd/serve/rest_test.go)

4. *Logging Enhancements*:
   - Modify [cmd/serve/log.go](cmd/serve/log.go) LogHandler.Listen method
   - Add new event types to [internal/telnet/events.go](internal/telnet/events.go) if needed

### Testing

Run the full test suite:
```bash
go test ./...
```

Run tests for specific packages:
```bash
# Telnet protocol tests
go test ./internal/telnet/...

# Event system tests
go test ./internal/event/...

# Management API tests
go test ./api/... ./cmd/serve/...
```

## License

Iris is licensed under the GNU General Public License v3.0. See the [LICENSE](LICENSE) file for details.

## Support

For issues, questions, or contributions, please use the GitHub issue tracker associated with this repository.

-----

*Copyright (c) 2023 Samantha Tesla*
