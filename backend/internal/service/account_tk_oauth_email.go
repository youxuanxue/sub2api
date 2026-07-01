package service

// GetOAuthAccountEmail returns the OAuth-linked email stored on an edge account.
func (a *Account) GetOAuthAccountEmail() string {
	if a == nil || !a.IsOAuth() {
		return ""
	}
	return ResolveAccountEmail(a.Extra, a.Credentials)
}
