package zoom

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNext(t *testing.T) {
	tests := []struct {
		name    string
		current float64
		step    int
		want    float64
	}{
		{name: "zooms in by 10%", current: 1.0, step: 1, want: 1.1},
		{name: "zooms out by 10%", current: 1.0, step: -1, want: 0.9},
		{name: "resets to 100%", current: 2.3, step: 0, want: 1.0},
		{name: "stops at 300%", current: 3.0, step: 1, want: 3.0},
		{name: "stops at 50%", current: 0.5, step: -1, want: 0.5},
		{name: "snaps a drifted level back to the grid", current: 1.2000000001, step: 1, want: 1.3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Next(tt.current, tt.step); got != tt.want {
				t.Errorf("Next(%v, %d) = %v, want %v", tt.current, tt.step, got, tt.want)
			}
		})
	}
}

// Ten steps in from 100% must land exactly on 200%, not 1.9999999.
func TestNextDoesNotDrift(t *testing.T) {
	level := Default
	for range 10 {
		level = Next(level, 1)
	}
	if level != 2.0 {
		t.Errorf("after ten steps in: %v, want 2", level)
	}
}

// On macOS the user config directory's homeshell/ is the daemon's data
// directory, so the desktop app keeps its file in a directory of its own.
func TestDefaultStoreHasItsOwnDirectory(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store, err := DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore: %v", err)
	}
	if got := filepath.Base(filepath.Dir(store.Path)); got != "homeshell-desktop" {
		t.Errorf("DefaultStore().Path = %s, want it in a homeshell-desktop directory", store.Path)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "nested", "desktop.json")}
	if got := store.Load(); got != Default {
		t.Fatalf("Load with no file = %v, want %v", got, Default)
	}
	if err := store.Save(1.4); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := store.Load(); got != 1.4 {
		t.Errorf("Load after Save(1.4) = %v", got)
	}
}

func TestStoreLoadToleratesBadFiles(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    float64
	}{
		{name: "not JSON", content: "{", want: Default},
		{name: "no zoom key", content: `{}`, want: Default},
		{name: "negative", content: `{"zoom":-1}`, want: Default},
		{name: "too large is clamped", content: `{"zoom":9}`, want: 3.0},
		{name: "too small is clamped", content: `{"zoom":0.1}`, want: 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "desktop.json")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := (Store{Path: path}).Load(); got != tt.want {
				t.Errorf("Load(%s) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

// scriptHarness runs Script under Node with stub DOM globals, dispatches the
// events in EVENTS ([type, fields] pairs) and prints the steps passed to the
// binding, plus which events were swallowed.
const scriptHarness = `
const listeners = {};
const steps = [];
globalThis.addEventListener = (type, listener) => { listeners[type] = listener; };
globalThis.window = { ` + BindingName + `: (step) => steps.push(step) };
eval(process.env.SCRIPT);
const swallowed = [];
for (const [type, fields] of JSON.parse(process.env.EVENTS)) {
  let prevented = false;
  listeners[type]({ preventDefault() { prevented = true; }, stopImmediatePropagation() {}, ...fields });
  swallowed.push(prevented);
}
console.log(JSON.stringify({ steps, swallowed }));
`

func TestScript(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	tests := []struct {
		name          string
		goos          string
		events        string
		wantSteps     string
		wantSwallowed string
	}{
		{
			name:      "Ctrl keys zoom in, out and reset on Linux",
			goos:      "linux",
			events:    `[["keydown",{"key":"=","ctrlKey":true}],["keydown",{"key":"+","ctrlKey":true}],["keydown",{"key":"-","ctrlKey":true}],["keydown",{"key":"_","ctrlKey":true}],["keydown",{"key":"0","ctrlKey":true}]]`,
			wantSteps: `[1,1,-1,-1,0]`, wantSwallowed: `[true,true,true,true,true]`,
		},
		{
			name:      "other keys and modifiers reach the page",
			goos:      "linux",
			events:    `[["keydown",{"key":"-"}],["keydown",{"key":"a","ctrlKey":true}],["keydown",{"key":"-","ctrlKey":true,"altKey":true}],["keydown",{"key":"-","metaKey":true}]]`,
			wantSteps: `[]`, wantSwallowed: `[false,false,false,false]`,
		},
		{
			name:      "macOS zooms with Cmd and leaves Ctrl to the page",
			goos:      "darwin",
			events:    `[["keydown",{"key":"=","metaKey":true}],["keydown",{"key":"-","ctrlKey":true}]]`,
			wantSteps: `[1]`, wantSwallowed: `[true,false]`,
		},
		{
			name:      "Ctrl+wheel steps once per 40px notch",
			goos:      "linux",
			events:    `[["wheel",{"ctrlKey":true,"deltaY":-40,"deltaMode":0}],["wheel",{"ctrlKey":true,"deltaY":-40,"deltaMode":0}],["wheel",{"ctrlKey":true,"deltaY":40,"deltaMode":0}]]`,
			wantSteps: `[1,1,-1]`, wantSwallowed: `[true,true,true]`,
		},
		{
			name:      "small trackpad deltas add up and restart on a direction flip",
			goos:      "linux",
			events:    `[["wheel",{"ctrlKey":true,"deltaY":-30,"deltaMode":0}],["wheel",{"ctrlKey":true,"deltaY":20,"deltaMode":0}],["wheel",{"ctrlKey":true,"deltaY":20,"deltaMode":0}]]`,
			wantSteps: `[-1]`, wantSwallowed: `[true,true,true]`,
		},
		{
			name:      "a plain wheel scrolls",
			goos:      "linux",
			events:    `[["wheel",{"deltaY":-40,"deltaMode":0}]]`,
			wantSteps: `[]`, wantSwallowed: `[false]`,
		},
		{
			name:      "a pinch zooms while the fingers move, one step per factor of 1.1",
			goos:      "darwin",
			events:    `[["gesturestart",{"scale":1}],["gesturechange",{"scale":1.1}],["gesturechange",{"scale":1.3}],["gesturechange",{"scale":1.2}],["gestureend",{"scale":1.2}]]`,
			wantSteps: `[1,1,1,-1]`, wantSwallowed: `[true,true,true,true,true]`,
		},
		{
			name:          "pinching out and in by the same factor steps the same amount",
			goos:          "darwin",
			events:        `[["gesturestart",{"scale":1}],["gestureend",{"scale":3}],["gesturestart",{"scale":1}],["gestureend",{"scale":0.3333333333333333}]]`,
			wantSteps:     "[" + strings.Repeat("1,", 12) + strings.TrimSuffix(strings.Repeat("-1,", 12), ",") + "]",
			wantSwallowed: `[true,true,true,true]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(node, "-e", scriptHarness)
			cmd.Env = append(os.Environ(), "SCRIPT="+Script(tt.goos), "EVENTS="+tt.events)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("node: %v\n%s", err, out)
			}
			want := `{"steps":` + tt.wantSteps + `,"swallowed":` + tt.wantSwallowed + `}`
			if got := strings.TrimSpace(string(out)); got != want {
				t.Errorf("got  %s\nwant %s", got, want)
			}
		})
	}
}
