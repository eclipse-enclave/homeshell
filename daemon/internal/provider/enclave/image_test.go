//go:build dev

package enclave

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/agentcatalog"
	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/provider/enclave/sandboxschema"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

var testPNG = provider.Image{MediaType: "image/png", Data: []byte("\x89PNG\r\n\x1a\nimage")}

// imageSession starts a session in HomeShell against an Enclave with the image
// inbox, configured on, and returns its reference.
func imageSession(t *testing.T) (*Provider, *fakeCLI, *testBridge, provider.ProjectRef, provider.SessionRef) {
	t.Helper()
	p, cli, bridge, project := bridgeProvider(t)
	cli.imageInbox = true
	cli.imagePath = "/mnt/host-images/20261125T120000Z-0a1b2c3d.png"
	cli.config = map[string]sandboxschema.EffectiveConfig{"codex": {"image_inbox": option(`true`, "global")}}
	created := createBridge(t, p, project)
	return p, cli, bridge, project, provider.SessionRef{ProjectID: project.ID, SessionID: created.ID}
}

// attachImageOf reports whether the session the project shows declares attachImage.
func attachImageOf(t *testing.T, p *Provider, project provider.ProjectRef) bool {
	t.Helper()
	session := discoverBridge(t, p, project)
	return session.Capabilities != nil && session.Capabilities.AttachImage
}

// Until an inventory check lists it, a session HomeShell started or restarted has the image inbox its run resolved:
// the flag passed, else Enclave's config.
func TestAttachImageCapabilityOfALaunch(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		config     string
		want       bool
	}{
		{name: "configured on", configured: `true`, config: `{}`, want: true},
		{name: "configured off", configured: `false`, config: `{}`, want: false},
		{name: "turned on", configured: `false`, config: `{"enclave":{"imageInbox":true}}`, want: true},
		{name: "turned off", configured: `true`, config: `{"enclave":{"imageInbox":false}}`, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, cli, _, project := bridgeProvider(t)
			cli.imageInbox = true
			cli.config = map[string]sandboxschema.EffectiveConfig{"codex": {"image_inbox": option(tt.configured, "global")}}
			if _, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{
				WorkingDir: project.Path, OptionID: "codex", Config: json.RawMessage(tt.config),
			}); err != nil {
				t.Fatal(err)
			}
			if got := attachImageOf(t, p, project); got != tt.want {
				t.Errorf("attachImage after the start = %v, want %v", got, tt.want)
			}
		})
	}
}

// A restart replaces what was known of the container it stops, as it starts another.
func TestAttachImageCapabilityAfterARestart(t *testing.T) {
	p, cli, _, project, ref := imageSession(t)
	listedWith(cli, project, ref.SessionID, map[string]json.RawMessage{"image_inbox": json.RawMessage("true")})
	p.checkInventory(context.Background())
	if !attachImageOf(t, p, project) {
		t.Fatal("no attachImage with the inbox on")
	}
	restart, err := p.PrepareRestart(context.Background(), provider.RestartSessionRequest{Session: ref, WorkingDir: project.Path,
		OptionID: "codex", Config: json.RawMessage(`{"enclave":{"imageInbox":false}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if attachImageOf(t, p, project) {
		t.Error("attachImage after a restart that turned the inbox off")
	}
}

// listedWith makes the fake CLI list the session with the options `ps --json` recorded for it.
func listedWith(cli *fakeCLI, project provider.ProjectRef, name string, options map[string]json.RawMessage) {
	listed(cli, project, Container{Name: name, Tool: "codex", Directory: project.Path, Status: "running",
		SessionName: "hs-134", Background: true, Options: options})
}

// A session declares attachImage when Enclave has the image inbox, HomeShell has a bridge to it, and its recorded
// image inbox is on or unknown.
func TestAttachImageCapability(t *testing.T) {
	inbox := func(raw string) map[string]json.RawMessage {
		return map[string]json.RawMessage{"image_inbox": json.RawMessage(raw), "yolo": json.RawMessage("true")}
	}
	tests := []struct {
		name      string
		supported bool
		// listed is whether the inventory lists the session, with options.
		listed  bool
		options map[string]json.RawMessage
		want    bool
	}{
		{name: "started in HomeShell with it on, not listed yet", supported: true, want: true},
		{name: "an Enclave without the inbox", supported: false, listed: true, options: inbox("true"), want: false},
		{name: "inbox on", supported: true, listed: true, options: inbox("true"), want: true},
		{name: "inbox off", supported: true, listed: true, options: inbox("false"), want: false},
		{name: "options without image_inbox", supported: true, listed: true, options: map[string]json.RawMessage{"yolo": json.RawMessage("true")}, want: false},
		{name: "options missing", supported: true, listed: true, options: nil, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, cli, _, project, ref := imageSession(t)
			cli.imageInbox = tt.supported
			if tt.listed {
				listedWith(cli, project, ref.SessionID, tt.options)
				p.checkInventory(context.Background())
			}
			session := discoverBridge(t, p, project)
			if got := attachImageOf(t, p, project); got != tt.want {
				t.Fatalf("attachImage = %v, want %v (capabilities %+v)", got, tt.want, session.Capabilities)
			}
			if tt.want && !session.Capabilities.Live {
				t.Error("a session that can take an image no longer shows as live")
			}
			state, err := p.SessionState(context.Background(), ref)
			if err != nil {
				t.Fatal(err)
			}
			if stateGot := state.Capabilities != nil && state.Capabilities.AttachImage; stateGot != tt.want {
				t.Errorf("SessionState attachImage = %v, want %v", stateGot, tt.want)
			}
		})
	}
}

// The inventory's later answer wins: an inbox turned on by a restart shows once Enclave lists it.
func TestAttachImageCapabilityFollowsTheListing(t *testing.T) {
	p, cli, _, project, ref := imageSession(t)
	listedWith(cli, project, ref.SessionID, map[string]json.RawMessage{"image_inbox": json.RawMessage("false")})
	if !p.checkInventory(context.Background()) {
		t.Error("a listing that takes the capability away reported no change")
	}
	if session := discoverBridge(t, p, project); session.Capabilities != nil {
		t.Fatalf("capabilities = %+v, want none with the inbox off", session.Capabilities)
	}
	listedWith(cli, project, ref.SessionID, map[string]json.RawMessage{"image_inbox": json.RawMessage("true")})
	p.checkInventory(context.Background())
	if !attachImageOf(t, p, project) {
		t.Fatal("no attachImage with the inbox on")
	}
}

func TestAttachImageCapabilityNeedsABridge(t *testing.T) {
	t.Run("foreground CLI session", func(t *testing.T) {
		p, cli, _, project, _ := adoptedForeground(t)
		cli.imageInbox = true
		background := cliSession(project.Path, false)
		background.Options = map[string]json.RawMessage{"image_inbox": json.RawMessage("true")}
		listed(cli, project, background)
		p.checkInventory(context.Background())
		out, _ := p.DiscoverWatchedSessions(context.Background(), project)
		if len(out) != 1 || out[0].Session.Capabilities == nil || out[0].Session.Capabilities.AttachImage {
			t.Fatalf("sessions = %+v, want a foreground session without attachImage", out)
		}
	})
	t.Run("stopped session", func(t *testing.T) {
		p, _, _, project, ref := imageSession(t)
		if err := p.StopSession(context.Background(), ref); err != nil {
			t.Fatal(err)
		}
		if session := discoverBridge(t, p, project); session.Capabilities != nil {
			t.Fatalf("capabilities = %+v, want none once stopped", session.Capabilities)
		}
	})
}

// An attach imports the image from the session's directory into its container's inbox, then types the path and
// a space into the agent's input without submitting it.
func TestAttachImageTypesThePathWithoutSubmitting(t *testing.T) {
	p, cli, bridge, project, ref := imageSession(t)
	path, err := p.AttachImage(context.Background(), ref, testPNG)
	if err != nil {
		t.Fatal(err)
	}
	if path != cli.imagePath {
		t.Errorf("path = %q, want %q", path, cli.imagePath)
	}
	if len(cli.imports) != 1 || cli.imports[0].dir != project.Path || cli.imports[0].container != ref.SessionID ||
		string(cli.imports[0].image) != string(testPNG.Data) {
		t.Errorf("imports = %+v, want the image imported for %s from %s", cli.imports, ref.SessionID, project.Path)
	}
	if !slices.Equal(bridge.raw, []string{cli.imagePath + " "}) || bridge.input != "" || bridge.key != "" {
		t.Errorf("typed raw %q, input %q, key %q; want only the path and a space", bridge.raw, bridge.input, bridge.key)
	}
}

// An agent that lists the image's format in the catalog is sent Ctrl+V right after the import, for a native
// attachment; any other gets the path and a space. Neither is submitted.
func TestAttachImageDelivery(t *testing.T) {
	jpeg := provider.Image{MediaType: "image/jpeg", Data: []byte("\xff\xd8\xffimage")}
	tests := []struct {
		name       string
		imagePaste []string
		image      provider.Image
		paste      bool
	}{
		{name: "PNG to an agent that pastes PNG and JPEG", imagePaste: []string{"png", "jpeg"}, image: testPNG, paste: true},
		{name: "JPEG to an agent that pastes PNG and JPEG", imagePaste: []string{"png", "jpeg"}, image: jpeg, paste: true},
		{name: "PNG to an agent that pastes PNG", imagePaste: []string{"png"}, image: testPNG, paste: true},
		{name: "JPEG to an agent that pastes PNG", imagePaste: []string{"png"}, image: jpeg, paste: false},
		{name: "an agent that pastes none", imagePaste: nil, image: testPNG, paste: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, cli, bridge, _, ref := imageSession(t)
			file := agentcatalog.Defaults()
			for i := range file.Agents {
				if file.Agents[i].ID == "codex" {
					file.Agents[i].ImagePaste = tt.imagePaste
				}
			}
			p.SetAgentSpecs(agentcatalog.NewMemory(file))
			path, err := p.AttachImage(context.Background(), ref, tt.image)
			if err != nil {
				t.Fatal(err)
			}
			want := path + " "
			if tt.paste {
				want = "\x16"
			}
			if !slices.Equal(bridge.raw, []string{want}) || bridge.input != "" || bridge.key != "" {
				t.Errorf("typed raw %q, input %q, key %q; want only %q", bridge.raw, bridge.input, bridge.key, want)
			}
			if len(cli.imports) != 1 {
				t.Errorf("%d imports, want 1", len(cli.imports))
			}
		})
	}
}

// A session whose tool no agent in the catalog describes gets the path.
func TestAttachImageToAnUndescribedToolTypesThePath(t *testing.T) {
	p, _, bridge, _, ref := imageSession(t)
	p.SetAgentSpecs(agentcatalog.NewMemory(agentcatalog.File{SchemaVersion: 2}))
	path, err := p.AttachImage(context.Background(), ref, testPNG)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(bridge.raw, []string{path + " "}) {
		t.Errorf("typed raw %q, want the path and a space", bridge.raw)
	}
}

func TestAttachImageReturnsEnclavesError(t *testing.T) {
	p, cli, bridge, _, ref := imageSession(t)
	cli.importErr = errors.New(`session "hs-134" (enclave-codex-0123456789ab-hs-134) has no image inbox`)
	if _, err := p.AttachImage(context.Background(), ref, testPNG); err == nil || err.Error() != cli.importErr.Error() {
		t.Fatalf("AttachImage = %v, want Enclave's error as it is", err)
	}
	if len(bridge.raw) != 0 {
		t.Errorf("typed %q after a failed import", bridge.raw)
	}
}

func TestAttachImageRefusals(t *testing.T) {
	t.Run("an Enclave without the inbox", func(t *testing.T) {
		p, cli, _, _, ref := imageSession(t)
		cli.imageInbox = false
		if _, err := p.AttachImage(context.Background(), ref, testPNG); !errors.Is(err, provider.ErrCapabilityUnsupported) {
			t.Fatalf("AttachImage = %v, want ErrCapabilityUnsupported", err)
		}
	})
	t.Run("inbox off", func(t *testing.T) {
		p, cli, _, project, ref := imageSession(t)
		listedWith(cli, project, ref.SessionID, map[string]json.RawMessage{"image_inbox": json.RawMessage("false")})
		p.checkInventory(context.Background())
		if _, err := p.AttachImage(context.Background(), ref, testPNG); !errors.Is(err, provider.ErrCapabilityUnsupported) {
			t.Fatalf("AttachImage = %v, want ErrCapabilityUnsupported", err)
		}
	})
	t.Run("foreground", func(t *testing.T) {
		p, cli, _, _, ref := adoptedForeground(t)
		cli.imageInbox = true
		if _, err := p.AttachImage(context.Background(), ref, testPNG); !errors.Is(err, provider.ErrCapabilityUnsupported) {
			t.Fatalf("AttachImage = %v, want ErrCapabilityUnsupported", err)
		}
	})
	t.Run("stopped", func(t *testing.T) {
		p, cli, _, _, ref := imageSession(t)
		if err := p.StopSession(context.Background(), ref); err != nil {
			t.Fatal(err)
		}
		if _, err := p.AttachImage(context.Background(), ref, testPNG); !errors.Is(err, provider.ErrSessionStopped) {
			t.Fatalf("AttachImage = %v, want ErrSessionStopped", err)
		}
		if len(cli.imports) != 0 {
			t.Error("imported into a stopped session")
		}
	})
	t.Run("another project", func(t *testing.T) {
		p, cli, _, _, ref := imageSession(t)
		ref.ProjectID = "other"
		if _, err := p.AttachImage(context.Background(), ref, testPNG); err == nil {
			t.Fatal("attached through a project that does not own the session")
		}
		if len(cli.imports) != 0 {
			t.Error("imported through a project that does not own the session")
		}
	})
}

// Two attaches in one project run one after the other, import and delivery together; attaches in different
// projects do not wait for each other.
func TestAttachImageLocksPerProject(t *testing.T) {
	p, cli, bridge, _ := bridgeProvider(t)
	cli.imageInbox = true
	cli.imagePath = "/mnt/host-images/a.png"
	cli.config = map[string]sandboxschema.EffectiveConfig{"codex": {"image_inbox": option(`true`, "global")}}
	cli.runBase = "enclave-codex-0123456789ab"
	dirs := []string{t.TempDir(), t.TempDir()}
	p.SetStore(store.NewTestStore(t, []domain.Project{
		{ID: "p", Path: dirs[0], Workstreams: []domain.Workstream{{ID: "ws", WorkingDir: dirs[0], Sessions: []domain.Session{}}}},
		{ID: "q", Path: dirs[1], Workstreams: []domain.Workstream{{ID: "ws", WorkingDir: dirs[1], Sessions: []domain.Session{}}}},
	}))
	refs := []provider.SessionRef{}
	for _, project := range []provider.ProjectRef{{ID: "p", Path: dirs[0]}, {ID: "p", Path: dirs[0]}, {ID: "q", Path: dirs[1]}} {
		refs = append(refs, provider.SessionRef{ProjectID: project.ID, SessionID: createBridge(t, p, project).ID})
	}

	release := make(chan struct{})
	type entry struct {
		container string
		typed     int
	}
	entered := make(chan entry, len(refs))
	cli.onImport = func(call importCall) {
		bridge.mu.Lock()
		typed := len(bridge.raw)
		bridge.mu.Unlock()
		entered <- entry{call.container, typed}
		if call.container == refs[0].SessionID {
			<-release
		}
	}
	var wait sync.WaitGroup
	attach := func(ref provider.SessionRef) {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := p.AttachImage(context.Background(), ref, testPNG); err != nil {
				t.Error(err)
			}
		}()
	}
	waitFor := func() entry {
		t.Helper()
		select {
		case got := <-entered:
			return got
		case <-time.After(5 * time.Second):
			t.Fatal("no import started")
			return entry{}
		}
	}

	attach(refs[0])
	if got := waitFor(); got.container != refs[0].SessionID {
		t.Fatalf("first import = %+v", got)
	}
	attach(refs[2])
	if got := waitFor(); got.container != refs[2].SessionID {
		t.Fatalf("an attach in another project waited: %+v", got)
	}
	for typed := 0; typed < 1; time.Sleep(time.Millisecond) {
		bridge.mu.Lock()
		typed = len(bridge.raw)
		bridge.mu.Unlock()
	}
	cancelled, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := p.AttachImage(cancelled, refs[1], testPNG); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("an attach waiting for the project's lock = %v, want it to give up with its request", err)
	}
	attach(refs[1])
	select {
	case got := <-entered:
		t.Fatalf("a second attach in the project imported before the first was delivered: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if got := waitFor(); got.container != refs[1].SessionID || got.typed != 2 {
		t.Fatalf("second import = %+v, want it after the first path was typed", got)
	}
	wait.Wait()
}
