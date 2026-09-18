package service

import (
	"encoding/json"
	"testing"
)

func TestCleanGeminiRequest_PreservesResponseModalities(t *testing.T) {
	t.Parallel()
	in := []byte(`{
		"session_id":"drop-me",
		"contents":[{"role":"user","parts":[
			{"text":"hi"},
			{"inlineData":{"mimeType":"image/png","data":"[undefined]"}}
		]}],
		"generationConfig":{"responseModalities":["TEXT","IMAGE"]}
	}`)
	out, err := cleanGeminiRequest(in)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["session_id"]; ok {
		t.Fatal("session_id should be stripped")
	}
	gen, ok := m["generationConfig"].(map[string]any)
	if !ok {
		t.Fatal("generationConfig missing")
	}
	mods, ok := gen["responseModalities"].([]any)
	if !ok || len(mods) != 2 {
		t.Fatalf("modalities preserved, got %#v", gen["responseModalities"])
	}
	contents, ok := m["contents"].([]any)
	if !ok || len(contents) != 1 {
		t.Fatalf("contents: %#v", m["contents"])
	}
	cm, ok := contents[0].(map[string]any)
	if !ok {
		t.Fatal("content not map")
	}
	parts, ok := cm["parts"].([]any)
	if !ok || len(parts) != 1 {
		t.Fatalf("broken inlineData dropped, got %#v", cm["parts"])
	}
}
