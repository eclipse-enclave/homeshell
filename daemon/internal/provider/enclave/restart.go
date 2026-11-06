package enclave

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
)

// PrepareRestart readies a session to be started again with new settings.
//
// Enclave fixes a container's settings when it creates it and has no command that
// changes them, so a restart is `enclave stop` (which also removes the container)
// followed by `enclave continue` under the **same** `--name`. Enclave derives the
// container name from that name, so the container comes back under the session's
// ID, keeping its title and its row; and `continue` hands the tool its profile's
// continue arguments, so the agent picks its last conversation back up — that
// conversation lives in Enclave's per-project stores on the host, not in the
// container. A fresh restart uses `enclave run` instead, and so does a restart of a
// tool whose profile has no way to continue (cannotContinue).
//
// "Last conversation" is the tool's notion: two sessions of the same tool in the
// same directory share it, so either would continue the other's latest; such a
// session is only restarted fresh (ContinueRefusal).
//
// Everything that can refuse the restart — a container with no session name to
// restart it under (CheckRestart), settings that cannot become flags — is checked
// here, and nothing is changed: the session is only shown as starting once the
// returned func runs.
func (p *Provider) PrepareRestart(ctx context.Context, req provider.RestartSessionRequest) (provider.RestartFunc, error) {
	container := req.Session.SessionID
	if _, owned := p.bridgeRecordFor(container); !owned {
		return nil, fmt.Errorf("%w: unknown Enclave session", provider.ErrSessionNotRestartable)
	}
	tool := restartTool(req.OptionID)
	sessionName, err := restartSessionName(container, tool)
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
// settings: a container with no session name to restart it under.
func (p *Provider) CheckRestart(ref provider.SessionRef, optionID string) error {
	if _, owned := p.bridgeRecordFor(ref.SessionID); !owned {
		return fmt.Errorf("%w: unknown Enclave session", provider.ErrSessionNotRestartable)
	}
	_, err := restartSessionName(ref.SessionID, restartTool(optionID))
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

// restartSessionName is the session name a container is restarted under. Without
// one, `enclave run` would start the tool's default container, which is a
// different session — and that is what a container Enclave started without a
// name, outside HomeShell, is.
func restartSessionName(container, tool string) (string, error) {
	if sessionName := parseSessionName(container, tool); sessionName != "" {
		return sessionName, nil
	}
	return "", fmt.Errorf("%w: the container %q has no session name to restart it under: it is a tool's unnamed default container, started outside HomeShell; start a new session with the settings instead",
		provider.ErrSessionNotRestartable, container)
}

// restart replaces the session's container with one started with opts, reporting
// the session as starting meanwhile. A failure to start it is reported on the
// session, as a failed start is. A failure to stop the old container leaves the
// session as it was — the container is most likely still running it — and is
// returned instead, for the service to report.
//
// A restart is withdrawn by removing the session — RemoveSession takes it out of
// restarting — and cancelled by its context. A withdrawn or cancelled restart
// brings nothing back; one cancelled before it began returns the context's
// error, and one cancelled once it took the session into restarting, and not
// withdrawn, leaves it there and returns provider.ErrRestartInterrupted, for the
// service to remove the session.
func (p *Provider) restart(ctx context.Context, restarting restartProjection, opts RunOpts) error {
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
		bridgeErr = bridges.runtime.Stop(ctx, previous.target())
		return nil
	})
	if err != nil {
		return err
	}
	if stopErr != nil {
		return p.abandonRestart(ctx, container, fmt.Errorf("stop enclave container: %w", stopErr))
	}
	if bridgeErr != nil {
		return p.reportRestartFailure(ctx, restarting, fmt.Errorf("stop old enclave bridge: %w", bridgeErr))
	}
	bridges.mu.Lock()
	delete(bridges.outdated, container)
	bridges.restarts++
	bridges.mu.Unlock()

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
	if err == nil && started != container {
		// Enclave names the container after the session name it was given, so the
		// row stays the session's. One under another name is not this session: it
		// is stopped rather than left running for nothing to own.
		if stopErr := p.cli.Stop(context.WithoutCancel(ctx), started); stopErr != nil {
			slog.Warn("stopping a restarted container that came back under another name failed",
				"container", started, "error", stopErr)
		}
		err = fmt.Errorf("enclave started the session as %q instead of %q", started, container)
	}
	if err != nil && ctx.Err() == nil {
		return p.reportRestartFailure(ctx, restarting, fmt.Errorf("start enclave container: %w", err))
	}
	bridges.mu.Lock()
	_, stillRestarting := bridges.restarting[container]
	bridges.mu.Unlock()
	if stillRestarting && ctx.Err() == nil && err == nil {
		err = p.registerBridge(ctx, Container{Name: container, Tool: opts.Tool, Directory: restarting.workingDir, Status: "running"})
		if err != nil {
			return p.reportRestartFailure(ctx, restarting, err)
		}
	}
	bridges.mu.Lock()
	_, stillRestarting = bridges.restarting[container]
	if ctx.Err() != nil && stillRestarting {
		// Cancelled, for RemoveSession to take the entry, and stop the container,
		// whatever this restart got as far as.
		bridges.mu.Unlock()
		return provider.ErrRestartInterrupted
	}
	delete(bridges.restarting, container)
	bridges.mu.Unlock()
	if !stillRestarting {
		// Removed while it was coming back, after the removal stopped the old
		// container: the one started now, and its bridge, belong to nothing.
		_ = bridges.withLifecycle(container, func(*bridgeGuard) error {
			record, exists := p.bridgeRecordFor(container)
			if stopErr := p.cli.Stop(context.WithoutCancel(ctx), container); stopErr != nil {
				slog.Debug("stopping the container of a withdrawn restart failed", "container", container, "error", stopErr)
			}
			if exists {
				_ = bridges.runtime.Stop(context.WithoutCancel(ctx), record.target())
			}
			bridges.mu.Lock()
			defer bridges.mu.Unlock()
			delete(bridges.records, container)
			delete(bridges.observed, container)
			if err := bridges.save(); err != nil {
				slog.Error("persist withdrawn restart", "error", err)
			}
			return nil
		})
	}
	p.notify()
	return nil
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
	bridges.records[container] = current
	if err := bridges.save(); err != nil {
		slog.Warn("persisting a restarting enclave session failed", "container", container, "error", err)
	}
	restore := func() {
		bridges.mu.Lock()
		defer bridges.mu.Unlock()
		if _, exists := bridges.records[container]; exists {
			bridges.records[container] = previous
			if hadObservation {
				bridges.observed[container] = previousObservation
			}
		}
	}
	return previous, restore, nil
}

// abandonRestart withdraws a restart that could not take the session down,
// leaving the session as it was, and returns why — unless the restart was
// withdrawn meanwhile, or cancelled, which leaves the session to RemoveSession
// (interrupted).
func (p *Provider) abandonRestart(ctx context.Context, container string, err error) error {
	slog.Warn("restarting an enclave session failed; it was left as it was", "container", container, "error", err)
	bridges := p.bridges
	bridges.mu.Lock()
	_, stillRestarting := bridges.restarting[container]
	if !stillRestarting || ctx.Err() != nil {
		bridges.mu.Unlock()
		return interrupted(ctx, stillRestarting)
	}
	delete(bridges.restarting, container)
	// A check meanwhile may have compared the container with the settings it was
	// to be restarted with.
	bridges.restarts++
	bridges.mu.Unlock()
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

// reportRestartFailure shows a restart's failure on its session, unless the
// restart was withdrawn or cancelled meanwhile; a cancelled one is left to
// RemoveSession (interrupted).
func (p *Provider) reportRestartFailure(ctx context.Context, restarting restartProjection, err error) error {
	container := restarting.session.ID
	slog.Warn("restarting an enclave session failed", "container", container, "error", err)
	bridges := p.bridges
	bridges.mu.Lock()
	_, stillRestarting := bridges.restarting[container]
	if !stillRestarting || ctx.Err() != nil {
		bridges.mu.Unlock()
		return interrupted(ctx, stillRestarting)
	}
	delete(bridges.restarting, container)
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
	bridges.records[container] = record
	bridges.observed[container] = failed
	if saveErr := bridges.save(); saveErr != nil {
		slog.Error("persist failed restart", "error", saveErr)
	}
	bridges.mu.Unlock()
	p.notify()
	return nil
}

// interrupted is what a restart that stopped short returns: ErrRestartInterrupted
// when it was cancelled with the session still restarting, for RemoveSession to
// take it out; nil when it was withdrawn, or not cancelled.
func interrupted(ctx context.Context, stillRestarting bool) error {
	if stillRestarting && ctx.Err() != nil {
		return provider.ErrRestartInterrupted
	}
	return nil
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
