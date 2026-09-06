package service

import (
	"encoding/json"
	"maps"
)

// IsSupplierManagedAccount reports whether Extra carries the supplier_source_id
// marker used by supplier-source sync and admin UI badges. Presence alone is
// enough (fail closed on malformed values).
//
// Do NOT use this alone on gateway/scheduler hot paths: scheduler Extra
// intentionally omits supplier_source_id. Prefer
// HasSupplierManagedTransportIdentity for transport exceptions that must also
// work on scheduler-cache-shaped accounts.
func IsSupplierManagedAccount(account *Account) bool {
	if account == nil || account.Extra == nil {
		return false
	}
	_, exists := account.Extra[SupplierSourceIDExtraKey]
	return exists
}

// HasSupplierManagedTransportIdentity is the SSOT for supplier-managed
// transport exceptions on gateway/scheduler hot paths.
//
// Primary signal: credentials.protocol_endpoints_exclusive (kept by scheduler
// cache; identity uses account credentials, not Extra.supplier_source_id).
// Fallback: Extra.supplier_source_id on full DB-loaded accounts.
func HasSupplierManagedTransportIdentity(account *Account) bool {
	if accountDeclaresExclusiveProtocolEndpoints(account) {
		return true
	}
	return IsSupplierManagedAccount(account)
}

// ValidateSupplierReservedAccountExtra rejects forging supplier ownership keys
// through ordinary create/import Extra payloads. Managed accounts receive these
// keys only from supplier-source sync commands.
func ValidateSupplierReservedAccountExtra(extra map[string]any) error {
	if extra == nil {
		return nil
	}
	if _, exists := extra[SupplierSourceIDExtraKey]; exists {
		return ErrSupplierReservedAccountExtra
	}
	if _, exists := extra[SupplierDiscountBandExtraKey]; exists {
		return ErrSupplierReservedAccountExtra
	}
	return nil
}

// StripSupplierReservedAccountExtra removes reserved ownership keys from an
// incoming Extra map so ordinary edits can echo Extra without forging.
func StripSupplierReservedAccountExtra(extra map[string]any) map[string]any {
	if extra == nil {
		return nil
	}
	out := maps.Clone(extra)
	delete(out, SupplierSourceIDExtraKey)
	delete(out, SupplierDiscountBandExtraKey)
	return out
}

// PreserveSupplierManagedExtraKeys re-applies reserved ownership keys from the
// existing account onto a replacement Extra map so ordinary Extra edits cannot
// drop supplier sync identity.
func PreserveSupplierManagedExtraKeys(account *Account, extra map[string]any) map[string]any {
	if account == nil || account.Extra == nil {
		return extra
	}
	sourceID, sourceOK := account.Extra[SupplierSourceIDExtraKey]
	band, bandOK := account.Extra[SupplierDiscountBandExtraKey]
	if !sourceOK && !bandOK {
		return extra
	}
	if extra == nil {
		extra = make(map[string]any, 2)
	} else {
		extra = maps.Clone(extra)
	}
	if sourceOK {
		extra[SupplierSourceIDExtraKey] = sourceID
	}
	if bandOK {
		extra[SupplierDiscountBandExtraKey] = band
	}
	return extra
}

func supplierSourceIDFromAccount(account *Account) (int64, bool) {
	if account == nil || account.Extra == nil {
		return 0, false
	}
	return supplierInt64(account.Extra[SupplierSourceIDExtraKey])
}

func supplierDiscountBandFromAccount(account *Account) (int, bool) {
	if account == nil || account.Extra == nil {
		return 0, false
	}
	value, ok := supplierInt64(account.Extra[SupplierDiscountBandExtraKey])
	if !ok || value < 1 || value > 6 {
		return 0, false
	}
	return int(value), true
}

func supplierInt64(value any) (int64, bool) {
	// Align with parseExtraInt so jsonb Extra values that arrive as
	// json.Number or decimal strings still resolve supplier identity keys.
	parsed := int64(parseExtraInt(value))
	if parsed <= 0 {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		if typed != float64(parsed) {
			return 0, false
		}
	case json.Number:
		if raw, err := typed.Float64(); err == nil && raw != float64(parsed) {
			return 0, false
		}
	}
	return parsed, true
}
