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

// accountParticipatesInProtocolPlan is the operator scheduling switch.
// Temporary unreadiness (temp_unschedulable, rate limit, overload, quota)
// stays inside Plan and becomes a capacity miss only after a legal path
// exists. A manual Schedulable=false account is not a pool member.
func accountParticipatesInProtocolPlan(account *Account) bool {
	return account != nil && account.Schedulable
}
