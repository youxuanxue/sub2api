package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// CandidateRequest owns the authorized execution and billing path for one
// request. Handlers retain their forwarding and reservation lifecycle.
type CandidateRequest struct {
	resolver              *UniversalRoutingResolver
	key                   *APIKey
	groups                []Group
	shape                 UniversalShape
	path                  string
	model                 string
	body                  []byte
	contentType           string
	forcePlatform         string
	websocket             bool
	session               string
	promptCacheSession    string
	hasSoftAffinity       bool // Recomputed from the binding and account pool on each selection.
	continuationAccountID int64
	current               *candidateExecutionPath
	subscription          *UserSubscription
	onBind                func(context.Context, *Group, *UserSubscription)
	billingHook           func() error
	balanceReserved       bool
	inflightReservation   *InflightReservation
	selectionOptions      candidateSelectOptions
	rpm                   candidateRPMAdmission
	chatAttempts          int
	chatDeadline          time.Time
	capacityPlatformHint  string
	capacityGroupHint     int64
	capacityDiag          *CandidateCapacityDiag
	bodyModelCandidates   []string
	bodyModelsResolved    bool
	requestProfile        protocolrouter.RequestProfile
	requestProfileValid   bool
	// Request-scoped account list shared by admission and the following
	// scheduler select. Concurrency load is not cached: the acquiring select
	// runs after billing and user-slot waits, and a stale "full" bit would
	// queue on one account while another has already freed a slot.
	accountsCached    bool
	cachedAccounts    []Account
	cachedAccountsKey string
	skipGroupRefresh  bool
	// admissionPaths are the static candidate paths built by the universal
	// ingress pass. The handler immediately selects again after billing/user
	// admission; reusing these paths avoids evaluating the same account×group
	// policy twice. The selected path is still rechecked against fresh group and
	// account state before a slot is committed.
	admissionPaths      []*candidateExecutionPath
	admissionPathsValid bool
	admissionPathsUsed  bool
}

type candidateRequestContextKey struct{}

type candidateExecutionPath struct {
	account *Account
	group   *Group
	ctx     context.Context
	model   string
	plan    *protocolrouter.Plan
	channel ChannelMappingResult
	reserve bool
	sticky  bool
}

type candidateSelectOptions struct {
	excluded        map[int64]struct{}
	transport       OpenAIUpstreamTransport
	capability      OpenAIEndpointCapability
	imageCapability OpenAIImagesCapability
	video           bool
	compact         bool
	acquire         bool
}

func CandidateRequestFromContext(ctx context.Context) *CandidateRequest {
	if ctx == nil {
		return nil
	}
	r, _ := ctx.Value(candidateRequestContextKey{}).(*CandidateRequest)
	return r
}

func CandidateSubscription(ctx context.Context, fallback *UserSubscription) *UserSubscription {
	if r := CandidateRequestFromContext(ctx); r != nil {
		return r.subscription
	}
	return fallback
}

func CandidateExecutionPlatform(ctx context.Context) (string, bool) {
	if r := CandidateRequestFromContext(ctx); r != nil && r.current != nil {
		return r.current.account.Platform, true
	}
	return "", false
}

func CandidateSessionHash(ctx context.Context) (string, bool) {
	if r := CandidateRequestFromContext(ctx); r != nil {
		return r.session, true
	}
	return "", false
}

func CandidateChannelMapping(ctx context.Context, fallback ChannelMappingResult) ChannelMappingResult {
	if r := CandidateRequestFromContext(ctx); r != nil && r.current != nil {
		return r.current.channel
	}
	return fallback
}

func CandidateEffectiveModel(ctx context.Context, fallback string) string {
	if r := CandidateRequestFromContext(ctx); r != nil && r.current != nil {
		return r.current.model
	}
	return fallback
}

// The canonical candidate request already includes channel and Direct model
// preprocessing. Forwarders consume it without applying the rewrite again.
func CandidateForwardMapping(ctx context.Context, fallback ChannelMappingResult) ChannelMappingResult {
	if r := CandidateRequestFromContext(ctx); r != nil && r.current != nil {
		return ChannelMappingResult{MappedModel: r.current.model}
	}
	return fallback
}

func SetCandidateBillingHook(ctx context.Context, hook func() error) bool {
	if r := CandidateRequestFromContext(ctx); r != nil {
		r.billingHook = hook
		return true
	}
	return false
}

func SetCandidateBalanceReserved(ctx context.Context, reserved bool) {
	if r := CandidateRequestFromContext(ctx); r != nil {
		r.balanceReserved = reserved
	}
}

// SetBindingObserver keeps the middleware's request-local group/subscription
// view current when selection or failover changes an authorized origin.
func (r *CandidateRequest) SetBindingObserver(observer func(context.Context, *Group, *UserSubscription)) {
	r.onBind = observer
	if r.current != nil && observer != nil {
		observer(r.current.ctx, r.current.group, r.subscription)
	}
}

func (r *UniversalRoutingResolver) CandidateSchedulingEnabled() bool {
	return r != nil && r.candidateGateway != nil && r.candidateOpenAI != nil
}

// PrepareCandidateRequest selects an initial billing path without taking an
// account slot. The real selector repeats the same policy and acquires capacity.
func (r *UniversalRoutingResolver) PrepareCandidateRequest(ctx context.Context, key *APIKey, shape UniversalShape, path, model string, body []byte, session, forcedPlatform string) (context.Context, *CandidateRequest, error) {
	return r.prepareCandidateRequest(ctx, key, shape, path, model, body, session, forcedPlatform, false, "application/json", "")
}

func (r *UniversalRoutingResolver) PrepareCandidateWebSocket(ctx context.Context, key *APIKey, path, model string, body []byte, session, forcedPlatform string) (context.Context, *CandidateRequest, error) {
	return r.prepareCandidateRequest(ctx, key, ShapeOpenAIChat, path, model, body, session, forcedPlatform, true, "application/json", "")
}

// authorizeResponsesContinuation pins an owned previous_response_id to the
// account that served it. A missing binding, another user's id, a message id,
// or a failed lookup is removed from the body and is not pinned, so selection
// serves the current body without forwarding the pointer.
func (s *OpenAIGatewayService) authorizeResponsesContinuation(ctx context.Context, key *APIKey, eligible []Group, body []byte) ([]byte, int64, error) {
	previous := strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String())
	if previous == "" {
		return body, 0, nil
	}
	if s != nil {
		accountID, err := s.ResolveCandidateContinuation(ctx, key, eligible, previous)
		if err == nil && accountID > 0 {
			return body, accountID, nil
		}
	}
	stripped, stripErr := DropUnauthorizedPreviousResponseID(body)
	if stripErr != nil || bytes.Equal(stripped, body) {
		return nil, 0, ErrCandidateContinuationUnavailable
	}
	return stripped, 0, nil
}

func (r *UniversalRoutingResolver) prepareCandidateRequest(ctx context.Context, key *APIKey, shape UniversalShape, path, model string, body []byte, session, forcedPlatform string, websocket bool, contentType, promptCacheSession string) (context.Context, *CandidateRequest, error) {
	if !r.CandidateSchedulingEnabled() || key == nil || shape == ShapeSkip || strings.TrimSpace(model) == "" {
		return ctx, nil, nil
	}
	var groups []Group
	if key.IsUniversal() {
		var err error
		groups, err = r.span(ctx, key.UserID)
		if err != nil {
			return ctx, nil, err
		}
		ctx = WithUniversalKeyRouting(ctx)
	} else if key.Group != nil {
		groups = []Group{*key.Group}
	}
	eligible := make([]Group, 0, len(groups))
	for _, group := range groups {
		if group.IsActive() && (!key.IsUniversal() || !isUniversalProbeGroup(group)) {
			eligible = append(eligible, group)
		}
	}
	if len(eligible) == 0 {
		return ctx, nil, ErrUniversalNoEntitledGroup
	}
	ctx = context.WithValue(ctx, ctxkey.UserID, key.UserID)
	ctx = WithCandidateIdentity(ctx, key.UserID, key.ID)
	state := &CandidateRequest{resolver: r, key: key, groups: eligible, shape: shape, path: path, model: model,
		// pathContext rewrites "model" with sjson.SetBytes. A same-length
		// replacement edits the backing array in place, so this copy keeps the
		// caller's bytes (handler forward body, canonical digest) intact.
		body: append([]byte(nil), body...), contentType: contentType, forcePlatform: forcedPlatform, session: session, websocket: websocket, promptCacheSession: promptCacheSession}
	canonicalInbound, canonicalPath, canonicalShapeOK := candidateCanonicalProtocol(shape, path)
	if canonical, ok := ProtocolRoutingRequest(ctx); ok && canonicalShapeOK &&
		canonical.InboundProtocol() == canonicalInbound && canonical.ResponsesPath() == canonicalPath && bytes.Equal(canonical.Body(), body) {
		state.requestProfile, state.requestProfileValid = canonical.Profile(), true
	} else {
		state.requestProfile, state.requestProfileValid = candidateRequestProfile(shape, path, model, body)
	}
	ctx = context.WithValue(ctx, candidateRequestContextKey{}, state)
	nextBody, owner, err := r.candidateOpenAI.authorizeResponsesContinuation(ctx, key, eligible, body)
	if err != nil {
		return ctx, nil, err
	}
	if websocket && !bytes.Equal(nextBody, body) {
		// The forwarded WebSocket frame is the caller's original payload.
		// Rewriting only this planning copy would still send an unverified
		// previous_response_id upstream.
		return ctx, nil, ErrCandidateContinuationUnavailable
	}
	if !bytes.Equal(nextBody, body) {
		body = nextBody
		state.body = append([]byte(nil), body...)
		ctx = r.WithRequest(ctx, shape, path, model, body)
		if refreshed, ok := ProtocolRoutingRequest(ctx); ok {
			state.requestProfile, state.requestProfileValid = refreshed.Profile(), true
		} else {
			state.requestProfile, state.requestProfileValid = candidateRequestProfile(shape, path, model, body)
		}
	} else if owner > 0 {
		state.continuationAccountID = owner
	}

	_, err = state.selectAccount(ctx, candidateSelectOptions{})
	if err != nil {
		return ctx, nil, err
	}
	return state.current.ctx, state, nil
}

// RevalidateTurn pins the execution account while rechecking authorization and
// payment before a subsequent WebSocket turn reserves funds.
func (r *CandidateRequest) RevalidateTurn(ctx context.Context, accountID int64, model string, body []byte) error {
	if previous := strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String()); previous != "" {
		owner, err := r.resolver.candidateOpenAI.ResolveCandidateContinuation(ctx, r.key, r.groups, previous)
		if err != nil {
			return err
		}
		if owner != accountID {
			return ErrCandidateContinuationUnavailable
		}
	}
	r.current, r.billingHook = nil, nil
	r.rpm = candidateRPMAdmission{}
	// A turn can observe a group allowlist or profit multiplier that changed
	// after admission. Drop the one-shot scheduler reuse before selecting again.
	r.skipGroupRefresh = false
	r.accountsCached = false
	r.admissionPaths = nil
	r.admissionPathsValid = false
	r.admissionPathsUsed = false
	r.continuationAccountID = accountID
	r.model, r.body = model, append([]byte(nil), body...)
	r.bodyModelsResolved = false
	r.bodyModelCandidates = nil
	r.requestProfile, r.requestProfileValid = candidateRequestProfile(r.shape, r.path, model, body)
	ctx = r.withRequest(ctx, model, body)
	_, err := r.selectAccount(ctx, candidateSelectOptions{})
	return err
}

func (r *CandidateRequest) bind(ctx context.Context, selected *candidateExecutionPath) error {
	defer BeginImageIngressPhase(ctx, ImageIngressBind)()
	subscription, err := r.admit(ctx, selected.group)
	if err != nil {
		return err
	}
	if billing := r.resolver.candidateGateway.billingCacheService; billing != nil && r.rpm.started {
		if err := r.checkRPM(ctx, billing, r.key.User, selected.group); err != nil {
			return err
		}
	}
	changed := r.current != nil && (r.current.group.ID != selected.group.ID || r.current.account.ID != selected.account.ID)
	previous, previousSubscription := r.current, r.subscription
	previousGroup, previousGroupID := r.key.Group, r.key.GroupID
	hadBalanceReservation := r.balanceReserved
	rollback := func() {
		r.current, r.subscription = previous, previousSubscription
		r.key.Group, r.key.GroupID = previousGroup, previousGroupID
		// The hold hook owns balanceReserved: a successful release followed by
		// a failed reserve must not resurrect coverage from the previous hold.
		if r.onBind != nil && previous != nil {
			r.onBind(previous.ctx, previousGroup, previousSubscription)
		}
	}
	r.current, r.subscription = selected, subscription
	r.key.Group = selected.group
	gid := selected.group.ID
	r.key.GroupID = &gid
	selected.ctx = context.WithValue(selected.ctx, ctxkey.Group, selected.group)
	if r.onBind != nil {
		r.onBind(selected.ctx, selected.group, subscription)
	}
	if changed && r.billingHook != nil {
		if err := r.billingHook(); err != nil {
			rollback()
			return err
		}
		if hadBalanceReservation && !r.balanceReserved && subscription == nil {
			// Reservation outages retain the existing serve-without-hold policy,
			// but the released hold no longer covers the ordinary wallet gate.
			if _, err := r.admit(ctx, selected.group); err != nil {
				rollback()
				return err
			}
		}
	}
	return nil
}

func (r *CandidateRequest) admit(ctx context.Context, group *Group) (*UserSubscription, error) {
	var subscription *UserSubscription
	if group.IsSubscriptionType() {
		if r.resolver.candidateSubscriptions == nil {
			return nil, ErrBillingServiceUnavailable
		}
		var err error
		subscription, err = r.resolver.candidateSubscriptions.GetActiveSubscription(ctx, r.key.UserID, group.ID)
		if err != nil {
			return nil, err
		}
	}
	if r.key.User == nil {
		return nil, ErrBillingServiceUnavailable
	}
	if billing := r.resolver.candidateGateway.billingCacheService; billing != nil {
		prospective := *r.key
		prospective.Group = group
		prospective.GroupID = &group.ID
		if err := billing.CheckCandidateBillingEligibility(ctx, r.key.User, &prospective, group, subscription); err != nil {
			return nil, err
		}
		if err := r.peekGroupRPM(ctx, billing, r.key.User, group); err != nil {
			return nil, err
		}
	}
	return subscription, nil
}

func (r *CandidateRequest) pathContext(ctx context.Context, group *Group) (context.Context, string, ChannelMappingResult, error) {
	model := r.model
	if !r.key.IsUniversal() && group.Platform == PlatformComposite {
		decision, err := r.resolver.candidateGateway.compositeResolver.Resolve(ctx, group.ID, model, CompositeRouteEndpointForPath(r.path))
		if err != nil {
			return ctx, "", ChannelMappingResult{}, err
		}
		if decision.Matched && decision.Source == CompositeRouteSourceExplicit {
			model = decision.UpstreamModel
		}
	}
	body := r.body
	if request, ok := ProtocolRoutingRequest(ctx); ok && !UsesGeminiImagesAdapter(r.shape, r.model) {
		body = request.Body()
	}
	mapping := ChannelMappingResult{MappedModel: model}
	if channels := r.resolver.candidateGateway.channelService; channels != nil {
		lookup, err := channels.lookupGroupChannel(ctx, group.ID)
		if err != nil {
			return ctx, "", mapping, err
		}
		if lookup != nil {
			mapped := resolveMapping(lookup, group.ID, model)
			mapping = mapped
			model = mapped.MappedModel
			billingModel := billingModelForRestriction(mapped.BillingModelSource, r.model, model)
			if billingModel != "" && checkRestricted(lookup, group.ID, billingModel) {
				return ctx, "", mapping, ErrUniversalUnsupportedModel
			}
		}
	}
	if !r.key.IsUniversal() && IsOpenAICompatPlatform(group.Platform) && (r.shape == ShapeAnthropicMessages || r.shape == ShapeOpenAIChat || r.shape == ShapeAnthropicCountTokens) {
		if mapped := strings.TrimSpace(group.ResolveMessagesDispatchModel(model)); mapped != "" {
			model = mapped
		}
	}
	if !r.key.IsUniversal() && group.Platform == PlatformGemini && (r.shape == ShapeAnthropicMessages || r.shape == ShapeAnthropicCountTokens) {
		if mapped := group.TKResolveGeminiDispatchModel(model); mapped != "" {
			model = mapped
		}
	}
	// GetBytes.Exists is enough — ValidBytes would walk the full payload again.
	if r.shape != ShapeGemini && gjson.GetBytes(body, "model").Exists() {
		var err error
		body, err = sjson.SetBytes(body, "model", model)
		if err != nil {
			return ctx, "", mapping, err
		}
	}
	ctx = r.withRequest(ctx, model, body)
	ctx = withProtocolNativeOnly(ctx, (r.shape == ShapeAnthropicMessages || r.shape == ShapeAnthropicCountTokens) && !group.AllowMessagesDispatch)
	ctx = context.WithValue(ctx, ctxkey.Group, group)
	return ctx, model, mapping, nil
}

func (r *CandidateRequest) withRequest(ctx context.Context, model string, body []byte) context.Context {
	if r.requestProfileValid {
		ctx = r.resolver.WithRequestProfile(ctx, r.shape, r.path, model, body, r.requestProfile)
	} else {
		ctx = r.resolver.WithRequest(ctx, r.shape, r.path, model, body)
	}
	return ctx
}

func candidateAccountInGroup(account *Account, groupID int64) bool {
	for _, id := range account.GroupIDs {
		if id == groupID {
			return true
		}
	}
	for _, edge := range account.AccountGroups {
		if edge.GroupID == groupID {
			return true
		}
	}
	return false
}

func candidatePathAllowsEndpoint(ctx context.Context, account *Account, group *Group, shape UniversalShape, model string, plan *protocolrouter.Plan) bool {
	if group.ClaudeCodeOnly && !IsClaudeCodeClient(ctx) && !IsClaudeDesktopGatewayClient(ctx) {
		return false
	}
	if group.RequireOAuthOnly && account.Type == AccountTypeAPIKey {
		return false
	}
	if group.RequirePrivacySet && !account.IsPrivacySet() {
		return false
	}
	if universalShapeRequiresImageGenerationEnabled(shape) && !group.AllowImageGeneration {
		return false
	}
	if request, ok := ProtocolRoutingRequest(ctx); ok && !group.AllowImageGeneration &&
		IsExplicitImageGenerationIntent("", model, request.Body()) {
		return false
	}
	if shape == ShapeAnthropicMessages || shape == ShapeAnthropicCountTokens {
		// The dispatch switch authorizes conversion, not native Messages.
		if plan != nil {
			return plan.TargetProtocol() == protocolrouter.ProtocolMessages || group.AllowMessagesDispatch
		}
		if IsOpenAICompatPlatform(account.Platform) {
			for _, supported := range routingSupportedProtocols(account) {
				if supported == protocolrouter.ProtocolMessages {
					return true
				}
			}
			return group.AllowMessagesDispatch
		}
	}
	return true
}

func candidateSelectionError(supported bool, err error, model, platformHint string, groupID int64, diag *CandidateCapacityDiag) error {
	if !supported && unsupportedRequestSelection(err) {
		return fmt.Errorf("%w: %s", ErrUniversalUnsupportedRequest, model)
	}
	if err != nil && (!supported || !errors.Is(err, protocolrouter.ErrNoLegalRoute)) {
		capacity := newUniversalCapacityError(platformHint, groupID, diag)
		if remapped, ok := remapProtocolSelectionFailure(err, capacity); ok {
			return remapped
		}
		return err
	}
	if supported {
		return newUniversalCapacityError(platformHint, groupID, diag)
	}
	return fmt.Errorf("%w: %s", ErrUniversalUnsupportedModel, model)
}

// remapProtocolSelectionFailure keeps a supported-but-not-ready selection off
// opaque platform 500s ("Failed to prepare authorized candidates").
//
// A finished selection where nothing supported the request is classified first
// by unsupportedRequestSelection, including unknown or conflicted capability
// evidence. That is a client 400, not a busy pool and not unsupported-model —
// see candidate-eligibility SSOT /
// TestCandidateEligibilityUnknownCapabilityIsNotEntitlement.
func unsupportedRequestSelection(err error) bool {
	return err != nil &&
		(errors.Is(err, ErrProtocolRouteUnavailable) || errors.Is(err, ErrProtocolCapabilityUnknown))
}

func remapProtocolSelectionFailure(err, capacity error) (error, bool) {
	if err == nil {
		return nil, false
	}
	if !errors.Is(err, ErrProtocolCapabilityUnknown) && !errors.Is(err, ErrProtocolRouteUnavailable) {
		return nil, false
	}
	if capacity != nil {
		return capacity, true
	}
	return ErrUniversalCapacityUnavailable, true
}

func candidateIgnorableSupportError(err error) bool {
	if errors.Is(err, ErrProtocolCapabilityUnknown) {
		return false
	}
	return errors.Is(err, ErrUniversalUnsupportedModel) || errors.Is(err, protocolrouter.ErrModelNotAllowed) || errors.Is(err, protocolrouter.ErrNoLegalRoute)
}
