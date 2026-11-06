package notify

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDarwinScript(t *testing.T) {
	tests := []struct {
		name string
		n    Notification
		want string
	}{
		{
			name: "title body and default sound",
			n:    Notification{Title: "Implement is waiting for input", Body: "alpha · feature/x", Sound: "default"},
			want: `display notification "alpha · feature/x" with title "Implement is waiting for input" sound name "Ping"`,
		},
		{
			name: "chime maps to Glass",
			n:    Notification{Title: "t", Body: "b", Sound: "chime"},
			want: `display notification "b" with title "t" sound name "Glass"`,
		},
		{
			name: "silent omits the sound clause",
			n:    Notification{Title: "t", Body: "b", Sound: "none"},
			want: `display notification "b" with title "t"`,
		},
		{
			name: "double quotes are escaped",
			n:    Notification{Title: `say "hi"`, Body: "b", Sound: "none"},
			want: `display notification "b" with title "say \"hi\""`,
		},
		{
			name: "backslashes are escaped before quotes",
			n:    Notification{Title: `a\b`, Body: "b", Sound: "none"},
			want: `display notification "b" with title "a\\b"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := darwinScript(tt.n)
			if got != tt.want {
				t.Errorf("darwinScript =\n  %q\nwant\n  %q", got, tt.want)
			}
		})
	}
}

func TestDarwinArgs(t *testing.T) {
	args := darwinArgs(Notification{Title: "t", Body: "b"})
	if len(args) != 3 || args[0] != "osascript" || args[1] != "-e" {
		t.Fatalf("unexpected args: %v", args)
	}
}

// Title and body come after "--": both carry agent- and user-chosen text that
// notify-send would otherwise parse as options.
func TestLinuxArgs(t *testing.T) {
	notification := Notification{Title: "Waiting", Body: "alpha · feature/x"}
	optionShaped := Notification{Title: "--app-name=Evil waiting", Body: "-? · feature/x"}
	tests := []struct {
		notification Notification
		hint         string
		want         []string
	}{
		{notification, "", []string{"notify-send", "--app-name=HomeShell", "--", "Waiting", "alpha · feature/x"}},
		{notification, "int:suppress-sound:1", []string{"notify-send", "--app-name=HomeShell", "--hint=int:suppress-sound:1", "--", "Waiting", "alpha · feature/x"}},
		{optionShaped, "", []string{"notify-send", "--app-name=HomeShell", "--", "--app-name=Evil waiting", "-? · feature/x"}},
	}
	for _, tt := range tests {
		if got := linuxArgs(tt.notification, tt.hint); !slices.Equal(got, tt.want) {
			t.Errorf("linuxArgs(%q, hint %q) = %v, want %v", tt.notification.Title, tt.hint, got, tt.want)
		}
	}
}

func TestLinuxSoundCommand(t *testing.T) {
	if cmd := linuxSoundCommand("none"); cmd != nil {
		t.Errorf("silent sound should yield no player, got %v", cmd)
	}
	cmd := linuxSoundCommand("default")
	if len(cmd) == 0 || cmd[0] != "canberra-gtk-play" {
		t.Errorf("default sound should use canberra-gtk-play, got %v", cmd)
	}
}

// showLinux must leave exactly one sound source on, and none for a silent
// notification: the notification server plays a sound of its own unless the
// suppress-sound hint says otherwise. Stub notify-send and canberra-gtk-play
// binaries record what the daemon actually ran.
func TestShowLinuxPlaysAtMostOneSound(t *testing.T) {
	const suppress = "--hint=boolean:suppress-sound:true"
	tests := []struct {
		name            string
		sound           string
		playerInstalled bool
		// oldNotifySend rejects boolean hints, as older notify-send builds do.
		oldNotifySend bool
		wantHint      string
		wantPlayed    bool
	}{
		{name: "silent with a player installed", sound: "none", playerInstalled: true, wantHint: suppress},
		{name: "silent without a player", sound: "none", wantHint: suppress},
		{name: "audible with a player plays the chosen sound only", sound: "chime", playerInstalled: true, wantHint: suppress, wantPlayed: true},
		{name: "audible without a player leaves the server's sound on", sound: "chime"},
		{name: "an old notify-send gets the hint typed int", sound: "none", oldNotifySend: true, wantHint: "--hint=int:suppress-sound:1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binDir := t.TempDir()
			logPath := filepath.Join(binDir, "calls.log")
			stub := func(name, guard string) {
				script := "#!/bin/sh\n" + guard + "printf '%s\\n' \"" + name + "\" \"$@\" >> \"" + logPath + "\"\n"
				if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			guard := ""
			if tt.oldNotifySend {
				guard = "case \"$*\" in *--hint=boolean:*) exit 1;; esac\n"
			}
			stub("notify-send", guard)
			if tt.playerInstalled {
				stub("canberra-gtk-play", "")
			}
			t.Setenv("PATH", binDir)

			if err := showLinux(context.Background(), Notification{Title: "t", Body: "b", Sound: tt.sound}); err != nil {
				t.Fatalf("showLinux: %v", err)
			}
			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			var hint string
			for _, line := range lines {
				if strings.HasPrefix(line, "--hint=") {
					hint = line
				}
			}
			if hint != tt.wantHint {
				t.Errorf("hint = %q, want %q; calls:\n%s", hint, tt.wantHint, data)
			}
			if got := slices.Contains(lines, "canberra-gtk-play"); got != tt.wantPlayed {
				t.Errorf("player ran = %v, want %v; calls:\n%s", got, tt.wantPlayed, data)
			}
		})
	}
}
