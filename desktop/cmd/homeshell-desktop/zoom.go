package main

import (
	"log"
	"runtime"

	webview "github.com/webview/webview_go"

	"github.com/eclipsesource/homeshell/desktop/internal/zoom"
)

// setUpZoom restores the saved zoom level and makes the browser zoom keys and
// gestures work in the window (docs/ARCHITECTURE.md, "Desktop shell").
//
// The webview keeps its zoom level across navigations, so it is applied once
// here. Failing to save a level only loses it at the next launch, so that is
// logged rather than surfaced.
func setUpZoom(window webview.WebView) {
	store, storeErr := zoom.DefaultStore()
	level := zoom.Default
	if storeErr == nil {
		level = store.Load()
	}
	setZoom(window.Window(), level)

	// Bindings are called on the main thread, where the native call must run.
	bindErr := window.Bind(zoom.BindingName, func(step int) {
		level = zoom.Next(level, step)
		setZoom(window.Window(), level)
		if storeErr != nil {
			return
		}
		if err := store.Save(level); err != nil {
			log.Printf("homeshell-desktop: could not save the zoom level: %v", err)
		}
	})
	if bindErr != nil {
		log.Printf("homeshell-desktop: zoom is unavailable: %v", bindErr)
		return
	}
	window.Init(zoom.Script(runtime.GOOS))
}
