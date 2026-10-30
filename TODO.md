# TODO

Known problems and follow-up work, collected from the review of the
`auth-rework` branch (the separate Iris password). Code is referred to by
file and function, since line numbers drift.

## Races

None of these has shown up under `-race` in tests, but that may only mean
they aren't tested: a race between `upstream.Close` and `sendDownstream`,
fixed during review, showed up as soon as a test sent game output to
clients. Each needs a specific interleaving of goroutines, or of a REST
request and a Telnet login.

- [ ] **`logFile.Reopen` swaps the open file while `sendDownstream` may be
      writing to it** (`cmd/serve/session.go`). `ReopenHistories` (SIGHUP)
      calls `Reopen`, which closes and replaces `logFile.File`, while the
      upstream's read loop may be writing through the same `logFile`.
  - A write can land on a closed file and fail. `sendDownstream` then drops
    the history from the client list, so that upstream stops logging until
    it reconnects.
  - Give `logFile` its own lock around `Write`, `Reopen` and `Close`.
  - Renames depend on this too, because a renamed upstream's log file only
    picks up the new name on reopen. See "Reopen the log on rename" below.
- [ ] **A Telnet login can race with REST delete.**
  - Sequence: a login loads the upstream row (`loadUpstream`), then the
    upstream is deleted and `closeUpstream` runs, then the login calls
    `upstreamFor`.
  - Result: `upstreamFor` adds a fresh session for the deleted id and
    connects to the game, using the password it already loaded.
  - Re-check that the row still exists once the upstream is in the pool, or
    after `Connect`, and close it if not.
- [ ] **A Telnet login can race with REST rename.**
  - Sequence: a login loads the upstream under its old name, then the rename
    commits and `renameUpstream` runs, then the login calls `upstreamFor`.
  - Result: if nothing was connected yet, `renameUpstream` finds no session,
    and `upstreamFor` creates one with the **old** name. `GET /sessions`
    then shows the old name, and the log file uses it, until the next
    rename.
  - Have `upstreamFor` take the name from the row once in the pool, or have
    `loadUpstream` and `upstreamFor` run under one lock with
    `renameUpstream`.
- [ ] **Two REST renames of the same upstream can race.**
  - Sequence: request A renames `x` to `y` and request B renames `y` to `z`.
    The commits are ordered A then B, but `editUpstream`
    (`cmd/serve/rest.go`) calls `renameUpstream` after the commit and
    outside any lock, so B's call can run before A's.
  - Result: the database says `z`, but the live session, `GET /sessions` and
    the log file use `y` until the next rename.
  - Having `renameUpstream` read the current name from the database, instead
    of taking it from the caller, would fix this and the race above.

## Bugs

- [x] **`log.level` has no effect.** `Serve` (`cmd/serve/serve.go`) calls
      `logger.Level(l)` and discards the result; it should be
      `logger = logger.Level(l)`. Marked with a TODO in the code. Fixing it
      changes what reaches the logs, so check the configured level first.
- [ ] **Shutdown exits with status 1.** After SIGTERM, `runTelnetProxy`
      closes the listener, and the accept goroutine then calls
      `logger.Fatal` on `use of closed network connection`. A service
      manager such as systemd then marks the service as failed after an
      ordinary stop. Treat `net.ErrClosed` as a normal exit.
- [ ] **`IRIS_CONFIG` is ignored.** `initConfig` (`cmd/root.go`) calls
      `viper.GetString("config")` before `viper.AutomaticEnv()`, and nothing
      else binds the key, so the variable is never read.
  - Setting `IRIS_CONFIG` only appears to work when the search path happens
    to find the same file, such as `~/.config/iris/config.yml`.
  - A `config.*` in the working directory takes priority over it, so
    running `iris password` or `iris upstream` from the repo uses the repo's
    config instead.
  - Fix with `viper.BindEnv("config")`, or read `os.Getenv("IRIS_CONFIG")`
    directly, before the check.
- [ ] **Input sent with the login is dropped.** `readLogin`
      (`cmd/serve/session.go`) decodes through a `json.Decoder`, which
      buffers past the end of the JSON object. Anything the client sends
      right after its login, in the same packet, is lost instead of going to
      the game. Forward `decoder.Buffered()` to the upstream once attached.
- [ ] **History is only replayed after charset negotiation resolves.**
      `downstream.Listen` waits for `EventCharsetResolved`, so a client that
      never answers Telnet option negotiation joins a live session and gets
      no history. Not yet checked against real MU clients. History replay
      also has no automated test, for the same reason.
- [ ] **`upstream was closed while connecting` rarely reaches the client.**
      When an upstream is deleted during a login, `closeUpstream` closes
      every attached client, including the one still logging in, before
      `writeLoginError` can tell it why. The client just sees the connection
      close. `TestLoginWhileDeletedReportsError` only sees the message
      because it closes the upstream before the client attaches.
- [ ] **`iris upstream edit` can't clear a field.** `Edit`
      (`cmd/upstream/upstream.go`) treats an empty flag as "not given", so
      there's no way to go back to the default connect script.
  - The API can't express it either: `"script": ""` stores an empty script,
    and Iris then sends a blank line rather than the default
    `connect %LOGIN% %PASSWORD%`.
  - `cmd.Flags().Changed("script")` would tell "not given" from "given as
    empty", and would let the tests stop resetting the package-level flag
    variables between runs.
- [ ] **Every `iris upstream` flag is accepted by every subcommand.** The
      flags are persistent flags on `iris upstream` (`cmd/upstream/upstream.go`),
      so for example `delete` accepts `-a` and ignores it. Register each
      flag only on the subcommands that use it, so a wrong flag is an
      error.

## Security

- [ ] **TLS** for the Telnet listener and the REST API. Both carry the Iris
      password in cleartext. That's fine for the REST API on localhost, but
      not for Telnet clients on other machines.
- [ ] **Rate limiting** failed Iris password attempts on both listeners.
  - Each unauthenticated Telnet connection costs one bcrypt check (about
    50 ms) and can hold a goroutine for up to the 30-second login timeout,
    with no cap on how many.
- [ ] **`-p PASSWORD` leaves the upstream password in shell history.**
      `-p -` avoids it. Consider warning when a literal password is given.

## Decisions to make

- [ ] **Reopen the log on rename.** A renamed upstream's history log keeps
      its old file name until the next SIGHUP. Should a rename reopen the
      log right away? That needs the `logFile` lock from "Races" first, and
      then `renameUpstream` can call `Reopen` directly.
- [ ] **Require the Basic auth username to be `iris`.** `requireAuth`
      (`cmd/serve/rest.go`) ignores the username today, so
      `curl -u anything:password` works. Once multiple users exist and
      usernames are checked, those clients would start failing without
      warning. Requiring `iris` now keeps every working client working, as
      long as `iris` becomes the first user. The Go client
      (`api.DefaultUsername`) and the README already send `iris`.
      Recommended in review.

## Multi-user support

- [ ] Move `iris_auth` into a `users` table.
- [ ] Start checking the Basic auth username (the client already sends
      `api.DefaultUsername`). See "Require the Basic auth username" above.
- [ ] Add a `user` field to the Telnet login JSON.
- [ ] Decide who owns upstreams.

## Considered and decided against, for now

- **Caching the Iris password check.** Each REST request costs one bcrypt
  comparison (about 50 ms), which is fine for CLI use.
- **Deriving the encryption key from the Iris password** (Argon2id) instead
  of using a key file.
  - It's stronger: the database plus the server's disk wouldn't be enough to
    decrypt.
  - But Iris couldn't reconnect to games after a restart until someone logs
    in.
  - Changing the Iris password would mean re-encrypting every upstream
    password.
  - It makes shared upstreams awkward once there are multiple users.
  - The versioned value format in `internal/secret` leaves room to switch
    later.
- **Renaming the Telnet `password` field** (for example to `iris_password`).
  Keeping the name makes the upgrade a one-value change for clients.
- **Retrying key creation when two servers start at once.** `createKey`
  uses `O_EXCL`, so the second server fails with "file exists" rather than
  overwriting the key. That fails loudly, and two servers on one database is
  already a misconfiguration.
