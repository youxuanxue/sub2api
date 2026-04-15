package main

import (
	"flag"
	"os"
)

// tkParseServerCLIFlags parses server flags on a dedicated FlagSet so imported packages
// cannot collide with flag.CommandLine (e.g. duplicate -version).
func tkParseServerCLIFlags() (setupMode, showVersion bool) {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	setup := fs.Bool("setup", false, "Run setup wizard in CLI mode")
	ver := fs.Bool("version", false, "Show version information")
	_ = fs.Parse(os.Args[1:])
	return *setup, *ver
}
