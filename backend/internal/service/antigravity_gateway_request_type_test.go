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
}
