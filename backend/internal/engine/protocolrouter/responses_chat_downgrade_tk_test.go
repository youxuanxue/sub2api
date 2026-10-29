package protocolrouter

import (
	"bytes"
	"testing"
)

func TestResponsesToChatDropsUnsupportedFieldsBehindNative(t *testing.T) {
	body := []byte(`{
		"model":"deepseek-v4.1-flash",
		"previous_response_id":"resp_prev",
		"prompt_cache_key":"cache-1",
		"input":[{"role":"user","content":[
			{"type":"input_text","text":"hello","cache_control":{"type":"ephemeral"}},
			{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}}
		]}]
	}`)
	request, err := ParseCanonicalRequest(ProtocolResponses, ResponsesPathRoot, "deepseek-v4.1-flash", false, body)
	if err != nil {
		t.Fatalf("ParseCanonicalRequest: %v", err)
	}
	if PreservesResponsesToChat(request) {
		t.Fatal("lossy fields must stay outside the lossless preserve predicate")
	}

	router := New(allTestAdapters())
	chatOnly, err := router.Plan(request, testAccount(t, ProtocolChatCompletions))
	if err != nil {
		t.Fatalf("chat-only Plan: %v", err)
	}
	if chatOnly.TargetProtocol() != ProtocolChatCompletions || chatOnly.AdapterID() != AdapterResponsesToChat {
		t.Fatalf("chat-only Plan = %s/%s", chatOnly.TargetProtocol(), chatOnly.AdapterID())
	}
	if chatOnly.CompatibilityRank() != 1 || chatOnly.Adjustment() != ResponsesChatDroppedUnsupportedFields {
		t.Fatalf("chat-only rank/adjustment = %d/%q", chatOnly.CompatibilityRank(), chatOnly.Adjustment())
	}
	effective := chatOnly.effectiveRequest.Body()
	for _, dropped := range []string{"previous_response_id", "prompt_cache_key", "cache_control", "input_audio"} {
		if bytes.Contains(effective, []byte(dropped)) {
			t.Fatalf("effective body still contains %s: %s", dropped, effective)
		}
	}
	if !bytes.Contains(effective, []byte("hello")) {
		t.Fatalf("effective body dropped the text: %s", effective)
	}

	native, err := router.Plan(request, testAccount(t, ProtocolResponses, ProtocolChatCompletions))
	if err != nil {
		t.Fatalf("native Plan: %v", err)
	}
	if native.TargetProtocol() != ProtocolResponses || native.CompatibilityRank() != 0 {
		t.Fatalf("native Plan = %s rank %d", native.TargetProtocol(), native.CompatibilityRank())
	}
	if !bytes.Contains(native.effectiveRequest.Body(), []byte("previous_response_id")) ||
		!bytes.Contains(native.effectiveRequest.Body(), []byte("input_audio")) {
		t.Fatalf("native plan dropped fields: %s", native.effectiveRequest.Body())
	}

	audioOnlyBody := []byte(`{"model":"deepseek-v4.1-flash","input":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}}]}]}`)
	audioOnly, err := ParseCanonicalRequest(ProtocolResponses, ResponsesPathRoot, "deepseek-v4.1-flash", false, audioOnlyBody)
	if err != nil {
		t.Fatalf("ParseCanonicalRequest audio: %v", err)
	}
	droppedAudio, err := router.Plan(audioOnly, testAccount(t, ProtocolChatCompletions))
	if err != nil {
		t.Fatalf("audio-only Plan: %v", err)
	}
	if droppedAudio.CompatibilityRank() != 1 || bytes.Contains(droppedAudio.effectiveRequest.Body(), []byte("input_audio")) {
		t.Fatalf("audio-only plan = rank %d body %s", droppedAudio.CompatibilityRank(), droppedAudio.effectiveRequest.Body())
	}

	plain, err := ParseCanonicalRequest(ProtocolResponses, ResponsesPathRoot, "deepseek-v4.1-flash", false, []byte(`{"model":"deepseek-v4.1-flash","input":"hello"}`))
	if err != nil {
		t.Fatalf("ParseCanonicalRequest plain: %v", err)
	}
	plainPlan, err := router.Plan(plain, testAccount(t, ProtocolChatCompletions))
	if err != nil {
		t.Fatalf("plain Plan: %v", err)
	}
	if plainPlan.CompatibilityRank() != 0 || plainPlan.Adjustment() != "" {
		t.Fatalf("plain text must stay rank 0, got %d %q", plainPlan.CompatibilityRank(), plainPlan.Adjustment())
	}

	convBody := []byte(`{"model":"deepseek-v4.1-flash","conversation":"conv_1","conversation_id":"conv_probe","input":"hi"}`)
	convRequest, err := ParseCanonicalRequest(ProtocolResponses, ResponsesPathRoot, "deepseek-v4.1-flash", false, convBody)
	if err != nil {
		t.Fatalf("ParseCanonicalRequest conversation: %v", err)
	}
	if convRequest.Profile().Continuation != ContinuationConversation {
		t.Fatalf("conversation profile = %s", convRequest.Profile().Continuation)
	}
	convChat, err := router.Plan(convRequest, testAccount(t, ProtocolChatCompletions))
	if err != nil {
		t.Fatalf("conversation chat Plan: %v", err)
	}
	if convChat.CompatibilityRank() != 1 || convChat.Adjustment() != ResponsesChatDroppedUnsupportedFields {
		t.Fatalf("conversation chat rank/adjustment = %d/%q", convChat.CompatibilityRank(), convChat.Adjustment())
	}
	convEffective := convChat.effectiveRequest.Body()
	if bytes.Contains(convEffective, []byte("conversation_id")) || bytes.Contains(convEffective, []byte("conv_1")) || bytes.Contains(convEffective, []byte("conv_probe")) {
		t.Fatalf("conversation chat plan kept the id: %s", convEffective)
	}
	if !bytes.Contains(convEffective, []byte("hi")) {
		t.Fatalf("conversation chat plan dropped the text: %s", convEffective)
	}
	convNative, err := router.Plan(convRequest, testAccount(t, ProtocolResponses, ProtocolChatCompletions))
	if err != nil {
		t.Fatalf("conversation native Plan: %v", err)
	}
	if convNative.TargetProtocol() != ProtocolResponses || convNative.CompatibilityRank() != 0 {
		t.Fatalf("conversation native Plan = %s rank %d", convNative.TargetProtocol(), convNative.CompatibilityRank())
	}
	if !bytes.Contains(convNative.effectiveRequest.Body(), []byte("conversation_id")) || !bytes.Contains(convNative.effectiveRequest.Body(), []byte("conv_probe")) {
		t.Fatalf("native plan dropped conversation_id: %s", convNative.effectiveRequest.Body())
	}
}
