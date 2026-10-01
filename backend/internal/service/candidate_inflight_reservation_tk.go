package service

import "context"

func ReserveCandidateInflight(ctx context.Context, billing *BillingCacheService, estimate func(context.Context, *APIKey) (float64, bool)) (func(), error) {
	candidate := CandidateRequestFromContext(ctx)
	if candidate == nil || billing == nil || estimate == nil || !billing.InflightReservationEnabled() {
		return noopRelease, nil
	}
	var reservation *InflightReservation
	release := func() {
		reservation.HandlerDone()
		reservation = nil
		SetCandidateInflightReservation(ctx, nil)
	}
	refresh := func() error {
		release()
		key := candidate.key
		if candidate.subscription != nil || key == nil || key.User == nil {
			return nil
		}
		amount, priced := estimate(ctx, key)
		if !priced && billing.InflightReservationFailClosedOnUnpriced() {
			return ErrInsufficientBalance
		}
		var err error
		reservation, err = billing.ReserveInflight(ctx, key.User, key.Group, candidate.subscription, amount)
		if err != nil {
			return err
		}
		SetCandidateInflightReservation(ctx, reservation)
		return nil
	}
	if err := refresh(); err != nil {
		return noopRelease, err
	}
	SetCandidateBillingHook(ctx, refresh)
	return release, nil
}

func SetCandidateInflightReservation(ctx context.Context, reservation *InflightReservation) {
	if candidate := CandidateRequestFromContext(ctx); candidate != nil {
		candidate.inflightReservation = reservation
		candidate.balanceReserved = reservation != nil
	}
}
