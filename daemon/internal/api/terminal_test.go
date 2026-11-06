package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/notify"
	"github.com/eclipsesource/homeshell/daemon/internal/terminal"
)

func TestListTerminalEmulators(t *testing.T) {
	server := httptest.NewServer(handlerForTest(Deps{Notifications: notify.NewHub()}))
	defer server.Close()
	res, err := http.Get(server.URL + "/api/terminal-emulators")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var list terminal.EmulatorList
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Emulators) == 0 {
		t.Error("no emulators listed")
	}
	if want := terminal.Emulators().Automatic; list.Automatic != want {
		t.Errorf("automatic = %q, want %q", list.Automatic, want)
	}
}
