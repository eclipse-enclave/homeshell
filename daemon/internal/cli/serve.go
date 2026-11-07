package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/agentcatalog"
	"github.com/eclipsesource/homeshell/daemon/internal/api"
	"github.com/eclipsesource/homeshell/daemon/internal/daemonstate"
	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/filedialog"
	"github.com/eclipsesource/homeshell/daemon/internal/git"
	"github.com/eclipsesource/homeshell/daemon/internal/logging"
	"github.com/eclipsesource/homeshell/daemon/internal/notify"
	"github.com/eclipsesource/homeshell/daemon/internal/opener"
	"github.com/eclipsesource/homeshell/daemon/internal/persist"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/provider/fake"
	"github.com/eclipsesource/homeshell/daemon/internal/settings"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
	"github.com/eclipsesource/homeshell/daemon/internal/updater"

	// Blank-import provider packages so their init() self-registers.
	_ "github.com/eclipsesource/homeshell/daemon/internal/provider/enclave"
	_ "github.com/eclipsesource/homeshell/daemon/internal/provider/local"
)

// demoInterval is the cadence of the demo simulator's state transitions.
const demoInterval = 3 * time.Second

// fakeSessionStartupEnv makes the demo provider's session creation take a while
// and stream synthetic build output while it does, so the starting-session
// placeholder and its log tail can be driven over the public API. Read from the
// environment for the same reason as HOMESHELL_UPDATE_BASE_URL above: it exists
// for the test harness, not for users, so it stays off the flag surface.
const fakeSessionStartupEnv = "HOMESHELL_FAKE_SESSION_STARTUP_MS"

// fakeSessionStartup reads that knob. Anything unparseable or non-positive means
// off, which is the default and what the demo runs with.
func fakeSessionStartup() time.Duration {
	ms, err := strconv.Atoi(strings.TrimSpace(os.Getenv(fakeSessionStartupEnv)))
	if err != nil || ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

// newUpdateService builds the self-update service for the release channel this
// binary was stamped with. HOMESHELL_UPDATE_BASE_URL points it at another
// download server (dev and e2e runs).
func newUpdateService(env Env) *updater.Service {
	return updater.NewService(updater.New(updater.Config{
		Version: env.Version,
		Commit:  env.Commit,
		Channel: env.Channel,
		BaseURL: os.Getenv("HOMESHELL_UPDATE_BASE_URL"),
	}))
}

func init() {
	register(Command{
		Name:    "serve",
		Summary: "Run the daemon (serves the HTTP API and the web UI)",
		Run:     runServe,
	})
}

// serveFlags holds the parsed flags for the `serve` verb.
type serveFlags struct {
	addr        string
	demo        bool
	logLevel    string
	homeDir     string
	allowPublic bool
}

// parseServeArgs parses the flags for the `serve` verb.
func parseServeArgs(args []string, stderr io.Writer) (serveFlags, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addrFlag := fs.String("addr", "127.0.0.1:8420", "listen address for the HTTP API and web UI")
	demoFlag := fs.Bool("demo", false, "register the in-memory fake provider with seeded workstreams and a session simulator")
	logLevelFlag := fs.String("log-level", "", "log verbosity: verbose, debug, info, warn, or error (default info; overrides HOMESHELL_LOG_LEVEL)")
	homeDirFlag := fs.String("home-dir", "", "directory for all persisted data: state and catalog files, daemon.json, provider data (default: platform data dir)")
	allowPublicFlag := fs.Bool("allow-public", false, "allow binding a non-loopback address (exposes the token-protected API to the network)")
	if err := fs.Parse(args); err != nil {
		return serveFlags{}, err
	}
	return serveFlags{addr: *addrFlag, demo: *demoFlag, logLevel: *logLevelFlag, homeDir: *homeDirFlag, allowPublic: *allowPublicFlag}, nil
}

// resolveLogLevel picks the startup log level and reports whether it was set
// explicitly: the -log-level flag, else HOMESHELL_LOG_LEVEL (explicit in both
// cases), else info. A persisted level takes precedence over that default.
func resolveLogLevel(flagVal string) (level slog.Level, explicit bool, err error) {
	v := flagVal
	if v == "" {
		v = os.Getenv("HOMESHELL_LOG_LEVEL")
	}
	if v == "" {
		return slog.LevelInfo, false, nil
	}
	parsed, err := logging.ParseLevel(v)
	return parsed, err == nil, err
}

// runServe parses the serve flags and starts the daemon, returning an exit code.
func runServe(env Env, args []string) int {
	flags, err := parseServeArgs(args, env.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	level, explicit, err := resolveLogLevel(flags.logLevel)
	if err != nil {
		fmt.Fprintf(env.Stderr, "homeshell: %v\n", err)
		return 2
	}
	logLevelVar := logging.NewLevel(level)
	logHub := logging.Configure(env.Stderr, logLevelVar)
	// Set the home directory before anything reads paths (discover, DB open, etc.).
	if flags.homeDir != "" {
		persist.SetHomeDir(flags.homeDir)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, env, flags, logLevelVar, logHub, explicit); err != nil {
		slog.Error("daemon exited", "error", err)
		return 1
	}
	return 0
}

// run starts the daemon. It honors flags.addr, flags.demo and
// flags.allowPublic only: runServe has already applied flags.logLevel (into
// logLevel/logLevelExplicit) and flags.homeDir (into persist.SetHomeDir), so
// setting either in a directly-constructed serveFlags has no effect.
func run(ctx context.Context, env Env, flags serveFlags, logLevel *logging.Level, logHub *logging.Hub, logLevelExplicit bool) error {
	addr, demoEnabled, allowPublic := flags.addr, flags.demo, flags.allowPublic
	version := env.Version
	// Refuse to start alongside a daemon that is already running and reachable;
	// a stale state file is simply overwritten.
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 2*time.Second)
	existing, phase, _ := discover(probeCtx)
	cancelProbe()
	if phase == daemonRunning {
		return fmt.Errorf("a daemon is already running (pid %d) at %s", existing.PID, existing.Address)
	}

	// A non-loopback bind exposes the API to the network; refuse it unless the
	// operator opted in. This is the early gate, so a public socket is never
	// opened only to be closed again; publicBind below decides.
	loopback, classifyErr := classifyBind(addr)
	if classifyErr != nil {
		// Unresolvable host: stay fail-closed without mislabeling it "public".
		if !allowPublic {
			return fmt.Errorf("cannot verify %q binds only loopback (%v); pass --allow-public if you intend to expose the API to the network", addr, classifyErr)
		}
		// allowPublic: fall through and let the bound address decide.
	} else if !loopback && !allowPublic {
		return fmt.Errorf("refusing to bind non-loopback address %q; pass --allow-public to expose the API to the network", addr)
	}

	// The API token is a per-startup secret unless the operator pins one via the
	// environment. It is published in the state file (0600) for the CLI and set
	// as a SameSite cookie on the served web UI. Trimmed on intake because the
	// header path trims the presented value, so a pinned token with trailing
	// whitespace would authenticate over the cookie but never over Bearer.
	apiToken := strings.TrimSpace(os.Getenv("HOMESHELL_API_TOKEN"))
	tokenFromEnv := apiToken != ""
	if !tokenFromEnv {
		generated, err := generateToken()
		if err != nil {
			return fmt.Errorf("generating API token: %w", err)
		}
		apiToken = generated
	}

	listener, err := listenTCP("tcp", addr)
	if err != nil {
		return err
	}

	// publicBind is whether the API is actually network-reachable, decided by the
	// socket rather than by the flag or by classifyBind: --allow-public only
	// permits exposure, and classifyBind requires every resolved address to be
	// loopback while net.Listen binds exactly one of them.
	bound, isLoopbackBind := listenerIsLoopback(listener)
	if !isLoopbackBind && !allowPublic {
		// The socket disagrees with classifyBind: resolution changed underneath
		// us. Stay fail-closed on the opt-in.
		listener.Close()
		return fmt.Errorf("refusing to bind non-loopback address %q: it resolved to %s; pass --allow-public to expose the API to the network", addr, bound)
	}
	publicBind := !isLoopbackBind
	switch {
	case publicBind:
		slog.Warn("BINDING A NON-LOOPBACK ADDRESS — the HomeShell API is reachable from the network; access is gated only by the API token, and the served web UI will not work here: the token cookie is withheld on a public bind, so a browser pointed at this daemon renders and then 401s on every /api call (front it with a proxy that injects the token, or reach it over a loopback port-forward)", "addr", addr, "resolved", bound)
	case allowPublic:
		slog.Warn("ignoring --allow-public: the bind address is loopback, so the API is not network-reachable", "addr", addr, "resolved", bound)
	}

	// Where the token came from, never the token itself: nothing in a 401
	// distinguishes a pinned token from a generated one.
	if tokenFromEnv {
		slog.Info("API token pinned from HOMESHELL_API_TOKEN", "length", len(apiToken))
	} else {
		slog.Info("API token generated for this startup", "length", len(apiToken))
	}
	// On a network-reachable bind the token is the entire perimeter; on a
	// loopback one the document bootstrap hands it to any local process anyway.
	if publicBind && tokenFromEnv && len(apiToken) < minPinnedTokenLength {
		slog.Warn("PINNED API TOKEN IS SHORT — on a network-reachable bind it is the only access control, and this one is guessable; unset HOMESHELL_API_TOKEN for a generated 64-character token or pin a comparable secret",
			"length", len(apiToken), "minimum", minPinnedTokenLength)
	}

	// classifyBind resolves names and accepts IP spellings the /api guard does
	// not, so a bind host the guard would reject still binds and anything
	// addressing the daemon by that name gets 403. Checked against the flag
	// string, which is the name a browser will be pointed at; the state file
	// publishes the resolved address, so the CLI is unaffected.
	if !publicBind && !api.GuardAcceptsHost(addr) {
		slog.Warn("BIND HOST IS NOT A LOOPBACK LITERAL — a browser or client that addresses the daemon by this name gets 403 from the /api guard on every request; use 127.0.0.1 or localhost (the CLI is unaffected, it uses the resolved address)", "addr", addr, "resolved", bound)
	}

	// Publish the real listen address (matters for -addr host:0) so clients can
	// discover the daemon; remove it on clean shutdown.
	state := daemonstate.State{
		Address:   listener.Addr().String(),
		PID:       os.Getpid(),
		Version:   version,
		StartedAt: time.Now(),
		Token:     apiToken,
	}
	if err := daemonstate.Write(state); err != nil {
		listener.Close()
		return fmt.Errorf("writing state file: %w", err)
	}
	defer daemonstate.Remove()

	updateSvc := newUpdateService(env)

	// The store detects git-backing as projects are added. Seed paths are
	// illustrative and are deliberately not re-detected on startup.
	storeOpts := []store.Option{store.WithGitDetector(git.New())}

	registry := provider.NewRegistry()

	var projectStore *store.Store
	var agentCatalog *agentcatalog.Store
	if demoEnabled {
		// Demo mode keeps everything in memory: data is throwaway.
		seed := store.Seed()
		// The fake plays both roles in the demo. The store keeps the workstream
		// records; the sessions move to the fake, which reports them via
		// discovery keyed by working directory, so each has exactly one owner.
		ownedSeed := make(map[string][]domain.Workstream, len(seed))
		for i := range seed {
			var owned []domain.Workstream
			for j := range seed[i].Workstreams {
				w := seed[i].Workstreams[j]
				if w.WorkingDir != "" && len(w.Sessions) > 0 {
					sessions := make([]domain.Session, len(w.Sessions))
					copy(sessions, w.Sessions)
					owned = append(owned, domain.Workstream{WorkingDir: w.WorkingDir, Sessions: sessions})
				}
				// The store holds workstream identity only; the fake owns sessions.
				seed[i].Workstreams[j].Sessions = []domain.Session{}
			}
			if len(owned) > 0 {
				ownedSeed[seed[i].ID] = owned
			}
		}
		projectStore = store.New(seed, &persist.MemorySaver{}, storeOpts...)
		agentCatalog = agentcatalog.NewMemory(agentcatalog.Defaults())
		fakeProvider := fake.New(fake.Config{
			Seed:          ownedSeed,
			Simulate:      true,
			SimProject:    store.DemoProjectID,
			SimWorkingDir: store.DemoWorkstreamDir,
			SimInterval:   demoInterval,
			// A small cap so the demo exercises daemon-side limit enforcement,
			// well above what the simulator itself uses.
			MaxConcurrentSessions: 3,
			// Off unless asked for: a fake create is instant, which leaves the
			// starting-session placeholder and its startup log with no window to be
			// observed in. The e2e suite sets this to open one; the demo leaves it
			// unset so creating a session there stays immediate.
			SessionStartup: fakeSessionStartup(),
		})
		if err := registry.RegisterWorkstreamProvider(fakeProvider); err != nil {
			return fmt.Errorf("registering fake workstream provider: %w", err)
		}
		if err := registry.RegisterSessionProvider(fakeProvider); err != nil {
			return fmt.Errorf("registering fake session provider: %w", err)
		}
	} else {
		// Real session providers (Local, Enclave) self-register a factory from
		// init(); materialize them into the registry. The core workstream
		// provider is registered by provider.NewService.
		if err := provider.RegisterSessionFactories(registry); err != nil {
			return fmt.Errorf("registering providers: %w", err)
		}
		dataDir, err := persist.DefaultDir()
		if err != nil {
			return fmt.Errorf("locating data directory: %w", err)
		}
		warnAboutLeftoverDatabase(dataDir)
		// A corrupt state or catalog file aborts startup rather than silently
		// replacing what the user had with nothing.
		statePath := filepath.Join(dataDir, persist.StateFileName)
		snapshot, err := persist.LoadState(statePath)
		if err != nil {
			return fmt.Errorf("loading persisted state: %w", err)
		}
		projectStore = store.Restore(snapshot, persist.NewFileSaver(statePath), storeOpts...)
		agentCatalog, err = agentcatalog.Load(filepath.Join(dataDir, agentcatalog.FileName))
		if err != nil {
			return fmt.Errorf("loading the agent catalog: %w", err)
		}
		state.StatePath = statePath
		slog.Info("loaded persisted state", "dir", dataDir, "projects", len(snapshot.Projects))
	}

	// Re-write the discovery file now that we know the state path.
	if state.StatePath != "" {
		if err := daemonstate.Write(state); err != nil {
			slog.Warn("updating state file with state path", "error", err)
		}
	}

	registry.InjectStore(projectStore)
	// Startup reconciliation: let providers drop resources for projects that no
	// longer exist, since project removal does not cascade to providers.
	keepProjects := make([]string, 0, len(projectStore.Projects()))
	for _, project := range projectStore.Projects() {
		keepProjects = append(keepProjects, project.ID)
	}
	for _, p := range registry.SessionProviders() {
		if pruner, ok := p.(provider.Pruner); ok {
			if err := pruner.Prune(ctx, keepProjects); err != nil {
				slog.Warn("pruning provider state failed", "provider", p.ID(), "error", err)
			}
		}
	}

	// The agent catalog is two things at once: the resource /api/agents,
	// /api/agent-settings and /api/agent-models edit, and the agents every
	// provider can launch, with the settings each new session offers, how each
	// chosen value reaches the command line, and the models the Model field
	// lists. Injected into the providers here (they are built from init(), before
	// there is a catalog) and before anything can serve a session schema, because
	// without it a provider has nothing to offer at all.
	registry.InjectModelCatalog(agentCatalog)
	registry.InjectAgentSpecs(agentCatalog)

	// Client settings persist beside the state file (settings.json); a corrupt
	// file aborts startup rather than silently discarding the user's settings.
	settingsPath, err := settings.DefaultPath()
	if err != nil {
		return fmt.Errorf("locating settings file: %w", err)
	}
	settingsStore, err := settings.Load(settingsPath)
	if err != nil {
		return fmt.Errorf("loading settings: %w", err)
	}
	providers := provider.NewService(projectStore, registry, 0)
	// Let settings-aware providers read user settings live. After NewService,
	// which registers the core provider, and before providers.Run starts the
	// watchers below.
	registry.InjectSettings(settingsStore)
	// On every way out, after the server is shut down.
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		providers.Shutdown(shutdownCtx)
	}()
	// Wired before the first projection, so the first snapshot a client receives
	// already carries the user's "open with" entries in each Open menu.
	providers.SetSettings(settingsStore)
	providers.Reconcile(ctx)
	// Log-level precedence: an explicit flag/env wins; otherwise a persisted
	// level from settings.json overrides the built-in default. The live level is
	// authoritative from here on (reported/mutated via /api/config).
	if !logLevelExplicit {
		if stored := settingsStore.LogLevel(); stored != "" {
			if parsed, perr := logging.ParseLevel(stored); perr == nil {
				logLevel.Set(parsed)
			} else {
				slog.Warn("ignoring invalid persisted log level", "level", stored, "error", perr)
			}
		}
	}

	// Activity notifications. The monitor detects the trigger once, in the
	// daemon; the Notifications settings choose the style, the sound, which of
	// the two events notify, which of them are audible, and the per-session quiet
	// period — all read live. The hub is always wired into the API so the SSE
	// stream can relay in-app notifications; the DesktopNotifier delivers the
	// OS-native ones.
	//
	// The tester shares both channels and the same live configuration, so the
	// sample notification the settings page asks for travels the path a real one
	// will — including the OS-native command, which no client could stand in for.
	notifyHub := notify.NewHub()
	notifyNative := notify.NewDesktopNotifier()
	notifyConfig := func() notify.Config {
		return notify.ConfigFrom(settingsStore.Get().Notifications)
	}
	notifyMonitor := notify.NewMonitor(projectStore, notifyHub, notifyNative, notifyConfig)
	notifyTester := notify.NewTester(notifyHub, notifyNative, notifyConfig)
	go notifyMonitor.Run(ctx)

	server := newAPIServer(ctx, api.Handler(api.Deps{
		Version:       version,
		LogLevel:      logLevel,
		Logs:          logHub,
		Settings:      settingsStore,
		AgentCatalog:  agentCatalog,
		Store:         projectStore,
		Heartbeat:     api.DefaultHeartbeat,
		Updater:       updateSvc,
		Providers:     providers,
		FileDialog:    hostFileDialog(),
		Opener:        opener.NewOSOpener(),
		Notifications: notifyHub,
		NotifyTester:  notifyTester,
		ListenAddr:    listener.Addr().String(),
		APIToken:      apiToken,
		PublicBind:    publicBind,
	}))

	go providers.Run(ctx)

	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(listener) }()

	slog.Info("HomeShell daemon listening", "url", "http://"+listener.Addr().String(), "version", version, "demo", demoEnabled)
	// Echo the same report `status` would give, so a fresh `serve` shows where
	// it published itself without a second command.
	writeRunning(env.Stdout, state)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

// generateToken returns a fresh 256-bit random token, hex-encoded, for the
// per-startup API secret when HOMESHELL_API_TOKEN is not set.
func generateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// readHeaderTimeout bounds how long a client may take to send its request
// headers, so one that dribbles them cannot pin a goroutine indefinitely. It is
// the only timeout that can be set here: ReadTimeout, WriteTimeout and
// IdleTimeout bound the whole exchange and would cut the SSE streams off.
const readHeaderTimeout = 20 * time.Second

// newAPIServer builds the daemon's HTTP server. It is a function so the timeout
// wiring is reachable from a test.
func newAPIServer(ctx context.Context, handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		BaseContext:       func(_ net.Listener) context.Context { return ctx },
	}
}

// minPinnedTokenLength is the length below which a pinned HOMESHELL_API_TOKEN is
// warned about on a network-reachable bind. It is half of what generateToken
// produces, so it flags an obviously human-typed value.
const minPinnedTokenLength = 32

// listenTCP is net.Listen, indirected so a test can supply a listener that
// reports a network-reachable address without opening one.
var listenTCP = net.Listen

// listenerIsLoopback reports the address a listener actually bound and whether
// it is a loopback one; unlike classifyBind this involves no resolution. An
// unspecified address is not loopback, and anything that is not a *net.TCPAddr
// is treated as public, so an unexpected shape fails closed.
func listenerIsLoopback(listener net.Listener) (addr string, isLoopback bool) {
	bound := listener.Addr()
	tcpAddr, ok := bound.(*net.TCPAddr)
	return bound.String(), ok && tcpAddr.IP.IsLoopback()
}

// classifyBind reports whether addr binds only the loopback interface: an
// unspecified address is public, a literal IP is classified directly, and a
// hostname is accepted as loopback only when every resolved address is. A host
// that cannot be resolved returns an error rather than being mislabeled public.
//
// This answers "may we open this?" only. For the daemon's actual reachability
// use listenerIsLoopback: net.Listen resolves independently and binds one
// address.
func classifyBind(addr string) (loopback bool, err error) {
	host, _, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		host = addr // no port present; treat the whole value as the host
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return false, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback(), nil
	}
	resolved, err := net.LookupIP(host)
	if err != nil {
		return false, fmt.Errorf("cannot resolve bind host %q: %w", host, err)
	}
	if len(resolved) == 0 {
		return false, fmt.Errorf("cannot resolve bind host %q: no addresses", host)
	}
	for _, ip := range resolved {
		if !ip.IsLoopback() {
			return false, nil
		}
	}
	return true, nil
}

// hostFileDialog is the native folder chooser, unless the host has asked for it
// to be off.
//
// HOMESHELL_NO_NATIVE_DIALOG exists for automated runs. The chooser is a real
// window on the daemon's screen and the request stays open until somebody
// answers it, so one stray call in a headless or CI run blocks a suite on a
// dialog nobody can see. Off, the endpoint reports 501 and every client falls
// back to the in-app picker, which is what a host without a dialog tool already
// gets — so this closes the hole without inventing a new code path.
func hostFileDialog() filedialog.Chooser {
	if truthyEnv(os.Getenv("HOMESHELL_NO_NATIVE_DIALOG")) {
		slog.Info("native folder chooser disabled by HOMESHELL_NO_NATIVE_DIALOG")
		return nil
	}
	return filedialog.NewOSChooser()
}

// truthyEnv reads the usual spellings of "on" for an environment flag.
func truthyEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// warnAboutLeftoverDatabase names a pre-release homeshell.db the daemon no
// longer reads. There is no importer: HomeShell was unreleased when the
// database was replaced by state.json and agents.json, so the file is left for
// the developer to delete by hand.
func warnAboutLeftoverDatabase(dataDir string) {
	path := filepath.Join(dataDir, "homeshell.db")
	if _, err := os.Stat(path); err == nil {
		slog.Warn("ignoring a pre-release database; it is no longer read and can be deleted", "path", path)
	}
}
