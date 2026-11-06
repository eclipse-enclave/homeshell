// Command homeshell-desktop is the native desktop shell for HomeShell. It is a
// thin window: it asks the homeshell CLI to ensure a daemon is running, then
// opens a system webview on the daemon's loopback URL. The daemon serves the UI
// and sets the HttpOnly auth cookie on that document, so the webview
// authenticates exactly like a browser (docs/ARCHITECTURE.md, "Desktop shell"). No UI
// assets are bundled here — doing so would break the daemon's loopback/cookie
// auth.
package main

import (
	"log"

	webview "github.com/webview/webview_go"

	"github.com/eclipsesource/homeshell/desktop/internal/launch"
	"github.com/eclipsesource/homeshell/desktop/internal/shell"
)

// version is set at build time via -ldflags "-X main.version=...".
var version string

func main() {
	log.SetFlags(0)
	if err := run(); err != nil {
		// Also on stderr, for whoever started this from a terminal. A packaged
		// launch has none, which is why run() renders failures into the window.
		log.Fatalf("homeshell-desktop: %v", err)
	}
}

// run opens the window first and resolves the daemon URL second.
//
// The order is what makes a failure visible. Every packaged launch path (a
// .desktop entry, a double-clicked .app) has no stderr, so reporting a failed
// daemon launch by printing to it means the user double-clicks and nothing
// whatsoever happens.
// With a window already up, the same message can be rendered into it.
func run() (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			// webview.New panics when the system webview is missing. There is no
			// window to draw into in that case, so this one can only go to stderr.
			err = shell.WebviewFailure(recovered)
		}
	}()

	window := webview.New(false)
	window.SetTitle(shell.WindowTitle(version))
	window.SetSize(1280, 800, webview.HintNone)
	window.SetHtml(shell.StartingPage())

	// Resolve off the main thread. SetHtml only queues a load — nothing renders
	// until Run() below pumps the event loop — so resolving here would leave the
	// window showing an unpainted frame for the duration.
	//
	// launchErr and closed are touched only on the main thread — inside Dispatch,
	// and after Run returns — so neither needs synchronisation of its own.
	var launchErr error
	closed := false
	resolved := make(chan struct{})
	go func() {
		defer close(resolved)
		url, resolveErr := launch.ResolveURL(launch.ExecRunner{})
		window.Dispatch(func() {
			// Waiting on `resolved` below only guarantees this callback is
			// *queued* before Destroy, not that it will not run: the engine
			// destructor nulls its webview and then depletes the event queue, so
			// a callback queued while the user was closing the window executes
			// against a destroyed webview (GLib criticals on GTK). Nothing to
			// navigate to by then.
			if closed {
				return
			}
			if resolveErr != nil {
				launchErr = resolveErr
				window.SetHtml(shell.FailurePage(resolveErr))
				return
			}
			window.Navigate(url)
		})
	}()

	window.Run()
	closed = true

	// Still wait for the goroutine: Dispatch itself must not be called on a
	// destroyed webview. Closing the window mid-resolve makes this wait for
	// ensure-running's own timeout, which is bounded.
	<-resolved
	window.Destroy()
	return launchErr
}
