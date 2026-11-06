package cli

// The `settings` verb: read and change the daemon-persisted user settings —
// the same document the settings page edits, shared by every client of a daemon.
//
// The CLI holds **no copy of the schema**. `get` flattens whatever the daemon
// serves into dotted keys, and `set` reads the document, replaces one leaf and
// writes the whole thing back, letting the daemon validate it. So a setting
// added to the daemon is readable and writable here the day it lands, and an
// invalid value comes back in the daemon's own words rather than this CLI's.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

func init() {
	register(Command{
		Name:    "settings",
		Summary: "Read and change the daemon's user settings",
		Run:     runSettings,
	})
}

func runSettings(env Env, args []string) int {
	return dispatchGroup(env, "settings",
		"Read and change the settings the daemon persists for all its clients.",
		settingsSubcommands(), args)
}

func settingsSubcommands() []clientCommand {
	return []clientCommand{settingsGet(), settingsSet()}
}

const settingsPath = "/api/settings"

func settingsGet() clientCommand {
	return clientCommand{
		Verb:    "settings",
		Name:    "get",
		Args:    "[key]",
		Summary: "Print all settings as dotted keys, or the value of one key",
		Run: func(call clientCall) error {
			if len(call.Args) > 1 {
				return surplusArgsError(call.Args[1:])
			}
			payload, err := requestJSON(call.Ctx, call.Client, http.MethodGet, settingsPath, nil)
			if err != nil {
				return err
			}
			var document any
			if err := decodePayload(payload, &document); err != nil {
				return err
			}

			if len(call.Args) == 0 {
				if call.Options.asJSON {
					// The whole document, as the daemon sent it.
					writeRaw(call.Env.Stdout, payload)
					return nil
				}
				for _, entry := range flattenSettings(document) {
					fmt.Fprintf(call.Env.Stdout, "%s=%s\n", entry.key, entry.value)
				}
				return nil
			}

			key := call.Args[0]
			found, ok := valueAt(document, key)
			if !ok {
				return unknownSettingError(key, document)
			}
			if call.Options.asJSON {
				encoded, err := json.Marshal(found)
				if err != nil {
					return err
				}
				writeRaw(call.Env.Stdout, encoded)
				return nil
			}
			// Bare value, so `$(homeshell settings get sessions.previewLines)` is
			// directly usable in a script.
			fmt.Fprintln(call.Env.Stdout, scalarText(found))
			return nil
		},
	}
}

func settingsSet() clientCommand {
	return clientCommand{
		Verb:    "settings",
		Name:    "set",
		Args:    "<key> <value>",
		Summary: "Change one setting (the daemon validates and persists it)",
		Run: func(call clientCall) error {
			key, value, err := call.two("a settings key", "a value")
			if err != nil {
				return err
			}
			payload, err := requestJSON(call.Ctx, call.Client, http.MethodGet, settingsPath, nil)
			if err != nil {
				return err
			}
			var document any
			if err := decodePayload(payload, &document); err != nil {
				return err
			}
			current, ok := valueAt(document, key)
			if !ok {
				return unknownSettingError(key, document)
			}
			// The existing value's JSON type is what the typed string is read as, so
			// `confirmRemove true` becomes a boolean and `previewLines 8` a number
			// without this CLI knowing the schema. Whether the *value* is acceptable
			// is the daemon's judgement, on the write below.
			typed, err := coerceLike(current, value)
			if err != nil {
				return &usageError{message: fmt.Sprintf("%s: %v", key, err)}
			}
			if err := setValueAt(document, key, typed); err != nil {
				return err
			}
			// PUT replaces the whole document, which is why it is read first: this
			// changes one leaf of what the daemon currently holds rather than writing
			// a document assembled here.
			written, err := requestJSON(call.Ctx, call.Client, http.MethodPut, settingsPath, document)
			if err != nil {
				return err
			}
			if call.Options.asJSON {
				writeRaw(call.Env.Stdout, written)
				return nil
			}
			fmt.Fprintf(call.Env.Stdout, "%s=%s\n", key, scalarText(typed))
			return nil
		},
	}
}

// --- dotted-key access over the decoded document ---

type settingEntry struct{ key, value string }

// flattenSettings turns the nested document into sorted dotted key=value lines.
// A non-scalar leaf (an array, or an object with no children) is rendered as its
// JSON, so nothing in the document is silently unprintable.
func flattenSettings(document any) []settingEntry {
	var entries []settingEntry
	var walk func(prefix string, node any)
	walk = func(prefix string, node any) {
		object, ok := node.(map[string]any)
		if !ok || len(object) == 0 {
			entries = append(entries, settingEntry{key: prefix, value: scalarText(node)})
			return
		}
		names := make([]string, 0, len(object))
		for name := range object {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			child := name
			if prefix != "" {
				child = prefix + "." + name
			}
			walk(child, object[name])
		}
	}
	walk("", document)
	return entries
}

// valueAt resolves a dotted key against the document.
func valueAt(document any, key string) (any, bool) {
	node := document
	for _, segment := range strings.Split(key, ".") {
		object, ok := node.(map[string]any)
		if !ok {
			return nil, false
		}
		node, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	return node, true
}

// setValueAt replaces the leaf a dotted key names. The parent chain must already
// exist — this only ever edits a document the daemon just served, so a key it
// does not contain is a mistake in the invocation rather than a new field.
func setValueAt(document any, key string, value any) error {
	segments := strings.Split(key, ".")
	node := document
	for _, segment := range segments[:len(segments)-1] {
		object, ok := node.(map[string]any)
		if !ok {
			return &usageError{message: fmt.Sprintf("%s is not a settings group", segment)}
		}
		node = object[segment]
	}
	object, ok := node.(map[string]any)
	if !ok {
		return &usageError{message: fmt.Sprintf("%q does not name a setting", key)}
	}
	object[segments[len(segments)-1]] = value
	return nil
}

// coerceLike reads the typed string as the same JSON type the current value has.
// A string setting takes the text verbatim; anything else is parsed, so a typo
// (`previewLines eight`) is caught here as a malformed invocation rather than
// sent to the daemon as a string it would reject with a less direct message.
func coerceLike(current any, value string) (any, error) {
	switch current.(type) {
	case string:
		return value, nil
	case bool:
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("%q is not a boolean (true or false)", value)
		}
		return parsed, nil
	case float64:
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", value)
		}
		return parsed, nil
	case nil:
		// A null leaf carries no type to imitate; accept JSON so the value can be
		// given exactly, and fall back to the string.
		var decoded any
		if json.Unmarshal([]byte(value), &decoded) == nil {
			return decoded, nil
		}
		return value, nil
	default:
		// An object or array leaf can only be replaced wholesale, as JSON.
		var decoded any
		if err := json.Unmarshal([]byte(value), &decoded); err != nil {
			return nil, fmt.Errorf("this setting holds a JSON value; %q is not valid JSON", value)
		}
		return decoded, nil
	}
}

// scalarText renders a decoded JSON value for human output: numbers without a
// float tail, strings unquoted, anything structured as compact JSON.
func scalarText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(encoded)
	}
}

// unknownSettingError reports a key the document does not hold, listing the keys
// it does — the document is the authority on what exists, so the message is
// built from it rather than from a list kept here.
func unknownSettingError(key string, document any) error {
	entries := flattenSettings(document)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.key)
	}
	return &usageError{message: fmt.Sprintf(
		"no setting is named %q — known settings: %s", key, strings.Join(names, ", "))}
}
