package agentcatalog

import "github.com/eclipsesource/homeshell/daemon/internal/agentspec"

// Defaults is the catalog a fresh install starts with: HomeShell's first draft,
// written once and the user's from then on.
//
// It is not a transcription of the Go the catalog replaced. A session option is
// one per agent instead of one per model; the model is a setting (RoleModel)
// beside the other knobs, and its choices come from the models below or from
// the CLI's own answer. The initial prompt is the last field of every form.
// Claude is the one resumable agent and the one whose transcript is read. The
// flags were checked against each CLI's own `--help`.
//
// A row here is not a claim that this host has the agent: availability is
// discovered (a binary on PATH, `enclave tools`), and an agent that is not
// installed is simply never offered. Nothing is seeded in the model list for an
// agent whose CLI reports its own models (opencode, pi): what the installation
// reports is better than any list shipped in a release, and wins over rows
// added here.
func Defaults() File {
	prompt := func(agent string, delivery agentspec.Kind, arg string) agentspec.Setting {
		return agentspec.Setting{
			ID: "agent-setting-" + agent + "-prompt", Agent: agent, Field: "prompt", Role: agentspec.RolePrompt,
			Type: agentspec.TypeString, Title: "Initial prompt", Description: "Optional task to start the session with.",
			Format: "textarea", Delivery: delivery, DeliveryArg: arg,
		}
	}
	model := func(agent, description string) agentspec.Setting {
		return agentspec.Setting{
			ID: "agent-setting-" + agent + "-model", Agent: agent, Field: "model", Role: agentspec.RoleModel,
			Type: agentspec.TypeString, Title: "Model", Description: description,
			Delivery: agentspec.Flag, DeliveryArg: "--model",
		}
	}
	title := func(agent string) agentspec.Setting {
		return agentspec.Setting{
			ID: "agent-setting-" + agent + "-title", Agent: agent, Field: "title", Role: agentspec.RoleTitle,
			Type: agentspec.TypeString, Title: "Session title", Description: "The session name, taken from the session rather than the form.",
			Delivery: agentspec.Flag, DeliveryArg: "--name",
		}
	}
	choice := func(agent, id, field, label, description string, choices []string, defaultValue string, delivery agentspec.Kind, arg, key string) agentspec.Setting {
		return agentspec.Setting{
			ID: "agent-setting-" + agent + "-" + id, Agent: agent, Field: field,
			Type: agentspec.TypeString, Title: label, Description: description,
			Choices: choices, Default: defaultValue, Delivery: delivery, DeliveryArg: arg, DeliveryKey: key,
		}
	}
	plain := func(id, binary, display string) agentspec.Agent {
		return agentspec.Agent{ID: id, Binary: binary, Display: display, Settings: []agentspec.Setting{}}
	}
	return File{
		SchemaVersion: schemaVersion,
		Agents: []agentspec.Agent{
			{
				ID: "claude", Binary: "claude", Display: "Claude Code",
				SessionIDFlag: "--session-id", ResumeFlag: "--resume", Transcript: true,
				Settings: []agentspec.Setting{
					model("claude", "Which Claude model the session runs. Leave unset for the CLI's own choice."),
					choice("claude", "effort", "effort", "Reasoning effort", "How much reasoning the model spends per turn.",
						[]string{"low", "medium", "high", "xhigh", "max"}, "", agentspec.Flag, "--effort", ""),
					choice("claude", "permission-mode", "permissionMode", "Permission mode", "How the session asks before acting. bypassPermissions skips all prompts.",
						[]string{"default", "plan", "acceptEdits", "dontAsk", "auto", "bypassPermissions"}, "default", agentspec.Flag, "--permission-mode", ""),
					title("claude"),
					prompt("claude", agentspec.Positional, ""),
				},
			},
			{
				ID: "codex", Binary: "codex", Display: "Codex",
				Settings: []agentspec.Setting{
					model("codex", "Which model Codex runs. Leave unset for the CLI's own choice."),
					// A config override rather than a flag, which is what Assign is for.
					choice("codex", "reasoning-effort", "reasoningEffort", "Reasoning effort", "How much reasoning the model spends per turn.",
						[]string{"low", "medium", "high", "xhigh"}, "", agentspec.Assign, "-c", "model_reasoning_effort"),
					choice("codex", "approval", "approval", "Approval policy", "When Codex asks before running a command.",
						[]string{"untrusted", "on-request", "never"}, "", agentspec.Flag, "-a", ""),
					choice("codex", "sandbox", "sandbox", "Sandbox", "How much of the machine Codex may touch. danger-full-access removes the sandbox.",
						[]string{"read-only", "workspace-write", "danger-full-access"}, "", agentspec.Flag, "-s", ""),
					prompt("codex", agentspec.Positional, ""),
				},
			},
			{
				ID: "gemini", Binary: "gemini", Display: "Gemini",
				Settings: []agentspec.Setting{
					model("gemini", "Which model Gemini runs. Leave unset for the CLI's own choice."),
					choice("gemini", "approval-mode", "approvalMode", "Approval mode", "When Gemini asks before acting. yolo auto-approves everything.",
						[]string{"default", "auto_edit", "yolo", "plan"}, "", agentspec.Flag, "--approval-mode", ""),
					// `-i <text>` executes, then stays interactive: the prompt is a flag here.
					prompt("gemini", agentspec.Flag, "-i"),
				},
			},
			{
				// The interactive TUI is the default command and `opencode run` the
				// non-interactive one, so there is no initial prompt to hand a session.
				ID: "opencode", Binary: "opencode", Display: "OpenCode", ListModels: []string{"models"},
				Settings: []agentspec.Setting{
					model("opencode", "Model as provider/model. The choices are the ones opencode itself reported; leave unset for its default."),
				},
			},
			{
				ID: "pi", Binary: "pi", Display: "Pi", ListModels: []string{"--list-models"},
				Settings: []agentspec.Setting{
					model("pi", "Model as provider/id, optionally :<thinking>. The choices are the ones pi itself reported; leave unset for its default."),
					choice("pi", "thinking", "thinking", "Thinking level", "How much reasoning the model spends per turn.",
						[]string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}, "", agentspec.Flag, "--thinking", ""),
					title("pi"),
					prompt("pi", agentspec.Positional, ""),
				},
			},
			{
				// No model flag at all: any config field can be overridden by a VIBE_*
				// variable, and the model is the active_model field.
				ID: "mistral-vibe", Binary: "vibe", Display: "Mistral Vibe",
				Settings: []agentspec.Setting{
					{
						ID: "agent-setting-mistral-vibe-model", Agent: "mistral-vibe", Field: "model", Role: agentspec.RoleModel,
						Type: agentspec.TypeString, Title: "Model",
						Description: "Alias of a model configured for vibe (its active_model). Its CLI cannot list them, so type one; leave unset for its default.",
						Delivery:    agentspec.Env, DeliveryArg: "VIBE_ACTIVE_MODEL",
					},
					{
						ID: "agent-setting-mistral-vibe-auto-approve", Agent: "mistral-vibe", Field: "autoApprove",
						Type: agentspec.TypeBoolean, Title: "Auto-approve tool calls",
						Description: "Allow every tool call without asking (vibe --auto-approve).",
						Default:     "false", Delivery: agentspec.BoolFlag, DeliveryArg: "--auto-approve",
					},
					prompt("mistral-vibe", agentspec.Positional, ""),
				},
			},
			plain("amp", "amp", "Amp"),
			plain("agy", "agy", "Antigravity"),
			plain("cline", "cline", "Cline"),
			plain("copilot", "copilot", "GitHub Copilot"),
			plain("cursor", "cursor-agent", "Cursor"),
			plain("devin", "devin", "Devin"),
			plain("droid", "droid", "Droid"),
			plain("grok", "grok", "Grok"),
			plain("kiro", "kiro-cli", "Kiro"),
			plain("letta", "letta", "Letta"),
			plain("maki", "maki", "Maki"),
			plain("muse", "muse", "Muse"),
			plain("qodercli", "qodercli", "Qoder CLI"),
			plain("qwen", "qwen", "Qwen Code"),
		},
		// Most capable first. Gemini's were never verified against a CLI: none is
		// installed and no Enclave profile ships one.
		Models: []Model{
			{ID: "agent-model-claude-opus-5", Agent: "claude", Model: "claude-opus-5"},
			{ID: "agent-model-claude-sonnet-5", Agent: "claude", Model: "claude-sonnet-5"},
			{ID: "agent-model-claude-haiku-4-5", Agent: "claude", Model: "claude-haiku-4-5"},
			{ID: "agent-model-claude-fable-5", Agent: "claude", Model: "claude-fable-5"},
			{ID: "agent-model-codex-gpt-5.6", Agent: "codex", Model: "gpt-5.6"},
			{ID: "agent-model-codex-gpt-5.5", Agent: "codex", Model: "gpt-5.5"},
			{ID: "agent-model-codex-gpt-5.4", Agent: "codex", Model: "gpt-5.4"},
			{ID: "agent-model-gemini-3-pro-preview", Agent: "gemini", Model: "gemini-3-pro-preview"},
			{ID: "agent-model-gemini-2.5-flash", Agent: "gemini", Model: "gemini-2.5-flash"},
		},
	}
}
