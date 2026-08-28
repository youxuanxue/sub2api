package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func main() {
	outputPath := flag.String("output", "", "write the generated artifact to this path")
	checkPath := flag.String("check", "", "fail when this file differs from the generated artifact")
	flag.Parse()

	if strings.TrimSpace(*outputPath) != "" && strings.TrimSpace(*checkPath) != "" {
		fatal("--output and --check are mutually exclusive")
	}
	payload, err := modelFamilyRulesPayload()
	if err != nil {
		fatal("marshal artifact: %v", err)
	}

	if path := strings.TrimSpace(*checkPath); path != "" {
		current, readErr := os.ReadFile(path)
		if readErr != nil {
			fatal("read %s: %v", path, readErr)
		}
		if !bytes.Equal(current, payload) {
			fatal("artifact drift: run go run ./cmd/model-family-rules --output %s", path)
		}
		return
	}
	if path := strings.TrimSpace(*outputPath); path != "" {
		if err := writeAtomic(path, payload); err != nil {
			fatal("write %s: %v", path, err)
		}
		return
	}
	if _, err := os.Stdout.Write(payload); err != nil {
		fatal("write stdout: %v", err)
	}
}

func modelFamilyRulesPayload() ([]byte, error) {
	payload, err := json.MarshalIndent(service.ExportModelFamilyRules(), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(payload, '\n'), nil
}

func writeAtomic(path string, payload []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	if err := tmp.Chmod(0o644); err != nil {
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

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "model-family-rules: "+format+"\n", args...)
	os.Exit(2)
}
