package cli

import (
	"net/http"
	"strings"
	"testing"
)

const testProvidersJSON = `[` +
	`{"id":"local","label":"Local System","status":{"state":"available"},` +
	`"capabilities":{"maxConcurrentSessions":0,"oneOffPrompt":true,"sessionStartStop":true,"startStop":false}},` +
	`{"id":"enclave","label":"Eclipse Enclave","status":{"state":"unavailable","reason":"enclave not on PATH"},` +
	`"capabilities":{"oneOffPrompt":false}}]`

func TestProviderList(t *testing.T) {
	t.Run("prints each provider's availability and capabilities", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/providers": {http.StatusOK, testProvidersJSON},
		})

		code, stdout, stderr := runClient(providerList())
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		for _, want := range []string{"ID", "LABEL", "STATE", "REASON", "CAPABILITIES",
			"local", "Local System", "available", "oneOffPrompt", "sessionStartStop",
			"enclave", "unavailable", "enclave not on PATH"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("stdout = %q, want it to contain %q", stdout, want)
			}
		}
	})

	t.Run("a capability the provider does not declare is left out", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/providers": {http.StatusOK, testProvidersJSON},
		})

		_, stdout, _ := runClient(providerList())
		// startStop is false on local and oneOffPrompt false on enclave: a column of
		// negatives answers "what can this do" worse than a short list.
		if strings.Contains(stdout, "startStop=false") || strings.Contains(stdout, "oneOffPrompt=false") {
			t.Errorf("stdout = %q, want undeclared capabilities omitted rather than printed false", stdout)
		}
		for _, line := range strings.Split(stdout, "\n") {
			if strings.HasPrefix(line, "enclave") && strings.Contains(line, "oneOffPrompt") {
				t.Errorf("enclave's row = %q, want no oneOffPrompt (it declares false)", line)
			}
		}
	})

	t.Run("--json passes the payload through untouched", func(t *testing.T) {
		newStubDaemon(t, map[string]stubResponse{
			"GET /api/providers": {http.StatusOK, testProvidersJSON},
		})

		code, stdout, stderr := runClient(providerList(), "--json")
		if code != exitOK {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		if strings.TrimSpace(stdout) != testProvidersJSON {
			t.Errorf("stdout = %q, want the exact payload the daemon sent", stdout)
		}
	})
}

func TestEnabledCapabilities(t *testing.T) {
	tests := []struct {
		name string
		caps string
		want []string
	}{
		{"a true flag is named", `{"oneOffPrompt":true}`, []string{"oneOffPrompt"}},
		{"a false flag is omitted", `{"oneOffPrompt":false}`, nil},
		{"a null is omitted", `{"oneOffPrompt":null}`, nil},
		{"a number is printed as key=value", `{"maxConcurrentSessions":3}`, []string{"maxConcurrentSessions=3"}},
		{"a string is unquoted", `{"kind":"session"}`, []string{"kind=session"}},
		{"names are sorted for stable output", `{"b":true,"a":true}`, []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var decoded providerDescriptor
			if err := decodePayload([]byte(`{"capabilities":`+tt.caps+`}`), &decoded); err != nil {
				t.Fatal(err)
			}
			got := enabledCapabilities(decoded.Capabilities)
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("enabledCapabilities(%s) = %v, want %v", tt.caps, got, tt.want)
			}
		})
	}
}
