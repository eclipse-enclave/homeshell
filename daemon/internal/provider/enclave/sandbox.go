package enclave

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/provider/enclave/sandboxschema"
)

// A session's sandbox: the container settings Enclave itself owns. Enclave stays
// the authority: the form's defaults are what `enclave config` and `enclave
// features list` resolve for the session's directory and tool (sandboxschema), and
// what the user changes reaches `enclave run` as the flags Enclave documents for
// it. Nothing is written back to Enclave's config.

// sandboxBaseTTL is how long one of Enclave's answers is reused. The create form
// asks for every tool's schema each time it opens, and the create that follows
// needs the same answer again; Enclave's config changes far more slowly.
const sandboxBaseTTL = 30 * time.Second

// failedAnswerTTL is how long a failure is reused, so a broken Enclave is not
// asked again by every request; short, so a passing failure is soon gone.
const failedAnswerTTL = 5 * time.Second

// sandboxBases caches what Enclave resolves: the config per directory and tool,
// the feature catalogue per directory.
type sandboxBases struct {
	configs  answerCache[ResolvedConfig]
	features answerCache[[]sandboxschema.Feature]
}

// answerCache remembers Enclave's answers, successes for sandboxBaseTTL and
// failures for failedAnswerTTL.
type answerCache[T any] struct {
	mu      sync.Mutex
	entries map[string]cachedAnswer[T]
}

type cachedAnswer[T any] struct {
	at    time.Time
	value T
	err   error
}

// get returns the answer for key, from the cache when it is fresh and from load
// otherwise. The load does not run under ctx: a caller going away, such as an
// abandoned form request, must not have its cancellation remembered as the answer.
// The CLI's own timeout bounds it.
func (cache *answerCache[T]) get(ctx context.Context, key string, load func(context.Context) (T, error)) (T, error) {
	cache.mu.Lock()
	cached, ok := cache.entries[key]
	cache.mu.Unlock()
	ttl := sandboxBaseTTL
	if cached.err != nil {
		ttl = failedAnswerTTL
	}
	if ok && time.Since(cached.at) < ttl {
		return cached.value, cached.err
	}
	value, err := load(context.WithoutCancel(ctx))
	cache.mu.Lock()
	if cache.entries == nil {
		cache.entries = map[string]cachedAnswer[T]{}
	}
	cache.entries[key] = cachedAnswer[T]{at: time.Now(), value: value, err: err}
	cache.mu.Unlock()
	return value, err
}

// forget drops the answer for key, so the next get asks again.
func (cache *answerCache[T]) forget(key string) {
	cache.mu.Lock()
	delete(cache.entries, key)
	cache.mu.Unlock()
}

// forgetBase drops what Enclave answered for tool in dir, so sandboxBase asks
// again. Starting a session needs that: its list flags replace the configured
// lists, so flags built from an answer older than an edit of Enclave's config
// would undo the edit.
func (p *Provider) forgetBase(dir, tool string) {
	p.bases.configs.forget(configKey(dir, tool))
	p.bases.features.forget(dir)
}

// configKey keys enclaveConfig's remembered answer for tool in dir.
func configKey(dir, tool string) string {
	return dir + "\x00" + tool
}

// enclaveConfig is what Enclave resolves for tool in dir, remembered.
func (p *Provider) enclaveConfig(ctx context.Context, dir, tool string) (ResolvedConfig, error) {
	return p.bases.configs.get(ctx, configKey(dir, tool), func(ctx context.Context) (ResolvedConfig, error) {
		return p.cli.Config(ctx, dir, tool, nil)
	})
}

// enclaveFeatures is the feature catalogue Enclave lists in dir, remembered.
func (p *Provider) enclaveFeatures(ctx context.Context, dir string) ([]sandboxschema.Feature, error) {
	return p.bases.features.get(ctx, dir, func(ctx context.Context) ([]sandboxschema.Feature, error) {
		return p.cli.Features(ctx, dir)
	})
}

// sandboxBase resolves the base for tool in dir. Both of Enclave's answers are
// needed: without the config the lists cannot be extended, and without the
// features the checklist has nothing to list.
func (p *Provider) sandboxBase(ctx context.Context, dir, tool string) (sandboxschema.Base, error) {
	features, err := p.enclaveFeatures(ctx, dir)
	if err != nil {
		return sandboxschema.Base{}, err
	}
	config, err := p.enclaveConfig(ctx, dir, tool)
	if err != nil {
		return sandboxschema.Base{}, err
	}
	return sandboxschema.Base{Config: config.Options, Features: features}, nil
}

// sandboxChoice is the sandbox group as a create form submits it. A field left
// out means "Enclave's default".
type sandboxChoice struct {
	Features     *[]string           `json:"features,omitempty"`
	Dirs         []sandboxschema.Dir `json:"dirs,omitempty"`
	Ports        []string            `json:"ports,omitempty"`
	BridgePorts  []int               `json:"bridgePorts,omitempty"`
	AllowDomains []string            `json:"allowDomains,omitempty"`
	HostConfig   *bool               `json:"hostConfig,omitempty"`
}

// unavailableSandboxSchema stands in for the sandbox group when Enclave could not
// report its settings: no fields, and a title saying why, which is what a
// collapsed group shows. A config carrying the group is not refused as unknown
// settings; sandboxFlagsFor refuses one that sets anything, saying why.
var unavailableSandboxSchema = json.RawMessage(`{"type":"object","title":"Container (Enclave did not report its settings)","description":"Enclave did not report its container settings, so this session starts with Enclave's own.","properties":{}}`)

// isEmpty reports whether the group sets nothing, which leaves every setting to
// Enclave.
func (choice sandboxChoice) isEmpty() bool {
	return choice.Features == nil && len(choice.Dirs) == 0 && len(choice.Ports) == 0 &&
		len(choice.BridgePorts) == 0 && len(choice.AllowDomains) == 0 && choice.HostConfig == nil
}

// sandboxOf reads the sandbox group out of a session config.
func sandboxOf(config json.RawMessage) (sandboxChoice, bool) {
	if len(bytes.TrimSpace(config)) == 0 {
		return sandboxChoice{}, false
	}
	var groups map[string]json.RawMessage
	if err := json.Unmarshal(config, &groups); err != nil {
		return sandboxChoice{}, false
	}
	choice, ok := decode[*sandboxChoice](groups, sandboxschema.Key)
	if !ok || choice == nil {
		return sandboxChoice{}, false
	}
	return *choice, true
}

// decode reads the named field of a JSON object as a T; false when it is missing or holds no T.
func decode[T any](fields map[string]json.RawMessage, name string) (T, bool) {
	var value T
	if err := json.Unmarshal(fields[name], &value); err != nil {
		var zero T
		return zero, false
	}
	return value, true
}

// sandboxFlags are the `enclave run` flags for what the user changed from base.
// A setting left at Enclave's value produces no flag, so a session nobody
// customized starts with exactly the command it always did.
func sandboxFlags(choice sandboxChoice, base sandboxschema.Base) []string {
	config := base.Config
	var flags []string

	if choice.Features != nil && !sameSet(*choice.Features, config.List("features")) {
		value := strings.Join(*choice.Features, ",")
		if len(*choice.Features) == 0 {
			value = "none"
		}
		flags = append(flags, "--features", value)
	}

	var writable, readonly []string
	for _, dir := range choice.Dirs {
		if path := strings.TrimSpace(dir.Path); path != "" {
			if dir.Writable() {
				writable = append(writable, path)
			} else {
				readonly = append(readonly, path)
			}
		}
	}
	flags = append(flags, extendList("--add-dir", config.List("add_dirs"), writable)...)
	flags = append(flags, extendList("--add-readonly-dir", config.List("add_readonly_dirs"), readonly)...)
	flags = append(flags, extendList("-p", config.List("ports"), choice.Ports)...)
	bridge := make([]string, len(choice.BridgePorts))
	for i, port := range choice.BridgePorts {
		bridge[i] = strconv.Itoa(port)
	}
	flags = append(flags, extendList("--bridge-port", config.List("bridge_ports"), bridge)...)
	flags = append(flags, extendList("--allow-domain", config.List("allow_domains"), choice.AllowDomains)...)

	if choice.HostConfig != nil && *choice.HostConfig != (config.String("host_config") == "passthrough") {
		value := "none"
		if *choice.HostConfig {
			value = "passthrough"
		}
		flags = append(flags, "--host-config", value)
	}
	return flags
}

// extendList is the flags that add extras to a list option Enclave's config
// already sets. Enclave's list flags replace the configured list (enclave#99), so
// adding means passing the whole list again: what is configured, then the extras,
// each once. No extras, no flags — the configured list applies as it is.
func extendList(flag string, configured, extras []string) []string {
	var added []string
	for _, extra := range extras {
		extra = strings.TrimSpace(extra)
		if extra != "" && !slices.Contains(configured, extra) && !slices.Contains(added, extra) {
			added = append(added, extra)
		}
	}
	if len(added) == 0 {
		return nil
	}
	var flags []string
	for _, value := range append(slices.Clone(configured), added...) {
		flags = append(flags, flag, value)
	}
	return flags
}

// sameSet reports whether two lists hold the same entries, in any order.
func sameSet(first, second []string) bool {
	first, second = slices.Clone(first), slices.Clone(second)
	slices.Sort(first)
	slices.Sort(second)
	return slices.Equal(slices.Compact(first), slices.Compact(second))
}

// sandboxChoiceFromOptions is the sandbox group a session's recorded options
// amount to, as far as the create form models them, relative to base: the
// settings Enclave resolves for it now. A setting equal to base's is left unset,
// and list entries base already lists are not repeated, since lists only extend
// (extendList). Options the form does not model are ignored.
func sandboxChoiceFromOptions(running map[string]json.RawMessage, base sandboxschema.EffectiveConfig) sandboxChoice {
	listed := func(name string) []string {
		items, _ := decode[[]string](running, name)
		return slices.DeleteFunc(items, func(item string) bool { return slices.Contains(base.List(name), item) })
	}
	var choice sandboxChoice
	if features, ok := decode[[]string](running, "features"); ok && !sameSet(features, base.List("features")) {
		choice.Features = &features
	}
	for _, dirOption := range []struct {
		name     string
		readonly bool
	}{{"add_dirs", false}, {"add_readonly_dirs", true}} {
		for _, path := range listed(dirOption.name) {
			choice.Dirs = append(choice.Dirs, formDir(path, dirOption.readonly))
		}
	}
	choice.Ports = listed("ports")
	for _, port := range listed("bridge_ports") {
		if number, err := strconv.Atoi(port); err == nil {
			choice.BridgePorts = append(choice.BridgePorts, number)
		}
	}
	choice.AllowDomains = listed("allow_domains")
	if hostConfig, ok := decode[string](running, "host_config"); ok {
		if passthrough := hostConfig == "passthrough"; passthrough != (base.String("host_config") == "passthrough") {
			choice.HostConfig = &passthrough
		}
	}
	return choice
}

// formDir is a directory as the create form submits it: Readonly left out for a read-only one, which is the
// default, and false for a writable one.
func formDir(path string, readonly bool) sandboxschema.Dir {
	if readonly {
		return sandboxschema.Dir{Path: path}
	}
	return sandboxschema.Dir{Path: path, Readonly: new(bool)}
}

// config is the choice as a session config: only the fields it sets, in the
// sandbox group; `{}` for one that sets nothing.
func (choice sandboxChoice) config() json.RawMessage {
	if choice.isEmpty() {
		return json.RawMessage(`{}`)
	}
	config, _ := json.Marshal(map[string]any{sandboxschema.Key: choice})
	return config
}

// ObservedLaunchConfig is the launch config of a session started with the CLI,
// from the options Enclave recorded for its container (provider.LaunchObserver):
// a restart or Continue replays and records it, so from then on the session
// drifts and is edited like one started here. False for a session with no
// recorded options (one started here, or an older one), or when Enclave cannot
// say what it resolves now.
func (p *Provider) ObservedLaunchConfig(ctx context.Context, ref provider.SessionRef, optionID string) (json.RawMessage, bool) {
	record, owned := p.bridgeRecordFor(ref.SessionID)
	if !owned || record.Container.Options == nil {
		return nil, false
	}
	base, err := p.sandboxBase(ctx, record.Container.Directory, restartTool(optionID))
	if err != nil {
		slog.Debug("cannot tell what a session was started with: Enclave did not report its settings",
			"session", ref.SessionID, "error", err)
		return nil, false
	}
	return sandboxChoiceFromOptions(record.Container.Options, base.Config).config(), true
}
