# Configuration

How to configure HomeShell: [settings](#settings), [command-line flags](#command-line-flags) and
[environment variables](#environment-variables). Terms (project, workstream, session, provider,
…) are defined in [CONTEXT.md](../CONTEXT.md). Variables read only by the dev server and the e2e
suites are in [DEV.md](DEV.md#dev-server-and-token).

## Data directory

Everything the daemon persists lives in one directory. The first of these that applies wins:

1. `--home-dir` on `serve` (and on every client verb, which must name the same directory).
2. `$XDG_DATA_HOME/homeshell`, when `XDG_DATA_HOME` is set.
3. The platform default:
   - Linux: `~/.local/share/homeshell`
   - macOS: `~/Library/Application Support/homeshell`

Which files it holds is in [ARCHITECTURE.md](ARCHITECTURE.md#persistence).

- Two daemons with different data directories are fully separate, including their session
  bridges. Give each its own `--addr` too.
- The desktop app passes `HOMESHELL_HOME_DIR` and `HOMESHELL_ADDR` on (see
  [Environment variables](#environment-variables)).

## Settings

Settings are stored by the daemon in `settings.json` in the data directory and shared by every
client of that daemon. Change them in any of three ways:

- **Web UI:** the gear in the toolbar opens the settings page, one category per section below.
- **CLI:** `homeshell settings get [key]` and `homeshell settings set <key> <value>`, with keys
  written as dotted paths (`sessions.previewLines`). `set` reads the value as the key's current
  type, so `true` and `8` become a boolean and a number.
- **API:** `GET`/`PUT /api/settings` ([API.md](API.md#settings)).

Behavior:

- A change takes effect at once; nothing needs a restart.
- The daemon validates every write. An invalid value is refused with a message naming the key.
- A key missing from the file takes its default.
- A hand-edited file with a value out of range is ignored as a whole: the daemon logs a warning
  and uses the defaults. An invalid `terminal` alone falls back to `auto` and keeps the rest.
- A file that is not valid JSON, has a value of the wrong type, or has an unknown
  `schemaVersion` stops the daemon from starting.
- "Reset all" on the settings page restores the defaults.

### Remember last used

Settings shown as `{value, useLastUsed}` below hold either a fixed default or "the value used
last time":

| Field | Meaning |
|---|---|
| `value` | The fixed default. Empty means the provider's own default |
| `useLastUsed` | When `true`, the value used last time wins once there is one |
| `lastUsed` | Recorded by the client on each use. Not set by hand |

### Projects

| Key | Default | Effect |
|---|---|---|
| `projects.rememberLastSelected` | `true` | Reopen the project this browser had open last |
| `projects.sidebarSort` | `insertion` | Sidebar order: `insertion` (order added) or `name` (A–Z) |
| `projects.confirmRemove` | `true` | Ask before removing a project |

### Workstreams

| Key | Default | Effect |
|---|---|---|
| `workstreams.defaultProvider` | `{value: ""}` | Workstream provider preselected in the create form |
| `workstreams.defaultStrategy` | `{value: ""}` | Filesystem strategy preselected: `in-place`, `worktree` or `copy` |
| `workstreams.nameTemplate` | `""` | Name pre-filled in the web UI's create form |
| `workstreams.location` | `""` | Where worktree and copy directories are created |
| `workstreams.confirmRemove` | `true` | Ask before removing a workstream |

- `nameTemplate`: `{project}` expands to the project name. Empty leaves the name to the
  provider. Only the web UI applies it.
- `location`: an absolute path, a `~/…` path, or a path relative to each project's directory
  (such as `../worktrees`). Empty uses `workstreams/` in the data directory. A copy may not end
  up inside the project it copies.

### Sessions & agents

| Key | Default | Effect |
|---|---|---|
| `sessions.defaultAgent` | `{value: "claude"}` | Stored; no client applies it |
| `sessions.previewLines` | `20` | Terminal lines an expanded session row shows, 1–24 |

The agents themselves are configured in the [agent catalog](API.md#agent-catalog), edited with
[`homeshell agent`](#agent).

### Providers

| Key | Default | Effect |
|---|---|---|
| `providers.refreshIntervalSec` | `0.5` | How often each session's state is re-checked, in seconds (0.1–86400) |

### Open with

`openWith.entries` is the list of [open-with entries](../CONTEXT.md#opening-and-actions).

| Field | Meaning |
|---|---|
| `id` | Stable ID; generated when empty |
| `label` | Name shown in the UI. Required |
| `command` | Shell line run in the workstream's directory |
| `inTerminal` | Run inside a terminal window. Off, the command runs detached |
| `fixed` | Set by the daemon on the entry the plain "Open" runs |

- Default: one entry, `terminal`, labelled "Terminal": an interactive shell in a terminal
  window (empty `command`, `inTerminal: true`).
- The `terminal` entry can be edited but not removed; the daemon restores it if it is missing.
- An empty `command` is allowed only with `inTerminal`, and means an interactive shell (`$SHELL`).
- IDs must be unique.

### Terminal

Which terminal emulator every window the daemon opens comes up in: session attach, workstream
shell, and "open with" entries that run in a terminal.

| Key | Default | Effect |
|---|---|---|
| `terminal.emulator` | `auto` | `auto`, `custom`, or the ID of an emulator listed for this platform |
| `terminal.command` | `""` | For `custom`: a shell line with `{command}` where the command goes |

- `auto` uses `$TERMINAL` when it is on `PATH` (not on macOS), else the first installed emulator.
- The settings page lists the emulators for this platform and marks which are installed.
- Windows opened with `custom` have no title.

### Notifications

| Key | Default | Effect |
|---|---|---|
| `notifications.style` | `native` | `native`, `in-app`, `both` or `none` |
| `notifications.sound` | `default` | `default`, `chime`, `glass`, `ping` or `none` |
| `notifications.waiting.notify` | `true` | Notify when a session needs input |
| `notifications.waiting.sound` | `true` | Play the sound with that notification |
| `notifications.finished.notify` | `true` | Notify when a session finishes working |
| `notifications.finished.sound` | `true` | Play the sound with that notification |
| `notifications.quietSeconds` | `8` | No repeat if the session left that state less than this ago |

- The styles and what they show are in [CONTEXT.md](../CONTEXT.md#agents-and-notifications);
  `none` turns every notification off.
- `quietSeconds` ranges from 0 (off) to 3600. Why: state is read off the terminal screen and
  can flicker, and each flicker would otherwise notify.
- Picking a style or sound on the settings page sends a test notification or plays the sound.

## Log level

The daemon's log level is live runtime configuration, not a setting
([`/api/config`](API.md#daemon)). Change it with `homeshell config set logLevel <level>` or in
the settings page's Diagnostics section.

- Levels: `verbose`, `debug`, `info`, `warn`, `error`.
- At startup the first of these wins: `--log-level`, `HOMESHELL_LOG_LEVEL`, the level last set
  while running, `info`.

## Per-browser preferences

Kept in the browser's `localStorage`, so each browser (and the desktop app) has its own.

| Key | Default | Effect |
|---|---|---|
| `hs-theme` | `system` | Theme: `system`, `light` or `dark`. Set under Appearance or with the toolbar toggle |
| `hs-last-project` | — | Project reopened when `projects.rememberLastSelected` is on |
| `hs-logpane-height` | `260` | Height of the log pane in pixels; set by dragging it |
| `hs-log-buffer` | `1000` | Log entries the log viewer keeps, at least 100. Set under Diagnostics |

## Command-line flags

`homeshell` is one binary with verbs. `homeshell <verb> -h` prints a verb's flags. Flags take one
or two dashes (`-addr` and `--addr` are the same).

Top level: `homeshell --version` prints the version; `homeshell`, `-h`, `--help` and `help` print
usage.

### serve

Runs the daemon in the foreground.

| Flag | Default | Effect |
|---|---|---|
| `--addr` | `127.0.0.1:8420` | Listen address for the API and web UI |
| `--home-dir` | see [Data directory](#data-directory) | Where all data lives |
| `--log-level` | unset | Startup log level (see [Log level](#log-level)) |
| `--allow-public` | `false` | Allow a non-loopback `--addr` |
| `--demo` | `false` | Run with the in-memory demo provider and seeded workstreams instead of the real ones |

- Without `--allow-public` a non-loopback address is refused. With it, what still works and how
  the token guards the API is in [API.md](API.md#authentication).

### Client flags

Every verb that talks to a running daemon (`agent`, `config`, `events`, `project`, `provider`,
`session`, `settings`, `version`, `workstream`) takes these:

| Flag | Default | Effect |
|---|---|---|
| `--home-dir` | see [Data directory](#data-directory) | Find the daemon of this data directory |
| `--addr` | — | Talk to this `host:port` directly instead of finding the daemon |
| `--json` | `false` | Print the daemon's JSON instead of a table |
| `--timeout` | `30s` | How long to wait for an answer |

- With `--addr` the token comes from `HOMESHELL_API_TOKEN`. Failing that, a loopback address
  uses the local daemon's token; any other host gets none.
- `events` streams until interrupted and ignores `--timeout` and `--json`.

### Daemon lifecycle verbs

`status`, `stop` and `ensure-running` take `--home-dir` to pick the daemon, as above.

| Verb | Flag | Default | Effect |
|---|---|---|---|
| `ensure-running` | `--addr` | `127.0.0.1:8420` | Listen address for a daemon it starts |
| `ensure-running` | `--json` | `false` | Print `{url, spawned}` as JSON |
| `ensure-running` | `--timeout` | `15s` | How long to wait for a started daemon to be ready |
| `events` | `--heartbeats` | `false` | Also print the idle heartbeat events |

`update` takes no flags; it asks the daemon of the default data directory to update itself.

### project

| Subcommand | Flag | Effect |
|---|---|---|
| `add <path>` | `--name` | Display name; defaults to the directory's name |

### workstream

| Subcommand | Flag | Effect |
|---|---|---|
| `list` | `--project` | Only this project's workstreams |
| `create` | `--project` | Project to create in. Required |
| `create` | `--provider` | Workstream provider; optional when the project offers one |
| `create` | `--name` | Workstream name |
| `create` | `--config` | Provider options as JSON, e.g. `'{"strategy":"worktree"}'` |
| `providers` | `--project` | Project to list the workstream providers of. Required |
| `rename`, `note`, `clear-status`, `remove`, `open` | `--project` | Narrow the lookup to this project |
| `remove` | `--keep-files` | Keep the working directory on disk |
| `remove` | `--force` | Stop busy sessions and remove anyway |
| `open` | `--mode` | Open mode ID; empty uses the default open |

### session

Every subcommand takes `--project` and `--workstream` to narrow the lookup.

| Subcommand | Flag | Effect |
|---|---|---|
| `new`, `agents` | `--workstream` | Workstream to create in or list the options of. Required |
| `new` | `--agent` | Session option ID; optional when the workstream offers one |
| `new` | `--provider` | Session provider; only needed when two offer the same option |
| `new` | `--config` | Launch options as JSON, e.g. `'{"model":"opus"}'` |
| `restart` | `--config` | New launch options as JSON; omitted, the last ones |
| `restart` | `--fresh` | Start a new conversation instead of continuing the last one |
| `open` | `--mode` | Open mode ID; empty uses the terminal |
| `rename` | `--clear` | Drop the custom title and use the provider's again |

### agent

Edits the agent catalog. Field meanings are in [API.md](API.md#agent-catalog).

| Subcommand | Flag | Effect |
|---|---|---|
| `add`, `set` | `--binary` | Executable to run; also selects the detection rules |
| `add`, `set` | `--display` | Name shown to users |
| `add`, `set` | `--session-id-flag` | Flag that hands the agent a session ID |
| `add`, `set` | `--resume-flag` | Flag that resumes that session; empty means no resume |
| `add`, `set` | `--list-models` | Arguments that make the agent print its models |
| `add`, `set` | `--transcript` | The agent writes a Claude-format transcript |
| `setting-list`, `model-list` | `--agent` | Only this agent's rows |
| `setting-add` | `--agent` | Agent the setting belongs to. Required |
| `setting-add`, `setting-set` | `--field` | Property name in the create form |
| `setting-add`, `setting-set` | `--role` | `model`, `prompt`, `title` or empty |
| `setting-add`, `setting-set` | `--type` | `string` or `boolean` |
| `setting-add`, `setting-set` | `--title` | Label in the create form |
| `setting-add`, `setting-set` | `--description` | Help text in the create form |
| `setting-add`, `setting-set` | `--format` | Rendering hint, e.g. `textarea` |
| `setting-add`, `setting-set` | `--choices` | Comma-separated allowed values; empty means free text |
| `setting-add`, `setting-set` | `--default` | Default value; empty leaves it unset |
| `setting-add`, `setting-set` | `--delivery` | `flag`, `bool-flag`, `assign`, `env` or `positional` |
| `setting-add`, `setting-set` | `--delivery-arg` | Flag or variable name the delivery uses |
| `setting-add`, `setting-set` | `--delivery-key` | Key an `assign` delivery sets, e.g. `model_reasoning_effort` |

## Environment variables

| Variable | Read by | Effect |
|---|---|---|
| `HOMESHELL_API_TOKEN` | daemon, CLI | Pins the API token instead of a fresh one per start; CLI token for `--addr` |
| `HOMESHELL_LOG_LEVEL` | daemon | Startup log level; `--log-level` wins over it |
| `HOMESHELL_HOME_DIR` | desktop app | Data directory of the daemon to attach to or start |
| `HOMESHELL_ADDR` | desktop app | Listen address of a daemon it starts |
| `XDG_DATA_HOME` | daemon, CLI | Parent of the default data directory |
| `XDG_RUNTIME_DIR` | daemon | Where [session bridge](ARCHITECTURE.md#session-bridge) sockets go |
| `TERMINAL` | daemon | Emulator `terminal.emulator: auto` prefers, outside macOS |
| `SHELL` | daemon | Shell for terminal windows with no command; else `/bin/sh` |
| `CLAUDE_CONFIG_DIR` | daemon | Where Claude Code keeps its transcripts; else `~/.claude` |

- `HOMESHELL_API_TOKEN`: the daemon warns when a pinned token under 32 characters guards a
  network-reachable bind.

### For tests

Read by the daemon, meant for automated runs rather than users.

| Variable | Effect |
|---|---|
| `HOMESHELL_SETTINGS_FILE` | Path of the settings file, instead of `settings.json` in the data directory |
| `HOMESHELL_NO_NATIVE_DIALOG` | `1`, `true`, `yes` or `on` turns the native folder chooser off |
| `HOMESHELL_UPDATE_BASE_URL` | Download updates from this server instead of `download.eclipse.org` |
| `HOMESHELL_FAKE_SESSION_STARTUP_MS` | With `--demo`, new sessions take this many ms to start, with fake output |
