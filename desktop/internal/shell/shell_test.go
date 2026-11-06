package shell

import (
	"errors"
	"strings"
	"testing"
)

func TestWindowTitle(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{name: "plain when unstamped", version: "", want: "HomeShell"},
		{name: "includes a stamped version", version: "1.2.3", want: "HomeShell 1.2.3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WindowTitle(tt.version); got != tt.want {
				t.Errorf("WindowTitle(%q) = %q, want %q", tt.version, got, tt.want)
			}
		})
	}
}

// A missing system webview used to reach the user as a Go stack trace, because
// webview.New panics instead of returning an error.
func TestWebviewFailureReportsThePanicAndWhatToInstall(t *testing.T) {
	err := WebviewFailure(errors.New("failed to create webview"))
	if err == nil {
		t.Fatal("WebviewFailure returned nil; a failed webview must be an error")
	}
	for _, want := range []string{"could not open a system webview", "failed to create webview", RuntimeHint()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
}

func TestWebviewFailureHandlesANonErrorPanic(t *testing.T) {
	// A cgo panic value is not necessarily an error, so the message must not
	// depend on it being one.
	if err := WebviewFailure("boom"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("WebviewFailure(\"boom\") = %v, want it to quote the panic value", err)
	}
}

// A packaged launch has no stderr, so the window is the only place a startup
// failure can be reported. Printing it instead is the same as discarding it.
func TestFailurePageShowsTheReason(t *testing.T) {
	err := errors.New("daemon exited before it was ready: " +
		"listen tcp 127.0.0.1:8420: bind: address already in use")
	pageHTML := FailurePage(err)

	for _, want := range []string{"address already in use", "127.0.0.1:8420", "<html", "</html>"} {
		if !strings.Contains(pageHTML, want) {
			t.Errorf("page missing %q:\n%s", want, pageHTML)
		}
	}
}

// The message embeds the daemon's own output — arbitrary text including local
// paths — so it must not be able to close a tag or inject markup.
func TestFailurePageEscapesTheMessage(t *testing.T) {
	pageHTML := FailurePage(errors.New("bad <script>alert(1)</script> & \"quoted\""))

	if strings.Contains(pageHTML, "<script>") {
		t.Errorf("page did not escape the error text:\n%s", pageHTML)
	}
	for _, want := range []string{"&lt;script&gt;", "&amp;"} {
		if !strings.Contains(pageHTML, want) {
			t.Errorf("page missing escaped %q:\n%s", want, pageHTML)
		}
	}
}

func TestStartingPageIsSelfContained(t *testing.T) {
	pageHTML := StartingPage()

	if !strings.Contains(pageHTML, "Starting HomeShell") {
		t.Errorf("page does not say what it is doing:\n%s", pageHTML)
	}
	// It renders when the daemon may be unreachable, so it cannot fetch anything.
	for _, forbidden := range []string{"<script", "src=", "href="} {
		if strings.Contains(pageHTML, forbidden) {
			t.Errorf("page references an external asset (%q):\n%s", forbidden, pageHTML)
		}
	}
}

func TestRuntimeHintNamesEachPlatformsWebview(t *testing.T) {
	tests := []struct {
		goos string
		want string // the runtime a user has to install, named in the message
	}{
		{goos: "linux", want: "libwebkit2gtk-4.1-0"},
		{goos: "darwin", want: "WKWebView"},
		{goos: "plan9", want: "no supported system webview"},
	}
	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			if got := runtimeHintFor(tt.goos); !strings.Contains(got, tt.want) {
				t.Errorf("runtimeHintFor(%q) = %q, want it to name %q", tt.goos, got, tt.want)
			}
		})
	}
}
