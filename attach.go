package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func runAttach(args []string) int {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	var session string
	fs.StringVar(&session, "s", "dev-services", "Name of the tmux session")
	fs.StringVar(&session, "session-name", "dev-services", "Name of the tmux session")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: flowlog attach [flags]")
		fmt.Fprintln(fs.Output(), "  -s, --session-name <name>   Name of the tmux session")
	}
	fs.Parse(args)

	sess := newTmuxSession(session, "")
	if err := sess.attachToSession(); err != nil {
		if strings.Contains(err.Error(), "No active tmux session") {
			fmt.Println(err)
		} else {
			fmt.Fprintf(os.Stderr, "Tmux Error: %v\n", err)
		}
		return 1
	}
	return 0
}
