# HomeShell — build and development entry points.

GO        ?= go
NODE      ?= node
PNPM      ?= pnpm
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BIN_DIR   := bin
BINARY    := $(BIN_DIR)/homeshell
DESKTOP_DIR := desktop
DESKTOP_BIN := $(BIN_DIR)/homeshell-desktop
WEB_DIST  := webapp/apps/web/dist
EMBED_DIR := daemon/internal/webui/dist

.PHONY: all help setup check-tools build build-webapp embed build-daemon build-desktop check-webview check-desktop-linkage package-linux run run-local run-desktop run-desktop-local dev dev-local dev-demo dev-daemon dev-update dev-update-build dev-update-server dev-update-daemon dev-webapp test test-docs-lint test-e2e check-dev-targets print-dev-token lint docs-lint fmt clean

all: build

# Report every missing prerequisite at once instead of failing on the first.
check-tools:
	@missing=""; \
	command -v $(GO) >/dev/null || missing="$$missing  $(GO) — install Go 1.24+\n"; \
	command -v $(NODE) >/dev/null || missing="$$missing  $(NODE) — install Node.js 24+\n"; \
	command -v $(PNPM) >/dev/null || missing="$$missing  $(PNPM) — install pnpm 11+\n"; \
	if [ -n "$$missing" ]; then \
		printf 'Missing required tools:\n%b' "$$missing"; \
		printf 'See the Prerequisites section in README.md for instructions.\n'; \
		exit 1; \
	fi

## setup        install all dependencies (Go modules + pnpm workspace + Playwright browser)
setup: check-tools
	cd daemon && $(GO) mod download
	cd webapp && $(PNPM) install
	cd webapp && $(PNPM) --filter @homeshell/web-e2e exec playwright install chromium

## build        build the web app, embed it, and produce bin/homeshell
build: build-webapp build-daemon

build-webapp:
	cd webapp && $(PNPM) nx build @homeshell/web

## embed        copy the built web app into the daemon's embed directory
embed:
	rm -rf $(EMBED_DIR)
	mkdir -p $(EMBED_DIR)
	cp -r $(WEB_DIST)/. $(EMBED_DIR)/
	touch $(EMBED_DIR)/.gitkeep

build-daemon: embed
	cd daemon && $(GO) build -ldflags "-X main.version=$(VERSION)" -o ../$(BINARY) ./cmd/homeshell

# webview_go hardcodes `pkg-config: webkit2gtk-4.0`, which Ubuntu dropped in
# 24.04. packaging/linux/pkgconfig/ answers that request from 4.1 — the same C
# API — so the build works on current distros; see the .pc file for the details.
#
# The shim is used whenever 4.1 is available, because 4.1 is what a released
# binary links and a dev build should match it. Only a machine *without* 4.1
# falls back to a system webkit2gtk-4.0, so an older workstation can still
# build; check-webview fails when neither is present.
#
# Note the fallback has to be conditional rather than a matter of search order:
# PKG_CONFIG_PATH is searched ahead of the system pkgconfig directories, so
# putting the shim in it at all shadows a real webkit2gtk-4.0.pc, whichever end
# of PKG_CONFIG_PATH it sits at.
#
# Linux only: macOS builds against the WebKit framework, which does not go
# through pkg-config.
WEBKIT_PC_DIR := $(CURDIR)/packaging/linux/pkgconfig
ifeq ($(shell uname -s),Linux)
DESKTOP_PKG_CONFIG = $(shell pkg-config --exists webkit2gtk-4.1 \
                       && printf 'PKG_CONFIG_PATH=%s' '$(WEBKIT_PC_DIR)$(if $(PKG_CONFIG_PATH),:$(PKG_CONFIG_PATH),)')
endif

## build-desktop build the native desktop shell (CGO; needs a system webview:
##               libwebkit2gtk-4.1-dev on Linux)
build-desktop: check-webview
	cd $(DESKTOP_DIR) && $(DESKTOP_PKG_CONFIG) CGO_ENABLED=1 $(GO) build -ldflags "-X main.version=$(VERSION)" -o ../$(DESKTOP_BIN) ./cmd/homeshell-desktop
	@$(MAKE) --no-print-directory check-desktop-linkage

# A successful build is not evidence that PKG_CONFIG_PATH above was honored: the
# Go build cache does not key on it, so an object cached from a previous webkit
# selection is reused and the binary silently links the other version
# (docs/DEV.md, "Desktop shell"). Verified here because the mismatch is otherwise invisible
# until the packaged .deb cannot resolve its library on a user's machine.
check-desktop-linkage:
ifneq ($(DESKTOP_PKG_CONFIG),)
	@ldd $(DESKTOP_BIN) | grep -q 'libwebkit2gtk-4\.1' || { \
		printf '%s was built against the wrong WebKitGTK:\n' '$(DESKTOP_BIN)'; \
		ldd $(DESKTOP_BIN) | grep -i webkit || true; \
		printf 'Expected libwebkit2gtk-4.1 (what the .deb/.rpm declares).\n'; \
		printf 'Run `go clean -cache` and build again — the build cache ignores\n'; \
		printf 'PKG_CONFIG_PATH, so it reuses an object from a previous selection.\n'; \
		exit 1; \
	}
endif

# Name the package to install. Without this the failure is a wall of pkg-config
# and cgo output that never says which library is missing.
check-webview:
ifeq ($(shell uname -s),Linux)
	@pkg-config --exists webkit2gtk-4.1 || pkg-config --exists webkit2gtk-4.0 || { \
		printf 'Missing the system webview development files.\n'; \
		printf '  Debian/Ubuntu: sudo apt-get install libwebkit2gtk-4.1-dev\n'; \
		printf '  Fedora:        sudo dnf install webkit2gtk4.1-devel\n'; \
		exit 1; \
	}
endif

DAEMON_ARGS ?=
HOME_BRANCH ?= $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null | tr '/' '-')
LOCAL_ARGS   = -home-dir $(CURDIR)/db/$(HOME_BRANCH)

## run          build everything and start the daemon (debug logging, platform data dir)
run: build
	./$(BINARY) serve -log-level=debug $(DAEMON_ARGS)

## run-local    like run, but store all data in db/<branch> (gitignored)
run-local: build
	./$(BINARY) serve -log-level=debug $(LOCAL_ARGS) $(DAEMON_ARGS)

# The desktop shell takes no arguments (a launcher or a double-clicked .app
# passes none), so which daemon it talks to comes from the environment. Without
# a home dir it uses the platform default on the default port, which is a
# collision rather than an attach when a repo-local daemon is already up.
HOMESHELL_HOME_DIR ?=
HOMESHELL_ADDR     ?=

## package-linux build the Linux .deb/.rpm locally (needs goreleaser installed)
##               Usage: make package-linux
#
# GoReleaser's own config cannot name the shim directory: it has no template for
# the project root, and a relative PKG_CONFIG_PATH does not resolve because the go
# tool runs pkg-config from the cgo package's own directory in the module cache —
# verified by building, which produced a silently 4.0-linked binary. So the caller
# supplies the absolute path, and make is a caller that knows it.
#
# check-webview is not enough here: it accepts a 4.0-only machine, which
# build-desktop supports but a package cannot, since the shim below needs 4.1 and
# the declared dependency names it. So require 4.1 specifically.
package-linux: check-webview
	@pkg-config --exists webkit2gtk-4.1 || { \
		printf 'Packaging needs WebKitGTK 4.1: the .deb/.rpm declare it, and the\n'; \
		printf 'pkg-config shim resolves to it. A 4.0-only machine can build the\n'; \
		printf 'shell (make build-desktop) but cannot produce a package.\n'; \
		printf '  Debian/Ubuntu: sudo apt-get install libwebkit2gtk-4.1-dev\n'; \
		printf '  Fedora:        sudo dnf install webkit2gtk4.1-devel\n'; \
		exit 1; \
	}
	PKG_CONFIG_PATH="$(WEBKIT_PC_DIR)$(if $(PKG_CONFIG_PATH),:$(PKG_CONFIG_PATH),)" \
		goreleaser release --snapshot --clean

## run-desktop   build daemon + web + desktop shell, then open the desktop window
##               (HOMESHELL_HOME_DIR / HOMESHELL_ADDR pick the daemon to attach to)
run-desktop: build build-desktop
	HOMESHELL_HOME_DIR="$(HOMESHELL_HOME_DIR)" HOMESHELL_ADDR="$(HOMESHELL_ADDR)" ./$(DESKTOP_BIN)

## run-desktop-local  like run-desktop against db/<branch>, so it attaches to a
##                    `make run-local` daemon instead of colliding with it
run-desktop-local:
	$(MAKE) run-desktop HOMESHELL_HOME_DIR=$(CURDIR)/db/$(HOME_BRANCH)

## dev          run daemon and web dev server side by side (debug logging, platform data dir)
dev:
	$(MAKE) -j2 dev-daemon dev-webapp

## dev-local    like dev, but store all data in db/<branch> (gitignored)
dev-local:
	$(MAKE) -j2 dev-daemon dev-webapp DAEMON_ARGS="$(LOCAL_ARGS)"

dev-demo:
	$(MAKE) -j2 dev-daemon dev-webapp DAEMON_ARGS=-demo

# The daemon requires an API token on every /api request; in dev vite serves the
# UI and proxies /api, so it injects this token as a Bearer header
# (vite.config.mts). Both sides must agree, so pin a shared token for the dev
# targets (overridable). Production `make run` needs none: the daemon serves the
# UI itself and hands the browser the token via an HttpOnly cookie.
#
# The assignment is required: a target-scoped `export VAR` with no `= value` is
# parsed as two prerequisites, so `make dev*` died with "No rule to make target
# 'export'". check-dev-targets below is the gate that catches that.
# `?=` is not enough: it only fills in an *undefined* variable, so `export
# HOMESHELL_API_TOKEN=` in the shell survives as empty, reaches the daemon and the proxy
# as no token at all, and every `make dev*` then serves a UI that renders and 401s. The
# gate below could not see it either, because it compared empty against empty. `override`
# is what lets this reassign a command-line value; a non-empty one is preserved, since the
# right-hand side expands it. Same rule as the e2e config: empty or whitespace is no value.
override HOMESHELL_API_TOKEN := $(or $(strip $(HOMESHELL_API_TOKEN)),homeshell-dev-token)
dev-daemon dev-webapp dev-update-daemon print-dev-token: export HOMESHELL_API_TOKEN := $(HOMESHELL_API_TOKEN)

dev-daemon:
	cd daemon && $(GO) run ./cmd/homeshell serve $(DAEMON_ARGS)

## dev-update   test the self-update flow: build current (v0.0.1), compile a
##              separate "new" binary (v0.0.2), start the fake release server,
##              the daemon, and vite.  Usage: make dev-update [UPDATE_VERSION=0.0.2]
UPDATE_VERSION ?= 0.0.2
UPDATE_PORT    ?= 9876
# The running daemon must report a version *below* UPDATE_VERSION or the updater
# finds nothing to offer. VERSION defaults to `git describe`, which on a tagged
# checkout is higher than 0.0.2, so the dev-update targets pin it.
CURRENT_VERSION ?= 0.0.1

# Both binaries: the daemon under test at CURRENT_VERSION, and the "new" release the
# fake server publishes at UPDATE_VERSION.
dev-update-build:
	$(MAKE) build VERSION=$(CURRENT_VERSION)
	./scripts/fake-release-server.sh build $(UPDATE_VERSION)

# PREBUILT=1 tells the two serve targets that both binaries already exist, which is what
# dev-update does after running dev-update-build. Without it they rebuild — so each works
# standalone — and rebuilding *in the -j3 stage* is not merely redundant but broken: both
# paths go through `embed`, which `rm -rf`s the shared embed directory, and whichever
# `go build` reads it in that window dies on `//go:embed all:dist`.
PREBUILT ?=

dev-update:
	$(MAKE) dev-update-build
	$(MAKE) -j3 PREBUILT=1 dev-update-server dev-update-daemon dev-webapp

dev-update-server:
	[ -n "$(PREBUILT)" ] || ./scripts/fake-release-server.sh build $(UPDATE_VERSION)
	./scripts/fake-release-server.sh serve $(UPDATE_VERSION) $(UPDATE_PORT)

dev-update-daemon:
	[ -n "$(PREBUILT)" ] || $(MAKE) build VERSION=$(CURRENT_VERSION)
	HOMESHELL_UPDATE_BASE_URL=http://127.0.0.1:$(UPDATE_PORT) ./$(BINARY) serve

# Run vite directly rather than through `nx dev`: the Nx terminal UI puts the
# shared TTY into raw mode (Ctrl-C stops generating SIGINT for the whole
# `make -j2` group) and Nx detaches continuous tasks into their own session,
# leaking the vite process when the make group is torn down.
dev-webapp:
	cd webapp/apps/web && $(PNPM) exec vite dev

## test         run daemon and webapp unit tests (+ docs-lint and dev-target gates)
test: test-docs-lint check-dev-targets
	cd daemon && $(GO) test -tags=dev ./...
	cd $(DESKTOP_DIR) && $(GO) test ./internal/...
	cd webapp && $(PNPM) nx run-many -t test

# print-dev-token exists only for check-dev-targets: a dry run cannot see whether the
# token really lands in a dev recipe's environment, so this prints what a dev recipe
# would see. It shares the export line above, so it fails exactly when they would.
print-dev-token:
	@printenv HOMESHELL_API_TOKEN

DEV_TARGETS = dev dev-local dev-demo dev-daemon dev-webapp \
              dev-update dev-update-build dev-update-server dev-update-daemon

## check-dev-targets  check that every dev target parses, has a recipe, and gets the
##              API token (run by `make test`)
#
# Why it exists: a target-scoped `export VAR` without `= value` parses as two
# prerequisites, which broke every `make dev*` target while CI stayed green because the
# e2e harness pins its own token; and `dev-update`/`dev-update-build` were .PHONY with no
# recipe at all, which make reports as success. Nothing else in the suite parses these
# rules, so both were invisible. See docs/DEV.md, "Dev-target gate".
check-dev-targets:
	@for target in $(DEV_TARGETS); do \
		out=$$($(MAKE) --dry-run --no-print-directory $$target 2>&1) || { echo "$$out"; exit 1; }; \
		case "$$out" in \
			*"Nothing to be done"*) \
				echo "make $$target has no recipe — it is .PHONY and exits 0 doing nothing"; \
				exit 1 ;; \
		esac; \
	done
	@token=$$($(MAKE) --no-print-directory print-dev-token); \
	test -n "$$token" \
		|| { echo "HOMESHELL_API_TOKEN reaches the dev recipes empty, which is no token"; exit 1; }; \
	test "$$token" = "$(HOMESHELL_API_TOKEN)" \
		|| { echo "HOMESHELL_API_TOKEN is not exported into the dev recipes"; exit 1; }
	@wipes=$$($(MAKE) --dry-run --no-print-directory dev-update \
		| grep -c 'rm -rf $(EMBED_DIR)' || true); \
	test "$$wipes" = 1 || { \
		echo "make dev-update wipes $(EMBED_DIR) $$wipes times, want exactly 1"; \
		echo "  more than 1: the -j3 stage is building again, so two concurrent 'embed' runs"; \
		echo "  rm -rf that directory and a go build reading it dies on //go:embed all:dist"; \
		echo "  (see PREBUILT above). 0: this check stopped matching and now proves nothing."; \
		exit 1; \
	}

## test-docs-lint  test the docs lint against throwaway repos (scripts/docs-lint)
test-docs-lint:
	bash scripts/docs-lint/lint.test.sh

## test-e2e     run Playwright e2e tests (daemon API + web UI); starts both servers
test-e2e:
	cd webapp && $(PNPM) nx e2e @homeshell/web-e2e

## fmt          format Go source files
fmt:
	gofmt -w daemon/ $(DESKTOP_DIR)/

## docs-lint    check Markdown line length and links to files that don't exist
docs-lint:
	bash scripts/docs-lint/lint.sh

## lint         static checks for both projects, plus the docs lint
lint: docs-lint
	@unformatted=$$(gofmt -l daemon/ $(DESKTOP_DIR)/); \
	if [ -n "$$unformatted" ]; then \
		printf 'Go files not formatted (run "make fmt" to fix):\n'; \
		printf '  %s\n' $$unformatted; \
		exit 1; \
	fi
	cd daemon && $(GO) vet -tags=dev ./...
	cd $(DESKTOP_DIR) && $(GO) vet ./internal/...
	cd webapp && $(PNPM) nx run-many -t lint typecheck

## clean        remove build artifacts
clean:
	rm -rf $(BIN_DIR) $(WEB_DIST) $(EMBED_DIR) scripts/fake-release
	mkdir -p $(EMBED_DIR)
	touch $(EMBED_DIR)/.gitkeep

## help         list available targets
help:
	@grep -E '^## ' Makefile | sed 's/^## //'
