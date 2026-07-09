package service

import "context"

// openaiStickyAccountStillInGroup reports whether a sticky-bound account should
// still be treated as a member of groupID.
//
// TK (upstream Wei-Shaw/sub2api#1934): OpenAI-compat sticky bindings are
// namespaced by (groupID, sessionHash) in Redis, but the bound account is
// resolved by *global* account ID (getSchedulableAccount → snapshot/DB GetByID).
// When an account is moved out of the group (group switch / removed from a
// group), the stale binding keeps resolving it, so subsequent requests for that
// group keep landing on an account that no longer belongs to the group —
// surfacing as 404 / wrong-group billing / 503. The load-balance path is immune
// because it enumerates strictly by group bucket
// (ListSchedulableByGroupIDAndPlatform); only the three sticky fast paths
// (tryStickySessionHit, the Layer-1 inline block in SelectAccountWithLoadAwareness,
// and scheduler selectBySessionHash) skipped the group check. This mirrors
// GatewayService.isAccountInGroup for the OpenAI-compat pool (openai / newapi).
//
// Conservative semantics: only treat the account as out-of-group when its group
// membership is *known* (AccountGroups / GroupIDs non-empty) and excludes
// groupID. An empty membership set is ambiguous — the scheduler snapshot serves
// accounts already filtered by group bucket and does not re-populate
// AccountGroups on every code path — so an empty set keeps the binding rather
// than risk falsely clearing a healthy session. The fresh DB account returned by
// recheckOpenAICompatAccountFromDB (accountRepo.GetByID → accountsToService) does
// populate group membership, which is what makes the real #1934 scenario (account
// reassigned to another group) detectable.
//
// Known residual gap: an account removed from ALL groups (made fully ungrouped)
// has empty membership and is therefore KEPT by the conservative rule, so its
// stale sticky binding is not invalidated by this guard. We accept this because
// the alternative — treating empty membership as "drifted out" — would falsely
// clear healthy bindings whenever a code path serves a group-filtered snapshot
// account without re-populating AccountGroups (the existing US013/US015 sticky-HIT
// fixtures rely on empty membership meaning "unknown, keep"). The far more common
// reassign-to-another-group case is fully covered.
func openaiStickyAccountStillInGroup(account *Account, groupID int64) bool {
	if account == nil {
		return false
	}
	if groupID <= 0 {
		return true
	}
	known := false
	for _, ag := range account.AccountGroups {
		if ag.GroupID <= 0 {
			continue
		}
		known = true
		if ag.GroupID == groupID {
			return true
		}
	}
	for _, id := range account.GroupIDs {
		if id <= 0 {
			continue
		}
		known = true
		if id == groupID {
			return true
		}
	}
	// Membership known and groupID absent → account drifted out of the group.
	// Membership unknown (empty) → keep the binding (conservative).
	return !known
}

func openaiStickyAccountGroupMembershipKnown(account *Account) bool {
	if account == nil {
		return false
	}
	for _, ag := range account.AccountGroups {
		if ag.GroupID > 0 {
			return true
		}
	}
	for _, id := range account.GroupIDs {
		if id > 0 {
			return true
		}
	}
	return false
}

func (s *OpenAIGatewayService) openAIStickyAccountStillInGroupForRequest(ctx context.Context, groupID *int64, platform string, account *Account) bool {
	if account == nil {
		return false
	}
	if groupID == nil || *groupID <= 0 {
		return openAIStickyAccountMatchesGroup(account, nil)
	}
	if openaiStickyAccountGroupMembershipKnown(account) {
		return openaiStickyAccountStillInGroup(account, *groupID)
	}
	accounts, err := s.listOpenAICompatSchedulableAccounts(ctx, groupID, platform)
	if err != nil {
		return true
	}
	for i := range accounts {
		if accounts[i].ID == account.ID {
			return true
		}
	}
	return false
}
