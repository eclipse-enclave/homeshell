package notify

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strings"
)

// DesktopNotifier is the native (daemon-shown) delivery channel: it raises a
// platform notification with sound via the OS's notification tooling, the same
// os/exec hand-off strategy internal/terminal uses for opening terminals.
//
// It is best-effort by design: if the platform tool is missing or errors,
// the failure is logged and the daemon carries on. Delivery is asynchronous so
// a multi-hundred-millisecond spawn never stalls the Monitor's event loop.
type DesktopNotifier struct{}

// NewDesktopNotifier returns the production native notifier.
func NewDesktopNotifier() *DesktopNotifier { return &DesktopNotifier{} }

// Deliver shows the notification natively. It returns immediately, running the
// platform command in the background.
func (d *DesktopNotifier) Deliver(ctx context.Context, notification Notification) {
	go func() {
		if err := show(ctx, notification); err != nil {
			slog.Warn("native notification failed", "error", err, "kind", notification.Kind)
		}
	}()
}

// show dispatches to the platform-specific command.
func show(ctx context.Context, notification Notification) error {
	switch runtime.GOOS {
	case "darwin":
		return runCommand(ctx, darwinArgs(notification))
	default:
		return showLinux(ctx, notification)
	}
}

func runCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("empty command")
	}
	return exec.CommandContext(ctx, args[0], args[1:]...).Run()
}

// showLinux posts the notification with notify-send, then best-effort plays a
// sound with a freedesktop sound player if one is installed.
//
// The notification server plays a sound of its own unless hinted not to, so it
// is left on only for an audible notification with no player installed. A
// player that is installed but fails leaves the notification silent.
func showLinux(ctx context.Context, notification Notification) error {
	var playerPath string
	player := linuxSoundCommand(notification.Sound)
	if player != nil {
		if path, err := exec.LookPath(player[0]); err == nil {
			playerPath = path
		}
	}
	if err := postLinux(ctx, notification, silentSound(notification.Sound) || playerPath != ""); err != nil {
		return err
	}
	if playerPath != "" {
		_ = exec.CommandContext(ctx, playerPath, player[1:]...).Run()
	}
	return nil
}

// --- pure command builders (GOOS-independent, unit-tested on any platform) ---

// darwinArgs builds the osascript invocation that shows a macOS notification.
func darwinArgs(notification Notification) []string {
	return []string{"osascript", "-e", darwinScript(notification)}
}

// darwinScript builds the AppleScript `display notification` statement. Each
// user string is a double-quoted AppleScript literal, so backslashes are
// escaped first (before the quotes they would otherwise double-escape), then
// double quotes — matching internal/terminal.darwinScript.
func darwinScript(notification Notification) string {
	script := fmt.Sprintf("display notification %s with title %s", appleString(notification.Body), appleString(notification.Title))
	if sound := darwinSound(notification.Sound); sound != "" {
		script += " sound name " + appleString(sound)
	}
	return script
}

func appleString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

// darwinSound maps a notification sound name to a built-in macOS system sound.
// Unknown names fall back to the default so a bad name never silences the
// notification.
func darwinSound(name string) string {
	if silentSound(name) {
		return ""
	}
	switch strings.ToLower(name) {
	case "", "default", "ping":
		return "Ping"
	case "chime", "glass":
		return "Glass"
	case "hero":
		return "Hero"
	case "submarine":
		return "Submarine"
	case "pop":
		return "Pop"
	default:
		return "Ping"
	}
}

// silentSound reports whether a sound name means "no sound".
func silentSound(name string) bool {
	switch strings.ToLower(name) {
	case SilentSound, "silent":
		return true
	default:
		return false
	}
}

// postLinux runs notify-send, with the freedesktop suppress-sound hint if
// suppressServerSound. Older notify-send builds reject boolean hints, so a
// failure retries with the hint typed int.
func postLinux(ctx context.Context, notification Notification, suppressServerSound bool) error {
	if !suppressServerSound {
		return runCommand(ctx, linuxArgs(notification, ""))
	}
	if err := runCommand(ctx, linuxArgs(notification, "boolean:suppress-sound:true")); err == nil {
		return nil
	}
	return runCommand(ctx, linuxArgs(notification, "int:suppress-sound:1"))
}

// linuxArgs builds the notify-send invocation, with the given hint unless it is
// empty. Arguments are passed directly to exec (no shell); the "--" keeps a title
// or body that starts with "-" (agent titles, project names) from parsing as an option.
func linuxArgs(notification Notification, hint string) []string {
	args := []string{"notify-send", "--app-name=HomeShell"}
	if hint != "" {
		args = append(args, "--hint="+hint)
	}
	return append(args, "--", notification.Title, notification.Body)
}

// linuxSoundCommand returns a best-effort sound-player command for the given
// sound name, or nil to play nothing. It uses freedesktop sound names via
// canberra-gtk-play, which is present on most desktops; a missing binary is
// handled by the caller.
func linuxSoundCommand(name string) []string {
	if event := freedesktopSound(name); event != "" {
		return []string{"canberra-gtk-play", "-i", event}
	}
	return nil
}

func freedesktopSound(name string) string {
	if silentSound(name) {
		return ""
	}
	switch strings.ToLower(name) {
	case "", "default", "ping", "chime", "glass":
		return "message-new-instant"
	case "bell":
		return "bell"
	case "complete":
		return "complete"
	default:
		return "message-new-instant"
	}
}
