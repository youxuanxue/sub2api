package apicompat

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func legacyNormalizeAnthropicHistory(messages []AnthropicMessage) []AnthropicMessage {
	return legacyMergeConsecutiveMessages(legacyNormalizeAnthropicToolPairing(legacyMergeConsecutiveMessages(messages)))
}

func TestAnthropicHistoryMatchesLegacy(t *testing.T) {
	// Duplicates, missing/orphan calls, mixed content, singleton extensions and
	// invalid content accepted by the pre-existing repair contract.
	contents := []string{
		`"text"`, `null`, `[]`, `{}`, `[{"type":"text","text":"","extension":true}]`,
		`[{"type":"tool_use","id":"a","name":"exec","input":{"n":9007199254740993}}]`,
		`[{"type":"tool_use","id":"b","name":"exec","input":{}}]`,
		`[{"type":"tool_use","id":""}]`,
		`[{"type":"tool_result","tool_use_id":"a","content":"first"}]`,
		`[{"type":"tool_result","tool_use_id":"a","content":[{"type":"text","text":"last"}]}]`,
		`[{"type":"tool_result","tool_use_id":"b","content":[{"type":"image","source":{"type":"base64","data":"YQ==","media_type":"image/png"}}]}]`,
		`[{"type":"tool_result","tool_use_id":"","content":"orphan"},{"type":"text","text":"keep"}]`,
		`[{"type":"thinking","thinking":"reason","signature":"signed"},{"type":"redacted_thinking","data":"opaque"}]`,
		`[{"type":"text","text":"hello","cache_control":{"type":"ephemeral","ttl":"1h"}}]`,
		`[{"type":"tool_result","tool_use_id":"missing","content":null}]`,
		`[{"type":"unknown","text":"preserved if untouched"}]`,
		`[{"type":"text","text":42}]`, `42`, `true`,
	}
	roles := []string{"user", "assistant", "system", "tool"}
	rng := rand.New(rand.NewSource(251))
	for n := 0; n < 600; n++ {
		messages := make([]AnthropicMessage, rng.Intn(32))
		for i := range messages {
			messages[i] = AnthropicMessage{Role: roles[rng.Intn(len(roles))], Content: json.RawMessage(contents[rng.Intn(len(contents))])}
		}
		before, err := json.Marshal(messages)
		require.NoError(t, err)
		want := legacyNormalizeAnthropicHistory(messages)
		require.Equal(t, want, normalizeAnthropicHistory(messages), "history %d", n)
		after, err := json.Marshal(messages)
		require.NoError(t, err)
		require.Equal(t, before, after, "input mutation at history %d", n)
	}
}

func FuzzAnthropicHistoryMatchesLegacy(f *testing.F) {
	for _, seed := range []string{
		`[]`,
		`[{"role":"user","content":"text"},{"role":"user","content":[]}]`,
		`[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"exec","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"ok"}]}]`,
		`[{"role":"assistant","content":[{"type":"thinking","thinking":"signed","signature":"signature"}]}]`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 64<<10 {
			t.Skip()
		}
		var messages []AnthropicMessage
		if json.Unmarshal([]byte(raw), &messages) != nil {
			return
		}
		require.Equal(t, legacyNormalizeAnthropicHistory(messages), normalizeAnthropicHistory(messages))
	})
}

func BenchmarkAnthropicHistory(b *testing.B) {
	for _, n := range []int{1, 16, 64} {
		messages := []AnthropicMessage{{Role: "user", Content: json.RawMessage(`"run tools"`)}}
		for i := 0; i < n; i++ {
			raw, err := json.Marshal([]AnthropicContentBlock{{Type: "tool_use", ID: fmt.Sprintf("call_%d", i), Name: "exec", Input: json.RawMessage(`{}`)}})
			require.NoError(b, err)
			messages = append(messages, AnthropicMessage{Role: "assistant", Content: raw})
		}
		for i := 0; i < n; i++ {
			output, err := json.Marshal(strings.Repeat("result ", 600))
			require.NoError(b, err)
			raw, err := json.Marshal([]AnthropicContentBlock{{Type: "tool_result", ToolUseID: fmt.Sprintf("call_%d", i), Content: output}})
			require.NoError(b, err)
			messages = append(messages, AnthropicMessage{Role: "user", Content: raw})
		}
		for _, impl := range []struct {
			name      string
			normalize func([]AnthropicMessage) []AnthropicMessage
		}{
			{"legacy", legacyNormalizeAnthropicHistory}, {"decoded", normalizeAnthropicHistory},
		} {
			b.Run(fmt.Sprintf("tools_%d/%s", n, impl.name), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if len(impl.normalize(messages)) != 3 {
						b.Fatal("invalid paired history")
					}
				}
			})
		}
	}
}
