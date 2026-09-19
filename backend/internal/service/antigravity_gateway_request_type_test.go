package service

import (
	"encoding/json"
	"testing"
)

func TestWrapNativeGeminiRequest_OmitsRequestTypeForPlainText(t *testing.T) {
	t.Parallel()
	svc := &AntigravityGatewayService{}
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	out, err := svc.wrapV1InternalRequest("proj", "gemini-3.1-pro-preview", body)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["requestType"]; ok {
		t.Fatalf("plain text must omit requestType, got %#v", m["requestType"])
	}
	if _, ok := m["enabledCreditTypes"]; ok {
		t.Fatalf("plain text must not declare credits, got %#v", m["enabledCreditTypes"])
	}
}

func TestWrapNativeGeminiRequest_SetsImageGen(t *testing.T) {
	t.Parallel()
	svc := &AntigravityGatewayService{}
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"draw"}]}],"generationConfig":{"responseModalities":["TEXT","IMAGE"]}}`)
	out, err := svc.wrapV1InternalRequest("proj", "gemini-3.1-flash-image", body)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["requestType"] != "image_gen" {
		t.Fatalf("want image_gen, got %#v", m["requestType"])
	}
	if _, ok := m["enabledCreditTypes"]; ok {
		t.Fatalf("image_gen must not declare credits, got %#v", m["enabledCreditTypes"])
	}
}

func TestWrapNativeGeminiRequest_SetsAgentWhenTools(t *testing.T) {
	t.Parallel()
	svc := &AntigravityGatewayService{}
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"fn","parameters":{"type":"object"}}]}]}`)
	out, err := svc.wrapV1InternalRequest("proj", "gemini-3.1-pro-preview", body)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["requestType"] != "agent" {
		t.Fatalf("want agent, got %#v", m["requestType"])
	}
	credits, _ := m["enabledCreditTypes"].([]any)
	if len(credits) != 1 || credits[0] != "GOOGLE_ONE_AI" {
		t.Fatalf("agent must declare GOOGLE_ONE_AI, got %#v", m["enabledCreditTypes"])
	}
}

func TestWrapNativeGeminiRequest_SetsWebSearchWhenGoogleSearch(t *testing.T) {
	t.Parallel()
	svc := &AntigravityGatewayService{}
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"googleSearch":{}}]}`)
	out, err := svc.wrapV1InternalRequest("proj", "gemini-3.1-pro-preview", body)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["requestType"] != "web_search" {
		t.Fatalf("want web_search, got %#v", m["requestType"])
	}
	if _, ok := m["enabledCreditTypes"]; ok {
		t.Fatalf("web_search must not declare credits, got %#v", m["enabledCreditTypes"])
	}
}
