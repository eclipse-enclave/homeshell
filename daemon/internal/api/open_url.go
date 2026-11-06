package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
)

func init() {
	register("POST /api/open-url", func(deps Deps) http.HandlerFunc {
		return openURLHandler(deps)
	})
}

type openURLRequest struct {
	URL string `json:"url"`
}

// openURLHandler hands a URL to the host's default browser.
//
// It exists because `window.open` is not universally available to the UI. The
// desktop shell's system webview blocks it outright (it has no second window to
// open, and webview_go exposes no hook to give it one), and even a real browser
// blocks it when the call comes after an `await` rather than straight out of a
// click — which is exactly the shape of "ask the daemon where the web IDE is,
// then open it". Both cases now fall back here, so the desktop shell and the
// browser behave the same and neither silently does nothing.
//
// Routing it through the daemon costs nothing in reach: the daemon already
// hands files to the desktop (internal/opener, the workstream note) and already
// launches terminals and editors. This is the same hand-off with a URL.
func openURLHandler(deps Deps) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if deps.Opener == nil {
			jsonError(writer, "opening URLs is unavailable", http.StatusServiceUnavailable)
			return
		}
		// Same property as the native folder chooser: the browser window opens on
		// the *daemon's* screen. On a loopback bind that is the caller's screen too;
		// on a public bind it is somebody else's, so a remote client whose
		// window.open was blocked would pop a window up on the server's desktop and
		// see nothing itself. Refuse, and let the client report that it could not
		// open the URL rather than acting at a distance.
		if deps.PublicBind {
			jsonError(writer, "opening a URL is only available to a client on the daemon's own host", http.StatusConflict)
			return
		}

		var body openURLRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			jsonError(writer, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if err := validateExternalURL(body.URL); err != nil {
			jsonError(writer, err.Error(), http.StatusBadRequest)
			return
		}

		if err := deps.Opener.Open(request.Context(), body.URL); err != nil {
			jsonError(writer, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(writer, http.StatusOK, struct {
			Opened bool `json:"opened"`
		}{Opened: true})
	}
}

// validateExternalURL accepts only the web URLs this endpoint exists to open.
//
// The scheme allow-list is the whole security control. The platform helpers
// behind internal/opener ("xdg-open", "open", "start") are general: given
// `file:///etc/passwd` they open a file manager, and given a scheme some
// installed application has registered they launch that application with the
// rest of the URL as its argument. An endpoint that forwards anything would
// therefore turn one authenticated request into arbitrary local program
// launches. http and https reach a browser and nothing else.
func validateExternalURL(raw string) error {
	if raw == "" {
		return errors.New("a url is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return errors.New("url is not a valid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("only http and https URLs can be opened")
	}
	if parsed.Host == "" {
		return errors.New("url must name a host")
	}
	return nil
}
