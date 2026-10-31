# Plan: unresolved races and bugs in `TODO.md`

- **Branch:** `bug-squash-2`
- **Written against:** commit `ddf45bf`. All line numbers are correct at that
  commit. They'll drift as commits land, so re-grep them before each commit.
- **Status:** not yet implemented.
- **In scope:** the unticked items in the Races and Bugs sections of `TODO.md`,
  plus the problems found while planning (listed below).
- **Out of scope:** the Security, Decisions to make, and Multi-user sections of
  `TODO.md`. That includes reopening the log on a rename.

This file is written so a fresh session can pick up with no other context.

## Resuming in a fresh session

1. Read `README.md`, `TODO.md`, and this file.
2. Run `git log --oneline ddf45bf..HEAD` and compare it with the checklist below
   to see what's done. Trust the git log if the two disagree.
3. Run `go test -race -count=1 ./...`. It passed at `ddf45bf`, on Go 1.26.5, and
   should pass before you start.
4. Take the first unfinished step. Re-read the code it touches and re-grep every
   location: line numbers here are hints, and function names are the stable
   anchors.
5. Rebuild that step's reproduction from "Reproduction tests" at the end of this
   file, and tick the step in the checklist as part of its commit.

## Checklist

Phase 1: Reporting and log files
- [x] 1. `don't list closed upstreams in GET /sessions`
- [ ] 2. `keep the old log open when a reopen fails`
- [ ] 3. `keep history replay across a reopen` (not in `TODO.md`)
- [ ] 4. `test the SIGHUP handler with a real signal`

Phase 2: Connecting
- [ ] 5. `close the upstream when its connect script fails`
- [ ] 6. `send the connect script before attaching the first client`
- [ ] 7. `attach an upstream's history in the same step as connecting`

Phase 3: History replay
- [ ] 8. `keep joining clients in step with the log` (not in `TODO.md`)
- [ ] 9. `stop waiting to replay once a client disconnects`
- [ ] 10. `test a failed history read during a join`
- [ ] 11. `limit the replay buffer`

Phase 4: Stuck connections
- [ ] 12. `close clients that fail a write` (not in `TODO.md`)
- [ ] 13. `add write deadlines to telnet connections`

Phase 5: Encoding and configuration
- [ ] 14. `make the negotiation timeout configurable`
- [ ] 15. `let a telnet connection pin its encoding`
- [ ] 16. `add a per-upstream encoding setting`

The order matters, because later steps depend on earlier ones:

- Step 3 builds on the `Reopen` rewritten in step 2.
- Step 6 relies on the cleanup from step 5.
- Step 11 changes the `flushReplay` from step 9.
- Step 13 needs step 12, or a timed-out client stays connected to nothing.
- Step 16 needs step 15.

## Which step resolves which `TODO.md` item

| `TODO.md` item | Line | Step |
|---|---|---|
| `GET /sessions` can list a closed upstream | `TODO.md:52` | 1 |
| A failed `logFile.Reopen` disconnects the upstream | `TODO.md:115` | 2 |
| Try a real SIGHUP against a running server | `TODO.md:123` | 4 |
| Test that `Connect` closes the history when attaching it is refused | `TODO.md:128` | 7 |
| A delete just after the first client attaches gives it no reason | `TODO.md:137` | 6 |
| A failed connect script write leaves the upstream connected | `TODO.md:142` | 5 |
| A stuck write holds up everything behind `writeMu` | `TODO.md:148` | 13 |
| A game that doesn't agree a charset can't send or receive non-ASCII text | `TODO.md:157` | 15, 16 |
| A joining client that doesn't answer `WILL CHARSET` waits 5 seconds | `TODO.md:168` | 14 |
| The replay buffer has no size limit | `TODO.md:173` | 11 |
| Test that `flushReplay` exits after a client disconnects | `TODO.md:177` | 9 |
| Test a failed history read during a join | `TODO.md:181` | 10 |

## Constraints to respect

These are deliberate behaviours. Don't "fix" them.

- **Scripts and input simulate typing at a keyboard.** The game doesn't process
  input until it receives a newline. The trailing newline on the connect script
  (`cmd/serve/session.go:446`) stays, and forwarded client input keeps its own
  newlines. The one exception is the line ending that finishes the login
  (`cmd/serve/session.go:539`).
- **History replay waits for negotiation on purpose.** The Telnet default is 7-bit
  ASCII, and the log can hold non-ASCII text, so replaying before the encoding is
  settled corrupts it (`cmd/serve/session.go:323-326`).
- **Telnet commands don't take `writeMu`** (`TODO.md:252-255`). Each is a single
  write, and Telnet allows commands in the middle of data.
- **`Close` never takes a lock held during a blocking write**
  (`cmd/serve/session.go:305-307`, `internal/telnet/telnet.go:37-39`), so closing
  always unblocks a stuck writer. New locks must keep this true.

## What reproducing changed

The "found by reading" items were run in a scratch copy, plus a real SIGHUP on an isolated server on private ports. "Reproduction tests" at the end of this file describes how to rebuild each one.

| Item | Result |
|---|---|
| `GET /sessions` lists a closed upstream | Confirmed: 1 session listed after close |
| Failed script write leaves the upstream connected | Confirmed: still in the pool and connected |
| Delete just after attach gives no reason | Confirmed: the client only sees `connecting to ...` |
| Failed `Reopen` disconnects the upstream | Confirmed: pool size drops to 0 |
| `flushReplay` lingers after a disconnect | Confirmed: still alive 300ms after the client left, with a 2s timeout |
| **Real SIGHUP against a running server** | **Works:** the upstream stays connected, keeps logging, and a renamed upstream moves to its new file. It also exited with status 0 on SIGTERM. |

These problems aren't listed in `TODO.md`:

| New problem | Evidence | Handled in |
|---|---|---|
| A reopen empties replay, so a client joining after SIGHUP gets no history | Replay was `""` after `ReopenHistories` | Step 3 |
| A joining client can get a chunk of game output twice | Count 2 when the log write lands between attach (`cmd/serve/session.go:331`) and history read (`:341`) | Step 8 |
| A joining client can lose a chunk of game output | Count 0, though the log has it, when `sendDownstream` took its client list before the attach | Step 8 |
| A stuck client stalls the game's read loop and every other client | The client after it received nothing | Step 13 |
| Clients that fail a write are dropped but never closed | `dropped=true closed=false`, which would make deadlines leave clients connected to nothing | Step 12 |
| A non-ASCII upstream password is corrupted | The game received `connect me caf\x1a` | Step 16, partly |
| Answers plus a login over about 512 bytes in one packet lose input | The login timed out at 600 bytes and worked at 100 | `TODO.md` only |

## Rules for every step

- **Commit contents:** each step is one commit with its tests, the matching `TODO.md` boxes ticked, and the README updated if behaviour changes.
- **Testing:** write the test first and watch it fail, except where a step says otherwise. Each commit must pass `go test -race -count=1 ./...`, and concurrency tests also run with `-count=20`.
- **Commit messages:** a one-sentence subject, a blank line, then one paragraph. There's no `Claude-Session` trailer; the `Co-Authored-By` line stays, as in the existing history.
- **Code references:** always cite code by file and line number, from a fresh grep.
- **New `TODO.md` entries:** issues noticed that are out of scope go in `TODO.md`, and resolved issues are marked done there. Entries cite `file:line` plus the function name, because the file's header notes that line numbers drift.
- **Manual runs:** these use `IRIS_CONFIG` and private ports, because the live install is running on 40042. Without a local config, `iris` uses the live config and database in `~/.config/iris`. "Manual SIGHUP check" at the end of this file has a working setup.
- **Staging:** add files by name, because an editor may leave `#BUG-SQUASH-PLAN.md#` and `.#BUG-SQUASH-PLAN.md` in the working tree.

## Phase 1: Reporting and log files

### 1. `don't list closed upstreams in GET /sessions`
- **Fix:** `upstream.info` (`cmd/serve/session.go:771-778`) checks `closed` as well as `telnetSession`, matching `IsConnected` (`:764-768`). `listSessions` (`cmd/serve/rest.go:117-121`) then skips it.
- **Test:** extend `TestListSessions` (`cmd/serve/rest_test.go:578`) with a closed upstream still in the pool.
- **`TODO.md`:** tick the race, and add the out-of-scope items below that don't belong to a later step (encoding swap, stale clients).

### 2. `keep the old log open when a reopen fails`
- **Fix in `Reopen`** (`cmd/serve/session.go:912-920`):
  - Open the new file first.
  - On failure, return with the old file and `key` untouched.
  - On success, write `closed` to the old file, close it, swap, then write `opened`. This order matters because `readHistory` looks for the last `opened` separator.
- **Other changes:**
  - Split the separator out of `open` (`:883-898`).
  - Add a `closed` flag, because opening first would otherwise let a closed log reopen, which `TestLogFileReopenAfterClose` (`cmd/serve/session_test.go:1164`) forbids. The flag also makes `Close` (`:900-910`) safe to call twice.
  - `ReopenHistories` logs the error and no longer calls `session.Close()` (`:115`).
- **Tests:**
  - With a missing `log.dir`, the upstream stays connected and output still reaches the old file.
  - A later reopen succeeds.
  - A failed reopen under a new name keeps the old name.

### 3. `keep history replay across a reopen`
- **Fix:** `Reopen` saves the current replay tail in memory, and `readHistory` (`cmd/serve/session.go:939-976`) returns the last `historySize` bytes of the saved tail plus the current file. This works the same for an unchanged path, a rename and a date rollover. Separators don't change.
- **Tests:** replay survives each of those three cases and is capped at `historySize`.
- **Existing test changed:** `TestLogReplayOfReusedLogIsLastSessionOnly` (`cmd/serve/session_test.go:2198`) uses `Reopen` to stand in for a new session, which pins the bug. It will use a second `newHistory` on the same key instead, which still checks that a new connection doesn't replay old sessions.

### 4. `test the SIGHUP handler with a real signal`
- **Fix:** move `cmd/serve/serve.go:102-111` into a function that returns a stop function. Behaviour doesn't change.
- **Test:** send SIGHUP to the test process with `syscall.Kill`, then check that a new `opened` separator appears and the upstream stays connected.
- **`TODO.md`:** tick the item. The commit message records the manual check above, which is repeated on the final code.

## Phase 2: Connecting

### 5. `close the upstream when its connect script fails`
- **Fix:** in the deferred cleanup (`cmd/serve/session.go:465-469`), a failing first login (`connecting != nil`) always removes and closes the upstream. Joining logins keep using `removeUnconnected` (`:171-178`), so they can't close a healthy upstream. Cleanup already runs after `writeLoginError`, so the client is told first.
- **Tests:**
  - With a `dial` stub whose `Write` fails, the client gets the error, the pool is empty and the game connection and log are closed.
  - A waiting login gets `upstream failed to connect`.

### 6. `send the connect script before attaching the first client`
- **Fix:** in `connectNewUpstream` (`cmd/serve/session.go:430-448`), the order becomes write script, `AddDownstream`, `start`. Nothing can fail after the attach, so a delete can no longer take away the client's failure message. The read loop still starts after the attach, so the game's first output isn't missed. A write that fails because the upstream closed returns `errUpstreamClosed`.
- **Side effect:** the script now always goes out before Iris's option negotiation, which starts in `upstream.runForever` (`cmd/serve/session.go:786`). Before, the two raced, so no game could rely on either order. The script is sent in ASCII either way, unless step 16 pins an encoding.
- **Tests:**
  - A delete during the script write gives `login failed: upstream was closed while connecting`.
  - The existing `TestFirstClientReceivesGameOutputSentOnAccept` (`cmd/serve/session_test.go:1720`) still passes.

### 7. `attach an upstream's history in the same step as connecting`
- **Fix:** `TODO.md` suggests a test hook for the window before `:705`. Removing the window is better: `Connect` (`cmd/serve/session.go:692-708`) attaches the history while holding `mux` and `state`, in the documented lock order (`:620-621`), so the refused branch at `:705` goes away.
- **Tests:**
  - A replaceable `openHistory` variable wraps the call at `:686`, like `dial`, and closes the upstream during it. The test checks that the log and game connection are closed.
  - A `-race` loop runs `Close` against `Connect` and checks the log always ends up closed.

## Phase 3: History replay

### 8. `keep joining clients in step with the log`
- **Fix:**
  - Add `upstream.sendMu`.
  - `sendDownstream` (`cmd/serve/session.go:802-817`) holds it while taking the client list and writing to the log, then releases it before writing to clients.
  - `joinUpstream` holds it across `AddDownstream` and the history read (`:331-343`).
  - Each chunk then reaches the joining client exactly once. A stuck client can't hold the lock, and the lock order is `replayMu` then `sendMu`.
- **Why the log only:** the log is fast local I/O, and `logFile.WriteTo` only writes into the in-memory `replayBuf` here, so `sendMu` is never held across a client write. To tell the log from clients in `sendDownstream`, compare against `s.history`. It stays in the client list, because `Close` and `TestGameOutputDuringReopenHistories` (`cmd/serve/session_test.go:1118`) rely on that.
- **Tests:** deterministic versions of both reproductions, checking the chunk arrives exactly once. The hooks have to change, because after the fix they would deadlock on `sendMu`. See "Reproduction tests".

### 9. `stop waiting to replay once a client disconnects`
- **Fix:** `telnetSession.Close` (`cmd/serve/session.go:233-235`) closes a channel that `flushReplay` also selects on (`:356-359`). On close, it clears the pending state and buffer without writing, so the next game output fails and drops the client.
- **Test:** extend `TestClientDisconnectsDuringReplayWait` (`cmd/serve/session_test.go:2143`) with a 30s timeout and a `replayDone` channel, so it checks the goroutine exits promptly.

### 10. `test a failed history read during a join`
- **Change:** test only, so it passes on arrival.
- **Test:** delete the log file, which was confirmed to make `WriteTo` fail. The client still joins (`cmd/serve/session.go:341-343`) and receives live output.

### 11. `limit the replay buffer`
- **Fix:** add a 1 MiB limit in `downstream.Write` (`cmd/serve/session.go:314-321`). On overflow, it ends the wait early, as the timeout does, by writing the buffer and then `p`. `flushReplay` wakes and finds nothing to do.
- **Decision:** chosen over dropping output, because degraded text is better than missing text and order is kept.
- **Test:** with a small limit, the client gets everything in order, once, and the wait is over.

## Phase 4: Stuck connections

### 12. `close clients that fail a write`
- **Fix:** `sendDownstream` (`cmd/serve/session.go:809-817`) logs and closes writers that fail, after releasing `mux`. This must land before deadlines, or a timed-out client would stay connected to nothing. It also stops leaking the log file's descriptor.
- **Log file:** a closed log can still be replayed, because `readHistory` opens the file by name (`cmd/serve/session.go:945`).
- **Test:** a failing writer is both dropped and closed, and other clients still get the output.
- **`TODO.md`:** add "a failed log write stops logging until reconnect".

### 13. `add write deadlines to telnet connections`
- **Fix:**
  - Wrap the `net.Conn` in `deadlineConn` before `telnet.Wrap` (`cmd/serve/session.go:219`). This covers every write path, including negotiation (`internal/telnet/telnet.go:98`), with no change to the `telnet` package.
  - `newSession` takes a timeout (call sites `:94`, `:700`, and `cmd/serve/session_test.go:120`).
  - `upstream` gets its own `Write` that closes the upstream on failure, because a timed-out write may have sent a partial line.
  - `writeLoginError` (`:546`) and `loginErrorTimeout` (`:46-48`) go away, since the wrapper would override them.
- **Timeouts:** 10s for clients, because a stuck client stalls everyone. 60s for games, because a stall that long is abnormal and dropping the connection is costly.
- **Tests, using `net.Pipe`:**
  - A stuck client is closed within the timeout, and other clients recover.
  - A stuck game closes the upstream and its clients.
  - An encoding change behind a stuck write unblocks.
- **`TODO.md`:** add "a slow client that is still reading delays the others", since writes remain sequential.

## Phase 5: Encoding and configuration

### 14. `make the negotiation timeout configurable`
- **Fix:** add the key `negotiation_timeout` (`IRIS_NEGOTIATION_TIMEOUT`) in `cmd/root.go:37-43`. It's parsed strictly in `Serve` (`cmd/serve/serve.go:35`), following `setLogLevel` (`:48-59`), and sets `negotiationTimeout` (`cmd/serve/session.go:52`).
- **Default:** drops from 5s to 2s. Full negotiation takes about three round trips, and `tf` and `telnet` answered at once in earlier checks, so this mostly shortens the `tt++` wait.
- **Tests:** next to `TestSetLogLevel` (`cmd/serve/serve_test.go:59`), covering valid, invalid and negative values.
- **Manual check:** re-time `tt++`.
- **README:** update `:115` and the table at `:140-149`.

### 15. `let a telnet connection pin its encoding`
- **Fix:** add `conn.ForceEncoding`, which sets both directions and makes later `SetReadEncoding` and `SetWriteEncoding` calls (`internal/telnet/telnet.go:139-148`) do nothing. Without it, a refused Transmit Binary would reset the encoding to ASCII (`internal/telnet/encoding.go:59`, `:67`, `:138`).
- **Tests:** in `internal/telnet/encoding_test.go`, the pinned encoding survives accepted charset, refused charset and refused binary.

### 16. `add a per-upstream encoding setting`
- **Fix:**
  - **Database:** a new migration adds a nullable `encoding` column.
  - **`requireMigrated`:** must also check for the column (`internal/database/database.go:34-37`), because it only looks for `iris_auth` and an unmigrated database would otherwise fail on later queries.
  - **API:** `api/types.go:20-24`, `:39-45` and `:65-71`.
  - **REST:** `cmd/serve/rest.go:134-146`, `:160` and `:215-229`, returning 400 for an unknown encoding.
  - **CLI:** `-e` in `cmd/upstream/upstream.go:76-81`, `:97`, `:144` and `:184-186`. An empty value clears it, like `-s`.
  - **Connection:** `upstreamConfig` and `loadUpstream` (`cmd/serve/session.go:379-396`) load it, and `Connect` applies it before the script is written.
- **Decision:** a stored setting, not a login option (`:819-845`), because a login option depends on which client connects first.
- **Semantics:**
  - A pinned encoding wins over anything the game negotiates, because the user chose it explicitly.
  - Text from the game is decoded to UTF-8 on the way in, so the log and clients are unaffected by the choice.
  - Encoding names are validated in `cmd/serve/rest.go`, using the same lookup as `getEncoding` (`internal/telnet/encoding.go:229-237`), so the `api` package doesn't need to import `telnet`.
- **Tests:**
  - The non-ASCII reproductions now pass in both directions, the log is clean, and the password arrives intact.
  - API and CLI round trips, and clearing the setting.
  - `requireMigrated` refuses an older database.
- **`TODO.md`:** add "without the setting, a non-ASCII password is still corrupted", because the script goes out before negotiation.
- **Deployment:** run `goose` on the live database before deploying this one. The server will refuse to start until that's done.

## Added to `TODO.md`, not fixed in this plan

- **Encoding swap loses input** (`internal/telnet/telnet.go:139-141`, `SetReadEncoding`): the old decoder's buffered bytes are discarded. It takes answers plus a long login in one packet, and a real login is far shorter than 512 bytes, so it's left out. A proper fix means reworking the read path.
- **Disconnected clients stay listed until the next game output** (`cmd/serve/session.go:814`, `sendDownstream`): that's the only place clients are removed, so an idle game keeps them. It costs a little memory and nothing else.
- **Added by steps 12, 13 and 16:** the three entries named in those steps.

## Choices open to being overruled

These are built into the plan as written:

- **Step 3:** changes behaviour a test currently pins, on the reading that the test was simulating a new session.
- **Step 13:** a game that won't accept data for 60s loses its connection. This is the one step that can drop a persistent game connection.
- **Step 14:** shortens the default timeout to 2s, so a client on a very slow link could fall back to ASCII.
- **Step 16:** the only schema change. Steps 15 and 16 can be dropped together without affecting anything else.

## Reproduction tests

These were throwaway tests in `package serve`, run with `go test -race -count=1 -v -run TestRepro ./cmd/serve/`. To keep the repo clean they ran in a copy of the tree, made with `git archive HEAD | tar -x -C <scratch dir>`.

They only logged what they saw with `t.Logf` and always passed. **The real tests must assert the correct behaviour instead**, so that they fail until the step's fix lands. "Observed" is the result at `ddf45bf`.

### Existing helpers

All in `cmd/serve/session_test.go`:

| Helper | Line | Use |
|---|---|---|
| `newTestPool` | `:35` | Pool with a migrated database and an upstream named `game` |
| `loginMsg` | `:64` | The JSON login |
| `login`, `loginOutput` | `:70`, `:78` | Run `connectUpstream` over a pipe; `loginOutput` also returns what the client received |
| `addConnected` | `:113` | An upstream in the pool that looks connected, with no real connection |
| `setLoginTimeout`, `setNegotiationTimeout` | `:151`, `:1929` | Shorten or lengthen a timeout for one test |
| `listenUpstream`, `listenGame` | `:172`, `:180` | A fake game on TCP; `listenGame` also returns a function that sends game output |
| `waitForHistory`, `waitForLine` | `:223`, `:240` | Wait for text in the log, or at the game |
| `stuckClient` | `:757` | A client whose `Write` blocks until `Close` |
| `joinClient`, `startReplay` | `:1990`, `:2017` | `startReplay` connects a first client with text in the history; `joinClient` serves a second client and records its output |
| `refuseCharset`, `acceptCharset`, `acceptBinary` | `:2036`, `:2034`, `:2037` | Telnet answers a client can send |

### Stubs to add

```go
// a game connection whose writes fail while its reads keep blocking, so the
// read loop can't notice the failure and clean up by itself
type failWrite struct {
	net.Conn
	fail atomic.Bool
}

func (f *failWrite) Write(b []byte) (int, error) {
	if f.fail.Load() {
		return 0, errors.New("boom")
	}
	return f.Conn.Write(b)
}

// a game connection that runs f just before the connect script is written
type closeOnWrite struct {
	net.Conn
	f func()
}

func (c *closeOnWrite) Write(b []byte) (int, error) {
	if bytes.Contains(b, []byte("connect ")) {
		c.f()
	}
	return c.Conn.Write(b)
}

// a client whose writes always fail
type badClient struct{ closed atomic.Bool }

func (b *badClient) Write([]byte) (int, error) { return 0, errors.New("nope") }
func (b *badClient) Close() error              { b.closed.Store(true); return nil }

// a client that records what it receives
type recorder struct{ strings.Builder }

func (r *recorder) Close() error { return nil }

// a history that runs hook just before the join reads it
type hookHistory struct {
	History
	hook func()
}

func (h *hookHistory) WriteTo(w io.Writer) (int64, error) {
	if h.hook != nil {
		h.hook()
	}
	return h.History.WriteTo(w)
}

// a history whose Write signals entered and then waits for release
type gateHistory struct {
	History
	release chan struct{}
	entered chan struct{}
}

func (h *gateHistory) Write(p []byte) (int, error) {
	close(h.entered)
	<-h.release
	return h.History.Write(p)
}
```

`failWrite` and `closeOnWrite` are installed by replacing `dial` and restoring it in `t.Cleanup`, wrapping the connection the real `dial` returns.

### Reproductions

**Closed upstream listed (step 1).** `addConnected(t, p, "game")`, then `u.Close()`, then `(&apiServer{db: p.db, sessions: p, box: p.box}).listSessions()`. The upstream stays in the pool because only `runForever` and `closeUpstream` remove it.
- Observed: 1 session.
- Correct: 0.

**Failed reopen (step 2).** `listenUpstream`, `newTestPool`, `login`. Then `viper.Set("log.dir", filepath.Join(t.TempDir(), "missing"))`, `p.ReopenHistories()`, and wait 100ms.
- Observed: `poolSize(p)` is 0.
- Correct: 1, the upstream still connected, and output still landing in the old file.

**Reopen empties replay (step 3).** `listenGame`, `newTestPool`, `login`. `send("before sighup\r\n")`, then `waitForHistory(t, "game", "before sighup")`, then `p.ReopenHistories()`. Then call `d.upstream.history.WriteTo(&buf)`.
- Observed: `""`.
- Correct: contains `before sighup`.

**Failed script write (step 5).** `listenUpstream`, `newTestPool`, and a `dial` returning `failWrite` with `fail` set. Run `loginOutput`, wait 50ms, and look up the upstream in `p.streams`.
- Observed: `err` is `error writing to (...): boom`, the pool size is 1 and `IsConnected()` is true. The client received `connecting to ...login failed: error writing to (...): boom`.
- Correct: the same client message, but an empty pool and a closed game connection.

**Delete during the script write (step 6).** `listenUpstream`, `newTestPool`, and a `dial` returning `closeOnWrite` whose `f` calls `p.closeUpstream(id)`. Run `loginOutput`.
- Observed: the client received only `connecting to ...`, with no reason. `err` was `error writing to (...): use of closed network connection`.
- Correct: the client receives `login failed: upstream was closed while connecting`, and `err` is `errUpstreamClosed`.

**Duplicate chunk on join (step 8).** `setNegotiationTimeout(t, 30*time.Second)`, then `startReplay(t, "old\n")`. Under `u.state`, replace `u.history` with `hookHistory{History: real}`. Its hook runs `go u.sendDownstream([]byte("CHUNK\n"))`, then `waitForHistory(t, "game", "CHUNK")`, then sleeps 50ms. Then `joinClient`, `io.WriteString(c, refuseCharset)` and `c.waitFor(t, "CHUNK")`, wait 100ms, and count `CHUNK` in `c.output()`.
- Observed: 2.
- Correct: 1.
- Change for the real test: after the fix this hook deadlocks, because the join holds `sendMu` and the chunk can't reach the log. Start the `sendDownstream` in the hook and don't wait for it, then wait for the log after the join returns.

**Lost chunk on join (step 8).** `setNegotiationTimeout(t, 30*time.Second)`, then `startReplay(t, "old\n")`. Under `u.mux`, replace the real history in `u.downstream` with a `gateHistory`, leaving `u.history` alone. `go u.sendDownstream([]byte("CHUNK\n"))`, then wait on `entered`: the send has now taken its client list and is stuck in the log write. Then `joinClient`, and wait until `len(u.clients()) >= 3`. Wait 50ms, close `release`, `waitForHistory(t, "game", "CHUNK")`, send `refuseCharset`, `c.waitFor(t, "old")`, wait 100ms, and count `CHUNK`.
- Observed: 0, though the log has it.
- Correct: 1.
- Change for the real test: after the fix the join blocks on `sendMu` until `release` closes, so don't wait for the client list to grow. Close `release` after a short sleep, then assert the count.

**Failed history read during a join (step 10).** `startReplay(t, "hello\n")`, then delete every `*.log` in `viper.GetString("log.dir")`.
- Observed: `u.history.WriteTo` fails with `no such file or directory`. The scratch test stopped there.
- Correct, and what the real test checks: `joinClient` still attaches the client, and after `refuseCharset` it receives live game output.

**`flushReplay` lingers (step 9).** `setNegotiationTimeout(t, 2*time.Second)`, `startReplay(t, "h\n")`, `joinClient`, and wait until `len(u.clients()) >= 3`. `c.Close()`, wait 300ms, then search `runtime.Stack(buf, true)` for `flushReplay`.
- Observed: still there.
- Real test: use a `replayDone` channel, not a stack search.

**Failed writer not closed (step 12).** `listenGame`, `newTestPool`, `login`. Look up the upstream, `u.AddDownstream(&badClient{})`, `send("x\r\n")`, and wait 200ms.
- Observed: dropped from `u.clients()`, with `closed` false.
- Correct: `closed` true.

**Stuck client stalls others (step 13).** `listenGame`, `newTestPool`, `login`. Look up the upstream, then `AddDownstream` a `newStuckClient()` followed by a `recorder`, in that order. `send("one\r\n")` and wait 300ms.
- Observed: the recorder received `""`, because `sendDownstream` writes to clients in turn.
- Limit: `stuckClient` has no deadline, so the real test must use a client on `net.Pipe` that isn't reading, with a short timeout.

**Non-ASCII upstream password (step 16).** `listenUpstream`, `newTestPool(t, "unused:1")`, `insertUpstream(t, p.db, p.box, "g2", addr, "café")`, `login` to `g2`, and `waitForLine(t, lines, "connect")`.
- Observed: `connect me caf\x1a\r\n`.
- Correct with the encoding set to UTF-8: `connect me caf\xc3\xa9\r\n`.
- Non-ASCII game text in both directions was reproduced earlier. The results are at `TODO.md:160-163`.

**Encoding swap loses input (`TODO.md` only).** `setLoginTimeout(t, 500*time.Millisecond)`, `listenUpstream`, `newTestPool`. For each padding size, make a `net.Pipe`, drain the client side, and write this to it in one call:

```go
acceptBinary + acceptCharset + fmt.Sprintf(`{"name":"game","password":%q,"x":%q}`,
	testIrisPassword, strings.Repeat("a", pad))
```

Then run `d := p.NewDownstream(server)`, `d.negotiateOptions()` and `d.connectUpstream()`.
- Observed: `pad` 100 succeeds. `pad` 600 and 3000 fail with `read pipe: i/o timeout`.
- Cause, from reading the code and not separately tested: `SetReadEncoding` (`internal/telnet/telnet.go:139-141`) replaces the decoder during a read, which discards what the old decoder has buffered. Only those three sizes were run, so the threshold of about 512 bytes is inferred from the `json.Decoder` read size.

## Manual SIGHUP check

Run at `ddf45bf`, and repeated on the final code for step 4. Use an empty temp directory `$R`, and replace `$R` in the config with its absolute path:

```yaml
# $R/config.yaml
addr: 127.0.0.1:14042
db: $R/iris.db
secret.key_file: $R/iris.key
api.server.addr: 127.0.0.1:14043
api.client.url: http://127.0.0.1:14043
api.client.password: testpw
log.dir: $R/logs
log.level: debug
```

1. `mkdir -p $R/logs`, `go build -o $R/iris .` from the repo, `cd $R` and `export IRIS_CONFIG=$R/config.yaml`.
2. `goose -dir <repo>/migrations sqlite3 $R/iris.db up`, then `echo testpw | ./iris password`.
3. Start a fake game on 127.0.0.1:14044 that sends `tick-N\r\n` every 200ms.
4. Run `./iris serve`, then `./iris upstream add g -a 127.0.0.1:14044 -l me -p pw`.
5. Connect a client to 14042, send `{"name":"g","password":"testpw"}\n`, and keep reading.
6. `kill -HUP` the server. It should stay alive, and the log should have a second `opened` separator and keep growing.
7. `./iris upstream edit g -n g2`, then `kill -HUP` again. `*-g.log` should end with its `closed` separator, `*-g2.log` should keep growing, and `GET /sessions` should list `g2` with the original start time.
8. `kill -TERM` the server. It should exit with status 0.

All of this held at `ddf45bf`.
