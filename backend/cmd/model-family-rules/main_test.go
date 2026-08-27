package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestModelFamilyRulesArtifactMatchesOwner(t *testing.T) {
	payload, err := modelFamilyRulesPayload()
	if err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join("..", "..", "..", "ops", "observability", "generated", "model-family-rules.json")
	current, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, payload) {
		t.Fatalf("artifact drift: run go run ./cmd/model-family-rules --output %s", artifact)
	}
}
