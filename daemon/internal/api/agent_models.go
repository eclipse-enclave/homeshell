package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/eclipsesource/homeshell/daemon/internal/agentcatalog"
)

func init() {
	register("GET /api/agent-models", func(d Deps) http.HandlerFunc {
		return listAgentModelsHandler(d)
	})
	register("POST /api/agent-models", func(d Deps) http.HandlerFunc {
		return createAgentModelHandler(d)
	})
	register("PATCH /api/agent-models/{entryId}", func(d Deps) http.HandlerFunc {
		return updateAgentModelHandler(d)
	})
	register("DELETE /api/agent-models/{entryId}", func(d Deps) http.HandlerFunc {
		return deleteAgentModelHandler(d)
	})
}

// The model catalog: which models each agent offers in a new session's Model field.
//
// A separate resource from `/api/settings` because these are *rows*, not fields.
// Settings is one document the client replaces wholesale (`PUT /api/settings`); an
// open-ended collection edited one entry at a time needs an id per row to address
// (`PATCH`/`DELETE …/{entryId}`) and would otherwise mean rewriting every preference
// to delete one model. They live in the agent catalog file for that reason
// (internal/agentcatalog).
//
// A change here needs no projection refresh: session options are read per request
// (`GET …/session-schemas`), so the next create form already reflects it. The agent
// must be one that has a description (`/api/agents`), as for `/api/agent-settings`;
// the model is not validated against the agent — the agent is the only authority on
// whether a model exists, and a wrong id fails at launch with the agent's own
// message.

// agentModelBody is the request shape. Both fields are optional on a PATCH, which
// only ever changes the model: moving a row to another agent would be a different
// row, so the agent is fixed at creation.
type agentModelBody struct {
	Agent *string `json:"agent"`
	Model *string `json:"model"`
}

// agentModelsUnavailable reports the endpoint as unusable when the daemon was built
// without persistence (tests that do not exercise it). A 503 rather than a panic: the
// rest of the API is fine, this part of it is not here.
func agentModelsUnavailable(writer http.ResponseWriter, d Deps) bool {
	if d.AgentCatalog == nil {
		jsonError(writer, "the model catalog is unavailable", http.StatusServiceUnavailable)
		return true
	}
	return false
}

func listAgentModelsHandler(d Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, _ *http.Request) {
		if agentModelsUnavailable(writer, d) {
			return
		}
		writeJSON(writer, http.StatusOK, struct {
			Entries []agentcatalog.Model `json:"entries"`
		}{d.AgentCatalog.ListModels()})
	}
}

// createAgentModelHandler offers a model for an agent. Both fields are required and
// neither may be blank: a row with no agent belongs to no menu, and one with no model
// puts an empty choice in it.
func createAgentModelHandler(d Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if agentModelsUnavailable(writer, d) {
			return
		}
		var body agentModelBody
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			jsonError(writer, "invalid JSON", http.StatusBadRequest)
			return
		}
		agent, model := trimmed(body.Agent), trimmed(body.Model)
		fields := map[string]string{}
		if agent == "" {
			fields["/agent"] = "must not be empty"
		}
		if model == "" {
			fields["/model"] = "must not be empty"
		}
		if len(fields) > 0 {
			jsonErrorFields(writer, "a catalog entry needs an agent and a model",
				fields, http.StatusBadRequest)
			return
		}
		entry := agentcatalog.Model{ID: generateID("agent-model"), Agent: agent, Model: model}
		if err := d.AgentCatalog.InsertModel(entry); err != nil {
			jsonError(writer, err.Error(), agentModelStatus(err, http.StatusInternalServerError))
			return
		}
		writeJSON(writer, http.StatusCreated, entry)
	}
}

// updateAgentModelHandler corrects a row's model, keeping its agent and its place in
// that agent's menu.
func updateAgentModelHandler(d Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if agentModelsUnavailable(writer, d) {
			return
		}
		var body agentModelBody
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			jsonError(writer, "invalid JSON", http.StatusBadRequest)
			return
		}
		model := trimmed(body.Model)
		if model == "" {
			jsonErrorFields(writer, "model must not be empty",
				map[string]string{"/model": "must not be empty"}, http.StatusBadRequest)
			return
		}
		entry, err := d.AgentCatalog.UpdateModel(request.PathValue("entryId"), model)
		if err != nil {
			jsonError(writer, err.Error(), agentModelStatus(err, http.StatusInternalServerError))
			return
		}
		writeJSON(writer, http.StatusOK, entry)
	}
}

func deleteAgentModelHandler(d Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if agentModelsUnavailable(writer, d) {
			return
		}
		if err := d.AgentCatalog.DeleteModel(request.PathValue("entryId")); err != nil {
			jsonError(writer, err.Error(), agentModelStatus(err, http.StatusInternalServerError))
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}
}

// agentModelStatus maps a catalog error onto a status: a missing row is a 404, a
// model that agent already offers is a 409 (the request is well-formed, the state
// refuses it), an agent with no description is a 400 (the request names nothing),
// and anything else is the fallback the caller chose.
func agentModelStatus(err error, fallback int) int {
	switch {
	case errors.Is(err, agentcatalog.ErrUnknownAgent):
		return http.StatusBadRequest
	case errors.Is(err, agentcatalog.ErrModelNotFound):
		return http.StatusNotFound
	case errors.Is(err, agentcatalog.ErrModelDuplicate):
		return http.StatusConflict
	default:
		return fallback
	}
}

// trimmed is the trimmed value of an optional string field, empty when the body left
// it out — so "absent" and "blank" are the same thing to a validator that requires it.
func trimmed(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
