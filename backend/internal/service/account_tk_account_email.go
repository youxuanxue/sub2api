package service

import (
	"net/mail"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	accountExtraEmailAddressKey = "email_address"
	accountExtraEmailKey        = "email"
)

// ResolveAccountEmail returns the best-effort OAuth/account email stored on an
// account (extra first, then credentials).
func ResolveAccountEmail(extra, credentials map[string]any) string {
	for _, src := range []map[string]any{extra, credentials} {
		if src == nil {
			continue
		}
		for _, key := range []string{accountExtraEmailAddressKey, accountExtraEmailKey} {
			if v, ok := src[key]; ok {
				if s := strings.TrimSpace(anyString(v)); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

// ApplyAccountEmail writes or clears the canonical account email fields on
// extra/credentials. Empty email clears all known email keys.
func ApplyAccountEmail(extra, credentials map[string]any, email string) (map[string]any, map[string]any, error) {
	email = strings.TrimSpace(email)
	if email != "" {
		if err := validateAccountEmail(email); err != nil {
			return extra, credentials, err
		}
	}
	if extra == nil {
		extra = map[string]any{}
	}
	if credentials == nil {
		credentials = map[string]any{}
	}

	clearAccountEmailKeys(extra)
	clearAccountEmailKeys(credentials)

	if email == "" {
		return extra, credentials, nil
	}

	extra[accountExtraEmailAddressKey] = email
	extra[accountExtraEmailKey] = email
	credentials[accountExtraEmailAddressKey] = email
	credentials[accountExtraEmailKey] = email
	return extra, credentials, nil
}

func validateAccountEmail(email string) error {
	addr, err := mail.ParseAddress(email)
	if err != nil || addr == nil {
		return infraerrors.BadRequest("INVALID_ACCOUNT_EMAIL", "invalid account email")
	}
	if strings.TrimSpace(addr.Address) == "" {
		return infraerrors.BadRequest("INVALID_ACCOUNT_EMAIL", "invalid account email")
	}
	return nil
}

func clearAccountEmailKeys(m map[string]any) {
	if m == nil {
		return
	}
	delete(m, accountExtraEmailAddressKey)
	delete(m, accountExtraEmailKey)
}

func anyString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return ""
	}
}
