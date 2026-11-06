# sbridge protocol (`sbridge-1`)

How a client talks to a running bridge, and what the bridge leaves behind
when its agent exits. The bridge is `homeshell bridge serve` (`bridgecli`);
the daemon's client is this package, over `proto`.

## Versioning

Every request line starts with the protocol version, `sbridge-1`
(`proto.Version`). A bridge answers a line with another version, or a
request kind it does not know, with `err unsupported handshake` and closes.

A bridge outlives the daemon that started it (`serve` is its own session
leader), so an upgraded daemon meets bridges of the previous build. Hence:

- **Same version** for anything an older peer can ignore: a new JSON field
  (readers ignore unknown fields and read a missing one as its zero value), a
  new request kind, a new flag.
- **Next version** for anything an older peer would misread: a field renamed,
  removed or given a new meaning, a changed request line. The daemon keeps
  speaking the previous version to bridges that may still be running it.

The reverse, an older daemon starting a newer bridge, is ruled out on Linux:
the daemon execs its own running image (`/proc/self/exe`), so a daemon that
outlives an in-place upgrade still starts bridges of its own build. Elsewhere
it execs the file at its path, so after such an upgrade a newer bridge must
also answer the previous version.

The exit record follows the same rules; it is read from disk, possibly by a
newer daemon than the one that started the bridge.

## Socket

One unix socket per session, at the path the caller passes (`--sock`),
created with mode 0600. Paths longer than 100 bytes exceed `sun_path` on some
systems: `serve` then binds the base name from inside the directory, and
clients dial through a short symlink alias to it.

`serve` owns its socket:

- It refuses a path a live bridge answers on (`socket is served by another
  running bridge`, exit 1) and replaces a stale one.
- It recreates its socket within a second when something deletes it, such as
  a cleaned `XDG_RUNTIME_DIR`; a client reads a missing socket as a dead
  session.
- On exit it removes the path only if it is still the socket it bound.

## Requests

One request per connection: a request line ending in `\n`, then the
response. Response lines are `ok` or `err <message>`. The request line must
arrive within 3 seconds.

| Request line | Response |
|---|---|
| `sbridge-1 dump` | One snapshot as a JSON line, then close. |
| `sbridge-1 attach <cols> <rows>` | `ok`, then the attach stream. |
| `sbridge-1 resize <cols> <rows>` | `ok` once the PTY has the size, then close. |
| `sbridge-1 send` | Raw input until the client half-closes, then `ok` once it reached the PTY. 30 s bound. |
| `sbridge-1 stop <grace_ms>` | `ok`, then SIGTERM to the agent and SIGKILL to its process group after the grace. |

`stop` holds the connection open until the bridge exits.

## Snapshot

The `dump` payload (`sbridge.Snapshot`):

| Field | Type | Meaning |
|---|---|---|
| `screen` | string | The visible screen as plain text (see below) |
| `osc_title` | string | The last title set with OSC 0 or 2; empty once the agent sets an empty one |
| `osc_progress` | string or null | The last OSC 9;4 progress as `state[;percent]`; null when none or cleared |
| `error` | string, omitted when empty | Set when the emulator failed; `screen` and the title are then empty |

- `screen` has one line per row, each ending in `\n`, with trailing spaces stripped and no colours or
  attributes. A wide rune (CJK, most emoji) is one rune standing for two cells.
- The screen has the PTY's size: `--size` (default 80x24) until a terminal attaches, then the size of
  the last terminal that attached.
- Agents set an empty title on exit, which empties `osc_title`.
- An emulator `error` does not stop the agent; it keeps running.

## Attach stream

Raw bytes both ways after `ok`: the agent's output down, keystrokes up.

- `<cols> <rows>` above zero is a terminal; the PTY takes its size, briefly
  changing the row count when it already matches, so the agent gets a
  SIGWINCH and redraws. `0 0` is no terminal (a piped client).
- The stream starts with a repaint of the current screen: the background
  colour the agent set (OSC 11), if any; clear and home; the screen text;
  the cursor moved to the agent's cursor.
- One client at a time. A new attach takes over: the previous client gets
  `\r\n[sbridge] taken over by a new client\r\n`, then its connection closes.
- A client that falls 256 chunks behind, or whose write stalls for 30 s, is
  disconnected; the PTY never waits for it.
- The stream closes when the agent exits, after the last output (bounded by
  2 s).
- `homeshell bridge attach` resets the terminal background (OSC 111) on exit
  when the stream set one.

## Terminal queries

While no terminal is attached (no client, or one that sent `0 0`), `serve`
answers the agent's terminal queries in a terminal's place, so an agent
probing at startup does not wait out a timeout:

| Query | Reply |
|---|---|
| DA1: `CSI c`, `CSI 0 c` | `CSI ? 62 ; 22 c` |
| DSR: `CSI 5 n` | `CSI 0 n` |
| Cursor position: `CSI 6 n` | `CSI <row> ; <col> R`, where the cursor was at the query |
| Colours: `OSC 10 ; ?`, `OSC 11 ; ?` | `rgb:e5e5/e5e5/e5e5` and `rgb:0000/0000/0000` (a dark terminal) |

Colour replies end with the query's own terminator.

The kitty keyboard query (`CSI ? u`) stays unanswered: claiming support would
make agents expect key encodings that `send` does not produce.

The emulator (vt10x) never sees CSI sequences it would misread as another
sequence: those with a `<`, `=` or `>` prefix or intermediate bytes, and
`?`-prefixed ones other than modes (`h`, `l`) and selective erase (`J`, `K`).
An attached terminal still receives them.

## Exit record

`serve --exit-file PATH` removes `PATH` at startup and, when the agent
exits, writes it before the socket goes away, atomically (`sbridge.Exit`):

| Field | Type | Meaning |
|---|---|---|
| `code` | number | The agent's exit status; 128 + the signal number when a signal ended it. |
| `screen` | string | The last screen, as in the snapshot. |
| `ended_by_bridge` | boolean | Whether the bridge ended the agent (see below) |

- `ended_by_bridge` is `true` after a `stop` request, or a SIGTERM, SIGINT, SIGHUP or SIGQUIT that
  `serve` forwarded.
- It is `false` when the agent exited on its own (e.g. the user's `/quit`) or something else killed
  it (see `code`).
- Records from bridges that predate the field don't have it.

No record is written when the bridge itself is killed.
