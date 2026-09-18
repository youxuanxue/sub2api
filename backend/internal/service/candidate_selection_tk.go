package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/requestmodel"
)

type candidateAccountLister interface {
	ListCandidateAccounts(context.Context, []int64) ([]Account, error)
}

func (r *CandidateRequest) accounts(ctx context.Context) ([]Account, error) {
	repo := r.resolver.candidateGateway.accountRepo
	ids := make([]int64, 0, len(r.groups))
	for _, group := range r.groups {
		ids = append(ids, group.ID)
	}
	if list, ok := repo.(candidateAccountLister); ok {
		return list.ListCandidateAccounts(ctx, ids)
	}
	// Compatibility for existing repository doubles. Production has a batch read.
	seen := make(map[int64]bool)
	var result []Account
	for _, group := range r.groups {
		accounts, err := repo.ListAllWithFilters(ctx, "", "", "", "", group.ID, "", 0)
		if err != nil {
			return nil, err
		}
		for _, account := range accounts {
			if !seen[account.ID] {
				result = append(result, account)
				seen[account.ID] = true
			}
		}
	}
	return result, nil
}

func (r *CandidateRequest) candidates(ctx context.Context, options candidateSelectOptions) ([]*candidateExecutionPath, bool, error) {
	ctx = r.withProfitPricing(ctx)
	if r.key.IsUniversal() {
		groups, err := r.resolver.span(ctx, r.key.UserID)
		if err != nil {
			return nil, false, err
		}
		r.groups = activeCapabilityGroups(groups)
	} else if r.resolver.candidateGateway.groupRepo != nil && len(r.groups) == 1 {
		group, err := r.resolver.candidateGateway.groupRepo.GetByID(ctx, r.groups[0].ID)
		if err != nil {
			return nil, false, err
		}
		if group == nil || !group.IsActive() {
			return nil, false, ErrUniversalNoEntitledGroup
		}
		r.groups = []Group{*group}
	}
	accounts, err := r.accounts(ctx)
	if err != nil {
		return nil, false, err
	}
	diag := &CandidateCapacityDiag{AccountTotal: len(accounts)}
	r.capacityDiag = diag
	gw, openai := r.resolver.candidateGateway, r.resolver.candidateOpenAI
	ctx = gw.withRPMPrefetch(ctx, accounts)
	ctx = gw.withWindowCostPrefetch(ctx, accounts)
	ctx = openai.withOpenAIQuotaAutoPauseContext(ctx)
	var stickyID int64
	if r.session != "" && gw.cache != nil {
		stickyID, _ = gw.cache.GetSessionAccountID(ctx, 0, r.session)
	}
	usable := make(map[int64]bool)
	var failure error
	for _, group := range r.groups {
		if !group.IsActive() {
			continue
		}
		ok, gateErr := r.resolver.subscriptionGroupUsable(ctx, r.key.UserID, &group)
		if gateErr != nil {
			failure = gateErr
			diag.reject("group_subscription_error")
			continue
		}
		if ok {
			if _, err := r.admit(ctx, &group); err != nil {
				failure = err
				slog.WarnContext(ctx, "candidate_billing_admit_failed", "group_id", group.ID, "error", err)
				diag.reject("group_admit_failed")
				continue
			}
		}
		usable[group.ID] = ok
	}
	supported := false
	var capacityHint *Group
	var candidates []*candidateExecutionPath
	// Memoize pathContext within this selection only. Do not retain across
	// RevalidateTurn / account refresh — group policy and body can change.
	preparePath := candidatePathContextPreparer(r)
	for i := range accounts {
		account := &accounts[i]
		if _, excluded := options.excluded[account.ID]; excluded {
			diag.reject("excluded")
			continue
		}
		if r.continuationAccountID > 0 && account.ID != r.continuationAccountID {
			diag.reject("continuation_mismatch")
			continue
		}
		if r.forcePlatform != "" && account.Platform != r.forcePlatform {
			diag.reject("force_platform_mismatch")
			continue
		}
		sticky := account.ID == stickyID || account.ID == r.continuationAccountID
		var subscriptions, balances []Group
		paths := make(map[int64]*candidateExecutionPath)
		accountSupported := false
		for j := range r.groups {
			group := &r.groups[j]
			if !usable[group.ID] || !candidateAccountInGroup(account, group.ID) {
				continue
			}
			path, pathErr := r.evaluatePathWithPreparation(ctx, account, group, preparePath)
			if pathErr != nil {
				if !candidateIgnorableSupportError(pathErr) {
					failure = pathErr
					diag.reject("support_error")
				}
				continue
			}
			if path == nil {
				continue
			}
			path.sticky = sticky
			supported = true
			accountSupported = true
			// First supported group only — must NOT call lessUniversalBacking here
			// (gateway-tk forbids group sort order as an account-selection weight).
			if capacityHint == nil {
				capacityHint = group
			}
			if ready, reason := r.pathReadyReason(path, options); !ready {
				diag.reject(reason)
				continue
			}
			paths[group.ID] = path
			if group.IsSubscriptionType() {
				subscriptions = append(subscriptions, *group)
			} else {
				balances = append(balances, *group)
			}
		}
		accountReady := false
		for _, origins := range [][]Group{subscriptions, balances} {
			if len(origins) == 0 {
				continue
			}
			// Compare billing origins only after preserving the best legal request.
			preferredOrigins := make([]Group, 0, len(origins))
			bestRank := -1
			for _, origin := range origins {
				rank := candidateCompatibilityRank(paths[origin.ID])
				if bestRank < 0 || rank < bestRank {
					preferredOrigins, bestRank = preferredOrigins[:0], rank
				}
				if rank == bestRank {
					preferredOrigins = append(preferredOrigins, origin)
				}
			}
			group, originErr := selectCandidateBillingOrigin(ctx, r.key.UserID, account, preferredOrigins, r.model, r.shape, gw.channelService, gw.userGroupRateResolver, gw.accountRepo)
			if originErr != nil {
				failure = originErr
				diag.reject("billing_origin")
				continue
			}
			path := paths[group.ID]
			path.group = group
			candidates = append(candidates, path)
			accountReady = true
		}
		if accountSupported {
			diag.Supported++
		}
		if accountReady {
			diag.Ready++
		}
	}
	if supported && capacityHint != nil {
		platform := strings.TrimSpace(capacityHint.Platform)
		r.capacityPlatformHint = platform
		r.capacityGroupHint = capacityHint.ID
	}
	return candidates, supported, failure
}

func (r *CandidateRequest) resolvedBodyModelCandidates() []string {
	if !r.bodyModelsResolved {
		r.bodyModelCandidates = requestmodel.FromBodyCandidates(r.path, r.contentType, r.body)
		r.bodyModelsResolved = true
	}
	return r.bodyModelCandidates
}

// evaluatePath is the support projection shared by scheduling and discovery.
// It evaluates a complete authorization path without changing runtime state.
// Single-path callers (refresh) use an uncached pathContext so fresh group
// policy is visible; multi-account selection passes a per-call preparer.
func (r *CandidateRequest) evaluatePath(ctx context.Context, account *Account, group *Group) (*candidateExecutionPath, error) {
	return r.evaluatePathWithPreparation(ctx, account, group, r.pathContext)
}

func (r *CandidateRequest) evaluatePathWithPreparation(ctx context.Context, account *Account, group *Group, prepare func(context.Context, *Group) (context.Context, string, ChannelMappingResult, error)) (*candidateExecutionPath, error) {
	if !group.IsActive() || !candidateAccountInGroup(account, group.ID) || (r.forcePlatform != "" && account.Platform != r.forcePlatform) {
		return nil, nil
	}
	if !group.ModelAllowlist.Allows(r.model) {
		return nil, nil
	}
	if group.ModelAllowlistEnabled() {
		for _, model := range r.resolvedBodyModelCandidates() {
			if !group.ModelAllowlist.Allows(model) {
				return nil, nil
			}
		}
	}
	pathCtx, model, channel, err := prepare(ctx, group)
	if err != nil {
		return nil, err
	}
	supported, err := r.resolver.candidateGateway.candidateSupportsRequest(pathCtx, account, account.Platform, false, model, r.shape)
	if err != nil || !supported {
		return nil, err
	}
	path := &candidateExecutionPath{account: account, group: group, ctx: pathCtx, model: model, channel: channel}
	if plan, governed, err := protocolPlanForAccount(pathCtx, account, model); governed {
		if err != nil {
			return nil, err
		}
		path.plan = &plan
	}
	if !candidatePathAllowsEndpoint(pathCtx, account, group, r.shape, model, path.plan) {
		return nil, nil
	}
	return path, nil
}

func (r *CandidateRequest) pathReady(path *candidateExecutionPath, options candidateSelectOptions) bool {
	ready, _ := r.pathReadyReason(path, options)
	return ready
}

func (r *CandidateRequest) pathReadyReason(path *candidateExecutionPath, options candidateSelectOptions) (bool, string) {
	gw, openai := r.resolver.candidateGateway, r.resolver.candidateOpenAI
	path.ctx = r.withProfitControl(path)
	account, ctx := path.account, path.ctx
	if gw.isAccountBlockedBySchedulingThreshold(ctx, account) {
		return false, "scheduling_threshold"
	}
	if IsOpenAICompatPlatform(account.Platform) {
		if r.websocket {
			options.transport = OpenAIUpstreamTransportResponsesWebsocketV2Ingress
			if account.Platform == PlatformGrok {
				options.transport = OpenAIUpstreamTransportHTTPSSE
			}
		}
		if openai.isOpenAIAccountBlockedBySchedulingThreshold(ctx, account) {
			return false, "openai_scheduling_threshold"
		}
		req := OpenAIAccountScheduleRequest{GroupID: &path.group.ID, GroupPlatform: account.Platform,
			RequestedModel: path.model, RestrictionModel: path.model, RequirePrivacySet: path.group.RequirePrivacySet,
			RequiredTransport: options.transport, RequiredCapability: options.capability,
			RequiredImageCapability: options.imageCapability, RequiredVideoSupport: options.video, RequireCompact: options.compact}
		scheduler := &defaultOpenAIAccountScheduler{service: openai}
		eligible, stats := scheduler.openAICandidatesBeforeWindow(ctx, []Account{*account}, req)
		if len(eligible) == 0 {
			if reason := openAIFilterPrimaryReason(stats); reason != "" {
				return false, reason
			}
			return false, "openai_not_ready"
		}
		path.reserve = !openai.isAccountSchedulableForOpenAIWindow(ctx, account, path.sticky)
		return true, ""
	}
	if r.websocket || options.transport == OpenAIUpstreamTransportResponsesWebsocketV2 || options.transport == OpenAIUpstreamTransportResponsesWebsocketV2Ingress {
		return false, "websocket_unsupported"
	}
	if reason := gw.gatewayAccountEligibilityReason(ctx, account, account.Platform, false, path.model, path.sticky); reason != "" {
		return false, reason
	}
	path.reserve = !gw.isAccountSchedulableForWindowCost(ctx, account, path.sticky)
	return true, ""
}

func openAIFilterPrimaryReason(stats openAISelectionFilterStats) string {
	if len(stats.reasons) == 0 {
		return ""
	}
	reasons := make([]string, 0, len(stats.reasons))
	for reason := range stats.reasons {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	return "openai_" + reasons[0]
}

// selectAccount is shared by initial admission, real slot acquisition and retry.
// Account ID is deduplicated only after selecting the admitted payment tier.
func (r *CandidateRequest) selectAccount(ctx context.Context, options candidateSelectOptions) (*AccountSelectionResult, error) {
	paths, supported, evaluationErr := r.candidates(ctx, options)
	capacityPlatform := r.capacityPlatformHint
	if fp := strings.TrimSpace(r.forcePlatform); fp != "" {
		capacityPlatform = fp
	}
	if len(paths) == 0 {
		return nil, candidateSelectionError(supported, evaluationErr, r.model, capacityPlatform, r.capacityGroupHint, r.capacityDiag)
	}
	gw := r.resolver.candidateGateway
	for _, subscriptionTier := range []bool{true, false} {
		pool := make([]*candidateExecutionPath, 0, len(paths))
		for _, path := range paths {
			if path.group.IsSubscriptionType() == subscriptionTier {
				pool = append(pool, path)
			}
		}
		if len(pool) == 0 {
			continue
		}
		pool = recoverCandidateWindowPool(pool)
		loads := make(map[int64]*AccountLoadInfo)
		loadRequests := make([]AccountWithConcurrency, 0, len(pool))
		accounts := make([]*Account, 0, len(pool))
		for _, path := range pool {
			loadRequests = append(loadRequests, AccountWithConcurrency{ID: path.account.ID, MaxConcurrency: path.account.Concurrency})
			accounts = append(accounts, path.account)
		}
		if gw.concurrencyService != nil {
			var err error
			loads, err = gw.concurrencyService.GetAccountsLoadBatchFresh(ctx, loadRequests)
			if err != nil {
				slog.WarnContext(ctx, "candidate_load_read_failed", "error", err)
			}
		}
		counts := gw.candidateSaturationState().counts(ctx, accounts, r.model)
		failureCounts := r.failureCounts(ctx, pool)
		// Configured capacity is an admission limit, not evidence of quality.
		// Membership and group ordering never contribute a vote.
		sort.Slice(pool, func(i, j int) bool { return pool[i].account.ID < pool[j].account.ID })
		rand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		sort.SliceStable(pool, func(i, j int) bool {
			a, b := pool[i], pool[j]
			if candidateCompatibilityRank(a) != candidateCompatibilityRank(b) {
				return candidateCompatibilityRank(a) < candidateCompatibilityRank(b)
			}
			pa, pb := candidateSelectionEffectivePriority(a.account, counts, failureCounts), candidateSelectionEffectivePriority(b.account, counts, failureCounts)
			if pa != pb {
				return pa < pb
			}
			return a.sticky && !b.sticky
		})
		var waiting []*candidateExecutionPath
		for _, path := range pool {
			if candidateFull(path.account, loads) {
				waiting = append(waiting, path)
				continue
			}
			var acquired *AcquireResult
			if options.acquire {
				var err error
				acquired, err = gw.tryAcquireAccountSlot(ctx, path.account.ID, path.account.Concurrency)
				if err != nil {
					evaluationErr = err
					continue
				}
				if !acquired.Acquired {
					waiting = append(waiting, path)
					continue
				}
				if err := r.recheck(path, options); err != nil {
					acquired.ReleaseFunc()
					evaluationErr = err
					continue
				}
				if !gw.checkAndRegisterSession(ctx, path.account, r.session) {
					acquired.ReleaseFunc()
					continue
				}
			}
			if err := r.bind(ctx, path); err != nil {
				if acquired != nil && acquired.ReleaseFunc != nil {
					acquired.ReleaseFunc()
				}
				evaluationErr = err
				continue
			}
			result := attachSelectionProfitGate(path.ctx, &AccountSelectionResult{Account: path.account, ProtocolPlan: path.plan})
			r.selectionOptions = options
			if acquired != nil {
				result.Acquired = true
				result.ReleaseFunc = sync.OnceFunc(acquired.ReleaseFunc)
				if r.session != "" && gw.cache != nil {
					_ = gw.cache.SetSessionAccountID(path.ctx, 0, r.session, path.account.ID, time.Hour)
				}
			}
			return result, nil
		}
		// Busy subscription capacity retains payment priority over balance.
		for _, busy := range waiting {
			if err := r.bind(ctx, busy); err != nil {
				evaluationErr = err
				continue
			}
			cfg := gw.schedulingConfig()
			r.selectionOptions = options
			return attachSelectionProfitGate(busy.ctx, &AccountSelectionResult{Account: busy.account, ProtocolPlan: busy.plan,
				WaitPlan: &AccountWaitPlan{AccountID: busy.account.ID, MaxConcurrency: busy.account.Concurrency,
					Timeout: cfg.FallbackWaitTimeout, MaxWaiting: cfg.FallbackMaxWaiting}}), nil
		}
	}
	err := candidateSelectionError(supported, evaluationErr, r.model, capacityPlatform, r.capacityGroupHint, r.capacityDiag)
	if errors.Is(err, ErrUniversalCapacityUnavailable) && r.capacityDiag != nil && len(paths) > 0 {
		r.capacityDiag.reject("selection_exhausted")
	}
	return nil, err
}

func candidateCompatibilityRank(path *candidateExecutionPath) int {
	if path.plan == nil {
		return 0
	}
	return path.plan.CompatibilityRank()
}

func recoverCandidateWindowPool(paths []*candidateExecutionPath) []*candidateExecutionPath {
	ready := make([]*candidateExecutionPath, 0, len(paths))
	for _, path := range paths {
		if !path.reserve {
			ready = append(ready, path)
		}
	}
	if len(ready) > 0 {
		return ready
	}
	var best []*candidateExecutionPath
	bestUtil := 2.0
	for _, path := range paths {
		util, known := openAIAccountWindowUtilization(path.account, time.Now())
		if path.account.IsAnthropicOAuthOrSetupToken() {
			util, known = anthropicAccountWindowUtilization(path.account, time.Now())
		}
		if !known {
			util = 0
		}
		if len(best) == 0 || util < bestUtil {
			best, bestUtil = []*candidateExecutionPath{path}, util
		} else if util == bestUtil {
			best = append(best, path)
		}
	}
	if best == nil {
		return nil
	}
	return best
}

func (r *CandidateRequest) recheck(path *candidateExecutionPath, options candidateSelectOptions) error {
	group := path.group
	if r.key.IsUniversal() {
		groups, err := r.resolver.span(path.ctx, r.key.UserID)
		if err != nil {
			return err
		}
		group = nil
		for i := range groups {
			if groups[i].ID == path.group.ID {
				group = &groups[i]
				break
			}
		}
	} else if repo := r.resolver.candidateGateway.groupRepo; repo != nil {
		var err error
		group, err = repo.GetByID(path.ctx, path.group.ID)
		if err != nil {
			return err
		}
	}
	if group == nil || !group.IsActive() {
		return ErrUniversalNoEntitledGroup
	}
	fresh, err := r.resolver.candidateGateway.accountRepo.GetByID(path.ctx, path.account.ID)
	if err != nil {
		return err
	}
	if fresh == nil || !candidateAccountInGroup(fresh, path.group.ID) {
		return ErrUniversalCapacityUnavailable
	}
	rebuilt, err := r.evaluatePath(path.ctx, fresh, group)
	if err != nil {
		return err
	}
	if rebuilt == nil {
		return ErrUniversalCapacityUnavailable
	}
	rebuilt.sticky = path.sticky
	if !r.pathReady(rebuilt, options) {
		return ErrUniversalCapacityUnavailable
	}
	if path.plan != nil {
		request, ok := ProtocolRoutingRequest(rebuilt.ctx)
		if !ok {
			return ErrProtocolRouteUnavailable
		}
		snapshot, err := protocolAccountSnapshotForRouting(rebuilt.ctx, fresh, request)
		if err != nil {
			return err
		}
		freshPlan, err := planProtocolRoute(rebuilt.ctx, r.resolver.router, request, snapshot)
		if err != nil {
			return err
		}
		if !protocolPlansRoutingEquivalent(*path.plan, freshPlan) {
			return protocolrouter.ErrStalePlan
		}
	}
	// Selection and the wait consumer share this account pointer. A changed
	// route is rejected above; ordinary runtime fields can be refreshed in place.
	*path.account = *fresh
	path.group, path.ctx, path.model, path.channel = group, rebuilt.ctx, rebuilt.model, rebuilt.channel
	return nil
}

// RecheckCandidateAccountSlot closes the admission gap after a handler waited
// for capacity. The caller releases the newly acquired slot on any error.
func RecheckCandidateAccountSlot(ctx context.Context, accountID int64) error {
	r := CandidateRequestFromContext(ctx)
	if r == nil {
		return nil
	}
	if r.current == nil || r.current.account.ID != accountID {
		return ErrUniversalCapacityUnavailable
	}
	if err := r.recheck(r.current, r.selectionOptions); err != nil {
		return err
	}
	if err := r.bind(ctx, r.current); err != nil {
		return err
	}
	gw := r.resolver.candidateGateway
	if !gw.checkAndRegisterSession(ctx, r.current.account, r.session) {
		return ErrUniversalCapacityUnavailable
	}
	if r.session != "" && gw.cache != nil {
		_ = gw.cache.SetSessionAccountID(ctx, 0, r.session, accountID, time.Hour)
	}
	return nil
}

func candidateFull(account *Account, loads map[int64]*AccountLoadInfo) bool {
	load := loads[account.ID]
	return account.Concurrency > 0 && load != nil && load.CurrentConcurrency >= account.Concurrency
}

func selectCandidateFromContext(ctx context.Context, options candidateSelectOptions) (*AccountSelectionResult, bool, error) {
	request := CandidateRequestFromContext(ctx)
	if request == nil {
		return nil, false, nil
	}
	result, err := request.selectAccount(ctx, options)
	if err != nil && !openAIProxyStreamQuarantineBypassed(ctx) && request.resolver.candidateOpenAI.getOpenAIProxyStreamCircuit().activeBlockCount(time.Now()) > 0 {
		result, err = request.selectAccount(withOpenAIProxyStreamQuarantineBypass(ctx), options)
	}
	if err != nil {
		return nil, true, fmt.Errorf("candidate selection: %w", err)
	}
	return result, true, nil
}
