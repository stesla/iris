# Iris: MU Proxy/Gateway

Iris is a Telnet proxy/gateway application written in Go that acts as an intermediary between Telnet clients and servers. It keeps persistent connections to upstream servers, replays recent history to clients when they reconnect, and handles Telnet option negotiation. Upstreams are managed through a REST API and the `iris upstream` command line client.

## Features

- **Persistent Upstreams**:
  - Upstream connections stay open when clients disconnect
  - Multiple clients can share the same upstream connection
  - Configurable connect script sent to the upstream on connection
- **Security**:
  - One Iris password, stored as a bcrypt hash, for both Telnet clients and the management API
  - Upstream (game) passwords encrypted at rest with XChaCha20-Poly1305, using a key kept outside the database
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

### Setting the Iris Password

Iris has a single password, used to log in from Telnet clients and by the `iris upstream` command line client. Set it with:

```bash
./iris password
```

This prompts for the password twice, or reads it from the first line of standard input if that isn't a terminal. It writes directly to the database, so it works whether or not the server is running. Run it again to change the password. It refuses a database that doesn't exist or hasn't been migrated, before prompting.

### Running the Server

Iris requires a configuration file (see [Configuration](#configuration)). A minimal `config.yaml` in the current directory:

```yaml
db: ./iris.db
```

Then start the server:

```bash
./iris serve
```

By default this starts the Telnet proxy on port 4042 and the management API on `localhost:40042`. The server refuses to start if the database doesn't exist or hasn't been migrated, or if no Iris password has been set.

On first start, Iris creates the encryption key for upstream passwords at `secret.key_file` (default `./iris.key`); see [Encryption Key](#encryption-key).

### Adding an Upstream

Use the command line client to add an upstream server. The password is the upstream account's password, which Iris sends in the connect script. With `-p -`, you're prompted for it, which keeps it out of your shell history:

```bash
./iris upstream add mygame -a game.example.com:4201 -l mycharacter -p -
```

The command line client prompts for the Iris password, unless it is set in `api.client.password` (see [Configuration](#configuration)).

### Connecting a Client

Connect with any Telnet client:

```bash
telnet localhost 4042
```

The first thing the client sends must be a JSON object naming the upstream, with the Iris password:

```json
{"name": "mygame", "password": "iris-password"}
```

If the upstream is already connected, Iris attaches the client to it and replays recent history. Otherwise Iris connects to the upstream's address, sends the connect script with the upstream's stored password, and starts recording history. Iris gives up connecting after 30 seconds. If another client is already connecting the upstream, Iris waits for that to finish and then attaches this client too, or fails its login with `upstream failed to connect`. From then on, everything the client sends goes to the upstream, including anything sent right after the login, which follows the connect script. The one exception is a line ending directly after the login, which only finishes the login line.

The login must arrive within 30 seconds and be at most 64 KiB. If it fails, Iris sends a line saying why (for example `login failed: incorrect Iris password`) and disconnects. The Iris password is checked before the upstream is looked up, so a client without it can't find out which upstreams exist. For the same reason, an unexpected error before the password has been checked, such as a database error, is only reported as `internal error`; the details go to the server log.

When opening a new upstream connection, the client can also set upstream options:

```json
{"name": "mygame", "password": "iris-password", "options": {"force_suppress_go_ahead": "true"}}
```

| Option | Description | Values |
|--------|-------------|--------|
| `always_allow_charset` | Allow charset negotiation without Transmit Binary | `true` or `false` |
| `force_suppress_go_ahead` | Force suppression of GA signals | `true` or `false` |

Options are ignored when joining an upstream that is already connected, or that another client is still connecting.

## Configuration

Iris reads `config.yaml` (or any format supported by [Viper](https://github.com/spf13/viper), e.g. `config.json` or `config.toml`) from the current directory or `$HOME/.config/iris/`. A configuration file is required, even if it only sets defaults.

Set `IRIS_CONFIG` to the path of a configuration file to use that file instead, for both the server and the command line tools. It takes priority over a `config.yaml` in the current directory, and Iris exits with an error if the file doesn't exist rather than falling back to those locations.

Every setting can also be overridden with an environment variable: prefix the key with `IRIS_`, uppercase it, and replace `.` with `_`.

| Key | Environment Variable | Default | Description |
|-----|---------------------|---------|-------------|
| `addr` | `IRIS_ADDR` | `:4042` | Address the Telnet proxy listens on |
| `db` | `IRIS_DB` | `./iris.db` | Path to the SQLite database |
| `secret.key_file` | `IRIS_SECRET_KEY_FILE` | `./iris.key` | Key used to encrypt upstream passwords; created on first start |
| `api.server.addr` | `IRIS_API_SERVER_ADDR` | `localhost:40042` | Address the management API listens on |
| `api.client.url` | `IRIS_API_CLIENT_URL` | `http://localhost:40042` | Base URL `iris upstream` uses to reach the API |
| `api.client.password` | `IRIS_API_CLIENT_PASSWORD` | (none) | Iris password `iris upstream` sends; prompted for if not set |
| `log.level` | `IRIS_LOG_LEVEL` | `info` | Log level (trace, debug, info, warn, error, fatal, panic) |
| `log.dir` | `IRIS_LOG_DIR` | `./logs` | Directory for session history logs |

Example `config.yaml`:

```yaml
addr: :4042
db: /var/lib/iris/iris.db
secret.key_file: /var/lib/iris/iris.key
api.server.addr: localhost:40042
api.client.url: http://localhost:40042
log.level: info
log.dir: /var/lib/iris/logs
```

Relative paths are relative to the directory Iris is started in, so use absolute paths when running Iris as a service.

Neither the Telnet proxy nor the management API uses TLS, so the Iris password crosses the network in cleartext. Keep `api.server.addr` bound to localhost, and only expose `addr` on networks you trust. If you put `api.client.password` in a configuration file, make sure only you can read it.

### Encryption Key

Upstream passwords are stored encrypted, so Iris can send them to games without keeping them in plaintext. The key is a separate file, `secret.key_file`, which holds 64 hex digits:

- Iris creates it with mode `0600` the first time it starts, and refuses to start if it can be read by anyone but its owner.
- **Back it up separately from the database.** A backup that holds both defeats the encryption. Because the key is plain hex, you can keep a copy in a password manager.
- If the key is lost, the stored upstream passwords can't be recovered. Set each one again with `iris upstream edit NAME -p -`.

### Upgrading from Per-Upstream Passwords

Earlier versions had no Iris password: each upstream's password was used to log in to Iris and was stored only as a bcrypt hash. Those hashes can't be turned back into passwords, so after upgrading:

1. Back up the database, then stop Iris and run the migrations.
2. Set the Iris password with `iris password`.
3. Start Iris, and back up the newly created key file.
4. Set each upstream's password again with `iris upstream edit NAME -p -`. Until you do, logging in to that upstream fails with `login failed: upstream password not set`.
5. Change your Telnet clients to send the Iris password in their login.

> Older versions used the keys `grpc.server.addr` and `grpc.client.addr`. These are no longer read; rename them to `api.server.addr` and `api.client.url` (note that the client setting is now a URL).

## Command Line Client

`iris upstream` manages upstreams through the management API, using `api.client.url` to find the server. It authenticates with the Iris password from `api.client.password`, or prompts for it.

```bash
# Add an upstream (requires --address, --login, and --password)
iris upstream add NAME -a HOST:PORT -l LOGIN -p PASSWORD|- [-s SCRIPT]

# List upstreams
iris upstream list

# Edit an upstream; only the given flags are changed
iris upstream edit NAME [-n NEW_NAME] [-p PASSWORD|-] [-a HOST:PORT] [-l LOGIN] [-s SCRIPT]

# Delete an upstream
iris upstream delete NAME
```

| Flag | Description |
|------|-------------|
| `-a`, `--address` | Upstream server address (`host:port`) |
| `-l`, `--login` | Login used in the connect script |
| `-s`, `--script` | Connect script |
| `-n`, `--name` | New name (`edit` only) |
| `-p`, `--password` | Upstream password, or `-` to be prompted for it |

With `-p -`, the upstream password is prompted for twice. If standard input isn't a terminal, it is read from the first line of standard input instead, so it can be piped in from a password manager.

Upstream names can be up to 64 letters, digits, `.`, `_`, and `-`, and must start with a letter or digit. Names are used in history log file names.

The connect script is sent to the upstream when Iris connects to it. `%LOGIN%` and `%PASSWORD%` are replaced with the upstream's login and its stored password. If no script is set, Iris sends `connect %LOGIN% %PASSWORD%`. To go back to that default, clear the script with `iris upstream edit NAME -s ""`. The script is the only setting that can be cleared: giving any other flag an empty value is an error.

Arguments and flags are checked before any password is prompted for. The client exits with status 1 if a request fails.

## Commands

| Command | Description |
|---------|-------------|
| `iris serve` | Run the Telnet proxy and management API |
| `iris password` | Set or change the Iris password |
| `iris upstream` | Manage upstreams through the management API |

## Management API

The management API is a JSON REST API served at `api.server.addr`. Request and response bodies are JSON (`Content-Type: application/json`), and field names use `snake_case`.

| Method | Path | Description | Auth | Success |
|--------|------|-------------|------|---------|
| `GET` | `/sessions` | List active upstream sessions | Basic | `200 OK` |
| `GET` | `/upstreams` | List upstreams | Basic | `200 OK` |
| `POST` | `/upstreams` | Add an upstream | Basic | `201 Created` |
| `PATCH` | `/upstreams/{name}` | Edit an upstream | Basic | `204 No Content` |
| `DELETE` | `/upstreams/{name}` | Delete an upstream | Basic | `204 No Content` |

### Authentication

Every request requires HTTP Basic authentication with the Iris password. The username is ignored for now and reserved for when Iris supports multiple users; the Go client and `iris upstream` send `iris`.

```bash
curl -u iris:iris-password http://localhost:40042/upstreams
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

Adds an upstream. `name`, `address`, `login`, and `password` are required; `script` is optional, and leaving it out or empty means the default. `password` is the upstream account's password, which is stored encrypted. `name` must follow the [naming rules](#command-line-client).

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

Edits an upstream. All fields are optional, and only the fields present are changed. An empty object (`{}`) changes nothing. An empty `script` clears the script, so Iris goes back to sending the default; the other fields can't be empty.

```json
{
  "new_name": "othergame",
  "password": "hunter2",
  "address": "game.example.org:23",
  "login": "othercharacter",
  "script": "connect %LOGIN% %PASSWORD%"
}
```

Changes apply to future connections; an upstream that is already connected keeps its current connection. A rename takes effect immediately for `GET /sessions` and for logging in, but the connected upstream's history log keeps its old file name until histories are reopened (on SIGHUP).

> Before this version, the new password field was named `new_password`.

### `DELETE /upstreams/{name}`

Deletes an upstream. The request has no body. If the upstream is connected, Iris disconnects it and every client attached to it.

### Errors

Errors return a JSON body with a message:

```json
{"error": "incorrect Iris password"}
```

| Status | Meaning |
|--------|---------|
| `400 Bad Request` | Malformed JSON, unknown, missing, or empty fields, or an invalid upstream name |
| `401 Unauthorized` | Missing Basic auth or incorrect Iris password |
| `404 Not Found` | No upstream with that name |
| `405 Method Not Allowed` | Unsupported method for the path |
| `409 Conflict` | An upstream with that name already exists |
| `500 Internal Server Error` | Unexpected server error (details are logged by the server) |
| `503 Service Unavailable` | No Iris password has been set |

Authentication is checked first, so an unauthenticated request gets `401` even for an unknown path or method.

Request bodies are limited to 1 MiB, and unknown fields are rejected.

### Go Client

The [`api`](api) package contains the request and response types and a Go client:

```go
c := api.NewClient("http://localhost:40042", irisPassword)
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
- *cmd/password/*: The `iris password` command
- *api/*: Management API types and Go client
- *migrations/*: Database migrations, run with goose
- *internal/auth/*: Checking and setting the Iris password
- *internal/database/*: Opening the database, refusing one that is missing or unmigrated
  - *dbtest/*: Databases for tests, built by applying the real migrations
- *internal/secret/*: Encryption of upstream passwords, and the key file
- *internal/prompt/*: Password prompts for the command line
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
4. Client sends a JSON login naming an upstream, with the Iris password
5. Iris attaches the client to the existing upstream connection, or decrypts the upstream's password, connects, and sends the connect script
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
│   ├── password/        # `iris password`
│   │   ├── password.go
│   │   └── password_test.go
│   ├── serve/           # `iris serve`
│   │   ├── log.go       # Logging integration
│   │   ├── rest.go      # Management API handlers
│   │   ├── rest_test.go
│   │   ├── serve.go     # Server startup and signal handling
│   │   ├── serve_test.go
│   │   ├── session.go   # Session management logic
│   │   └── session_test.go
│   └── upstream/        # `iris upstream`
│       ├── upstream.go
│       └── upstream_test.go
├── migrations/          # goose database migrations
│   └── embed.go         # Embeds the migrations, for tests
└── internal/
    ├── auth/            # The Iris password
    │   ├── auth.go
    │   └── auth_test.go
    ├── database/        # Opening the database
    │   ├── database.go
    │   ├── database_test.go
    │   └── dbtest/      # Migrated databases for tests
    ├── event/           # Event system implementation
    │   ├── event.go
    │   └── event_test.go
    ├── prompt/          # Command line password prompts
    │   ├── prompt.go
    │   └── prompt_test.go
    ├── secret/          # Upstream password encryption
    │   ├── secret.go
    │   └── secret_test.go
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

# Management API and session tests
go test ./api/... ./cmd/serve/...

# Encryption, password, and command line tests
go test ./internal/secret/... ./internal/auth/... ./internal/database/... ./internal/prompt/... ./cmd/password/... ./cmd/upstream/...
```

## License

Iris is licensed under the GNU General Public License v3.0. See the [LICENSE](LICENSE) file for details.

## Support

For issues, questions, or contributions, please use the GitHub issue tracker associated with this repository.

-----

*Copyright (c) 2023 Samantha Tesla*
