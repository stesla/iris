# Bug Squash Plan

A commit-by-commit plan for the Races and Bugs sections of `TODO.md`, plus
problems found while planning. It is written so a fresh session can pick up
from here with no other context.

- **Branch:** `bug-squash`
- **Written against:** commit `4a85735`. All `file:line` references are correct
  at that commit, and drift as commits land.
- **In scope:** the Races and Bugs sections of `TODO.md` (12 items), plus 3
  problems `TODO.md` doesn't list.
- **Out of scope:** the Security, Decisions to make, and Multi-user sections.

## Resuming in a fresh session

1. Read `README.md`, `TODO.md`, and this file.
2. Run `git log --oneline 4a85735..HEAD` and compare it with the checklist
   below, to see what's done. Trust the git log if the two disagree.
3. Run `go test -race -count=1 ./...`. It should pass before you start.
4. Take the first unfinished commit. Re-read the code it touches, and re-grep
   every location: references are hints, and function names are the stable
   anchors.
5. Tick the commit in this file as part of the commit itself.

### Working agreements

- **Cite code as `file:line`, from a fresh grep each time.** Don't reuse a line
  number from this document or from earlier in a session.
- **One commit per step.** Each should build and pass tests on its own, in the
  order below, because later commits depend on earlier ones.
- **Test first.** Write the new test, watch it fail on the unfixed code, then
  fix. Add test coverage wherever possible.
- **Every commit:**
  - Run `go test -race -count=1 ./...`, and `-count=20` for concurrency tests.
  - Tick the matching item in `TODO.md`.
  - Update the README if behaviour changes.
- **Manual tests run from a temp directory with its own `config.yaml` and
  database.** Iris searches the current directory and then
  `$HOME/.config/iris/`, so running it anywhere else can use a real
  configuration and database.
- **Commit messages** have two parts:
  - The first line is a short, lowercase subject, like the existing history.
  - After a blank line, a paragraph explains the content of the commit.
  - Don't include a `Claude-Session` trailer, even if the session's attribution
    instructions ask for one.

## Checklist

Phase 1: Crash
- [x] 1. `fix panic on short telnet subnegotiation` (not in TODO)

Phase 2: Standalone bugs
- [x] 2. `make log.level take effect`
- [x] 3. `exit cleanly on shutdown`
- [x] 4. `honor IRIS_CONFIG`
- [x] 5. `register upstream flags per subcommand`
- [x] 6. `allow clearing an upstream's connect script`

Phase 3: Log file
- [x] 7. `guard logFile with a lock`

Phase 4: Session races
- [x] 8. `read the session name from the database on rename`
- [x] 9. `pick up renames that land during a login`
- [x] 10. `refuse to attach clients to a closed upstream` (not in TODO)
- [ ] 11. `don't connect an upstream deleted during login`
- [ ] 12. `tell the client when its upstream is deleted while connecting`

Phase 5: Client I/O
- [ ] 13. `forward input sent with the login`
- [ ] 14. `serialize telnet connection writes` (not in TODO)
- [ ] 15. `buffer history replay until negotiation completes`

## Constraints to respect

These are deliberate behaviours. Don't "fix" them.

- **Scripts and input simulate typing at a keyboard.** The game doesn't process
  input until it receives a newline.
  - The trailing newline on the connect script (`cmd/serve/session.go:341`) is
    intentional and stays. That includes a script reset to the default.
  - Forwarded client input keeps its own newlines. Iris never adds or removes
    one, apart from the single line ending that finishes the login itself (see
    commit 13).
- **History replay waits for negotiation on purpose.** The Telnet NVT defaults
  to 7-bit ASCII, and the log can hold non-ASCII text. Replaying before the
  encoding is settled corrupts that text. The fix for the missing-history bug
  must keep this protection, not remove the wait (see commit 15).

## Problems not listed in TODO.md

All of these were reproduced at `4a85735` unless marked otherwise.

| Problem | How to reproduce | Commit |
|---|---|---|
| A client can panic the server before logging in. | Send `IAC SB IAC SE` before the login: panics at `internal/telnet/telnet.go:235`. Or send `IAC DO CHARSET IAC SB CHARSET IAC SE`: panics at `internal/telnet/encoding.go:146`. Both reproduce in-process through `connectUpstream`. Nothing recovers in the goroutine at `cmd/serve/serve.go:140`, so this should kill the whole server (not run against a live server). | 1 |
| A client can panic the server with an unknown charset name. | Send `IAC DO CHARSET IAC DO TRANSMIT-BINARY IAC WILL TRANSMIT-BINARY IAC SB CHARSET ACCEPTED BOGUS IAC SE` before the login: `getEncoding` returns nil, and `setEncoding` passes it on to a nil pointer panic at `internal/telnet/telnet.go:134`. Found after this plan was written, at `40b9901`. | 1 |
| Telnet writes aren't goroutine-safe. | Call `d.upstream.Write` from two goroutines: `-race` reports it inside the encoder, reached from `internal/telnet/telnet.go:147`. | 14 |
| Replay is corrupted even after charset is accepted. | Put `caf\xc3\xa9` in the history, have a joining client answer `DO CHARSET` and `CHARSET ACCEPTED UTF-8` but not Transmit Binary. It receives `caf\x1a`. | 15 |
| A client that answers charset before logging in gets no history. | Send `IAC DONT CHARSET IAC WONT CHARSET` followed by the login in one write. The listener isn't registered until `cmd/serve/session.go:398`, so it misses the event. | 15 |
| Replay of a new log includes the `opened` separator. | `cmd/serve/session.go:794` tests `n > 0`, and in a new log the separator is at offset 0. | 15 |
| A second SIGTERM panics. | The loop at `cmd/serve/serve.go:108` repeats the `close` at `:111`. Reproduced at `32fb3a9`: two quick SIGTERMs panicked in 1 run of 20. | 3 |

## Where this plan differs from TODO.md's suggestions

- **`logFile` lock:** `WriteTo` needs the lock too, but must not hold it while
  writing to a client, or a stuck client would block logging.
- **`decoder.Buffered()`:** a login followed by Enter leaves `"\n"` in the
  buffer (confirmed: `{...}\nlook\n` buffers `"\nlook\n"`). Forwarding it as-is
  would send the game a blank line on every login.
- **`Changed("script")`:** this won't let the tests stop resetting flags.
  `Changed` stays `true` across runs on one command object (confirmed), so tests
  need a fresh command tree.
- **Reading the name from the database:** not enough without a lock, because a
  slow reader could overwrite a newer name with a stale one.
- **Delete re-check:** done before dialing, not after `Connect`, so a deleted
  upstream's password is never sent to the game.
- **History replay:** waiting for charset alone isn't enough; see commit 15.

---

## Phase 1: Crash

### 1. `fix panic on short telnet subnegotiation`

**Fix**
- `internal/telnet/telnet.go:232-236`: ignore a subnegotiation with no option
  byte.
- `internal/telnet/encoding.go:146`: ignore one with no command byte.
- `internal/telnet/encoding.go:149`: treat `CHARSET ACCEPTED` with an unknown or
  empty name as a rejection, so a nil encoding never reaches `setEncoding`.

**Tests**
- `telnet_test.go`: `IAC SB IAC SE` causes no panic and dispatches no event, and
  following bytes still read normally.
- `encoding_test.go`: an empty `CHARSET` subnegotiation with the option enabled,
  and `CHARSET ACCEPTED` with an unknown or empty name.
- `session_test.go`: all of these sequences sent before a valid login, and the
  login still succeeds.

## Phase 2: Standalone bugs

### 2. `make log.level take effect`

**Fix**
- Add `setLogLevel(level string) error`, which assigns
  `logger = logger.Level(l)`. It replaces `cmd/serve/serve.go:35-41` and removes
  the TODO comment there.
- Before deploying, check which `log.level` each configuration sets, as
  `TODO.md` asks. A configuration on the default `info` loses its debug and
  trace lines.

**Tests** (`serve_test.go`, with `logger` writing to a buffer and restored
afterwards)
- `warn` drops an Info line and keeps a Warn line.
- An invalid level returns an error.
- The pool created at `cmd/serve/serve.go:46` inherits the level.

### 3. `exit cleanly on shutdown`

**Fix**
- Extract the loop at `cmd/serve/serve.go:122-130` into
  `acceptConns(l, conns) error`, which returns nil on `net.ErrClosed`. Other
  errors stay fatal.
- Replace the `range` at `cmd/serve/serve.go:108` with a single receive, so a
  second signal can't double-close `chExit`.

**Tests**
- Closing the listener returns nil.
- A fake listener's other error is returned.
- Manual: the exit status is currently a race, so SIGTERM a temp-dir server 20
  times and check for status 0 each time. Then send two signals quickly and
  check for no panic.

### 4. `honor IRIS_CONFIG`

**Fix**
- Move `viper.AutomaticEnv()` from `cmd/root.go:50` to just after
  `cmd/root.go:26`, which removes the ordering trap.
- Split out `loadConfig() error` so it can be tested, since `cobra.CheckErr`
  exits the process.
- Document the variable near `README.md:134`.

**Behaviour change:** a shell with `IRIS_CONFIG` set now overrides
`./config.yaml`, including for `iris password`.

**Tests** (new `cmd/root_test.go`, using `t.Setenv` and `t.Chdir`)
- `IRIS_CONFIG` beats a decoy `./config.yaml`.
- A missing `IRIS_CONFIG` file is an error, with no fallback to the search path.
- With it unset, the search path still works.
- `IRIS_ADDR` still overrides.
- Viper is global and its search paths accumulate, so clean up between cases.

### 5. `register upstream flags per subcommand`

**Fix**
- Replace the persistent flags at `cmd/upstream/upstream.go:35-39` with
  `-a -l -p -s` on `add` and `-a -l -n -p -s` on `edit`. `delete` and `list` get
  none.
- Build the tree in a `newCommand()` function so every run starts clean.

**Not a behaviour change:** `iris upstream -a x edit game`, with the flag before
the subcommand, still works, because Cobra hands the flag to the subcommand.
`iris upstream -a x delete game` is rejected like any other wrong flag.

**Tests**
- Change `run` at `cmd/upstream/upstream_test.go:72` to use a fresh tree, and
  remove the reset at line 74.
- Add a table of rejected flags: `delete game -a x`, `list -l me`,
  `add game -n x ...`. Each returns an error and sends no requests.

### 6. `allow clearing an upstream's connect script`

**Decision:** an empty script means "use the default". Only `script` can be
cleared, because `api/types.go:75-84` rejects the other fields when empty.

**Fix**
- CLI: use `cmd.Flags().Changed(...)` at `cmd/upstream/upstream.go:82` and
  `:117-131`. `edit -a ""` now errors instead of being silently ignored.
- Server: store NULL for an empty script at `cmd/serve/rest.go:161` and `:219`.
- Connect: treat empty as default at `cmd/serve/session.go:333`, which also
  repairs rows already stored as empty. The script still ends in a newline.
- Docs: update `README.md:215` and `:289`.

**Tests**
- CLI: `edit game -s ""` sends `{"script":""}`, and `edit game -a ""` fails with
  no request sent.
- REST: a PATCH with an empty script stores NULL, and POST does the same.
- Login: a row with `script=''` sends `connect me ...\n`, not a blank line.

## Phase 3: Log file

### 7. `guard logFile with a lock`

**Fix**
- Change the embedded `*os.File` at `cmd/serve/session.go:729` to a named field
  and add a mutex, so no unlocked method gets promoted by accident.
- Lock `Write`, `Open`, `Close`, and `Reopen`.
- The `Fprintf(f, ...)` calls at `:746` and `:753` must write to the file
  directly, or they deadlock on the new lock.
- `WriteTo` (`:765`) reads under the lock and releases it before the client
  write at `:799`.

**Tests**
- A stress test of `Write` against `Reopen`: no failed writes, and every line
  lands in the file. The same test shape races on today's code.
- End to end: game output during a `ReopenHistories` loop, with the history still
  in `u.clients()` afterwards.
- `Reopen` after `Close` returns an error and doesn't reopen the file.
- `WriteTo` to a `stuckClient` (`cmd/serve/session_test.go:690`) doesn't block
  `Write`.

## Phase 4: Session races

### 8. `read the session name from the database on rename`

**Fix**
- Replace `renameUpstream` (`cmd/serve/session.go:132`) with
  `syncUpstream(ctx, id)`, which reads the name and sets it under a new pool
  `syncMu`.
- Why the lock is enough: every commit is followed by a sync, and syncs run one
  at a time, so the last one sees the latest commit.
- Don't use the pool lock or `state` for this. Both are held on paths that
  shouldn't wait on a database query.
- `cmd/serve/rest.go:236` runs after the commit, so use `context.WithoutCancel`.
  A client hanging up can't leave a stale name, and a failure there is logged
  rather than returned.

**Tests**
- Update `cmd/serve/session_test.go:593` and `:934` to change the row first.
- New: commit `x->y->z`, then run many syncs in random order, and the session
  ends on `z`.
- New: concurrent REST PATCH chains (retrying on 404) end with `GET /sessions`
  matching the database.

### 9. `pick up renames that land during a login`

**Fix**
- Call `syncUpstream` in the login that won `beginConnect`, before it attaches or
  dials. Not right after `upstreamFor`, as first planned: a failure there can't
  clean up, because `removeUnconnected` would close an upstream that another login
  is still connecting. The winner already owns that cleanup, and a login that
  joins doesn't need the sync, because the upstream is in the pool by then.
- This also closes the delete race for the login that connects: a missing row
  fails the sync, so the client sees `upstream not found` and nothing is dialed.
  Commit 11 still has to handle `closeUpstream` and a client that joins.
- Make `loadUpstream` (`:291`) a package variable, like `dial` at `:40`. Tests
  can then rename at exactly the racy moment.

**Tests**
- Wrap `loadUpstream` to run `editUpstream` after the real load.
- After login, `GET /sessions` shows the new name, `<date>-renamed.log` exists,
  and `<date>-game.log` doesn't.
- A rename during the dial reaches the session through its own sync.
- Wrap `loadUpstream` to delete after the real load: no dials, an empty pool, and
  `upstream not found`.

### 10. `refuse to attach clients to a closed upstream`

A prerequisite for commit 11. It also closes a window `TODO.md` doesn't list: a
login can attach just as the upstream closes, then sit there until the client
types.

**Fix**
- `AddDownstream` (`cmd/serve/session.go:593`) checks `closed` under `mux` and
  returns `errUpstreamClosed`. `Close` sets the flag before taking its snapshot,
  so each client is either refused or closed.
- `IsConnected` (`:627`) returns false once closed, which also lets
  `removeUnconnected` (`:148`) clean it up.
- `Connect` closes the history if attaching it fails at `:584`.

**Tests**
- Attaching after `Close` returns the error.
- A login to a connected but closed upstream fails and isn't attached.
- A stress test of `Close` against `AddDownstream`: every accepted client ends up
  closed.

### 11. `don't connect an upstream deleted during login`

**Fix**
- `syncUpstream` handles a missing row by calling `closeUpstream` and returning
  `sql.ErrNoRows`, so the client sees `upstream not found`.
- The re-check runs after the pool insert and before dialing.
- Why this closes the race: a delete committed before the re-check is seen by it.
  A delete committed after it reaches `closeUpstream`
  (`cmd/serve/rest.go:183`) and finds the upstream in the pool.

**Tests**
- Wrap `loadUpstream` to delete after the real load. Assert zero dials, an empty
  pool, and the `upstream not found` message.
- Same for a client joining a connected upstream: it isn't attached and the
  session is closed.
- Keep `TestCloseCancelsDial` for a delete that lands after the check.

### 12. `tell the client when its upstream is deleted while connecting`

**Fix**
- Remove the early attach at `cmd/serve/session.go:404`, so `closeUpstream` can't
  close the client before it's told why.
- Move `go s.runForever()` out of `Connect` (`:589`) into `start()`.
- `connectNewUpstream` then runs `Connect`, `AddDownstream(s)`, `start()`, then
  the script. The client is attached before the read loop starts, so it can't
  miss the game's first output.
- Update the comment at `:352`.

**Tests**
- A delete during a held dial: the client receives
  `login failed: upstream was closed while connecting\r\n` and then disconnects.
  `TestLoginWhileDeletedReportsError` can't catch this, as `TODO.md` notes.
- A fake game that writes immediately on accept: the first client still receives
  it.

## Phase 5: Client I/O

### 13. `forward input sent with the login`

**Fix**
- `readLogin` (`cmd/serve/session.go:415`) keeps `decoder.Buffered()` on the
  downstream.
- Drop exactly one leading `"\n"`: the Enter that finished the login line, which
  the Telnet reader has already turned from CR LF into LF. Everything after it
  is forwarded byte for byte, because it simulates typing (see Constraints).
- `:487` becomes `io.Copy(s.upstream, io.MultiReader(rest, s))`, so the connect
  script always goes first.

**Tests** (through `runForever`, sending everything in one write)
- `login\nlook\n`: the game gets the script, then `look\n`.
- `login\n`: the game gets only the script, with no blank line.
- `login\n\nlook\n`: the game gets the script, a blank line, then `look\n`.
- `login\nlook`, then `\n` in a later write: the game gets `look\n`, and no
  newline is added or lost.
- The same for a client joining a connected upstream.
- A failed login forwards nothing.

### 14. `serialize telnet connection writes`

**Fix**
- Add a `writeMu` around `internal/telnet/telnet.go:139` and `:147`.
- No self-deadlock: `handleSend` (`:92`) writes straight to the connection, and
  `Write` dispatches no events.
- `Close` doesn't take the lock, so closing still unblocks a stuck write.

**Tests**
- `telnet_test.go`: concurrent `Write` and `SetWriteEncoding` under `-race`, with
  every line arriving intact.
- `session_test.go`: two clients typing at once, which races on today's code.

### 15. `buffer history replay until negotiation completes`

**Why the wait exists:** the NVT defaults to 7-bit ASCII, and the log can hold
non-ASCII text, so replay must wait until the client's encoding is settled. Keep
that purpose. Fix how it's done.

**What's wrong today**
- `EventCharsetResolved` fires too early. `telnetSession.Listen` starts Transmit
  Binary at `cmd/serve/session.go:236`, then falls through and signals resolved
  at `:237-239` without waiting for the answer.
- The encoding only changes once Transmit Binary is enabled both ways
  (`internal/telnet/encoding.go:128-129` and `:135-136`). So replay still goes
  out as ASCII, with unsupported characters replaced
  (`internal/telnet/telnet.go:138`). This is the `caf\x1a` result above.
- A client that answers before logging in misses the event, because the listener
  is registered at `cmd/serve/session.go:398`.
- A client that never answers never gets history.

**Design: a per-session replay buffer**

1. **Track when negotiation completes** on `telnetSession`, with a channel closed
   once. It completes when either:
   - charset is refused or rejected (`:231`, `:238`), so the encoding stays
     ASCII; or
   - charset is accepted and Transmit Binary has then resolved in both directions,
     whether enabled or refused.

   Listeners run in registration order, and the handlers at `:193-194` register
   before the session's listeners at `:195-197`, so the encoding is already set
   when the session hears the event. `OptionData` reports each side's resolution
   (`internal/telnet/option.go:238-239`). Handle Transmit Binary already being
   enabled when charset is accepted, which may produce no new event. Because this
   is tracked from connection start, an answer sent before login still counts.
2. **Buffer on the downstream.** Give it a buffer, a lock, and a `pending` flag,
   and override `Write`: while pending, append to the buffer; otherwise write
   through. The zero value passes through, so first logins and login errors are
   unaffected.
3. **Fill the buffer on join.** At `:396-398`, holding the buffer lock: set
   pending, call `AddDownstream`, then copy the history into the buffer with
   `WriteTo`. If `AddDownstream` fails, clear pending first, so the login error
   reaches the client. Read `history` under `state`, and skip buffering if it's
   nil, because `addConnected` (`cmd/serve/session_test.go:116`) sets none.
4. **Buffer live output too.** While pending, game output joins the same buffer
   after the history. It has the same encoding problem, and writing it straight
   through would put it ahead of older history.
5. **Flush from a goroutine.** It waits for negotiation to complete or for a
   `negotiationTimeout` package variable (5 seconds), then writes the buffer and
   clears pending under the lock. It can't run inside the event dispatch, which
   holds the dispatcher's lock, and it can't block `connectUpstream`, because
   `runForever` must be reading for negotiation answers to arrive.
6. **On timeout, flush anyway.** The client hasn't negotiated, so NVT rules apply
   and non-ASCII text becomes `\x1a`. That's better than the client getting no
   history, which is the bug in `TODO.md`.

**Also**
- Remove `downstream.Listen` (`:270-280`), the event constant (`:476`), and the
  `telnetSession` dispatcher (`:181`, `:189`). `upstream` has its own dispatcher
  (`:509`), which stays.
- `Close` must not take the buffer lock, so a flush stuck on a client can still
  be unblocked.
- Lock order is buffer, then `mux` or the `logFile` lock. Neither of those is held
  while writing to a client (commit 7 guarantees this for `logFile`).
- Fix `:794` to use `n >= 0`, so a new log replays without its separator.
- Update the item in `TODO.md` and the replay description in the README.

**Tests** (the first automated replay coverage)
- Accepts charset, Transmit Binary unanswered: nothing is delivered. Then answer
  `IAC DO TRANSMIT-BINARY IAC WILL TRANSMIT-BINARY`: raw `caf\xc3\xa9` arrives.
  This is the fail-first test.
- Accepts charset, refuses Transmit Binary: replay arrives as ASCII.
- Refuses charset: replay arrives promptly as ASCII.
- Answers before login, in the same write as the login: replay still arrives.
- Never answers: replay arrives after a shortened timeout, with `\x1a` in place of
  non-ASCII text.
- Game output during the wait arrives after the history, in order.
- A client that disconnects during the wait: no hang, panic, or leak past the
  timeout.
- The first login gets no replay, and its output is never held back.
- A new log replays without the separator, and only the last session of a reused
  log is replayed.
- Manual: try `telnet` and a couple of real MU clients against a temp-dir server,
  with non-ASCII text in the history. `TODO.md` notes this hasn't been checked
  against real clients.

---

## Known and left alone

- **Replay ordering:** after commit 15, history comes before later live output.
  A chunk in flight during the join could still rarely be duplicated or missed,
  because `sendDownstream` writes to the log and the clients separately.
- **SIGHUP and replay:** replay starts at the last `opened` line, so a SIGHUP cuts
  what later clients see (`cmd/serve/session.go:794`).
- **Stuck clients:** one client that stops reading still stalls `sendDownstream`
  (`:665`) for everyone, including logging. A stuck first client can also block
  at `:327`, holding up later logins. Found by reading, not reproduced.
- **"Reopen the log on rename"** in `TODO.md` is still an open decision. Commit 7
  removes its blocker.
