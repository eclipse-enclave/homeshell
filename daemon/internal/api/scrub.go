package api

import (
	"encoding/json"
	"net/url"
	"strings"
)

// secretKeyNeedles are normalized substrings whose matching JSON keys have their
// values redacted from logged bodies. Bare "key"/"auth" are excluded to avoid
// redacting benign fields such as "publicKey" or "author".
var secretKeyNeedles = []string{
	"password", "passwd", "secret", "token", "credential",
	"apikey", "accesskey", "privatekey", "authorization",
}

// redacted replaces a secret value in logged bodies.
const redacted = "[REDACTED]"

// truncatedPlaceholder stands in for a body that was cut short by the size cap
// and no longer parses as JSON, so redaction cannot run over it. Dropping the
// captured prefix avoids leaking a secret that fell before the cut.
const truncatedPlaceholder = "[REDACTED: truncated, could not scrub]"

// scrubBody redacts secret-looking fields from a JSON body for logging. Valid
// JSON is redacted and re-serialized (so key order is normalized), scrubbing the
// whole body before any truncation shows. A body that does not parse is returned
// unchanged when it was captured whole, or replaced with a placeholder when it
// was truncated by the size cap, since a truncated prefix may carry an
// unredacted secret.
func scrubBody(s string, truncated bool) string {
	if s == "" {
		return s
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		if truncated {
			return truncatedPlaceholder
		}
		return s
	}
	redactSecrets(v)
	out, err := json.Marshal(v)
	if err != nil {
		return s
	}
	return string(out)
}

// scrubQuery renders URL query parameters for logging with secret-looking
// values redacted by key, mirroring scrubBody's key-name matching.
func scrubQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	safe := make(url.Values, len(q))
	for k, vs := range q {
		if isSecretKey(k) {
			safe[k] = []string{redacted}
		} else {
			safe[k] = vs
		}
	}
	return safe.Encode()
}

// redactSecrets walks a decoded JSON value, replacing values under secret keys.
func redactSecrets(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if isSecretKey(k) {
				t[k] = redacted
			} else {
				redactSecrets(val)
			}
		}
	case []any:
		for _, item := range t {
			redactSecrets(item)
		}
	}
}

// isSecretKey reports whether a JSON key names a secret value. Matching is
// case-insensitive and ignores "_" and "-" separators.
func isSecretKey(k string) bool {
	norm := strings.ToLower(k)
	norm = strings.ReplaceAll(norm, "_", "")
	norm = strings.ReplaceAll(norm, "-", "")
	for _, needle := range secretKeyNeedles {
		if strings.Contains(norm, needle) {
			return true
		}
	}
	return false
}
