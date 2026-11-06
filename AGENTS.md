# Agent guidance for HomeShell

Start at [docs/README.md](docs/README.md), the docs index. Key principle: all domain logic lives
in the Go daemon; the web UI only renders data.

When editing a file, check `.github/instructions/` for `*.instructions.md` files and follow
those whose `applyTo` glob matches it.

## Testing requirements

Every piece of new functionality you implement must come with tests — both
levels, not one or the other:

1. **Unit tests**
   - Go: `_test.go` files next to the package (prefer table-driven tests).
   - TypeScript: vitest `*.spec.ts` / `*.spec.tsx` files next to the source
     (Testing Library for React components).
2. **End-to-end tests** in `webapp/apps/web-e2e`:
   - New daemon API behavior → `src/api/` (black-box HTTP against the
     daemon's public API, no browser; these must survive a daemon rewrite).
   - New UI behavior → `src/ui/` (Playwright driving Chromium; prefer
     role-based selectors).

Bug fixes get a regression test at the level where the bug lived.

## Documentation

Docs change in the same PR as the code they describe. Where a doc and the code disagree, the code
wins: fix the doc. `CONTEXT.md` at the root is the glossary of domain terms.

### Trigger table

| Change touches | Update |
|---|---|
| daemon API handlers, SSE events, API types | `docs/API.md` |
| providers | `docs/PROVIDERS.md` |
| settings, CLI flags, env vars | `docs/CONFIGURATION.md` |
| Makefile, CI workflows, toolchain, test setup | `docs/DEV.md` |
| new package, process topology, state model, persistence | `docs/ARCHITECTURE.md` + diagram |
| new or renamed domain term | `CONTEXT.md` |
| user-visible behavior | `changelog` block in the PR description |
| install, run, first use | `README.md` |

### Changelog

Do not edit `CHANGELOG.md` in a PR, except the release-prep PR. Describe user-visible changes in
the `changelog` block of the PR description, or write `none` there, as
[Changelog notes](docs/DEV.md#changelog-notes) explains.

When reviewing, a missing `CHANGELOG.md` entry is not a finding. These are: an empty or missing
`changelog` block, a user-visible change marked `none`, and a note not written in the user's
terms.

### Writing rules

- **One home per fact.** Say it once and link to it from elsewhere; describe behavior rather
  than restating code.
- **Diagrams** (Mermaid) for states, sequences and topology, updated in the same PR as the
  behavior they show.
- **Scannable.** Short sections, lists over paragraphs, prose wrapped at ~100 columns, hard limit
  120 characters per line (table rows included).
- **Current state only.** Describe how the system works now. Rationale is a one-line "Why" next to
  the behavior in the owning doc, or goes in the commit body; the repo has no ADRs.
- **Code references** by file or package path, plus a symbol name where one function is the
  entry point. Point at sections by heading; line numbers and section numbers go stale.

## Before you finish

```sh
make test      # go test + vitest unit suites + the docs-lint tests
make test-e2e  # Playwright e2e (starts daemon + vite itself)
make lint      # docs lint + gofmt check + go vet + nx lint/typecheck
```

All three must pass. If you changed UI behavior, verify it visually as well
(e.g. `make run` and check http://127.0.0.1:8420). Update the docs the trigger
table names for your change.
