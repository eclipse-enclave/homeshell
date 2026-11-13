package enclave

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider/enclave/sandboxschema"
)

// Drift: whether a session still runs with what it would start with now.
//
// Enclave records the options a container was started with (`options` in
// `enclave ps --json`). A restart replays the session's recorded launch config
// over Enclave's current config, so the session is out of date where `enclave
// config --json`, given the flags that replay would pass, resolves an option to
// another value than the container runs with. Only sessions HomeShell started
// are compared: the flags another was started with are unknown, and each option
// they set would read as changed.
//
// Nothing tells HomeShell that Enclave's config changed, so the watch loop's
// inventory check asks (outdatedFrom) and discovery reports what it last found.

// driftCheckInterval is how often the watch loop asks. Each time costs Enclave
// calls per session, so it is slower than the state poll.
const driftCheckInterval = 30 * time.Second

// outdatedFrom works out again, from an inventory, which settings a restart of
// each running container would change, and reports whether that differs from
// what it found last time.
func (p *Provider) outdatedFrom(ctx context.Context, found inventory) bool {
	if p.store == nil {
		return false
	}
	bridges := p.bridges
	outdated := map[string][]string{}
	listed, checked, untold, asked := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	compared := map[string]domain.LaunchConfig{}
	listFailed := false
	// Listed per project, as a project's config may pick its own engine; a
	// container two projects list is checked once.
	for _, listing := range found.listings {
		if listing.err != nil {
			listFailed = true
		}
		for _, container := range listing.containers {
			listed[container.Name] = true
			if checked[container.Name] || container.Options == nil {
				continue
			}
			checked[container.Name] = true
			launch, ok := p.store.LaunchConfig(container.Name)
			if !ok || launch.ProviderID != p.ID() {
				continue
			}
			compared[container.Name] = launch
			// The config is asked afresh once per check, as a restart would, not once
			// per session; the feature catalogue, which only names the choices, is not.
			tool := restartTool(launch.OptionID)
			if key := configKey(container.Directory, tool); !asked[key] {
				asked[key] = true
				p.bases.configs.forget(key)
			}
			settings, told := p.outdatedSettings(ctx, container, tool, launch.Config)
			if !told {
				untold[container.Name] = true
			} else if len(settings) > 0 {
				outdated[container.Name] = settings
			}
		}
	}
	// A restart records its settings before it marks the session restarting, and
	// an abandoned one restores them after; one whose settings changed meanwhile
	// was compared against settings it may not run with.
	for name, launch := range compared {
		if now, ok := p.store.LaunchConfig(name); !ok || !sameLaunch(now, launch) {
			untold[name] = true
			delete(outdated, name)
		}
	}
	bridges.mu.Lock()
	defer bridges.mu.Unlock()
	// What could not be told this time, whose project could not list, that is
	// being restarted against settings it does not run with yet, or whose record
	// changed since the listing (a restart, even an abandoned one), keeps the last
	// result.
	for name := range bridges.restarting {
		untold[name] = true
		delete(outdated, name)
	}
	for name := range compared {
		if bridges.changedSince(found, name) {
			untold[name] = true
			delete(outdated, name)
		}
	}
	for name, settings := range bridges.outdated {
		if untold[name] || (listFailed && !listed[name]) {
			outdated[name] = settings
		}
	}
	changed := !maps.EqualFunc(bridges.outdated, outdated, slices.Equal[[]string])
	bridges.outdated = outdated
	return changed
}

// outdatedSettings names the options a restart of container as tool with its
// recorded config would change, sorted, and reports whether that could be told.
func (p *Provider) outdatedSettings(ctx context.Context, container Container, tool string, config json.RawMessage) ([]string, bool) {
	opts, err := p.launchOpts(ctx, container.Directory, tool, config)
	if err != nil {
		slog.Debug("cannot tell whether a session is out of date: its settings no longer apply", "session", container.Name, "error", err)
		return nil, false
	}
	current, err := p.cli.Config(ctx, container.Directory, tool, opts.SessionFlags())
	if err != nil || !current.Valid {
		slog.Debug("cannot tell whether a session is out of date: Enclave did not resolve its config",
			"session", container.Name, "error", err, "reason", current.Error)
		return nil, false
	}
	return changedOptions(container.Options, current.Options), true
}

// changedOptions names the options whose running value differs from current,
// sorted. An option either side does not report is unknown rather than changed,
// and so is one Enclave reports as null: it could not resolve it, as `features`
// when the features cannot be listed.
func changedOptions(running map[string]json.RawMessage, current sandboxschema.EffectiveConfig) []string {
	var changed []string
	for name, value := range running {
		option, ok := current[name]
		if !ok || isNull(value) || isNull(option.Value) {
			continue
		}
		if !sameJSON(value, option.Value) {
			changed = append(changed, name)
		}
	}
	slices.Sort(changed)
	return changed
}

func sameLaunch(first, second domain.LaunchConfig) bool {
	return first.ProviderID == second.ProviderID && first.OptionID == second.OptionID && bytes.Equal(first.Config, second.Config)
}

func isNull(value json.RawMessage) bool {
	return len(value) == 0 || bytes.Equal(value, []byte("null"))
}

// sameJSON reports whether two valid JSON values are equal, whatever their
// spacing.
func sameJSON(first, second json.RawMessage) bool {
	var compactFirst, compactSecond bytes.Buffer
	_ = json.Compact(&compactFirst, first)
	_ = json.Compact(&compactSecond, second)
	return bytes.Equal(compactFirst.Bytes(), compactSecond.Bytes())
}

// withOutdated overlays the latest drift result on a cached bridge observation.
// The caller holds b.mu.
func (b *hostBridges) withOutdated(session domain.Session) domain.Session {
	session.OutdatedSettings = b.outdated[session.ID]
	return session
}
