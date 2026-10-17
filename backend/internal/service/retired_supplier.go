package service

import (
	"net/url"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var ErrSupplierRetired = infraerrors.BadRequest("SUPPLIER_RETIRED", "Cursor account support has been retired")

// IsRetiredSupplier recognizes persisted identities even when their platform or
// channel was edited. Keep this tombstone so old exports cannot restore traffic.
func (a *Account) IsRetiredSupplier() bool {
	if a == nil {
		return false
	}
	if provider, _ := a.Extra["upstream_provider"].(string); strings.EqualFold(strings.TrimSpace(provider), "cursor") {
		return true
	}
	for _, key := range []string{"cursor_model_parameters", "cursor_wire_models"} {
		if _, ok := a.Credentials[key]; ok {
			return true
		}
	}
	return retiredSupplierEndpoint(a.Credentials["base_url"]) || retiredSupplierEndpoint(a.Credentials["api_base_urls"])
}

func retiredSupplierEndpoint(value any) bool {
	switch value := value.(type) {
	case string:
		endpoint, err := url.Parse(strings.TrimSpace(value))
		if err != nil {
			return false
		}
		host := strings.TrimSuffix(strings.ToLower(endpoint.Hostname()), ".")
		return host == "cursor.sh" || strings.HasSuffix(host, ".cursor.sh") || host == "cursor.com" || strings.HasSuffix(host, ".cursor.com")
	case map[string]any:
		for _, endpoint := range value {
			if retiredSupplierEndpoint(endpoint) {
				return true
			}
		}
	case map[string]string:
		for _, endpoint := range value {
			if retiredSupplierEndpoint(endpoint) {
				return true
			}
		}
	}
	return false
}
