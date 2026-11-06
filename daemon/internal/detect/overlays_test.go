package detect

import (
	"os"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
)

func TestCodexHooksReviewOverlay(t *testing.T) {
	screen, err := os.ReadFile("testdata/overlays/codex__hooks-review.screen.txt")
	if err != nil {
		t.Fatal(err)
	}
	result := Detect(Snapshot{Agent: "codex", Screen: string(screen)})
	if result.MatchedRule != "homeshell_hooks_review" || result.State != domain.SessionWaiting || !result.VisibleBlocker {
		t.Fatalf("hooks review = %+v", result)
	}
	for _, screen := range []string{"Hooks need review", "Trust all and continue\nContinue without trusting"} {
		if got := Detect(Snapshot{Agent: "codex", Screen: screen}); got.MatchedRule == "homeshell_hooks_review" {
			t.Fatalf("incomplete or historical screen matched overlay: %q", screen)
		}
	}
}
