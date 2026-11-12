# HomeShell

HomeShell watches and drives AI coding agents across projects. It maps every agent runtime onto
one normalized model of projects, workstreams and sessions, which clients render as plain data.

## Language

### Core entities

**Project**:
A named, remembered reference to a codebase on disk, optionally git-backed. Holds any number of
workstreams.
_Avoid_: repo, codebase (for the entity)

**Workstream**:
One line of work in a project: a working directory plus the sessions running in it, possibly
inside a container.
_Avoid_: workspace, environment, task

**Default workstream**:
The workstream every project is added with: in place, in the project folder, on the branch
checked out there and, until renamed, named after it.

**Session**:
One agent conversation running inside a workstream. A workstream holds any number of sessions,
which may run different agents.
_Avoid_: thread, run, job

**Agent**:
The coding CLI that drives a session, such as Claude Code or Codex.
_Avoid_: tool, model

**Daemon**:
The long-lived HomeShell background process that owns all state and logic. Clients (web UI,
desktop app, CLI) only display what it sends.
_Avoid_: server

### Providers

**Workstream provider**:
The owner of a workstream's record and working directory, from creation to removal.

**Core provider**:
The built-in workstream provider, and the only one.

**Session provider**:
The owner of one kind of session: the session options on offer, the sessions started from them,
and their live state. It works directly or through an external tool.
_Avoid_: backend, runtime, driver

**Local System**:
The session provider that runs agents as processes on the user's own machine.

**Session bridge**:
HomeShell's own terminal host for one agent, part of the daemon: it keeps the agent's rendered
screen and lets terminals attach and detach. Local System and Enclave sessions both run under it.
_Avoid_: tmux, sbridge (its package name)

**Workstream schema**:
The creation form a workstream provider offers for a new workstream, such as its filesystem
strategy and branch.
_Avoid_: workstream template

**Session option**:
One kind of session a session provider offers in a workstream, with a label and a creation form.
_Avoid_: session type, template

**Capability**:
An optional provider feature, declared as data, that clients use to decide what to offer.
Clients gate on capabilities, never on which provider it is.
_Avoid_: feature flag

**Adopted workstream**:
A workstream HomeShell did not create, made up on the fly for sessions a provider reports in a
directory that no workstream claims. A folder that is an untracked worktree is never adopted.

### Working directory

**Working directory**:
The directory a workstream's sessions run in, fixed when the workstream is created. It is how
sessions are matched to their workstream, so it is unique within a project.
_Avoid_: path, worktree (unless it is one), checkout

**Filesystem strategy**:
How a workstream gets its working directory: **in place** (the project directory itself), a
**copy** of the project directory, or a git **worktree**. The UI shows *copy* as **folder**.
_Avoid_: isolation mode

**Separate folder**:
A workstream's working directory when it is a **worktree** or a **folder** (copy), not the project
directory itself.

**Untracked worktree**:
A linked git worktree of a project's repository that no workstream works in. HomeShell suggests
adding it; it never adds one on its own.
_Avoid_: orphan worktree, discovered worktree

**Note**:
A Markdown file a workstream keeps in its working directory, readable by the user and by the
agents working there.

### States

**Session state**:
The normalized state of a session, whatever agent drives it: **idle** (ready), **running**
(working), **waiting** (needs user input), **starting** (still being brought up), **unknown**
(signal lost), **stopped** (not running, can be started again), **done** (finished on its own)
or **error** (never started).
_Avoid_: status

**Aggregate state**:
The session state of a workstream or project, derived from its children attention-first: the
state that most needs the user wins, **done** shows only when every child is done, and no
children reads as **idle**.

**Runtime state**:
The lifecycle of the environment a workstream runs in, such as its container: idle, creating,
starting, running, stopping, stopped, unknown or error. Independent of session state.
_Avoid_: container state

**Effective state**:
The single state a workstream or project displays: its runtime state while that is creating,
starting, stopping, stopped or error, otherwise its aggregate state.

**Busy**:
A session whose teardown would interrupt live work: running, waiting or starting.
Removing a workstream with a busy session needs to be forced.

**Terminal state**:
A session state in which the session is no longer running: done, stopped or error. Sessions in a
terminal state do not count towards a provider's concurrency cap. An error is terminal but not an
**ended session**.
Not to be confused with the terminal a session runs in.
_Avoid_: finished, inactive

**Ended session**:
A session that is stopped or done, a terminal state other than error. It is kept until removed
and can be continued.

**Detection**:
The derivation of a session's state from its rendered terminal screen.
_Avoid_: polling, scraping

### Session lifecycle

**Placeholder**:
The stand-in row the daemon shows for a workstream or session while its creation is in flight;
the real one replaces it, or it stays to report the failure until the user removes it.

**Launch config**:
What a session was last started or restarted with: its provider, its session option and that
option's settings.

**Restart with settings**:
A restart of a session from its launch config, optionally changed first.

**Restart in HomeShell**:
A restart of a session running in a terminal outside HomeShell that ends it there and continues its
conversation in HomeShell.

**Outdated settings**:
Provider settings whose current value differs from what a running session started with, so a
restart would pick up the change.

### Opening and actions

**Open mode**:
One way to hand a workstream to the user for interactive work, offered by the daemon as data:
an open-with entry, a session shell, or a link.
_Avoid_: handoff, launcher, opener

**Open-with entry**:
A user-configured application, such as an editor or terminal emulator, that opens a workstream's
working directory. The default entry opens a terminal there.

**Session shell**:
An interactive shell in the same environment as one session, for example inside its container.

**Link**:
A URL a provider offers for a session, opened in the browser.

**Action**:
A workstream-level command the daemon offers when it is possible right now, such as opening the
workstream's note.

### Agents and notifications

**Agent catalog**:
The user-editable list of agents HomeShell can launch, with the settings each offers and the
models it lists.

**Notification**:
A fire-once alert the daemon raises when a session starts waiting for input or finishes its
work. Never merged or replayed, unlike state.
_Avoid_: alert, toast (a toast is one way to show it)

**Notification style**:
How notifications reach the user: **in-app**, **native** (the operating system's own), **both**,
or **none**.

### Eclipse Enclave

**Eclipse Enclave**:
The external tool that runs agents in containers. HomeShell's Enclave session provider wraps it;
Enclave is not a workstream provider.
_Avoid_: yoloarena

**CLI session**:
An Enclave session started with the `enclave` CLI rather than in HomeShell. HomeShell attaches to
a background one and runs it as its own; a foreground one runs in the user's terminal.

**Sandbox**:
The container an Enclave session runs in, together with its settings, which Enclave owns and
HomeShell only passes through.

### Releases

**Release channel**:
Where a standalone release build gets its updates: **rolling** (every push to `main`) or
**stable** (tagged releases). Stamped into the binary at build time; packages, the macOS app and
source builds have none and never update themselves.
_Avoid_: track, stream (for the channel)
