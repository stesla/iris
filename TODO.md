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

- [x] **`logFile.Reopen` swaps the open file while `sendDownstream` may be
      writing to it** (`cmd/serve/session.go`). `ReopenHistories` (SIGHUP)
      calls `Reopen`, which closes and replaces `logFile.File`, while the
      upstream's read loop may be writing through the same `logFile`.
  - A write can land on a closed file and fail. `sendDownstream` then drops
    the history from the client list, so that upstream stops logging until
    it reconnects.
  - Give `logFile` its own lock around `Write`, `Reopen` and `Close`.
  - Renames depend on this too, because a renamed upstream's log file only
    picks up the new name on reopen. See "Reopen the log on rename" below.
- [x] **A Telnet login can race with REST delete.**
  - Sequence: a login loads the upstream row (`loadUpstream`), then the
    upstream is deleted and `closeUpstream` runs, then the login calls
    `upstreamFor`.
  - Result: `upstreamFor` adds a fresh session for the deleted id and
    connects to the game, using the password it already loaded.
  - Re-check that the row still exists once the upstream is in the pool, or
    after `Connect`, and close it if not.
- [x] **A Telnet login can race with REST rename.**
  - Sequence: a login loads the upstream under its old name, then the rename
    commits and `renameUpstream` runs, then the login calls `upstreamFor`.
  - Result: if nothing was connected yet, `renameUpstream` finds no session,
    and `upstreamFor` creates one with the **old** name. `GET /sessions`
    then shows the old name, and the log file uses it, until the next
    rename.
  - Have `upstreamFor` take the name from the row once in the pool, or have
    `loadUpstream` and `upstreamFor` run under one lock with
    `renameUpstream`.
- [x] **Two REST renames of the same upstream can race.**
  - Sequence: request A renames `x` to `y` and request B renames `y` to `z`.
    The commits are ordered A then B, but `editUpstream`
    (`cmd/serve/rest.go`) calls `renameUpstream` after the commit and
    outside any lock, so B's call can run before A's.
  - Result: the database says `z`, but the live session, `GET /sessions` and
    the log file use `y` until the next rename.
  - Having `renameUpstream` read the current name from the database, instead
    of taking it from the caller, would fix this and the race above.
- [x] **`GET /sessions` can list a closed upstream.** `upstream.info`
      (`cmd/serve/session.go`) reports an upstream as connected without checking
      `closed`, so it stays listed from when it closes until it leaves the pool.
      Found by reading the code, not reproduced. Having `info` check `closed`, as
      `IsConnected` does, would fix it.

## Bugs

- [x] **`log.level` has no effect.** `Serve` (`cmd/serve/serve.go`) calls
      `logger.Level(l)` and discards the result; it should be
      `logger = logger.Level(l)`. Marked with a TODO in the code. Fixing it
      changes what reaches the logs, so check the configured level first.
- [x] **Shutdown exits with status 1.** After SIGTERM, `runTelnetProxy`
      closes the listener, and the accept goroutine then calls
      `logger.Fatal` on `use of closed network connection`. A service
      manager such as systemd then marks the service as failed after an
      ordinary stop. Treat `net.ErrClosed` as a normal exit.
- [x] **`IRIS_CONFIG` is ignored.** `initConfig` (`cmd/root.go`) calls
      `viper.GetString("config")` before `viper.AutomaticEnv()`, and nothing
      else binds the key, so the variable is never read.
  - Setting `IRIS_CONFIG` only appears to work when the search path happens
    to find the same file, such as `~/.config/iris/config.yml`.
  - A `config.*` in the working directory takes priority over it, so
    running `iris password` or `iris upstream` from the repo uses the repo's
    config instead.
  - Fix with `viper.BindEnv("config")`, or read `os.Getenv("IRIS_CONFIG")`
    directly, before the check.
- [x] **Input sent with the login is dropped.** `readLogin`
      (`cmd/serve/session.go`) decodes through a `json.Decoder`, which
      buffers past the end of the JSON object. Anything the client sends
      right after its login, in the same packet, is lost instead of going to
      the game. Forward `decoder.Buffered()` to the upstream once attached.
- [x] **History is only replayed after charset negotiation resolves.**
      `downstream.Listen` waits for `EventCharsetResolved`, so a client that
      never answers Telnet option negotiation joins a live session and gets
      no history. Not yet checked against real MU clients. History replay
      also has no automated test, for the same reason.
  - Replay and game output now wait in a buffer until the encoding is settled,
    or for at most 5 seconds (`negotiationTimeout`).
  - Checked against real clients with `é` in the history. `tf` accepts charset
    and gets UTF-8 at once. `telnet` refuses it and gets ASCII at once. `tt++`
    with no configuration doesn't answer Iris's `WILL CHARSET`, so it waits the
    full 5 seconds on each join and then gets ASCII.
- [x] **`upstream was closed while connecting` rarely reaches the client.**
      When an upstream is deleted during a login, `closeUpstream` closes
      every attached client, including the one still logging in, before
      `writeLoginError` can tell it why. The client just sees the connection
      close. `TestLoginWhileDeletedReportsError` only sees the message
      because it closes the upstream before the client attaches.
- [x] **`iris upstream edit` can't clear a field.** `Edit`
      (`cmd/upstream/upstream.go`) treats an empty flag as "not given", so
      there's no way to go back to the default connect script.
  - The API can't express it either: `"script": ""` stores an empty script,
    and Iris then sends a blank line rather than the default
    `connect %LOGIN% %PASSWORD%`.
  - `cmd.Flags().Changed("script")` would tell "not given" from "given as
    empty", and would let the tests stop resetting the package-level flag
    variables between runs.
- [x] **Every `iris upstream` flag is accepted by every subcommand.** The
      flags are persistent flags on `iris upstream` (`cmd/upstream/upstream.go`),
      so for example `delete` accepts `-a` and ignores it. Register each
      flag only on the subcommands that use it, so a wrong flag is an
      error.
- [ ] **A failed `logFile.Reopen` disconnects the upstream**
      (`cmd/serve/session.go`). `ReopenHistories` closes the upstream if
      `Reopen` fails, for example on a full disk or a bad `log.dir`. One SIGHUP
      can then drop every game connection and every attached client.
  - Reproduced by pointing `log.dir` at a missing directory before calling
    `ReopenHistories`: the upstream left the pool and its connection closed.
  - `Reopen` closes the old file before opening the new one. Opening the new
    one first would let a failure keep logging to the old file instead.
- [ ] **Try a real SIGHUP against a running server.** The tests call
      `ReopenHistories` directly, so the signal handler in `runTelnetProxy`
      (`cmd/serve/serve.go`) has only been read, not run. Check by hand, from a
      temp directory with its own config and database, that a connected
      upstream keeps logging after the signal.
- [ ] **Test that `Connect` closes the history when attaching it is refused**
      (`cmd/serve/session.go`). No test in the repo reaches this path, because
      the upstream has to close in the small window before `AddDownstream`, so a
      regression wouldn't be caught.
  - It was checked once on a temporary copy of the code, with a 100ms delay added
    before the attach and a delete landing during it: the history closed, and
    removing `history.Close()` failed the check.
  - A test in the repo needs a way to close the upstream at that exact moment,
    such as a variable that tests can replace, like `dial`.
- [ ] **A delete just after the first client attaches gives it no reason.**
      `connectNewUpstream` (`cmd/serve/session.go`) attaches the client before
      sending the connect script. If `closeUpstream` runs in between, it closes
      the client, so `writeLoginError` can't tell it why. Found by reading the
      code, not reproduced. The window is tiny, and it only costs the message.
- [ ] **A failed connect script write leaves the upstream connected.**
      `connectNewUpstream` (`cmd/serve/session.go`) returns the error while the
      upstream is still open, and `removeUnconnected` leaves a connected upstream
      alone. It stays in the pool without having logged in to the game, and later
      clients join it. Found by reading the code, not reproduced. Closing the
      upstream when the write fails would fix it.
- [ ] **A stuck write holds up everything behind `writeMu`**
      (`internal/telnet/telnet.go`). Found by reading the code, not reproduced.
      Closing the connection clears both of these.
  - If the game stops reading, every client's writes to it wait in line behind
    the blocked one. Writes to the game could already block, so this mostly
    changes who waits.
  - If a client stops reading, an encoding change on that connection waits for
    the lock, which blocks the client's read goroutine.
  - Write deadlines would limit how long either can last.
- [ ] **A game that doesn't agree a charset can't send or receive non-ASCII
      text.** The upstream connection stays 7-bit ASCII for as long as it's open,
      whether the game stays silent or refuses.
  - Reproduced with a fake game that never answers and a client that had
    negotiated UTF-8: the game's `caf\xc3\xa9` reached the client and the log as
    `caf` and two U+FFFD characters, and the client's `caf\xc3\xa9` reached the
    game as `caf\x1a`.
  - The damage to the log is permanent, so replays repeat it.
  - `always_allow_charset` doesn't help, because it only applies once a charset
    has been agreed. A per-upstream setting that forces an encoding would be the
    natural fix.
- [ ] **A joining client that doesn't answer `WILL CHARSET` waits 5 seconds.**
      Seen with `tt++` on its default configuration: history and live game output
      are held for the full `negotiationTimeout` on each join (5.14s measured),
      then sent as ASCII. Consider whether the timeout should be shorter or
      configurable.
- [ ] **The replay buffer has no size limit** (`downstream.Write` in
      `cmd/serve/session.go`). A very busy game could grow it for up to
      `negotiationTimeout` while a client negotiates. Found by reading the code,
      not reproduced.
- [ ] **Test that `flushReplay` exits after a client disconnects**
      (`cmd/serve/session.go`). `TestClientDisconnectsDuringReplayWait` checks that
      the client gets dropped, but not that the goroutine ends. From reading the
      code, it stays until the timeout and then exits when its write fails.
- [ ] **Test a failed history read during a join** (`joinUpstream` in
      `cmd/serve/session.go`). The error is logged and the client joins without
      history, but no test covers this.
- [ ] **Changing the read encoding loses buffered input**
      (`SetReadEncoding`, `internal/telnet/telnet.go:139`). The old decoder's
      buffered bytes are discarded when it is replaced during a read.
  - Reproduced by sending Telnet answers and a login in one write: padding the
    login by 100 bytes worked, and by 600 or 3000 it timed out, having lost the
    end of the login. Those are the only sizes run.
  - It takes a long login in the same packet as the answers, and a real login is
    far shorter, so it hasn't been fixed. A proper fix means reworking the read
    path.
- [ ] **A disconnected client stays in the upstream's list until the next game
      output** (`sendDownstream`, `cmd/serve/session.go:814`). That is the only
      place clients are removed, so an idle game keeps them. Found by reading the
      code, not reproduced. It costs a little memory and nothing else.

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
- [ ] **Recover from panics in connection goroutines.** Nothing calls
      `recover()`, so a panic on one connection stops the whole server, along
      with every client and game connection. The Telnet parser runs before the
      password check, so a bad input bug can be reached without logging in.
  - Add a `recover()` that logs the panic and stack in the two goroutines
    that read from the network: the client one in `runTelnetProxy`
    (`cmd/serve/serve.go`) and `upstream.runForever` (`cmd/serve/session.go`).
    Their existing deferred cleanup already closes the connection.
  - The REST API doesn't need one, because `net/http` recovers panics in
    handlers.
  - This limits the damage from a panic, and doesn't replace fixing its cause.

## Decisions to make

- [ ] **Reopen the log on rename.** A renamed upstream's history log keeps
      its old file name until the next SIGHUP. Should a rename reopen the
      log right away? That needs the `logFile` lock from "Races" first, and
      then `syncUpstream` can call `Reopen` directly.
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
- **Taking `writeMu` for Telnet commands.** Option negotiation, Go-Ahead and
  End-of-Record write straight to the connection (`internal/telnet/telnet.go`),
  so one can land between the chunks of a long `Write`. Each is a single write,
  and Telnet allows commands in the middle of the data, so this is safe.
