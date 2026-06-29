// Command nyro-tools is the Nyro gateway CLI (ported from crates/nyro-tools).
//
// P0 placeholder. Commands such as dump-schema land in P3 alongside the
// storage/migration port.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "-h", "--help", "help":
		usage(os.Stdout)
	case "version":
		fmt.Println("nyro-tools (gateway) v0.0.0-dev")
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, "nyro-tools — Nyro gateway CLI (placeholder)\n\n"+
		"Usage:\n  nyro-tools <command>\n\nCommands:\n  version   Print version\n")
}
