package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	case "bundle":
		if err := bundle(os.Args[2:]); err != nil {
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
	fmt.Fprintln(os.Stderr, "       account-model-mapping bundle [--runtime-json JSON|@path|-] [--output path|--check path]")
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
	return writeJSON(os.Stdout, doc)
}

func bundle(args []string) error {
	fs := flag.NewFlagSet("bundle", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	runtimeArg := fs.String("runtime-json", "", "optional tk_account_model_mapping_runtime JSON, @path, or - for stdin")
	outputPath := fs.String("output", "", "write the deterministic bundle to this path")
	checkPath := fs.String("check", "", "fail if this file differs from the deterministic bundle")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*outputPath) != "" && strings.TrimSpace(*checkPath) != "" {
		return fmt.Errorf("--output and --check are mutually exclusive")
	}
	runtimeRaw, err := readRuntimeArg(*runtimeArg)
	if err != nil {
		return err
	}
	doc, err := service.ModelSurfaceBundleForOps(context.Background(), runtimeRaw)
	if err != nil {
		return err
	}
	payload, err := marshalJSON(doc)
	if err != nil {
		return err
	}
	if path := strings.TrimSpace(*checkPath); path != "" {
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read bundle %s: %w", path, readErr)
		}
		if !bytes.Equal(got, payload) {
			return fmt.Errorf("bundle drift: run account-model-mapping bundle --output %s", path)
		}
		return nil
	}
	if path := strings.TrimSpace(*outputPath); path != "" {
		if err := writeFileAtomic(path, payload, 0o644); err != nil {
			return fmt.Errorf("write bundle %s: %w", path, err)
		}
		return nil
	}
	_, err = os.Stdout.Write(payload)
	return err
}

func writeFileAtomic(path string, payload []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err := tmp.Write(payload); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func marshalJSON(doc any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeJSON(&buf, doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeJSON(w io.Writer, doc any) error {
	enc := json.NewEncoder(w)
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
