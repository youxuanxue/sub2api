package service

import (
	"errors"
	"sort"
	"strconv"
	"strings"
)

// CandidateCapacityDiag explains why entitled pools supported a model but
// produced no schedulable candidate. Supported counts accounts with a legal
// path; Ready counts accounts that actually entered the candidate pool after
// billing-origin selection. RejectReasons covers pathReady filters and
// selection_exhausted when a non-empty pool still failed to bind. Attached to
// UniversalCapacityError so ingress can log filter reasons instead of a bare
// capacity_unavailable line.
type CandidateCapacityDiag struct {
	AccountTotal  int            `json:"account_total"`
	Supported     int            `json:"supported"`
	Ready         int            `json:"ready"`
	RejectReasons map[string]int `json:"reject_reasons,omitempty"`
}

func (d *CandidateCapacityDiag) reject(reason string) {
	if d == nil || reason == "" {
		return
	}
	if d.RejectReasons == nil {
		d.RejectReasons = make(map[string]int, 4)
	}
	d.RejectReasons[reason]++
}

// RejectSummary renders stable "reason=count" pairs for logs and tests.
func (d *CandidateCapacityDiag) RejectSummary() string {
	if d == nil || len(d.RejectReasons) == 0 {
		return ""
	}
	reasons := make([]string, 0, len(d.RejectReasons))
	for reason := range d.RejectReasons {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	var b strings.Builder
	for i, reason := range reasons {
		if i > 0 {
			_, _ = b.WriteString(" ")
		}
		_, _ = b.WriteString(reason)
		_, _ = b.WriteString("=")
		_, _ = b.WriteString(strconv.Itoa(d.RejectReasons[reason]))
	}
	return b.String()
}

// CandidateCapacityDiagFromError extracts selection diagnostics from a capacity
// error. Non-capacity errors and bare ErrUniversalCapacityUnavailable yield nil.
func CandidateCapacityDiagFromError(err error) *CandidateCapacityDiag {
	var capErr *UniversalCapacityError
	if !errors.As(err, &capErr) || capErr == nil {
		return nil
	}
	return capErr.Diag
}
