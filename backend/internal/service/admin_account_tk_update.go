package service

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// tkApplyUpdateAccountTKFields applies TokenKey account-update mutations and
// persists the account (including upstream billing settings). Call
// tkFinalizeUpdateAccountSave afterward for post-save side effects.
func (s *adminServiceImpl) tkApplyUpdateAccountTKFields(ctx context.Context, account *Account, input *UpdateAccountInput) error {
	var err error
	if IsSupplierManagedAccount(account) {
		if input.Extra != nil {
			input.Extra = StripSupplierReservedAccountExtra(input.Extra)
		}
	} else if err := ValidateSupplierReservedAccountExtra(input.Extra); err != nil {
		return err
	}
	var normalizedExtra map[string]any
	if input.Extra != nil {
		normalizedExtra, err = normalizeOpenAILongContextBillingUpdateExtra(account, input)
		if err != nil {
			return err
		}
		normalizedExtra, err = normalizeGrokMediaEligibilityUpdateExtra(account, input, normalizedExtra)
		if err != nil {
			return err
		}
	}
	previousProbeIdentity := upstreamBillingProbeIdentity(account)
	previousOllamaUsageIdentity := ollamaCloudUsageIdentity(account)
	// 安全/身份不变量(影子账号):通用更新路径被 edit/re-auth/refresh/batch 共用,
	// 必须在此守住,否则仅在创建时的保证可被这些路径绕过。
	if account.IsCredentialShadow() {
		// 影子绝不持有凭据(凭据只在母账号)——外审 F5。
		if !isAllowedSparkShadowCredentialsUpdate(input.Credentials) {
			return infraerrors.Newf(http.StatusBadRequest, "SPARK_SHADOW_NO_CREDENTIALS",
				"spark shadow accounts do not hold auth credentials; only model mapping can be configured on the shadow account")
		}
		// 影子 type 不可变——很多上游逻辑按 account.Type 分支(OAuth transform / ChatGPT
		// header 注入 / WS OAuth 决策),改成 apikey 会让 spark 影子被选中后按错误协议转发(外审 G7)。
		if input.Type != "" && input.Type != account.Type {
			return infraerrors.Newf(http.StatusBadRequest, "SPARK_SHADOW_IMMUTABLE_TYPE",
				"spark shadow account type cannot be changed; it must remain an OpenAI OAuth shadow")
		}
	} else if input.Type != "" && input.Type != account.Type && input.Type != AccountTypeOAuth {
		// 母账号守卫(外审 D/P1):有 spark 影子的账号不能把 type 改出 OpenAI OAuth——影子读透母
		// 凭据,母变成 apikey/setup_token 会让影子被调度后按错协议失败(resolveCredentialAccount
		// 必报错)。须先删影子再改 type。
		shadows, serr := s.accountRepo.ListShadowsByParent(ctx, account.ID)
		if serr != nil {
			return serr
		}
		if len(shadows) > 0 {
			return infraerrors.New(http.StatusBadRequest, "SPARK_SHADOW_PARENT_IMMUTABLE_TYPE",
				"cannot change account type while it has a spark shadow; delete the shadow first")
		}
	}
	wasOveragesEnabled := account.IsOveragesEnabled()

	if input.Name != "" {
		account.Name = input.Name
	}
	if input.Type != "" {
		account.Type = input.Type
	}
	if input.ChannelType != nil {
		account.ChannelType = *input.ChannelType
	}
	if input.Notes != nil {
		account.Notes = normalizeAccountNotes(input.Notes)
	}
	if account.IsCredentialShadow() && input.Credentials != nil {
		account.Credentials = sanitizeSparkShadowCredentials(input.Credentials)
	} else if len(input.Credentials) > 0 {
		// SSOT credential merge: sensitive preserve-when-omitted + never inherit
		// stale protocol identity (api_base_urls / protocol_endpoints_exclusive).
		account.Credentials = MergeAccountCredentials(
			account.Credentials, input.Credentials, account.ChannelType, CredentialMergeAdmin,
		)
		// 校验并规范化请求头覆写配置（header 名小写化、格式检查）
		if err := NormalizeHeaderOverrideCredentials(account.Credentials); err != nil {
			return err
		}
		// Strip SSO/password residue that must never sit next to OAuth tokens.
		account.Credentials = SanitizeStoredCredentials(account.Platform, account.Credentials)
		account.Credentials = FinalizeAccountCredentials(account.Credentials, account.ChannelType)
	}
	// Extra 使用 map：需要区分“未提供(nil)”与“显式清空({})”。
	// 关闭配额限制时前端会删除 quota_* 键并提交 extra:{}，此时也必须落库。
	requestedProbeEnabledUpdate := input.ProbeEnabled
	requestedRateSyncEnabledUpdate := input.RateSyncEnabled
	if input.Extra != nil {
		delete(normalizedExtra, SupportedProtocolsExtraKey)
		requestedProbeEnabled, hasRequestedProbeEnabled := normalizedExtra[UpstreamBillingProbeEnabledExtraKey]
		if hasRequestedProbeEnabled {
			enabled, ok := requestedProbeEnabled.(bool)
			if !ok {
				return infraerrors.BadRequest("INVALID_UPSTREAM_BILLING_PROBE_ENABLED", "upstream_billing_probe_enabled must be a boolean")
			}
			if requestedProbeEnabledUpdate != nil && *requestedProbeEnabledUpdate != enabled {
				return infraerrors.BadRequest("CONFLICTING_UPSTREAM_BILLING_PROBE_ENABLED", "conflicting upstream_billing_probe_enabled values")
			}
			requestedProbeEnabledUpdate = &enabled
		}
		delete(normalizedExtra, UpstreamBillingProbeEnabledExtraKey)
		delete(normalizedExtra, UpstreamBillingRateSyncEnabledExtraKey)
		delete(normalizedExtra, UpstreamBillingProbeExtraKey)
		delete(normalizedExtra, OllamaCloudUsageSessionExtraKey)
		delete(normalizedExtra, OllamaCloudUsageAutoRefreshExtraKey)
		delete(normalizedExtra, OllamaCloudUsageSnapshotExtraKey)
		// 保留配额用量和专用服务受管字段，防止普通账号编辑意外覆盖。
		for _, key := range []string{
			SupportedProtocolsExtraKey,
			"quota_used",
			"quota_daily_used",
			"quota_daily_start",
			"quota_weekly_used",
			"quota_weekly_start",
			grokBillingExtraKey,
			UpstreamBillingProbeEnabledExtraKey,
			UpstreamBillingRateSyncEnabledExtraKey,
			UpstreamBillingProbeExtraKey,
			OllamaCloudUsageSessionExtraKey,
			OllamaCloudUsageAutoRefreshExtraKey,
			OllamaCloudUsageSnapshotExtraKey,
			SupplierSourceIDExtraKey,
			SupplierDiscountBandExtraKey,
		} {
			if v, ok := account.Extra[key]; ok {
				normalizedExtra[key] = v
			}
		}
		normalizedExtra = prepareCodexFingerprintExtraForUpdate(account, normalizedExtra)
		account.Extra = PreserveSupplierManagedExtraKeys(account, normalizedExtra)
		if account.Platform == PlatformAntigravity && wasOveragesEnabled && !account.IsOveragesEnabled() {
			delete(account.Extra, "antigravity_credits_overages") // 清理旧版 overages 运行态
			// 清除 AICredits 限流 key
			if rawLimits, ok := account.Extra[modelRateLimitsKey].(map[string]any); ok {
				delete(rawLimits, creditsExhaustedKey)
			}
		}
		if account.Platform == PlatformAntigravity && !wasOveragesEnabled && account.IsOveragesEnabled() {
			delete(account.Extra, modelRateLimitsKey)
			delete(account.Extra, "antigravity_credits_overages") // 清理旧版 overages 运行态
		}
		// 校验并预计算固定时间重置的下次重置时间
		if err := ValidateQuotaResetConfig(account.Extra); err != nil {
			return err
		}
		ComputeQuotaResetAt(account.Extra)
		NormalizeFixedQuotaWindows(account.Extra)
	}
	if input.AccountEmail != nil {
		var err error
		account.Extra, account.Credentials, err = ApplyAccountEmail(account.Extra, account.Credentials, *input.AccountEmail)
		if err != nil {
			return err
		}
	}
	if input.Extra == nil {
		account.Extra = prepareCodexFingerprintExtraForUpdate(account, account.Extra)
	}
	if requestedRateSyncEnabledUpdate != nil && *requestedRateSyncEnabledUpdate {
		if requestedProbeEnabledUpdate != nil && !*requestedProbeEnabledUpdate {
			return infraerrors.BadRequest(
				"UPSTREAM_BILLING_RATE_SYNC_REQUIRES_PROBE",
				"upstream billing rate sync requires upstream billing probe",
			)
		}
		enabled := true
		requestedProbeEnabledUpdate = &enabled
	}
	if requestedProbeEnabledUpdate != nil && !*requestedProbeEnabledUpdate {
		disabled := false
		requestedRateSyncEnabledUpdate = &disabled
	}
	if (requestedProbeEnabledUpdate != nil && *requestedProbeEnabledUpdate) ||
		(requestedRateSyncEnabledUpdate != nil && *requestedRateSyncEnabledUpdate) {
		if !isUpstreamBillingProbeAccount(account) {
			return ErrUpstreamBillingProbeAccountInvalid
		}
		if requestedProbeEnabledUpdate != nil && *requestedProbeEnabledUpdate && !upstreamBillingProbeSupportsSub2APIBilling(account) {
			return ErrUpstreamBillingProbeAccountInvalid
		}
	}
	if account.Extra == nil && (requestedProbeEnabledUpdate != nil || requestedRateSyncEnabledUpdate != nil) {
		account.Extra = make(map[string]any)
	}
	if requestedProbeEnabledUpdate != nil {
		account.Extra[UpstreamBillingProbeEnabledExtraKey] = *requestedProbeEnabledUpdate
	}
	if requestedRateSyncEnabledUpdate != nil {
		account.Extra[UpstreamBillingRateSyncEnabledExtraKey] = *requestedRateSyncEnabledUpdate
	}
	// 影子代理恒继承母账号(由 propagateProxyToShadows 同步),不接受独立编辑——外审 B/P1;
	// 否则要等母账号下次改 proxy 才被覆盖,期间影子会出现"有时继承、有时独立"的漂移。
	if input.ProxyID != nil && !account.IsCredentialShadow() {
		// 0 表示清除代理（前端发送 0 而不是 null 来表达清除意图）
		if *input.ProxyID == 0 {
			account.ProxyID = nil
		} else {
			account.ProxyID = input.ProxyID
		}
		account.Proxy = nil // 清除关联对象，防止 GORM Save 时根据 Proxy.ID 覆盖 ProxyID
	}
	if !reflect.DeepEqual(previousProbeIdentity, upstreamBillingProbeIdentity(account)) && account.Extra != nil {
		delete(account.Extra, UpstreamBillingProbeExtraKey)
		if !isUpstreamBillingProbeAccount(account) {
			delete(account.Extra, UpstreamBillingProbeEnabledExtraKey)
			delete(account.Extra, UpstreamBillingRateSyncEnabledExtraKey)
		}
	}
	if account.Extra != nil {
		if !IsOllamaCloudUsageAccount(account) {
			delete(account.Extra, OllamaCloudUsageSessionExtraKey)
			delete(account.Extra, OllamaCloudUsageAutoRefreshExtraKey)
			delete(account.Extra, OllamaCloudUsageSnapshotExtraKey)
		} else if !reflect.DeepEqual(previousOllamaUsageIdentity, ollamaCloudUsageIdentity(account)) {
			delete(account.Extra, OllamaCloudUsageSessionExtraKey)
			delete(account.Extra, OllamaCloudUsageAutoRefreshExtraKey)
			delete(account.Extra, OllamaCloudUsageSnapshotExtraKey)
		}
	}
	// 只在指针非 nil 时更新 Concurrency（支持设置为 0）
	if input.Concurrency != nil {
		account.Concurrency = normalizeAccountConcurrency(account.Platform, account.Type, *input.Concurrency)
	}
	// 只在指针非 nil 时更新 Priority（支持设置为 0）
	if input.Priority != nil {
		account.Priority = *input.Priority
	}
	if input.RateMultiplier != nil {
		if *input.RateMultiplier < 0 {
			return errors.New("rate_multiplier must be >= 0")
		}
		// 同步开启时倍率归上游所有，手工值活不过下一次成功探测（表现为"改了又自己
		// 变回去"），与批量路径一样直接拒绝。判断的是本次请求生效后的状态：上面
		// 已把请求携带的两个开关落进 account.Extra，所以"同一请求关闭同步 + 改倍率"
		// （用户显式收回所有权）会走到这里时读到 false，正常放行。
		if upstreamBillingRateSyncEnabled(account) {
			return ErrUpstreamBillingRateSyncConflict
		}
		account.RateMultiplier = input.RateMultiplier
	}
	if input.TierID != nil {
		if *input.TierID <= 0 {
			account.TierID = nil
		} else {
			account.TierID = input.TierID
		}
	}
	if input.LoadFactor != nil {
		if *input.LoadFactor <= 0 {
			account.LoadFactor = nil // 0 或负数表示清除
		} else if *input.LoadFactor > 10000 {
			return errors.New("load_factor must be <= 10000")
		} else {
			account.LoadFactor = input.LoadFactor
		}
	}
	if input.Status != "" {
		account.Status = input.Status
	}
	if input.ExpiresAt != nil {
		if *input.ExpiresAt <= 0 {
			account.ExpiresAt = nil
		} else {
			expiresAt := time.Unix(*input.ExpiresAt, 0)
			account.ExpiresAt = &expiresAt
		}
	}
	if input.AutoPauseOnExpired != nil {
		account.AutoPauseOnExpired = *input.AutoPauseOnExpired
	}

	// 先验证分组是否存在（在任何写操作之前）
	if input.GroupIDs != nil {
		if err := s.validateGroupIDsExist(ctx, *input.GroupIDs); err != nil {
			return err
		}

		// 检查混合渠道风险（除非用户已确认）
		if !input.SkipMixedChannelCheck {
			if err := s.checkMixedChannelRisk(ctx, account.ID, account.Platform, *input.GroupIDs); err != nil {
				return err
			}
		}
	}
	if err := resolveNewAPIMoonshotBaseURLOnSave(ctx, account); err != nil {
		return err
	}
	if account.Platform == PlatformGrok &&
		tkInputHasNonEmptyCredential(input.Credentials, "refresh_token") {
		if err := resolveGrokTokenOnSave(ctx, account); err != nil {
			return err
		}
	}
	SeedOfficialSupportedProtocols(account)

	billingSettingsAppliedAtomically := false
	updater := s.accountBillingRepo
	if updater == nil {
		// Unit tests and narrow internal callers may construct adminServiceImpl
		// directly; production wiring requires this capability through
		// AdminAccountRepository.
		updater, _ = s.accountRepo.(AccountBillingSettingsRepository)
	}
	if updater != nil {
		if err := updater.UpdateWithAccountBillingSettings(
			ctx,
			account,
			requestedProbeEnabledUpdate,
			requestedRateSyncEnabledUpdate,
			input.RateMultiplier,
		); err != nil {
			return err
		}
		billingSettingsAppliedAtomically = true
	}
	if !billingSettingsAppliedAtomically {
		if err := s.accountRepo.Update(ctx, account); err != nil {
			return err
		}
		if (requestedProbeEnabledUpdate != nil || requestedRateSyncEnabledUpdate != nil) &&
			isUpstreamBillingProbeAccount(account) {
			settings := make(map[string]any, 2)
			if requestedProbeEnabledUpdate != nil {
				settings[UpstreamBillingProbeEnabledExtraKey] = *requestedProbeEnabledUpdate
			}
			if requestedRateSyncEnabledUpdate != nil {
				settings[UpstreamBillingRateSyncEnabledExtraKey] = *requestedRateSyncEnabledUpdate
			}
			if err := s.accountRepo.UpdateExtra(ctx, account.ID, settings); err != nil {
				return err
			}
		}
	}
	return nil
}

// tkFinalizeUpdateAccountSave runs post-persist UpdateAccount side effects:
// proxy propagation to spark shadows, public-group aggregator policy, and
// group rebinding.
func (s *adminServiceImpl) tkFinalizeUpdateAccountSave(ctx context.Context, account *Account, input *UpdateAccountInput) error {
	// 将 proxy 变更传播到 spark 影子账号（同步；Update 内部已触发调度快照）。
	// 影子自身 proxy 不可独立编辑(见上),故对影子的更新不触发传播。
	if input.ProxyID != nil && !account.IsCredentialShadow() {
		if err := s.propagateProxyToShadows(ctx, account.ID, account.ProxyID); err != nil {
			return err
		}
	}

	// 绑定分组
	effectiveGroupIDs := account.GroupIDs
	if input.GroupIDs != nil {
		effectiveGroupIDs = *input.GroupIDs
	}
	if len(effectiveGroupIDs) > 0 {
		if err := s.checkPublicGroupAggregatorChannelPolicy(ctx, account.ID, account.Name, account.Platform, account.ChannelType, account.Credentials, effectiveGroupIDs); err != nil {
			return err
		}
	}
	if input.GroupIDs != nil {
		if err := s.accountRepo.BindGroups(ctx, account.ID, *input.GroupIDs); err != nil {
			return err
		}
	}
	return nil
}
