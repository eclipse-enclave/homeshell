package local

import (
	"context"
	"log/slog"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/agentmodel"
	"github.com/eclipsesource/homeshell/daemon/internal/agentspec"
)

// Asking an agent's CLI on this host which models it has.
//
// For OpenCode and pi the answer is not ours to know: both resolve
// `provider/model` against a catalog they download, filtered by whichever
// providers this installation has authenticated. So instead of a list written into
// HomeShell, the provider runs the CLI's own listing command and offers what it
// says. Which command that is comes from the agent's description (an agents row's
// list_models, via agentspec.ListModelsArgv); a CLI that cannot answer keeps the
// user's authored catalog, or a free-text field when there is none. The parsing and
// the remembering are shared with the Enclave provider (internal/agentmodel), which
// asks the same CLIs inside a container.

// modelListTimeout bounds the question. The create form waits on it, so it is
// short: the listing may hit the network (OpenCode fetches models.dev), and a slow
// answer must degrade to the free-text field rather than stall the form.
const modelListTimeout = 3 * time.Second

// modelsFor asks an agent's CLI which models it has and returns them, or nil when
// the agent has no listing command, the command fails, or nothing in its output
// looks like a model ID. nil is not an error: it means "offer what we had before",
// which is the catalog or a free-text field.
func (p *Provider) modelsFor(ctx context.Context, a agentspec.Agent) []string {
	listModels := agentspec.ListModelsArgv(a)
	if len(listModels) == 0 {
		return nil
	}
	if models, ok := p.models.Get(a.Binary, time.Now()); ok {
		return models
	}
	ctx, cancel := context.WithTimeout(ctx, modelListTimeout)
	defer cancel()
	out, err := p.runtime.Output(ctx, listModels)
	if err != nil {
		// Not a failure of anything the user asked for: the form still offers the
		// field, just without a list. Remembered as empty so a broken or
		// unauthenticated CLI is not re-run on every form open.
		slog.Debug("listing an agent's models failed; offering free text instead", "agent", a.Binary, "error", err)
		p.models.Put(a.Binary, time.Now(), nil)
		return nil
	}
	models := agentmodel.ParseList(out)
	p.models.Put(a.Binary, time.Now(), models)
	return models
}
