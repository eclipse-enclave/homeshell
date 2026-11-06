// Package bridgecli is the session bridge's process side, run as the hidden
// `homeshell bridge` command. serve runs an agent on a PTY, renders its output
// in a VT emulator (screen, OSC title, OSC 9;4 progress) and serves it over a
// unix socket that terminals attach to and detach from; the other verbs are
// its clients.
//
//	homeshell bridge serve  --sock PATH [--dir DIR] [--size COLSxROWS] [--term NAME]
//	                        [--exit-file PATH] -- CMD [ARG...]
//	homeshell bridge attach --sock PATH
//	homeshell bridge dump   --sock PATH
//	homeshell bridge send   --sock PATH [--text STRING] [--enter]
//	homeshell bridge stop   --sock PATH [--timeout DUR]
//
// serve stays in the foreground; the daemon spawns it with Setsid, so the
// agent outlives the daemon, and polls the socket.
package bridgecli

import (
	"fmt"
	"os"
)

// Main runs one bridge verb and returns its exit code.
func Main(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	verb, args := args[0], args[1:]
	switch verb {
	case "serve":
		return cmdServe(args)
	case "attach":
		return cmdAttach(args)
	case "dump":
		return cmdDump(args)
	case "send":
		return cmdSend(args)
	case "stop":
		return cmdStop(args)
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "homeshell bridge: unknown verb %q\n", verb)
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  homeshell bridge serve  --sock PATH [--dir DIR] [--size COLSxROWS] [--term NAME]
                          [--exit-file PATH] -- CMD [ARG...]
  homeshell bridge attach --sock PATH
  homeshell bridge dump   --sock PATH
  homeshell bridge send   --sock PATH [--text STRING] [--enter]
  homeshell bridge stop   --sock PATH [--timeout DUR]
`)
}
