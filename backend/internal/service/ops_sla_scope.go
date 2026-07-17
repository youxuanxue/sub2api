package service

import "strings"

// Ops error ownership vocabulary (persisted on ops_error_logs.error_owner).
const (
	OpsErrorOwnerClient   = "client"
	OpsErrorOwnerProvider = "provider"
	OpsErrorOwnerPlatform = "platform"
)

// IsOpsSLAFaultOwner reports whether a persisted error_owner counts against SLA /
// platform-service error numerators. Client-caused failures still belong in the
// request denominator but must not reduce SLA.
func IsOpsSLAFaultOwner(owner string) bool {
	switch strings.TrimSpace(strings.ToLower(owner)) {
	case OpsErrorOwnerProvider, OpsErrorOwnerPlatform:
		return true
	default:
		return false
	}
}

// OpsSLAFaultOwnersSQL returns the IN-list for raw SQL fragments.
func OpsSLAFaultOwnersSQL() string {
	return "'provider', 'platform'"
}

// OpsSLAFaultOwnerPredicate builds `colerror_owner IN ('provider','platform')`.
func OpsSLAFaultOwnerPredicate(ownerColumn string) string {
	col := strings.TrimSpace(ownerColumn)
	if col != "" && !strings.HasSuffix(col, ".") {
		col += "."
	}
	return "COALESCE(" + col + "error_owner, '') IN (" + OpsSLAFaultOwnersSQL() + ")"
}

// ComputeSLAMetrics derives dashboard SLA/error-rate from aggregate counts.
func ComputeSLAMetrics(successCount, errorTotal, slaFaultCount int64) (sla, errorRate float64) {
	requestTotal := successCount + errorTotal
	if requestTotal <= 0 {
		return 0, 0
	}
	good := requestTotal - slaFaultCount
	if good < 0 {
		good = 0
	}
	return float64(good) / float64(requestTotal), float64(slaFaultCount) / float64(requestTotal)
}
