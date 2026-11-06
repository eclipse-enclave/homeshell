package bridgecli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/eclipsesource/homeshell/daemon/internal/sbridge"

	"golang.org/x/term"
)

// cmdSend injects input into a running bridge, the tmux send-keys analogue.
// Bytes come from --text (with an optional trailing carriage return via
// --enter) or from stdin.
func cmdSend(args []string) int {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	sock := fs.String("sock", "", "unix socket of the running bridge (required)")
	text := fs.String("text", "", "text to send (default: read stdin)")
	enter := fs.Bool("enter", false, "append a carriage return (Enter)")
	_ = fs.Parse(args)
	if *sock == "" || (*text == "" && !*enter && term.IsTerminal(stdinFd())) {
		fmt.Fprintln(os.Stderr, "usage: homeshell bridge send --sock PATH --text STRING [--enter]  (or pipe input on stdin)")
		return 2
	}
	input := []byte(*text)
	if *text == "" && !*enter {
		var err error
		if input, err = io.ReadAll(os.Stdin); err != nil {
			fmt.Fprintf(os.Stderr, "sbridge: send: %v\n", err)
			return 1
		}
	}
	if *enter {
		input = append(input, '\r')
	}
	if err := sbridge.Send(context.Background(), *sock, input); err != nil {
		fmt.Fprintf(os.Stderr, "sbridge: send: %v\n", err)
		return 1
	}
	return 0
}
