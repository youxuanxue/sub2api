//go:build unit

package service

import (
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestApplyNewAPIAnthropicThinkingTemperature_PinsNonDefaultTemperature(t *testing.T) {
	account := &Account{ChannelType: newapiconstant.ChannelTypeAnthropic}
	cases := []struct {
		name string
		body string
	}{
		{
			name: "reasoning_effort",
			body: `{"model":"claude-opus-5-5","temperature":0.2,"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "reasoning max_tokens",
			body: `{"model":"claude-opus-5-5[1m]","temperature":0,"reasoning":{"max_tokens":2048},"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "thinking suffix",
			body: `{"model":"claude-opus-5-5-thinking","temperature":0.7,"messages":[{"role":"user","content":"hi"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := applyNewAPIAnthropicThinkingTemperature(account, gjson.Get(tc.body, "model").String(), []byte(tc.body))
			require.InDelta(t, 1.0, gjson.GetBytes(got, "temperature").Float(), 1e-9)
			require.Equal(t, "hi", gjson.GetBytes(got, "messages.0.content").String())
		})
	}
}

func TestApplyNewAPIAnthropicThinkingTemperature_LeavesUnconstrainedRequests(t *testing.T) {
	anthropic := &Account{ChannelType: newapiconstant.ChannelTypeAnthropic}
	other := &Account{ChannelType: newapiconstant.ChannelTypeOpenAI}
	cases := []struct {
		name    string
		account *Account
		body    string
	}{
		{
			name:    "temperature already 1",
			account: anthropic,
			body:    `{"model":"claude-opus-5-5","temperature":1,"reasoning_effort":"low","messages":[]}`,
		},
		{
			name:    "no thinking signal",
			account: anthropic,
			body:    `{"model":"claude-opus-5-5","temperature":0.2,"messages":[]}`,
		},
		{
			name:    "temperature omitted",
			account: anthropic,
			body:    `{"model":"claude-opus-5-5","reasoning_effort":"medium","messages":[]}`,
		},
		{
			name:    "non-claude model",
			account: anthropic,
			body:    `{"model":"gpt-6-luna","temperature":0.2,"reasoning_effort":"high","messages":[]}`,
		},
		{
			name:    "non-anthropic channel",
			account: other,
			body:    `{"model":"claude-opus-5-5","temperature":0.2,"reasoning_effort":"high","messages":[]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(tc.body)
			got := applyNewAPIAnthropicThinkingTemperature(tc.account, gjson.Get(tc.body, "model").String(), raw)
			require.JSONEq(t, tc.body, string(got))
		})
	}
}
