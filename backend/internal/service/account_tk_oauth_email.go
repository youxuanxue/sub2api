package service

import "strings"

// GetOAuthAccountEmail returns the OAuth-linked email stored on an edge account.
func (a *Account) GetOAuthAccountEmail() string {
	if a == nil || !a.IsOAuth() {
		return ""
	}
	for _, key := range []string{"email_address", "email"} {
		if v := strings.TrimSpace(a.GetExtraString(key)); v != "" {
			return v
		}
	}
	return strings.TrimSpace(a.GetCredential("email"))
}
