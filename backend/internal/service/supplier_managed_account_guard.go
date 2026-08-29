package service

func IsSupplierManagedAccount(account *Account) bool {
	if account == nil || account.Extra == nil {
		return false
	}
	_, exists := account.Extra[SupplierSourceIDExtraKey]
	return exists
}

func ValidateSupplierManagedAccountUpdate(account *Account) error {
	if !IsSupplierManagedAccount(account) {
		return nil
	}
	return ErrSupplierManagedAccountProtected
}

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
	switch typed := value.(type) {
	case int64:
		return typed, typed > 0
	case int:
		return int64(typed), typed > 0
	case float64:
		return int64(typed), typed > 0 && typed == float64(int64(typed))
	default:
		return 0, false
	}
}
