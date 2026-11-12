package enclave

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
)

// PrepareRestart readies a session to be started again with new settings. Enclave fixes a container's settings
// when it creates it and has no command that changes them, so a restart is `enclave stop`, which also removes the
// container, then `enclave continue` in the background (`enclave run` for a fresh one, or for a tool that cannot
// continue: cannotContinue). Which name the session goes on under (restartNames) and what it continues are in
// docs/PROVIDERS.md (Eclipse Enclave).
//
// Everything that can refuse the restart — a container that is not Enclave's (CheckRestart), settings that cannot
// become flags — is checked here, and nothing is changed: the session is only shown as starting once the returned
// func runs.
func (p *Provider) PrepareRestart(ctx context.Context, req provider.RestartSessionRequest) (provider.RestartFunc, error) {
	container := req.Session.SessionID
	record, owned := p.bridgeRecordFor(container)
	if !owned {
		return nil, fmt.Errorf("%w: unknown Enclave session", provider.ErrSessionNotRestartable)
	}
	tool := restartTool(req.OptionID)
	target, sessionName, err := restartNames(container, tool)
	if err != nil {
		return nil, err
	}
	p.forgetBase(req.WorkingDir, tool)
	opts, err := p.launchOpts(ctx, req.WorkingDir, tool, req.Config)
	if err != nil {
		return nil, err
	}
	opts.SessionName = sessionName
	// A profile with resume_args but no continue_args gets Enclave's session
	// picker instead, which a background container waits at with nobody to
	// answer it. Telling the two apart takes the profile, which `enclave tools`
	// does not report.
	opts.Continue = !req.Fresh

	restarting := restartProjection{
		workingDir: req.WorkingDir,
		projectDir: record.Container.ProjectDir,
		target:     target,
		session: domain.Session{
			ID:    container,
			Title: fmt.Sprintf("%s session", tool),
			Agent: tool,
			State: domain.SessionStarting,
		},
	}
	return func(ctx context.Context) error { return p.restart(ctx, restarting, opts) }, nil
}

// CheckRestart refuses a session PrepareRestart would refuse whatever its
// settings: one this provider has no record of, or a container not named as
// Enclave names the tool's.
func (p *Provider) CheckRestart(ref provider.SessionRef, optionID string) error {
	if _, owned := p.bridgeRecordFor(ref.SessionID); !owned {
		return fmt.Errorf("%w: unknown Enclave session", provider.ErrSessionNotRestartable)
	}
	_, _, err := restartNames(ref.SessionID, restartTool(optionID))
	return err
}

// ContinueRefusal refuses to continue a session's conversation while another
// session of the same tool works in its directory: `enclave continue` picks up
// the tool's latest conversation there, which may be that session's. A stopped
// neighbour counts too, since its conversation may still be the latest.
func (p *Provider) ContinueRefusal(optionID, workingDir string, neighbourOptionIDs []string) error {
	tool := restartTool(optionID)
	for _, neighbourOptionID := range neighbourOptionIDs {
		if restartTool(neighbourOptionID) == tool {
			return fmt.Errorf("%w: another %s session works in %s, and continuing picks up the latest %s conversation there, which may be that session's; restart it fresh",
				provider.ErrCannotContinue, tool, workingDir, tool)
		}
	}
	return nil
}

func restartTool(optionID string) string {
	if optionID == "" {
		return defaultTool
	}
	return optionID
}

// restartNames is the container a restart of container brings back and the --name it passes: the same ones for
// a named session; for the default container or an auto-assigned number (`1`, `2`, …) a new hs- name. Why: an
// unnamed start in a terminal takes the default container, or numbers its own when that is taken; continued
// under either name, the session would hold it or pass for one started there. A user's `--name 3` reads as
// a number too.
func restartNames(container, tool string) (target, sessionName string, err error) {
	base, ok := containerBase(container, tool)
	if !ok {
		return "", "", fmt.Errorf("%w: %q is not the name of an Enclave %s container", provider.ErrSessionNotRestartable,
			container, tool)
	}
	sessionName = strings.TrimPrefix(strings.TrimPrefix(container, base), "-")
	if unnamed := sessionName == "" || isDigits(sessionName); !unnamed {
		return container, sessionName, nil
	}
	sessionName = newSessionName()
	return base + "-" + sessionName, sessionName, nil
}

// containerBase is the name of the tool's default container in container's project, `enclave-<tool>-<12 hex>`,
// which every container of the tool there starts with; false for a name that does not.
func containerBase(container, tool string) (string, bool) {
	const hashLength = 12
	prefix := binaryName + "-" + tool + "-"
	if !strings.HasPrefix(container, prefix) || len(container) < len(prefix)+hashLength {
		return "", false
	}
	base := container[:len(prefix)+hashLength]
	if !isLowerHex(base[len(prefix):]) {
		return "", false
	}
	switch sessionPart := container[len(base):]; {
	case sessionPart == "":
		return base, true // the default container
	case len(sessionPart) > 1 && sessionPart[0] == '-':
		return base, true // a session name after the hash
	default:
		return "", false
	}
}

// isDigits reports whether text holds decimal digits only.
func isDigits(text string) bool {
	return !strings.ContainsFunc(text, func(char rune) bool { return char < '0' || char > '9' })
}

// isLowerHex reports whether text holds lower-case hexadecimal digits only.
func isLowerHex(text string) bool {
	return !strings.ContainsFunc(text, func(char rune) bool {
		return (char < '0' || char > '9') && (char < 'a' || char > 'f')
	})
}

// restart replaces the session's container with one started with opts, reporting the session as starting meanwhile:
// it takes the old container down (takeDown), brings the new one up (bringUp) and finishes (finishRestart). A
// failure to stop the old container leaves the session as it was — the container most likely still runs it — and
// is returned, for the service to report; a later failure is reported on the session, as a failed start is.
//
// A restart is withdrawn by removing the session — RemoveSession takes it out of restarting — and cancelled by its
// context; either brings nothing back. One cancelled before it began returns the context's error; one cancelled
// later, and not withdrawn, returns provider.ErrRestartInterrupted, for the service to remove the session.
func (p *Provider) restart(ctx context.Context, restarting restartProjection, opts RunOpts) error {
	previous, down, err := p.takeDown(ctx, restarting)
	if !down {
		return err
	}
	if err := p.bringUp(ctx, restarting, previous, opts); err != nil && ctx.Err() == nil {
		return p.reportRestartFailure(ctx, restarting, err)
	}
	return p.finishRestart(ctx, restarting)
}

// takeDown takes the session into restarting and stops its container and bridge, returning its record as it was
// and whether the restart goes on; when it does not, what the restart returns.
func (p *Provider) takeDown(ctx context.Context, restarting restartProjection) (bridgeRecord, bool, error) {
	container := restarting.session.ID
	bridges := p.bridges
	var previous bridgeRecord
	var stopErr, bridgeErr error
	err := bridges.withLifecycle(container, func(*bridgeGuard) error {
		var restore func()
		var err error
		if previous, restore, err = p.beginRestart(ctx, restarting); err != nil {
			return err
		}
		p.notify()
		// Always stopped: an ended state may have been inferred from a listing
		// that missed the container, and continue reuses its name.
		if err := p.cli.Stop(ctx, container); err != nil && previous.State == "" {
			restore()
			stopErr = err
			return nil
		}
		bridgeErr = bridges.stopBridge(ctx, previous)
		return nil
	})
	switch {
	case err != nil:
		return previous, false, err
	case stopErr != nil:
		return previous, false, p.abandonRestart(ctx, container, fmt.Errorf("stop enclave container: %w", stopErr))
	case bridgeErr != nil:
		bridgeErr = fmt.Errorf("stop old enclave bridge: %w", bridgeErr)
		return previous, false, p.reportRestartFailure(ctx, restarting, bridgeErr)
	}
	bridges.mu.Lock()
	delete(bridges.outdated, container)
	bridges.mu.Unlock()
	return previous, true, nil
}

// bringUp starts the session's container again and attaches it, unless the restart was withdrawn or cancelled
// while the container came up. previous is the session's record before the restart.
func (p *Provider) bringUp(ctx context.Context, restarting restartProjection, previous bridgeRecord, opts RunOpts) error {
	container, target := restarting.session.ID, restarting.target
	if err := p.runRestarted(ctx, restarting, opts); err != nil {
		return fmt.Errorf("start enclave container: %w", err)
	}
	bridges := p.bridges
	bridges.mu.Lock()
	_, stillRestarting := bridges.restarting[container]
	bridges.mu.Unlock()
	if !stillRestarting || ctx.Err() != nil {
		return nil
	}
	record := bridgeRecord{Container: Container{Name: target, Tool: opts.Tool, Directory: restarting.workingDir,
		ProjectDir: previous.Container.ProjectDir, Status: "running", Background: true}}
	if restarting.renames() {
		// Hidden until the restart ends (replace), or completed on load after a crash (completeReplacements).
		record.Replaces, record.ReplacingFrom = container, container
	}
	if err := p.registerBridge(ctx, record); err != nil {
		if restarting.renames() {
			// A record kept for a failed cleanup would be a second failed row beside the session's.
			p.forgetRestarted(ctx, target)
		}
		return err
	}
	return nil
}

// runRestarted runs the restarted container, showing its output on the session as it comes, and starts it fresh
// when the tool has no way to continue.
func (p *Provider) runRestarted(ctx context.Context, restarting restartProjection, opts RunOpts) error {
	container, target := restarting.session.ID, restarting.target
	bridges := p.bridges
	progress := provider.NewStartupSink(func(tail string) {
		bridges.mu.Lock()
		if current, ok := bridges.restarting[container]; ok {
			current.session.Preview = tail
			bridges.restarting[container] = current
		}
		bridges.mu.Unlock()
		p.notify()
	})
	opts.Progress = progress.Line
	started, err := p.cli.Run(ctx, restarting.workingDir, opts)
	if opts.Continue && cannotContinue(err) {
		slog.Info("the tool has no way to continue a conversation; starting the restarted session fresh",
			"container", container, "tool", opts.Tool)
		opts.Continue = false
		started, err = p.cli.Run(ctx, restarting.workingDir, opts)
	}
	progress.Close()
	if err != nil || started == target {
		return err
	}
	// Enclave names the container after the session name it was given. One under another name is not this
	// session: it is stopped rather than left running for nothing to own.
	if stopErr := p.cli.Stop(context.WithoutCancel(ctx), started); stopErr != nil {
		slog.Warn("stopping a restarted container that came back under another name failed",
			"container", started, "error", stopErr)
	}
	return fmt.Errorf("enclave started the session as %q instead of %q", started, target)
}

// finishRestart ends a restart whose container is back, or that was withdrawn or cancelled while it came back,
// telling which under one lock.
func (p *Provider) finishRestart(ctx context.Context, restarting restartProjection) error {
	container, target := restarting.session.ID, restarting.target
	bridges := p.bridges
	bridges.mu.Lock()
	_, stillRestarting := bridges.restarting[container]
	cancelled := ctx.Err() != nil
	if stillRestarting && !cancelled {
		delete(bridges.restarting, container)
		if restarting.renames() {
			bridges.replace(container, target)
		}
	}
	bridges.mu.Unlock()
	switch {
	case stillRestarting && cancelled:
		// Left restarting, for RemoveSession. Why a new name's container goes now: after a shutdown nothing
		// removes the session, and the next daemon would take an unrecorded hs- container for another daemon's.
		if restarting.renames() {
			p.forgetRestarted(ctx, target)
		}
		return provider.ErrRestartInterrupted
	case !stillRestarting:
		// Withdrawn after the removal stopped the old container: what this restart started belongs to nothing.
		p.forgetRestarted(ctx, target)
	}
	p.notify()
	return nil
}

// replace hands the replaced session's place to the record of target, which goes on from it under a new name, and
// saves; called with b.mu held. Both records change in one locked section, so no projection shows the two
// sessions, or neither. Not under target's lifecycle lock: the generation bump and invalidate reject what a poll
// read before, as taking the lock would.
func (b *hostBridges) replace(container, target string) {
	record, exists := b.records[target]
	if !exists {
		// bringUp registered it, and only a withdrawn restart, which does not get here, drops it.
		slog.Error("a restart under a new name lost the new container's record; keeping the old session",
			"container", container, "target", target)
		return
	}
	b.completeReplacement(target, record)
	b.guard(target).invalidate()
	if err := b.save(); err != nil {
		slog.Error("persist restart under a new name", "container", container, "target", target, "error", err)
	}
}

// completeReplacements completes each restart under a new name that a crash interrupted after it recorded the new
// container (ReplacingFrom), reporting whether it completed any; called on load. Completing is safe: ReplacingFrom
// is only written after the old container was stopped, so the new one runs the session's conversation, and
// recovery attaches it if the crash came before its bridge did.
func (b *hostBridges) completeReplacements() bool {
	completed := false
	for name, record := range b.records {
		if record.ReplacingFrom != "" {
			b.completeReplacement(name, record)
			completed = true
		}
	}
	return completed
}

// completeReplacement drops the record of the session target's record replaces, and clears its ReplacingFrom
// marker; called with b.mu held.
func (b *hostBridges) completeReplacement(target string, record bridgeRecord) {
	b.dropRecord(record.ReplacingFrom)
	record.ReplacingFrom = ""
	b.setRecord(target, record)
}

// forgetRestarted stops the container a restart started that is not to go on — withdrawn, cancelled, or one that
// could not be attached — and its bridge, and drops its record. Best effort: what fails is logged.
func (p *Provider) forgetRestarted(ctx context.Context, target string) {
	ctx = context.WithoutCancel(ctx)
	bridges := p.bridges
	_ = bridges.withLifecycle(target, func(*bridgeGuard) error {
		// Debug: a restart withdrawn before its container came up has none to stop.
		if err := p.cli.Stop(ctx, target); err != nil {
			slog.Debug("stopping the container of a restart that does not go on failed", "container", target,
				"error", err)
		}
		record, exists := p.bridgeRecordFor(target)
		if !exists {
			return nil
		}
		if err := bridges.stopBridge(ctx, record); err != nil {
			slog.Warn("stopping the bridge of a restart that does not go on failed", "container", target, "error", err)
		}
		bridges.mu.Lock()
		defer bridges.mu.Unlock()
		bridges.dropRecord(target)
		if err := bridges.save(); err != nil {
			slog.Error("persist dropping a restart that does not go on", "container", target, "error", err)
		}
		return nil
	})
}

// beginRestart takes the session into restarting and returns its record and a
// func putting it back as it was. The caller holds its lifecycle lock.
func (p *Provider) beginRestart(ctx context.Context, restarting restartProjection) (bridgeRecord, func(), error) {
	container := restarting.session.ID
	bridges := p.bridges
	bridges.mu.Lock()
	defer bridges.mu.Unlock()
	previous, owned := bridges.records[container]
	if !owned {
		return previous, nil, fmt.Errorf("%w: unknown Enclave session", provider.ErrSessionNotRestartable)
	}
	if err := ctx.Err(); err != nil {
		return previous, nil, err
	}
	// A second restart would share, and on failure delete, the first's entry,
	// making the first withdraw the container it just started.
	if _, busy := bridges.restarting[container]; busy {
		return previous, nil, fmt.Errorf("%w: the session is already restarting", provider.ErrSessionStarting)
	}
	bridges.restarting[container] = restarting
	previousObservation, hadObservation := bridges.observed[container]
	delete(bridges.observed, container)
	// Starting lives in bridges.restarting. Persisted as live, a crash mid-restart
	// makes the next daemon inspect the container instead.
	current := previous
	current.State, current.Error = "", ""
	bridges.setRecord(container, current)
	if err := bridges.save(); err != nil {
		slog.Warn("persisting a restarting enclave session failed", "container", container, "error", err)
	}
	restore := func() {
		bridges.mu.Lock()
		defer bridges.mu.Unlock()
		if _, exists := bridges.records[container]; exists {
			bridges.setRecord(container, previous)
			if hadObservation {
				bridges.observed[container] = previousObservation
			}
		}
	}
	return previous, restore, nil
}

// abandonRestart withdraws a restart that could not take the session down, leaving the session as it was, and
// returns why — unless the restart was withdrawn or cancelled meanwhile (endFailedRestart).
func (p *Provider) abandonRestart(ctx context.Context, container string, err error) error {
	slog.Warn("restarting an enclave session failed; it was left as it was", "container", container, "error", err)
	bridges := p.bridges
	bridges.mu.Lock()
	ended, interrupted := bridges.endFailedRestart(ctx, container)
	bridges.mu.Unlock()
	if !ended {
		return interrupted
	}
	p.notify()
	return err
}

// noContinueRefusal is how `enclave continue` refuses a tool whose profile defines
// neither continue nor resume arguments. It refuses before it creates anything,
// and it is the one failure a fresh start gets past.
const noContinueRefusal = "defines neither continue_args"

// cannotContinue reports whether err is Enclave refusing to continue the tool.
func cannotContinue(err error) bool {
	return err != nil && strings.Contains(err.Error(), noContinueRefusal)
}

// reportRestartFailure shows a restart's failure on its session, unless the restart was withdrawn or cancelled
// meanwhile (endFailedRestart).
func (p *Provider) reportRestartFailure(ctx context.Context, restarting restartProjection, err error) error {
	container := restarting.session.ID
	slog.Warn("restarting an enclave session failed", "container", container, "error", err)
	bridges := p.bridges
	bridges.mu.Lock()
	ended, interrupted := bridges.endFailedRestart(ctx, container)
	if !ended {
		bridges.mu.Unlock()
		return interrupted
	}
	failed := restarting.session
	failed.State = domain.SessionError
	failed.Error = err.Error()
	record, ok := bridges.records[container]
	if !ok {
		record = bridgeRecord{Container: Container{Name: container, Tool: failed.Agent, Directory: restarting.workingDir}}
	}
	bridges.guard(container).invalidate()
	record.State = domain.SessionError
	record.Error = err.Error()
	bridges.setRecord(container, record)
	bridges.observed[container] = failed
	if saveErr := bridges.save(); saveErr != nil {
		slog.Error("persist failed restart", "error", saveErr)
	}
	bridges.mu.Unlock()
	p.notify()
	return nil
}

// endFailedRestart takes a failed restart's session out of restarting, reporting whether it did; called with b.mu
// held. It does not for a restart withdrawn meanwhile, which returns nil, nor for one cancelled, which returns
// ErrRestartInterrupted, for RemoveSession to take the session out.
func (b *hostBridges) endFailedRestart(ctx context.Context, container string) (bool, error) {
	if _, stillRestarting := b.restarting[container]; !stillRestarting {
		return false, nil
	}
	if ctx.Err() != nil {
		return false, provider.ErrRestartInterrupted
	}
	delete(b.restarting, container)
	return true, nil
}

// notify tells the reconciler something changed, if Watch has handed it one.
func (p *Provider) notify() {
	p.mu.Lock()
	sink := p.sink
	p.mu.Unlock()
	if sink != nil {
		sink.Changed()
	}
}
