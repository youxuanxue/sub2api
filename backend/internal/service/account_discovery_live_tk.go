package service

// IsLiveForDiscovery reports whether this account may contribute models to
// discovery menus (/v1/models, capabilities, me pricing candidate projection).
//
// Contract: docs/approved/discovery-require-live-account.md
// Live = status active AND Schedulable flag true. Instantaneous capacity
// (temp_unschedulable, rate-limit reset, overload) must NOT hide a model from
// the menu — RuntimeReadiness owns those at request time.
func (a *Account) IsLiveForDiscovery() bool {
	return a != nil && a.IsActive() && a.Schedulable
}
