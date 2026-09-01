package service

const (
	postgresInt32Min = -1 << 31
	postgresInt32Max = 1<<31 - 1

	// SupplierDiscountPriorityStep is the priority gap between adjacent discount bands.
	SupplierDiscountPriorityStep = 10
)

func SupplierDiscountBandForRatio(ratio *float64) (int, error) {
	if ratio == nil || *ratio == 1 {
		return 6, nil
	}
	if *ratio <= 0 || *ratio > 1 {
		return 0, ErrSupplierSourceInvalidPurchaseRatio
	}
	switch {
	case *ratio < 0.2:
		return 1, nil
	case *ratio < 0.4:
		return 2, nil
	case *ratio < 0.6:
		return 3, nil
	case *ratio < 0.8:
		return 4, nil
	default:
		return 5, nil
	}
}

// SupplierDiscountPriority returns the priority contribution for a discount band (10, 20, …, 60).
func SupplierDiscountPriority(discountBand int) (int, error) {
	if discountBand < 1 || discountBand > 6 {
		return 0, ErrSupplierSourceInvalidInput
	}
	return discountBand * SupplierDiscountPriorityStep, nil
}

func SupplierAccountPriority(basePriority, discountBand int) (int, error) {
	discountPriority, err := SupplierDiscountPriority(discountBand)
	if err != nil {
		return 0, err
	}
	if basePriority < postgresInt32Min || basePriority > postgresInt32Max-discountPriority {
		return 0, ErrSupplierSourceInvalidInput
	}
	return basePriority + discountPriority, nil
}
