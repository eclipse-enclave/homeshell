package local

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// claudeConfigDir is where the Claude CLI keeps its state: $CLAUDE_CONFIG_DIR when
// the user has moved it, else ~/.claude. Read from the *daemon's* environment, which
// is the process that launched the agent, so it matches what the agent itself
// resolved.
func claudeConfigDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determining home directory: %w", err)
	}
	return filepath.Join(home, ".claude"), nil
}

// hasConversation reports whether the agent has written a transcript for this
// session id — the file `--resume` needs. Production's answer to
// Provider.conversation.
//
// It searches by **file name** rather than computing the directory: Claude files a
// transcript under a directory derived from the working directory with the path
// separators mangled, and a session id is unique on its own — so a glob over the
// projects directory needs no knowledge of that mangling rule.
func hasConversation(agentSessionID string) bool {
	id := strings.TrimSpace(agentSessionID)
	// The id reaches this as a path fragment, so anything that could escape the
	// projects directory or widen the glob is refused rather than sanitized: a
	// session id is a UUID, and one that is not is not worth guessing at.
	if id == "" || strings.ContainsAny(id, `/\*?[]`) || strings.Contains(id, "..") {
		return false
	}
	dir, err := claudeConfigDir()
	if err != nil {
		return false
	}
	matches, err := filepath.Glob(filepath.Join(dir, "projects", "*", id+".jsonl"))
	return err == nil && len(matches) > 0
}
