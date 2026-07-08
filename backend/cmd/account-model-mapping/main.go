package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "floors":
		if err := floors(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "account-model-mapping:", err)
			os.Exit(2)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: account-model-mapping floors [--runtime-json JSON|@path|-]")
}

func floors(args []string) error {
	fs := flag.NewFlagSet("floors", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	runtimeArg := fs.String("runtime-json", "", "optional tk_account_model_mapping_runtime JSON, @path, or - for stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	runtimeRaw, err := readRuntimeArg(*runtimeArg)
	if err != nil {
		return err
	}
	doc, err := service.AccountModelMappingFloorForOps(context.Background(), runtimeRaw)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

func readRuntimeArg(arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", nil
	}
	if arg == "-" {
		b, err := io.ReadAll(os.Stdin)
		return string(b), err
	}
	if strings.HasPrefix(arg, "@") {
		b, err := os.ReadFile(strings.TrimPrefix(arg, "@"))
		return string(b), err
	}
	return arg, nil
}
