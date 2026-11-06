package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/notify"
	"github.com/eclipsesource/homeshell/daemon/internal/settings"
)

func settingsHandler(t *testing.T) http.Handler {
	t.Helper()
	store, err := settings.Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	return handlerForTest(Deps{Settings: store, Notifications: notify.NewHub()})
}

func TestGetSettingsReturnsDefaults(t *testing.T) {
	server := httptest.NewServer(settingsHandler(t))
	defer server.Close()

	res, err := http.Get(server.URL + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var body settings.Settings
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(body, settings.Defaults()) {
		t.Errorf("body = %+v, want defaults", body)
	}
}

// The stored document is echoed, so the client can address a new row at once.
func TestPutSettingsEchoesNormalizedOpenWithEntries(t *testing.T) {
	server := httptest.NewServer(settingsHandler(t))
	defer server.Close()

	next := settings.Defaults()
	next.OpenWith.Entries = append(next.OpenWith.Entries,
		settings.OpenWithEntry{Label: "VS Code", Command: "code ."})
	payload, _ := json.Marshal(next)
	req, _ := http.NewRequest(http.MethodPut, server.URL+"/api/settings", bytes.NewReader(payload))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var body settings.Settings
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	entries := body.OpenWith.Entries
	if len(entries) != 2 || entries[1].ID == "" {
		t.Errorf("echoed entries = %+v; want the new row carrying an assigned id", entries)
	}
}

func TestPutSettingsPersistsAndEchoes(t *testing.T) {
	server := httptest.NewServer(settingsHandler(t))
	defer server.Close()

	next := settings.Defaults()
	next.Workstreams.NameTemplate = "{project}-x"
	payload, _ := json.Marshal(next)

	req, _ := http.NewRequest(http.MethodPut, server.URL+"/api/settings", bytes.NewReader(payload))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}

	get, err := http.Get(server.URL + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	defer get.Body.Close()
	var body settings.Settings
	json.NewDecoder(get.Body).Decode(&body)
	if body.Workstreams.NameTemplate != "{project}-x" {
		t.Errorf("nameTemplate = %q, want {project}-x", body.Workstreams.NameTemplate)
	}
}

func TestPutSettingsRejectsInvalid(t *testing.T) {
	server := httptest.NewServer(settingsHandler(t))
	defer server.Close()

	bad := settings.Defaults()
	bad.Sessions.PreviewLines = 0
	payload, _ := json.Marshal(bad)
	req, _ := http.NewRequest(http.MethodPut, server.URL+"/api/settings", bytes.NewReader(payload))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.StatusCode)
	}
}

// TestPutSettingsNotificationEvents covers the per-event notification switches
// over the wire: a document that mutes the "finished" alert and turns off the
// "waiting" one round-trips exactly, so the daemon's Monitor reads back what the
// settings UI sent.
func TestPutSettingsNotificationEvents(t *testing.T) {
	server := httptest.NewServer(settingsHandler(t))
	defer server.Close()

	next := settings.Defaults()
	next.Notifications.Waiting = settings.NotificationEvent{Notify: false, Sound: false}
	next.Notifications.Finished = settings.NotificationEvent{Notify: true, Sound: false}
	next.Notifications.QuietSeconds = 45
	payload, _ := json.Marshal(next)

	req, _ := http.NewRequest(http.MethodPut, server.URL+"/api/settings", bytes.NewReader(payload))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}

	get, err := http.Get(server.URL + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	defer get.Body.Close()
	var body settings.Settings
	if err := json.NewDecoder(get.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Notifications != next.Notifications {
		t.Errorf("notifications = %+v, want %+v", body.Notifications, next.Notifications)
	}
}

// TestPutSettingsRejectsQuietSecondsOutOfRange keeps the ceiling a client-visible
// 400 rather than a value that would silence repeats for effectively ever.
func TestPutSettingsRejectsQuietSecondsOutOfRange(t *testing.T) {
	server := httptest.NewServer(settingsHandler(t))
	defer server.Close()

	for _, quiet := range []float64{-1, settings.MaxQuietSec + 1} {
		bad := settings.Defaults()
		bad.Notifications.QuietSeconds = quiet
		payload, _ := json.Marshal(bad)
		req, _ := http.NewRequest(http.MethodPut, server.URL+"/api/settings", bytes.NewReader(payload))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("quietSeconds=%v: status = %d, want 400", quiet, res.StatusCode)
		}
	}
}

func TestSettingsUnavailableWithoutStore(t *testing.T) {
	server := httptest.NewServer(handlerForTest(Deps{Notifications: notify.NewHub()}))
	defer server.Close()

	res, err := http.Get(server.URL + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", res.StatusCode)
	}
}

// The upgrade path through the *write* door, the mirror of
// settings.TestLoadKeepsNotificationDefaultsForAbsentKeys. A client written
// against an older schema — or a script sending only the fields it cares about —
// omits keys it has never heard of, and decoding that onto a zero value turns
// every one of them into a deliberate "off": the daemon would answer 200 and
// never notify again, with nothing invalid for Validate to catch, because a false
// that means "not mentioned" looks exactly like one the user chose.
func TestPutSettingsKeepsDefaultsForAbsentKeys(t *testing.T) {
	server := httptest.NewServer(settingsHandler(t))
	defer server.Close()

	// A notifications document as a pre-upgrade client would send it: the two keys
	// that existed then, and nothing else.
	body := `{"notifications":{"style":"native","sound":"default"}}`
	req, err := http.NewRequest(http.MethodPut, server.URL+"/api/settings", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}

	var stored settings.Settings
	if err := json.NewDecoder(res.Body).Decode(&stored); err != nil {
		t.Fatal(err)
	}
	// What the client did say is honoured.
	if stored.Notifications.Style != "native" || stored.Notifications.Sound != "default" {
		t.Errorf("style/sound = %q/%q, want native/default", stored.Notifications.Style, stored.Notifications.Sound)
	}
	// What it did not say keeps the default rather than becoming "off".
	if !stored.Notifications.Waiting.Notify || !stored.Notifications.Finished.Notify {
		t.Errorf("absent per-event keys = %+v/%+v, want the defaults kept",
			stored.Notifications.Waiting, stored.Notifications.Finished)
	}
	if stored.Notifications.QuietSeconds != settings.Defaults().Notifications.QuietSeconds {
		t.Errorf("quietSeconds = %v, want the default kept", stored.Notifications.QuietSeconds)
	}
	// And keys from entirely different sections are not collateral damage.
	if !stored.Projects.ConfirmRemove {
		t.Error("projects.confirmRemove was turned off by a body that never mentioned it")
	}
}

// The other half: a client that *does* say false means it, and the default must
// not override an explicit choice.
func TestPutSettingsHonoursAnExplicitFalse(t *testing.T) {
	server := httptest.NewServer(settingsHandler(t))
	defer server.Close()

	body := `{"notifications":{"style":"native","sound":"default","waiting":{"notify":false,"sound":false}}}`
	req, err := http.NewRequest(http.MethodPut, server.URL+"/api/settings", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	var stored settings.Settings
	if err := json.NewDecoder(res.Body).Decode(&stored); err != nil {
		t.Fatal(err)
	}
	if stored.Notifications.Waiting.Notify {
		t.Error("waiting.notify = true, want the explicit false honoured")
	}
	if !stored.Notifications.Finished.Notify {
		t.Error("finished.notify = false, want the untouched event left at its default")
	}
}

// A new "open with" entry sent without an id is added next to the terminal row.
// Decoding onto the defaulted document used to merge it into that row instead,
// replacing the terminal entry's command while keeping its id and flags.
func TestPutSettingsAddsOpenWithEntryWithoutID(t *testing.T) {
	server := httptest.NewServer(settingsHandler(t))
	defer server.Close()

	body := `{"openWith":{"entries":[{"label":"VS Code","command":"code {path}"}]}}`
	req, err := http.NewRequest(http.MethodPut, server.URL+"/api/settings", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}

	var stored settings.Settings
	if err := json.NewDecoder(res.Body).Decode(&stored); err != nil {
		t.Fatal(err)
	}
	terminal := settings.Defaults().OpenWith.Entries[0]
	var sawTerminal, sawAdded bool
	for _, entry := range stored.OpenWith.Entries {
		switch {
		case entry.ID == terminal.ID:
			sawTerminal = entry == terminal
		case entry.Label == "VS Code" && entry.Command == "code {path}":
			sawAdded = entry.ID != "" && !entry.InTerminal && !entry.Fixed
		}
	}
	if !sawTerminal || !sawAdded {
		t.Errorf("entries = %+v, want the untouched terminal row plus a new VS Code entry", stored.OpenWith.Entries)
	}
}
