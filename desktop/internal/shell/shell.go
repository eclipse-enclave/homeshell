// Package shell holds the desktop window's user-facing details — its title, the
// pages it shows before and instead of the app, and what to say when the system
// webview cannot be created. They live here rather than in package main so they
// are unit-testable: main imports webview_go and therefore needs CGO and a
// system webview present just to build.
package shell

import (
	"fmt"
	"html"
	"runtime"
	"strings"
)

// WindowTitle names the window, including the build version when one was
// stamped in via -ldflags.
func WindowTitle(version string) string {
	if version == "" {
		return "HomeShell"
	}
	return "HomeShell " + version
}

// WebviewFailure turns a recovered webview.New panic into an error.
//
// webview.New panics rather than returning an error when the system webview
// cannot be created, which is the expected failure on a machine missing
// WebKitGTK or the WebView2 runtime. Reporting it as an error keeps that case on
// the same clean one-line path as a failed daemon launch instead of printing a
// Go stack trace, and names the runtime to install.
func WebviewFailure(recovered any) error {
	return fmt.Errorf("could not open a system webview (%v); %s", recovered, RuntimeHint())
}

// RuntimeHint names the system webview this platform needs.
func RuntimeHint() string {
	return runtimeHintFor(runtime.GOOS)
}

// StartingPage is shown while the daemon is being found or started, so a slow
// spawn looks like progress rather than a hung empty window.
func StartingPage() string {
	return page("Starting HomeShell", "<p class=\"muted\">Looking for the HomeShell daemon…</p>")
}

// FailurePage renders a startup failure into the window.
//
// This is the only place most users will ever see it: a .desktop entry and a
// double-clicked .app have no stderr, so printing the reason is the same as
// discarding it. The daemon's own message is preformatted because it carries the
// child's output verbatim, newlines and all.
func FailurePage(err error) string {
	body := "<p>HomeShell could not start its daemon, so there is nothing to show.</p>" +
		"<pre>" + html.EscapeString(err.Error()) + "</pre>" +
		"<p class=\"muted\">Fix the problem above and start HomeShell again.</p>"
	return page("HomeShell — could not start", body)
}

// page wraps body in a minimal self-contained document. No external assets: this
// renders when the daemon is unreachable, so nothing may be fetched from it.
func page(title, body string) string {
	return strings.Join([]string{
		"<!doctype html><html><head><meta charset=\"utf-8\">",
		"<title>" + html.EscapeString(title) + "</title><style>",
		"body{font:14px/1.5 system-ui,sans-serif;margin:0;padding:2.5rem;",
		"background:#1b1d21;color:#e6e6e6}",
		"h1{font-size:1.15rem;margin:0 0 1rem}",
		".muted{color:#9aa0a6}",
		"pre{white-space:pre-wrap;word-break:break-word;background:#111316;",
		"border:1px solid #2b2f36;border-radius:6px;padding:1rem;color:#f2b8b5}",
		"</style></head><body>",
		"<h1>" + html.EscapeString(title) + "</h1>",
		body,
		"</body></html>",
	}, "")
}

// runtimeHintFor takes the OS as an argument so every platform's message is
// verifiable from a single test run.
func runtimeHintFor(goos string) string {
	switch goos {
	case "linux":
		return "install the WebKitGTK runtime (libwebkit2gtk-4.1-0), " +
			"or install HomeShell from the .deb/.rpm, which pulls it in"
	case "darwin":
		return "WKWebView ships with macOS, so this points at a broken system install"
	default:
		return "this platform has no supported system webview"
	}
}
