package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		in      string
		want    slog.Level
		wantErr bool
	}{
		{"verbose", LevelVerbose, false},
		{"debug", slog.LevelDebug, false},
		{"info", slog.LevelInfo, false},
		{"warn", slog.LevelWarn, false},
		{"warning", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"INFO", slog.LevelInfo, false},
		{"  Debug ", slog.LevelDebug, false},
		{"VERBOSE", LevelVerbose, false},
		{"", 0, true},
		{"loud", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseLevel(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseLevel(%q) = %v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseLevel(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseLevel(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestConfigureHonorsLevel(t *testing.T) {
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })

	var buf bytes.Buffer
	Configure(&buf, slog.LevelInfo)

	slog.Debug("should-be-dropped")
	slog.Info("should-appear")

	out := buf.String()
	if strings.Contains(out, "should-be-dropped") {
		t.Errorf("debug record leaked at info level: %q", out)
	}
	if !strings.Contains(out, "should-appear") {
		t.Errorf("info record missing: %q", out)
	}
}

func TestLevelNameRoundTripsWithParseLevel(t *testing.T) {
	for _, name := range []string{"verbose", "debug", "info", "warn", "error"} {
		lvl, err := ParseLevel(name)
		if err != nil {
			t.Fatalf("ParseLevel(%q) unexpected error: %v", name, err)
		}
		if got := LevelName(lvl); got != name {
			t.Errorf("LevelName(ParseLevel(%q)) = %q, want %q", name, got, name)
		}
	}
}

func TestConfigureVerboseLevel(t *testing.T) {
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })

	var buf bytes.Buffer
	Configure(&buf, LevelVerbose)

	slog.Log(context.Background(), LevelVerbose, "verbose-record")

	out := buf.String()
	if !strings.Contains(out, "verbose-record") {
		t.Errorf("verbose record dropped at verbose level: %q", out)
	}
	if !strings.Contains(out, "level=VERBOSE") {
		t.Errorf("verbose record not labelled VERBOSE: %q", out)
	}
}

func TestConfigureFansOutToHub(t *testing.T) {
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })

	var buf bytes.Buffer
	hub := Configure(&buf, slog.LevelInfo)
	ch, cancel := hub.Subscribe()
	defer cancel()

	slog.Info("streamed", "count", 3, "who", "tester")

	rec := recv(t, ch)
	if rec.Msg != "streamed" || rec.Level != "info" {
		t.Errorf("record = %+v, want msg=streamed level=info", rec)
	}
	if rec.Attrs["who"] != "tester" {
		t.Errorf("attrs = %v, want who=tester", rec.Attrs)
	}
	// The inner text handler still wrote to the configured writer.
	if !strings.Contains(buf.String(), "streamed") {
		t.Errorf("inner handler did not write the record: %q", buf.String())
	}
}

func TestConfigureFanoutDropsBelowLevel(t *testing.T) {
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })

	hub := Configure(&bytes.Buffer{}, slog.LevelWarn)
	ch, cancel := hub.Subscribe()
	defer cancel()

	slog.Info("below-threshold")
	slog.Warn("at-threshold")

	rec := recv(t, ch)
	if rec.Msg != "at-threshold" {
		t.Errorf("first streamed record = %q, want at-threshold (info must be dropped)", rec.Msg)
	}
}

func TestRecordFromFlattensGroups(t *testing.T) {
	r := slog.NewRecord(time.Unix(0, 0), slog.LevelWarn, "grouped", 0)
	r.AddAttrs(
		slog.String("scalar", "v"),
		slog.Group("req", slog.String("method", "GET"), slog.Int("status", 200)),
	)

	rec := recordFrom(r)
	if rec.Level != "warn" || rec.Msg != "grouped" {
		t.Fatalf("record = %+v, want level=warn msg=grouped", rec)
	}
	if rec.Attrs["scalar"] != "v" {
		t.Errorf("scalar attr = %v, want v", rec.Attrs["scalar"])
	}
	group, ok := rec.Attrs["req"].(map[string]any)
	if !ok {
		t.Fatalf("req attr = %T, want map", rec.Attrs["req"])
	}
	if group["method"] != "GET" || group["status"].(int64) != 200 {
		t.Errorf("group = %v, want method=GET status=200", group)
	}
}

func TestConfigureDropsVerboseAtDebug(t *testing.T) {
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })

	var buf bytes.Buffer
	Configure(&buf, slog.LevelDebug)

	slog.Log(context.Background(), LevelVerbose, "too-fine")
	slog.Debug("kept")

	out := buf.String()
	if strings.Contains(out, "too-fine") {
		t.Errorf("verbose record leaked at debug level: %q", out)
	}
	if !strings.Contains(out, "kept") {
		t.Errorf("debug record missing at debug level: %q", out)
	}
}
