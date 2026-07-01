package service

import (
	"errors"
	"net/mail"
	"strings"
)

const (
	accountExtraEmailAddressKey = "email_address"
	accountExtraEmailKey        = "email"
)

// ResolveAccountContactEmail returns the best-effort OAuth/contact email stored
// on an account (extra first, then credentials).
func ResolveAccountContactEmail(extra, credentials map[string]any) string {
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

// ApplyAccountContactEmail writes or clears the canonical contact email fields on
// extra/credentials. Empty email clears all known email keys.
func ApplyAccountContactEmail(extra, credentials map[string]any, email string) (map[string]any, map[string]any, error) {
	email = strings.TrimSpace(email)
	if email != "" {
		if err := validateAccountContactEmail(email); err != nil {
			return extra, credentials, err
		}
	}
	if extra == nil {
		extra = map[string]any{}
	}
	if credentials == nil {
		credentials = map[string]any{}
	}

	clearAccountContactEmailKeys(extra)
	clearAccountContactEmailKeys(credentials)

	if email == "" {
		return extra, credentials, nil
	}

	extra[accountExtraEmailAddressKey] = email
	extra[accountExtraEmailKey] = email
	credentials[accountExtraEmailAddressKey] = email
	credentials[accountExtraEmailKey] = email
	return extra, credentials, nil
}

func validateAccountContactEmail(email string) error {
	addr, err := mail.ParseAddress(email)
	if err != nil || addr == nil {
		return errors.New("invalid contact email")
	}
	if strings.TrimSpace(addr.Address) == "" {
		return errors.New("invalid contact email")
	}
	return nil
}

func clearAccountContactEmailKeys(m map[string]any) {
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
