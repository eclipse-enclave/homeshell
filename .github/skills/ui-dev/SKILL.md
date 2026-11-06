---
name: ui-dev
description: 'Develop and verify UI changes in the browser. Use when: making frontend changes, tweaking CSS/styles, adding UI components, adjusting layout, visual verification, theme changes, responsive design work. Opens localhost:4200, ensures the dev environment is running, and validates changes visually.'
argument-hint: 'Describe the UI change to make and verify'
---

# UI Development & Visual Verification

Workflow for making UI changes and verifying them live in the browser.
The HomeShell web app runs at `http://localhost:4200` during development.

## Prerequisites — Dev Environment

Before touching any UI code, confirm the dev environment is running.

1. **Check terminals** for a running `make dev` process (look for a terminal
   whose last command is `make dev` with no exit code, or whose output shows
   vite/daemon logs).
2. **If not running**, ask the user:
   > "The dev environment doesn't appear to be running. Should I start it with `make dev`?"
   - If yes, run `make dev` in async mode (it stays running). Wait for vite to
     report the local URL before proceeding.
   - If the user says it's running elsewhere, proceed.
3. **If already running**, proceed immediately — do not restart.

`make dev` starts both the Go daemon and the Vite dev server (with HMR) in
parallel. The webapp is served at `http://localhost:4200`.

## Procedure

### 1. Open the App

Open `http://localhost:4200` in the browser tool. If a page for that URL is
already shared, reuse it — do not open a duplicate.

### 2. Capture the Current State

Take a screenshot or read the page snapshot to understand the current layout
before making changes. This gives you the baseline to compare against.

### 3. Make the Code Changes

Edit the relevant files. The webapp source lives under:

- `webapp/apps/web/src/` — the main web app (React + CSS Modules)
- `webapp/packages/components/` — shared component library (`@homeshell/components`)

Key conventions:

- **CSS custom properties** are defined in `webapp/apps/web/src/styles.css` on
  `:root` (dark theme) and `[data-theme='light']` (light theme). All variables
  use the `--hs-` prefix.
- **CSS Modules** — every component has a co-located `.module.css` file.
- Components in the shared library carry inline fallbacks for every CSS variable
  (e.g. `var(--hs-text, #dbe4ee)`) so they render standalone.

### 4. Verify Visually

After saving changes (HMR applies them automatically):

1. **Reload** the browser page if HMR didn't pick up the change.
2. **Read the page** snapshot to verify structural/accessibility correctness.
3. **Take a screenshot** to verify visual appearance.
4. If the change involves theme support, toggle between dark and light themes
   and screenshot both.

### 5. Run Unit Tests

Run the webapp unit tests to make sure nothing broke:

```
cd webapp && npx nx test web -- --run
```

Or use the test runner tool with the relevant spec files.

### 6. Report to User

Show the screenshot and briefly confirm what changed. If something looks off,
iterate on the code and re-verify.
