// Package domain error sentinels.
//
// These sentinel errors are shared across layers (service, repository, handler).
// Moving them here breaks the repository -> service import for error identity checks.
//
// Migration note: service/ re-exports each symbol as a var alias so existing
// callers (handler/, service/ internal) continue to compile unchanged.
package domain

import (
	"errors"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// ---- entity not-found sentinels ----

var (
	ErrUserNotFound    = infraerrors.NotFound("USER_NOT_FOUND", "user not found")
	ErrAPIKeyNotFound  = infraerrors.NotFound("API_KEY_NOT_FOUND", "api key not found")
	ErrAccountNotFound = infraerrors.NotFound("ACCOUNT_NOT_FOUND", "account not found")
	ErrGroupNotFound   = infraerrors.NotFound("GROUP_NOT_FOUND", "group not found")
	ErrChannelNotFound = infraerrors.NotFound("CHANNEL_NOT_FOUND", "channel not found")
	ErrProxyNotFound   = infraerrors.NotFound("PROXY_NOT_FOUND", "proxy not found")
	ErrSettingNotFound = infraerrors.NotFound("SETTING_NOT_FOUND", "setting not found")

	ErrRedeemCodeNotFound = infraerrors.NotFound("REDEEM_CODE_NOT_FOUND", "redeem code not found")
	ErrPromoCodeNotFound  = infraerrors.NotFound("PROMO_CODE_NOT_FOUND", "promo code not found")
	ErrUsageLogNotFound   = infraerrors.NotFound("USAGE_LOG_NOT_FOUND", "usage log not found")

	ErrSubscriptionNotFound           = infraerrors.NotFound("SUBSCRIPTION_NOT_FOUND", "subscription not found")
	ErrAttributeDefinitionNotFound    = infraerrors.NotFound("ATTRIBUTE_DEFINITION_NOT_FOUND", "attribute definition not found")
	ErrAffiliateProfileNotFound       = infraerrors.NotFound("AFFILIATE_PROFILE_NOT_FOUND", "affiliate profile not found")
	ErrChannelMonitorNotFound         = infraerrors.NotFound("CHANNEL_MONITOR_NOT_FOUND", "channel monitor not found")
	ErrChannelMonitorTemplateNotFound = infraerrors.NotFound(
		"CHANNEL_MONITOR_TEMPLATE_NOT_FOUND", "channel monitor request template not found",
	)
)

// ---- conflict sentinels ----

var (
	ErrEmailExists    = infraerrors.Conflict("EMAIL_EXISTS", "email already exists")
	ErrGroupExists    = infraerrors.Conflict("GROUP_EXISTS", "group name already exists")
	ErrChannelExists  = infraerrors.Conflict("CHANNEL_EXISTS", "channel name already exists")
	ErrAPIKeyExists   = infraerrors.Conflict("API_KEY_EXISTS", "api key already exists")
	ErrRedeemCodeUsed = infraerrors.Conflict("REDEEM_CODE_USED", "redeem code already used")

	ErrAttributeKeyExists          = infraerrors.Conflict("ATTRIBUTE_KEY_EXISTS", "attribute key already exists")
	ErrSubscriptionAlreadyExists   = infraerrors.Conflict("SUBSCRIPTION_ALREADY_EXISTS", "subscription already exists for this user and group")
	ErrSubscriptionRestoreConflict = infraerrors.Conflict("SUBSCRIPTION_RESTORE_CONFLICT", "subscription already exists for this user and group")
	ErrAffiliateCodeTaken          = infraerrors.Conflict("AFFILIATE_CODE_TAKEN", "affiliate code already in use")
)

// ---- bad-request sentinels ----

var (
	ErrAccountNilInput      = infraerrors.BadRequest("ACCOUNT_NIL_INPUT", "account input cannot be nil")
	ErrSubscriptionNilInput = infraerrors.BadRequest("SUBSCRIPTION_NIL_INPUT", "subscription input cannot be nil")
	ErrAffiliateQuotaEmpty  = infraerrors.BadRequest("AFFILIATE_QUOTA_EMPTY", "no affiliate quota available to transfer")

	ErrIdentityProviderInvalid = infraerrors.BadRequest("IDENTITY_PROVIDER_INVALID", "identity provider is invalid")

	ErrAffiliateCodeInvalid = infraerrors.BadRequest("AFFILIATE_CODE_INVALID", "invalid affiliate code")
)

// ---- billing sentinels (plain errors, not ApplicationError) ----

var (
	ErrUsageBillingRequestIDRequired = errors.New("usage billing request_id is required")
	ErrUsageBillingRequestConflict   = errors.New("usage billing request fingerprint conflict")
)
