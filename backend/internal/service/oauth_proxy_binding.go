package service

import (
	"context"
	"errors"
	"time"
)

// ErrOAuthProxyUnavailable is a transport readiness failure, not a credential
// rejection. A pinned OAuth identity must never silently move to direct egress.
var ErrOAuthProxyUnavailable = errors.New("configured OAuth proxy is unavailable")

// Keep shared repository failures distinct from account route failures so Grok's
// existing failure owner does not quarantine accounts for a database outage.
var errOAuthProxyLookup = errors.New("OAuth proxy lookup unavailable")
var errOAuthProxyRepository = errors.New("OAuth proxy repository unavailable")

// validateOAuthProxyBinding owns pinned OAuth egress readiness for selection,
// authoritative execution and refresh. Explicit fallback first persists the new
// ProxyID (including nil for direct); it does not bypass this gate.
func validateOAuthProxyBinding(account *Account) error {
	if account == nil || (account.Type != AccountTypeOAuth && account.Type != AccountTypeSetupToken) || account.ProxyID == nil {
		return nil
	}
	proxy := account.Proxy
	if proxy == nil || proxy.ID != *account.ProxyID || !proxy.IsActive() || proxy.IsExpired(time.Now()) {
		return ErrOAuthProxyUnavailable
	}
	return nil
}

// resolveOAuthAccountProxyURL re-reads the configured route for refresh callers
// that do not load an account through the scheduler repository. Lookup errors
// deliberately do not include credentials or proxy URLs in the returned error.
func resolveOAuthAccountProxyURL(ctx context.Context, account *Account, repo ProxyRepository) (string, error) {
	if account.ProxyID == nil {
		return "", nil
	}
	if repo == nil {
		return "", errors.Join(ErrOAuthProxyUnavailable, errOAuthProxyRepository)
	}
	proxy, err := repo.GetByID(ctx, *account.ProxyID)
	if err != nil {
		if errors.Is(err, ErrProxyNotFound) {
			return "", ErrOAuthProxyUnavailable
		}
		return "", errors.Join(ErrOAuthProxyUnavailable, errOAuthProxyLookup)
	}
	fresh := *account
	fresh.Proxy = proxy
	if err := validateOAuthProxyBinding(&fresh); err != nil {
		return "", err
	}
	if proxy == nil {
		return "", ErrOAuthProxyUnavailable
	}
	return proxy.URL(), nil
}
