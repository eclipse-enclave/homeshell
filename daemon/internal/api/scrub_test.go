package api

import (
	"net/url"
	"strings"
	"testing"
)

func TestScrubBody(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		truncated bool
		want      string
	}{
		{"empty", "", false, ""},
		{"non-json passthrough", "not json at all", false, "not json at all"},
		{"complete non-json kept", `{"token":"abc"} trailing`, false, `{"token":"abc"} trailing`},
		{"truncated non-json placeholdered", `{"token":"abc`, true, truncatedPlaceholder},
		{"truncated but still parseable is scrubbed", `{"token":"abc"}`, true, `{"token":"[REDACTED]"}`},
		{"no secrets", `{"branch":"main"}`, false, `{"branch":"main"}`},
		{"redacts token", `{"token":"ghp_secret"}`, false, `{"token":"[REDACTED]"}`},
		{"redacts password", `{"password":"hunter2"}`, false, `{"password":"[REDACTED]"}`},
		{"case and separators", `{"API_KEY":"x"}`, false, `{"API_KEY":"[REDACTED]"}`},
		{"nested object", `{"config":{"secret":"x","name":"y"}}`, false, `{"config":{"name":"y","secret":"[REDACTED]"}}`},
		{"array of objects", `[{"accessKey":"x"},{"branch":"main"}]`, false, `[{"accessKey":"[REDACTED]"},{"branch":"main"}]`},
		{"public key not redacted", `{"publicKey":"ssh-rsa AAA"}`, false, `{"publicKey":"ssh-rsa AAA"}`},
		{"private key redacted", `{"privateKey":"x"}`, false, `{"privateKey":"[REDACTED]"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scrubBody(tt.in, tt.truncated)
			if got != tt.want {
				t.Errorf("scrubBody(%q, %v) = %q, want %q", tt.in, tt.truncated, got, tt.want)
			}
		})
	}
}

func TestScrubQuery(t *testing.T) {
	tests := []struct {
		name string
		in   url.Values
		want string
	}{
		{"empty", url.Values{}, ""},
		{"non-secret preserved", url.Values{"page": {"2"}}, "page=2"},
		{"secret redacted", url.Values{"token": {"ghp_secret"}}, "token=%5BREDACTED%5D"},
		{"mixed", url.Values{"apiKey": {"x"}, "name": {"main"}}, "apiKey=%5BREDACTED%5D&name=main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scrubQuery(tt.in)
			if got != tt.want {
				t.Errorf("scrubQuery(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestScrubQueryRedactsAPIToken documents the logging backstop for the API
// token: the token auth design keeps the secret out of query strings entirely
// (Bearer header or hs_api_token cookie), but if a "token" query key ever
// appears it is redacted from the access log rather than written in the clear.
func TestScrubQueryRedactsAPIToken(t *testing.T) {
	got := scrubQuery(url.Values{"token": {"deadbeef-api-token"}})
	if strings.Contains(got, "deadbeef-api-token") {
		t.Fatalf("scrubQuery leaked the token: %q", got)
	}
	if want := "token=%5BREDACTED%5D"; got != want {
		t.Errorf("scrubQuery = %q, want %q", got, want)
	}
}

func TestIsSecretKey(t *testing.T) {
	secret := []string{"token", "Token", "password", "apiKey", "api_key", "api-key", "clientSecret", "privateKey", "authorization", "accessKey"}
	notSecret := []string{"branch", "name", "path", "publicKey", "id", "providerId"}
	for _, k := range secret {
		if !isSecretKey(k) {
			t.Errorf("isSecretKey(%q) = false, want true", k)
		}
	}
	for _, k := range notSecret {
		if isSecretKey(k) {
			t.Errorf("isSecretKey(%q) = true, want false", k)
		}
	}
}
