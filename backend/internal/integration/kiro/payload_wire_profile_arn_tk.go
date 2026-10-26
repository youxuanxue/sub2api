package kiro

// Wire-size accounting for profileArn.
//
// The size guard and the truncator both measure json.Marshal(payload), but the
// account's profileArn used to be injected later, in
// CallKiroAPIWithDoerContext, after both had already run. Because ProfileArn
// carries `omitempty`, an empty value serializes to nothing at all, so the
// whole `"profileArn":"<arn>"` member — 81 bytes for the 65-character ARNs in
// use — escaped the measurement and still landed on the wire. A payload
// measured at 921,600 bytes was sent as 921,681.
//
// The fix binds the ARN before reduction and validation rather than widening
// the limit: the guard must measure the bytes Kiro counts. Binding before
// reduction, not merely before validation, is what keeps the two in agreement.
// The truncator targets the same maxPayloadBytes, so were the ARN bound after
// it, a payload trimmed to just under the limit would cross it again on
// injection and be rejected — when the approved policy says such a payload
// should have been reduced further instead.
//
// Contract: docs/approved/kiro-payload-overflow.md. This changes no approved
// behavior — not the reduction order, the no-rewrite rules for user prose and
// tool definitions, or the 413 semantics — and does not alter maxPayloadBytes.
// Requests that genuinely fit, profileArn included, stay byte-identical.

// BindWireProfileArn sets the payload's profileArn to the value this account
// will send upstream, then re-runs size reduction so the payload is measured
// and reduced with that field present.
//
// It is idempotent: binding the same account twice neither re-counts the ARN
// nor reduces the payload a second time. Callers must run it, and then
// ValidatePayloadSize, before dispatch. CallKiroAPIWithDoerContext still binds
// defensively for callers that reach it directly.
func BindWireProfileArn(payload *KiroPayload, account *Account) {
	if payload == nil {
		return
	}
	before := payload.ProfileArn
	setPayloadProfileArnForAccount(payload, account)
	if payload.ProfileArn == before {
		// Nothing was added to the wire, so the earlier measurement stands.
		// This covers the repeat call and an account with no ARN of its own.
		return
	}
	// Translation may already have truncated and recorded its own Before
	// breakdown. Reduction here would overwrite it with a post-truncation
	// snapshot, so the original pre-truncation measurement is restored and
	// only After reflects this pass.
	previous := payload.SizeDiagnostics
	truncatePayloadToLimit(payload, payload.hasSystemPriming)
	if previous != nil && payload.SizeDiagnostics != nil && payload.SizeDiagnostics != previous {
		payload.SizeDiagnostics.Before = previous.Before
	}
}
