# Iris: MU Proxy/Gateway

Iris is a Telnet proxy/gateway application written in Go that acts as an intermediary between Telnet clients and servers. It provides secure, managed Telnet connections with features including password authentication, session history persistence, Telnet option negotiation, event-driven protocol handling, and structured logging.

## Features

- **Secure Authentication**: Password-protected access for Telnet clients
- **Session Management**: 
  - Persistent session history stored in timestamped log files
  - Automatic history trimming (default 20KB)
  - History replay for new connections to existing upstreams
  - SIGHUP signal handling to reload histories without disconnecting
- **Telnet Protocol Support**:
  - RFC-compliant option negotiation (Suppress Go Ahead, End of Record, Transmit Binary, Charset)
  - Subnegotiation handling for character set negotiation
  - Event-driven architecture for protocol handling
- **Flexible Connection Handling**:
  - Connect to multiple upstream servers via commands
  - Separate downstream (client) and upstream (server) session management
  - Graceful shutdown with SIGINT/SIGTERM
- **Observability**:
  - Structured logging with Zerolog (JSON format)
  - Configurable log levels
  - Detailed Telnet event tracing
- **Standards Compliant**: Supports Telnet RFCs 854, 855, 856, 857, 858, 885, 930, 1073, 2066

## Getting Started

### Prerequisites

- Go 1.24.1 or later
- Git (for cloning the repository)

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

### Basic Usage

Iris requires a password for client authentication. You can provide this via command-line flag or environment variable.bash
# Using command-line flag
```bash
./iris -password mysecretpassword
```

# Using environment variable
```bash
IRIS_PASSWORD=mysecretpassword ./iris
```

By default, Iris listens on TCP port 4001. Clients can connect using any Telnet client:
```bash
telnet localhost 4001
```

Upon connection, clients must authenticate by sending:
`login mysecretpassword`

After authentication, clients can use the following commands:
- `connect <address>` - Connect to a new upstream Telnet server
- `upstream <key>` - Switch to an existing upstream session (by key)
- `option <name> <value>` - Configure upstream options (see Configuration below)
- `send <data>` - Send raw data to the current upstream
- Any other text is sent directly to the current upstream connection

## Configuration

### Command-Line Flags

| Flag | Environment Variable | Default | Description |
|------|---------------------|---------|-------------|
| `-addr` | `IRIS_ADDR` | `:4001` | TCP address to listen on |
| `-password` | `IRIS_PASSWORD` | *(required)* | Password for client authentication |
| `-level` | `IRIS_LOG_LEVEL` | `info` | Log level (trace, debug, info, warn, error, fatal, panic) |
| `-logdir` | `IRIS_LOG_DIR` | `./logs` | Directory for session history logs |

### Upstream Options

After connecting to an upstream server, you can configure these options using the =option= command:

| Option | Description | Values |
|--------|-------------|--------|
| `always_allow_charset` | Allow charset negotiation without Transmit Binary | `true` or `false` |
| `force_suppress_go_ahead` | Force suppression of GA signals | `true` or `false` |

Example usage:
```
option always_allow_charset true
option force_suppress_go_ahead true
```

## Architecture

Iris follows a modular architecture with clear separation of concerns:

- *main.go*: Application entry point handling flags, signals, and connection acceptance
- *session.go*: Core session management for downstream (client) and upstream (server) connections
- *log.go*: Zerolog integration for Telnet event tracing
- *internal/event/*: Lightweight event dispatcher for decoupled protocol handling
- *internal/telnet/*: Complete Telnet protocol implementation including:
  - Option negotiation state machine
  - Character set and Transmit Binary handling
  - Subnegotiation processing
  - Protocol-level encoding transformation

### Data Flow

1. Client connects to Iris listener (main.go)
2. New downstream session created (session.go)
3. Client authenticates with password
4. Client issues commands to manage upstream connections
5. Upstream sessions maintain persistent history in log files
6. Data flows bidirectionally between client and server with protocol translation
7. Telnet options are negotiated and handled according to RFC specifications
8. Events are logged for observability and debugging

## Development

### Project Structure
```
iris/
├── main.go              # Application entry point
├── session.go           # Session management logic
├── log.go               # Logging integration
├── go.mod               # Go module definition
├── LICENSE              # GPLv3.0 license
├── README.md            # This file
├── internal/
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

# Run with verbose output
go run main.go -password test -level debug
```

### Adding Features

1. *New Telnet Options*: 
   - Add constants to [internal/telnet/constants.go](internal/telnet/constants.go)
   - Implement handling in [internal/telnet/option.go](internal/telnet/option.go) and [internal/telnet/telnet.go](internal/telnet/telnet.go)
   - Add tests in [internal/telnet/option_test.go](internal/telnet/option_test.go) and [internal/telnet/telnet_test.go](interna/telnet/telnet_test.go)

2. *New Commands*:
   - Extend the command parser in [session.go](session.go) (downstreamSession.findUpstream)
   - Add handler methods to upstreamSession or downstreamSession as needed

3. *Logging Enhancements*:
   - Modify [log.go](log.go) LogHandler.Listen method
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
```

## License

Iris is licensed under the GNU General Public License v3.0. See the [LICENSE](LICENSE) file for details.

## Support

For issues, questions, or contributions, please use the GitHub issue tracker associated with this repository.

-----

*Copyright (c) 2023 Samantha Tesla*

