package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/eclipsesource/homeshell/daemon/internal/agentcatalog"
	"github.com/eclipsesource/homeshell/daemon/internal/agentspec"
)

func init() {
	register("GET /api/agents", func(d Deps) http.HandlerFunc {
		return listAgentsHandler(d)
	})
	register("POST /api/agents", func(d Deps) http.HandlerFunc {
		return createAgentHandler(d)
	})
	register("PATCH /api/agents/{agentId}", func(d Deps) http.HandlerFunc {
		return updateAgentHandler(d)
	})
	register("DELETE /api/agents/{agentId}", func(d Deps) http.HandlerFunc {
		return deleteAgentHandler(d)
	})
	register("POST /api/agent-settings", func(d Deps) http.HandlerFunc {
		return createAgentSettingHandler(d)
	})
	register("PATCH /api/agent-settings/{settingId}", func(d Deps) http.HandlerFunc {
		return updateAgentSettingHandler(d)
	})
	register("DELETE /api/agent-settings/{settingId}", func(d Deps) http.HandlerFunc {
		return deleteAgentSettingHandler(d)
	})
}

// The agent descriptions: which agents HomeShell knows how to launch, and which
// settings a new session of each one offers.
//
// Two resources over one collection, because there are two kinds of row and only one
// of them nests. `/api/agents` is the agents themselves, each carrying its settings
// so a client reads the whole picture in one request; `/api/agent-settings` addresses
// one launch knob, which is what a client editing a single field needs (`PATCH`/
// `DELETE …/{settingId}`). Rows rather than fields in `/api/settings` because
// settings.json is a document of known keys, and this is an open-ended collection
// whose rows need identity.
//
// Deleting an agent takes its settings and its models with it: the knobs and the
// menu of an agent nothing can launch have nowhere to appear.
//
// No projection refresh is needed after any of these: session options are read per
// request (`GET …/session-schemas`), so the next create form is already built from
// what this changed. And nothing here is checked against the CLI: whether `--effort`
// exists, or whether a model is real, is the agent's authority, and a wrong answer
// fails at launch with the agent's own message rather than being second-guessed by a
// table (validation is limited to what the daemon itself must be able to act on — see
// agentcatalog.validateSetting).

// agentBody is the request shape for an agent. Every field is optional so a PATCH can
// rename an agent without restating how it resumes; id is taken from the path on a
// PATCH and from the body on a POST.
type agentBody struct {
	ID            *string   `json:"id"`
	Binary        *string   `json:"binary"`
	RuleSetID     *string   `json:"ruleSetId"`
	EnclaveTool   *string   `json:"enclaveTool"`
	Display       *string   `json:"display"`
	SessionIDFlag *string   `json:"sessionIdFlag"`
	ResumeFlag    *string   `json:"resumeFlag"`
	Transcript    *bool     `json:"transcript"`
	ListModels    *[]string `json:"listModels"`
}

// agentSettingBody is the request shape for one launch knob, every field optional for
// the same reason. agent is fixed at creation: moving a knob to another agent would be
// a different knob, and its position belongs to the form it is in.
type agentSettingBody struct {
	Agent       *string   `json:"agent"`
	Field       *string   `json:"field"`
	Role        *string   `json:"role"`
	Type        *string   `json:"type"`
	Title       *string   `json:"title"`
	Description *string   `json:"description"`
	Format      *string   `json:"format"`
	Choices     *[]string `json:"choices"`
	Default     *string   `json:"default"`
	Delivery    *string   `json:"delivery"`
	DeliveryArg *string   `json:"deliveryArg"`
	DeliveryKey *string   `json:"deliveryKey"`
}

// agentSpecsUnavailable reports the endpoints as unusable when the daemon was built
// without persistence (tests that do not exercise them). A 503 rather than a panic:
// the rest of the API is fine, this part of it is not here.
func agentSpecsUnavailable(writer http.ResponseWriter, d Deps) bool {
	if d.AgentCatalog == nil {
		jsonError(writer, "the agent descriptions are unavailable", http.StatusServiceUnavailable)
		return true
	}
	return false
}

func listAgentsHandler(d Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, _ *http.Request) {
		if agentSpecsUnavailable(writer, d) {
			return
		}
		writeJSON(writer, http.StatusOK, struct {
			Agents []agentspec.Agent `json:"agents"`
		}{d.AgentCatalog.Agents()})
	}
}

// createAgentHandler describes a new launchable agent. It is not a claim that this
// host has it: availability is discovered (a binary on PATH, `enclave tools`), so a
// row for an agent that is not installed is simply never offered.
func createAgentHandler(d Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if agentSpecsUnavailable(writer, d) {
			return
		}
		var body agentBody
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			jsonError(writer, "invalid JSON", http.StatusBadRequest)
			return
		}
		agent := agentspec.Agent{
			ID:            trimmed(body.ID),
			Binary:        trimmed(body.Binary),
			RuleSetID:     trimmed(body.RuleSetID),
			EnclaveTool:   trimmed(body.EnclaveTool),
			Display:       trimmed(body.Display),
			SessionIDFlag: trimmed(body.SessionIDFlag),
			ResumeFlag:    trimmed(body.ResumeFlag),
			ListModels:    stringsOf(body.ListModels),
		}
		if body.Transcript != nil {
			agent.Transcript = *body.Transcript
		}
		fields := map[string]string{}
		if agent.ID == "" {
			fields["/id"] = "must not be empty"
		}
		if agent.Binary == "" {
			fields["/binary"] = "must not be empty"
		}
		if agent.Display == "" {
			fields["/display"] = "must not be empty"
		}
		if len(fields) > 0 {
			jsonErrorFields(writer, "an agent needs an id, a binary and a display name",
				fields, http.StatusBadRequest)
			return
		}
		if err := d.AgentCatalog.InsertAgent(agent); err != nil {
			jsonError(writer, err.Error(), agentSpecStatus(err))
			return
		}
		agent.Settings = []agentspec.Setting{}
		writeJSON(writer, http.StatusCreated, agent)
	}
}

// updateAgentHandler edits an agent. Fields left out are kept, so fixing a display
// name does not require restating how the agent resumes. Its id and its place in the
// selector are not editable — an id is what a persisted session and a settings row
// refer to it by.
func updateAgentHandler(d Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if agentSpecsUnavailable(writer, d) {
			return
		}
		var body agentBody
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			jsonError(writer, "invalid JSON", http.StatusBadRequest)
			return
		}
		existing, err := d.AgentCatalog.AgentByID(request.PathValue("agentId"))
		if err != nil {
			jsonError(writer, err.Error(), agentSpecStatus(err))
			return
		}
		if fields := applyAgentBody(&existing, body); len(fields) > 0 {
			jsonErrorFields(writer, "an agent needs a binary and a display name",
				fields, http.StatusBadRequest)
			return
		}
		if err := d.AgentCatalog.UpdateAgent(existing); err != nil {
			jsonError(writer, err.Error(), agentSpecStatus(err))
			return
		}
		writeJSON(writer, http.StatusOK, existing)
	}
}

// applyAgentBody copies the fields a PATCH supplied onto an existing agent, reporting
// per-field errors for the two that may not be blanked — so a form can blame the box
// the user emptied rather than the request as a whole.
func applyAgentBody(agent *agentspec.Agent, body agentBody) map[string]string {
	fields := map[string]string{}
	if body.RuleSetID != nil {
		agent.RuleSetID = strings.TrimSpace(*body.RuleSetID)
	}
	if body.EnclaveTool != nil {
		agent.EnclaveTool = strings.TrimSpace(*body.EnclaveTool)
	}
	if body.Binary != nil {
		if binary := strings.TrimSpace(*body.Binary); binary == "" {
			fields["/binary"] = "must not be empty"
		} else {
			agent.Binary = binary
		}
	}
	if body.Display != nil {
		if display := strings.TrimSpace(*body.Display); display == "" {
			fields["/display"] = "must not be empty"
		} else {
			agent.Display = display
		}
	}
	if body.SessionIDFlag != nil {
		agent.SessionIDFlag = strings.TrimSpace(*body.SessionIDFlag)
	}
	if body.ResumeFlag != nil {
		agent.ResumeFlag = strings.TrimSpace(*body.ResumeFlag)
	}
	if body.Transcript != nil {
		agent.Transcript = *body.Transcript
	}
	if body.ListModels != nil {
		agent.ListModels = stringsOf(body.ListModels)
	}
	return fields
}

func deleteAgentHandler(d Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if agentSpecsUnavailable(writer, d) {
			return
		}
		if err := d.AgentCatalog.DeleteAgent(request.PathValue("agentId")); err != nil {
			jsonError(writer, err.Error(), agentSpecStatus(err))
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}
}

// createAgentSettingHandler adds a launch knob to an agent's create form, at the end
// of it.
func createAgentSettingHandler(d Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if agentSpecsUnavailable(writer, d) {
			return
		}
		var body agentSettingBody
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			jsonError(writer, "invalid JSON", http.StatusBadRequest)
			return
		}
		// A create starts from the string default rather than the zero value: string is
		// the type all but one seeded setting has, and a body that omits it means "an
		// ordinary text/enum field", not "a type the daemon rejects".
		setting := agentspec.Setting{ID: generateID("agent-setting"), Type: agentspec.TypeString}
		applySettingBody(&setting, body)
		if setting.Agent == "" {
			jsonErrorFields(writer, "a setting needs the agent it belongs to",
				map[string]string{"/agent": "must not be empty"}, http.StatusBadRequest)
			return
		}
		if err := d.AgentCatalog.InsertSetting(setting); err != nil {
			jsonError(writer, err.Error(), agentSpecStatus(err))
			return
		}
		writeJSON(writer, http.StatusCreated, setting)
	}
}

// updateAgentSettingHandler edits one launch knob. Fields left out are kept, so
// rewording a label does not require restating the flag it delivers through.
func updateAgentSettingHandler(d Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if agentSpecsUnavailable(writer, d) {
			return
		}
		var body agentSettingBody
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			jsonError(writer, "invalid JSON", http.StatusBadRequest)
			return
		}
		existing, err := d.AgentCatalog.SettingByID(request.PathValue("settingId"))
		if err != nil {
			jsonError(writer, err.Error(), agentSpecStatus(err))
			return
		}
		applySettingBody(&existing, body)
		if err := d.AgentCatalog.UpdateSetting(existing); err != nil {
			jsonError(writer, err.Error(), agentSpecStatus(err))
			return
		}
		writeJSON(writer, http.StatusOK, existing)
	}
}

// applySettingBody copies the fields a body supplied onto a setting. Nothing is
// range-checked here: the store refuses a row nothing could act on (an unknown
// delivery or type, a delivery with no flag) and says which, so one place decides what
// is storable rather than two that can disagree.
func applySettingBody(setting *agentspec.Setting, body agentSettingBody) {
	if body.Agent != nil && setting.Agent == "" {
		setting.Agent = strings.TrimSpace(*body.Agent)
	}
	if body.Field != nil {
		setting.Field = strings.TrimSpace(*body.Field)
	}
	if body.Role != nil {
		setting.Role = strings.TrimSpace(*body.Role)
	}
	if body.Type != nil {
		setting.Type = strings.TrimSpace(*body.Type)
	}
	if body.Title != nil {
		setting.Title = strings.TrimSpace(*body.Title)
	}
	if body.Description != nil {
		setting.Description = strings.TrimSpace(*body.Description)
	}
	if body.Format != nil {
		setting.Format = strings.TrimSpace(*body.Format)
	}
	if body.Choices != nil {
		setting.Choices = stringsOf(body.Choices)
	}
	if body.Default != nil {
		setting.Default = strings.TrimSpace(*body.Default)
	}
	if body.Delivery != nil {
		setting.Delivery = agentspec.Kind(strings.TrimSpace(*body.Delivery))
	}
	if body.DeliveryArg != nil {
		setting.DeliveryArg = strings.TrimSpace(*body.DeliveryArg)
	}
	if body.DeliveryKey != nil {
		setting.DeliveryKey = strings.TrimSpace(*body.DeliveryKey)
	}
}

func deleteAgentSettingHandler(d Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if agentSpecsUnavailable(writer, d) {
			return
		}
		if err := d.AgentCatalog.DeleteSetting(request.PathValue("settingId")); err != nil {
			jsonError(writer, err.Error(), agentSpecStatus(err))
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}
}

// agentSpecStatus maps a store error onto a status: a missing row is a 404, a taken id
// or a duplicated field is a 409 (the request is well-formed, the state refuses it), a
// row the daemon could not act on — or a setting on an agent nothing describes — is a
// 400, and anything else is a persistence failure.
func agentSpecStatus(err error) int {
	switch {
	case errors.Is(err, agentcatalog.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, agentcatalog.ErrDuplicate):
		return http.StatusConflict
	case errors.Is(err, agentcatalog.ErrInvalid), errors.Is(err, agentcatalog.ErrUnknownAgent):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// stringsOf is a body's optional string list, trimmed and with blanks dropped. nil
// when the body left it out, and nil for a list that held nothing usable — which is
// how "this CLI cannot list its models" and "this field is free text" are both spelled.
func stringsOf(values *[]string) []string {
	if values == nil {
		return nil
	}
	out := make([]string, 0, len(*values))
	for _, value := range *values {
		if trimmedValue := strings.TrimSpace(value); trimmedValue != "" {
			out = append(out, trimmedValue)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
