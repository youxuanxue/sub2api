package service

import "testing"

func TestMirrorStubEdgeID(t *testing.T) {
	tests := []struct {
		name string
		acc  *Account
		want string
	}{
		{name: "edge api key", acc: &Account{Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api-us4.tokenkey.dev"}}, want: "us4"},
		{name: "trailing slash", acc: &Account{Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api-uk2.tokenkey.dev/"}}, want: "uk2"},
		{name: "oauth is not relay", acc: &Account{Type: AccountTypeOAuth, Credentials: map[string]any{"base_url": "https://api-us4.tokenkey.dev"}}},
		{name: "external host is not relay", acc: &Account{Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://example.com"}}},
		{name: "nil", acc: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MirrorStubEdgeID(tc.acc); got != tc.want {
				t.Fatalf("MirrorStubEdgeID() = %q, want %q", got, tc.want)
			}
		})
	}
}
