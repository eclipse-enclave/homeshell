// Package zoom holds the desktop window's page zoom: the levels the zoom keys
// and gestures step through, the script that catches them, and the level kept
// between launches. Applying a level is a native webview call and lives in
// package main, which needs CGO.
//
// Why native zoom rather than CSS zoom: it scales the page exactly like a
// browser's zoom, so pointer coordinates in canvas-drawn views stay correct.
package zoom

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
)

// Default is the level of a window that was never zoomed.
const Default = 1.0

const (
	minPercent  = 50
	maxPercent  = 300
	stepPercent = 10
)

// Next returns the level one step away from current: step > 0 zooms in,
// step < 0 zooms out and 0 resets to Default.
//
// Levels are whole percents, so repeated steps never drift off the 10% grid.
func Next(current float64, step int) float64 {
	if step == 0 {
		return Default
	}
	percent := int(math.Round(current * 100))
	if step > 0 {
		percent += stepPercent
	} else {
		percent -= stepPercent
	}
	return clamp(float64(percent) / 100)
}

func clamp(level float64) float64 {
	return math.Min(math.Max(level, minPercent/100.0), maxPercent/100.0)
}

// BindingName is the global function Script calls with a step.
const BindingName = "homeshellZoom"

// Script turns the browser zoom keys and gestures into calls to BindingName,
// since a bare system webview has no zoom shortcuts of its own.
//
//   - The platform's zoom modifier (Cmd on macOS, Ctrl elsewhere) with = or +
//     zooms in, with - or _ zooms out, with 0 resets. Only that modifier, so
//     Ctrl+- still reaches a terminal on macOS, as in Safari.
//   - Ctrl + wheel zooms one step per notch (40px in WebKitGTK); smaller
//     trackpad deltas add up to that first, restarting when direction flips.
//   - Pinch (WebKit gesture events, macOS) zooms while the fingers move, one
//     step per factor of 1.1 in scale. Why a factor: scale is multiplicative,
//     so pinching out to 3x and in to 1/3x step the same amount.
//
// Listeners capture on window, so they run before the page's own key handlers.
func Script(goos string) string {
	modifier := "ctrlKey"
	if goos == "darwin" {
		modifier = "metaKey"
	}
	return `(() => {
  const zoom = (step) => window.` + BindingName + `(step);
  const keySteps = { '=': 1, '+': 1, '-': -1, '_': -1, '0': 0 };
  addEventListener('keydown', (event) => {
    if (!event.` + modifier + ` || event.altKey) return;
    const step = keySteps[event.key];
    if (step === undefined) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    zoom(step);
  }, true);
  let wheelDelta = 0;
  addEventListener('wheel', (event) => {
    if (!event.ctrlKey) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    const delta = event.deltaMode === 0 ? event.deltaY : event.deltaY * 40;
    if (Math.sign(delta) !== Math.sign(wheelDelta)) wheelDelta = 0;
    wheelDelta += delta;
    if (Math.abs(wheelDelta) < 40) return;
    zoom(wheelDelta < 0 ? 1 : -1);
    wheelDelta = 0;
  }, { capture: true, passive: false });
  let pinchSteps = 0;
  const pinch = (event) => {
    event.preventDefault();
    const target = Math.round(Math.log(event.scale) / Math.log(1.1));
    for (; pinchSteps < target; pinchSteps++) zoom(1);
    for (; pinchSteps > target; pinchSteps--) zoom(-1);
  };
  addEventListener('gesturestart', (event) => {
    event.preventDefault();
    pinchSteps = 0;
  }, true);
  addEventListener('gesturechange', pinch, true);
  addEventListener('gestureend', pinch, true);
})();`
}

// Store keeps the zoom level in a JSON file between launches.
type Store struct {
	Path string
}

// DefaultStore keeps the level in a homeshell-desktop directory under the
// user's config directory. Why not the daemon's settings: zoom belongs to this
// window, not to every client. Why not homeshell/: on macOS that is the
// daemon's data directory.
func DefaultStore() (Store, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return Store{}, err
	}
	return Store{Path: filepath.Join(configDir, "homeshell-desktop", "desktop.json")}, nil
}

type state struct {
	Zoom float64 `json:"zoom"`
}

// Load returns the saved level, or Default when none is saved. An unreadable
// or out-of-range file must not keep the window from opening, so it is not an
// error: the level falls back to Default or is clamped.
func (s Store) Load() float64 {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return Default
	}
	var saved state
	if json.Unmarshal(data, &saved) != nil || saved.Zoom <= 0 {
		return Default
	}
	return clamp(saved.Zoom)
}

// Save writes level, creating the directory when needed.
func (s Store) Save(level float64) error {
	data, err := json.Marshal(state{Zoom: level})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.Path, data, 0o644)
}
