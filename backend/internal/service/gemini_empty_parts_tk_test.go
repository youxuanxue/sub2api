//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// geminiEmptyPartCleaners are the two outbound owners. Messages compat calls
// filterEmptyPartsFromGeminiRequest; Antigravity native calls cleanGeminiRequest.
var geminiEmptyPartCleaners = []struct {
	name string
	fn   func([]byte) ([]byte, error)
}{
	{name: "messages_compat", fn: filterEmptyPartsFromGeminiRequest},
	{name: "antigravity_native", fn: cleanGeminiRequest},
}

func TestGeminiOutbound_DropsEmptySystemInstructionParts(t *testing.T) {
	t.Parallel()
	in := []byte(`{
		"systemInstruction":{"role":"user","parts":[{}]},
		"contents":[{"role":"user","parts":[{"text":"hello"},{}]}]
	}`)
	for _, cleaner := range geminiEmptyPartCleaners {
		t.Run(cleaner.name, func(t *testing.T) {
			t.Parallel()
			out, err := cleaner.fn(in)
			require.NoError(t, err)
			require.False(t, gjson.GetBytes(out, "systemInstruction").Exists())
			require.False(t, gjson.GetBytes(out, "system_instruction").Exists())
			parts := gjson.GetBytes(out, "contents.0.parts").Array()
			require.Len(t, parts, 1)
			require.Equal(t, "hello", parts[0].Get("text").String())
		})
	}
}

func TestGeminiOutbound_KeepsTextPartInsideSystemInstruction(t *testing.T) {
	t.Parallel()
	in := []byte(`{
		"systemInstruction":{"parts":[{},{"text":"stay"}]},
		"contents":[{"role":"user","parts":[{"text":"hello"}]}]
	}`)
	for _, cleaner := range geminiEmptyPartCleaners {
		t.Run(cleaner.name, func(t *testing.T) {
			t.Parallel()
			out, err := cleaner.fn(in)
			require.NoError(t, err)
			sysParts := gjson.GetBytes(out, "systemInstruction.parts").Array()
			require.Len(t, sysParts, 1)
			require.Equal(t, "stay", sysParts[0].Get("text").String())
			require.Equal(t, "hello", gjson.GetBytes(out, "contents.0.parts.0.text").String())
		})
	}
}

func TestGeminiOutbound_KeepsInlineDataAndFunctionCallParts(t *testing.T) {
	t.Parallel()
	in := []byte(`{
		"systemInstruction":{"parts":[{"inlineData":{"mimeType":"image/png","data":"abc"}}]},
		"contents":[
			{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"q":"x"}}}]},
			{"role":"user","parts":[
				{"functionResponse":{"name":"lookup","response":{"result":"ok"}}},
				{"fileData":{"mimeType":"text/plain","fileUri":"gs://bucket/a.txt"}}
			]}
		]
	}`)
	for _, cleaner := range geminiEmptyPartCleaners {
		t.Run(cleaner.name, func(t *testing.T) {
			t.Parallel()
			out, err := cleaner.fn(in)
			require.NoError(t, err)
			require.Equal(t, "abc", gjson.GetBytes(out, "systemInstruction.parts.0.inlineData.data").String())
			require.Equal(t, "image/png", gjson.GetBytes(out, "systemInstruction.parts.0.inlineData.mimeType").String())
			require.Equal(t, "lookup", gjson.GetBytes(out, "contents.0.parts.0.functionCall.name").String())
			require.Equal(t, "x", gjson.GetBytes(out, "contents.0.parts.0.functionCall.args.q").String())
			require.Equal(t, "lookup", gjson.GetBytes(out, "contents.1.parts.0.functionResponse.name").String())
			require.Equal(t, "ok", gjson.GetBytes(out, "contents.1.parts.0.functionResponse.response.result").String())
			require.Equal(t, "gs://bucket/a.txt", gjson.GetBytes(out, "contents.1.parts.1.fileData.fileUri").String())
			require.Equal(t, 1, len(gjson.GetBytes(out, "systemInstruction.parts").Array()))
			require.Equal(t, 1, len(gjson.GetBytes(out, "contents.0.parts").Array()))
			require.Equal(t, 2, len(gjson.GetBytes(out, "contents.1.parts").Array()))
		})
	}
}

func TestGeminiOutbound_KeepsRealFieldsThatLookEmpty(t *testing.T) {
	t.Parallel()
	in := []byte(`{
		"system_instruction":{"parts":[{}]},
		"contents":[
			{"role":"model","parts":[{"thoughtSignature":"sig-only"}]},
			{"role":"user","parts":[
				{},
				{"text":""},
				{"thought":true,"thoughtSignature":"sig-with-thought"},
				{"executableCode":{"language":"PYTHON","code":"print(1)"}},
				{"function_call":{"name":"ping","args":{}}},
				{"inline_data":{"mime_type":"image/png","data":"abc"}},
				{"file_data":{"file_uri":"gs://bucket/a.txt"}}
			]}
		]
	}`)
	for _, cleaner := range geminiEmptyPartCleaners {
		t.Run(cleaner.name, func(t *testing.T) {
			t.Parallel()
			out, err := cleaner.fn(in)
			require.NoError(t, err)
			require.False(t, gjson.GetBytes(out, "system_instruction").Exists())
			require.False(t, gjson.GetBytes(out, "systemInstruction").Exists())
			contents := gjson.GetBytes(out, "contents").Array()
			require.Len(t, contents, 2)
			require.Equal(t, "sig-only", contents[0].Get("parts.0.thoughtSignature").String())
			require.Equal(t, 1, len(contents[0].Get("parts").Array()))
			parts := contents[1].Get("parts").Array()
			require.Len(t, parts, 6)
			require.Equal(t, "", parts[0].Get("text").String())
			require.True(t, parts[0].Get("text").Exists())
			require.True(t, parts[1].Get("thought").Bool())
			require.Equal(t, "sig-with-thought", parts[1].Get("thoughtSignature").String())
			require.Equal(t, "print(1)", parts[2].Get("executableCode.code").String())
			require.Equal(t, "ping", parts[3].Get("function_call.name").String())
			require.Equal(t, "abc", parts[4].Get("inline_data.data").String())
			require.Equal(t, "gs://bucket/a.txt", parts[5].Get("file_data.file_uri").String())
		})
	}
}

func TestGeminiOutbound_DropsEmptyContentsMessages(t *testing.T) {
	t.Parallel()
	in := []byte(`{
		"contents":[
			{"role":"user","parts":[]},
			{"role":"model","parts":[{}]},
			{"role":"user","parts":[{"text":"kept"}]}
		]
	}`)
	for _, cleaner := range geminiEmptyPartCleaners {
		t.Run(cleaner.name, func(t *testing.T) {
			t.Parallel()
			out, err := cleaner.fn(in)
			require.NoError(t, err)
			contents := gjson.GetBytes(out, "contents").Array()
			require.Len(t, contents, 1)
			require.Equal(t, "user", contents[0].Get("role").String())
			require.Equal(t, "kept", contents[0].Get("parts.0.text").String())
		})
	}
}
