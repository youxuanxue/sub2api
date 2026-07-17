package service

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net/url"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// TokenKey: Invite-to-Trial provisioning — fuse create-user + group + rate +
// balance + subscription(expiry) + trial-key into ONE batch call, and return
// ready-to-paste credential cards (平台/账号/密码).
//
// Why a standalone concrete service (NOT a method on the AdminService
// interface): AdminService has a large stub/mock surface; adding a method there
// would force updating every test stub (CLAUDE.md §2/§6). This service composes
// the existing atoms directly and is injected into a dedicated handler.
//
// Why we do NOT reuse adminServiceImpl.CreateUser: it best-effort assigns
// `default_subscriptions` on top, which would double-grant against the preset's
// chosen subscription. We build the user inline instead.
//
// The trial key is created via APIKeyService.Create directly (not
// IssueTrialKeyIfEnabled, which discards the plaintext key) so the card can echo
// the key once.

// TrialProvisionService provisions trial users in batch.
type TrialProvisionService struct {
	subscriptionService *SubscriptionService
	apiKeyService       *APIKeyService
	settingService      *SettingService
	userRepo            UserRepository
	userGroupRateRepo   UserGroupRateRepository
	groupRepo           GroupRepository
	redeemCodeRepo      RedeemCodeRepository
	entClient           *dbent.Client // 用于把开户余额与流水写入同一事务
}

// NewTrialProvisionService constructs the provisioning service.
func NewTrialProvisionService(
	subscriptionService *SubscriptionService,
	apiKeyService *APIKeyService,
	settingService *SettingService,
	userRepo UserRepository,
	userGroupRateRepo UserGroupRateRepository,
	groupRepo GroupRepository,
	redeemCodeRepo RedeemCodeRepository,
	entClient *dbent.Client,
) *TrialProvisionService {
	return &TrialProvisionService{
		subscriptionService: subscriptionService,
		apiKeyService:       apiKeyService,
		settingService:      settingService,
		userRepo:            userRepo,
		userGroupRateRepo:   userGroupRateRepo,
		groupRepo:           groupRepo,
		redeemCodeRepo:      redeemCodeRepo,
		entClient:           entClient,
	}
}

// createTrialUserWithLedger inserts the trial user and, when it is provisioned
// with a positive opening balance, records that balance as an admin_balance
// journal row in the SAME transaction so the trial credit shows in
// 充值和并发变动记录 and counts toward 总充值. Falls back to a plain create plus
// best-effort journal when no ent client is wired (unit tests).
func (s *TrialProvisionService) createTrialUserWithLedger(ctx context.Context, user *User) error {
	if user.Balance <= 0 {
		return s.userRepo.Create(ctx, user)
	}
	if s.entClient == nil {
		if err := s.userRepo.Create(ctx, user); err != nil {
			return err
		}
		bestEffortBalanceGrantLedger(ctx, s.redeemCodeRepo, user.ID, user.Balance, BalanceGrantNoteInviteTrial, "service.admin")
		return nil
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	if err := s.userRepo.Create(txCtx, user); err != nil {
		return err
	}
	if err := writeBalanceGrantLedger(txCtx, tx.Client(), user.ID, user.Balance, BalanceGrantNoteInviteTrial); err != nil {
		return err
	}
	return tx.Commit()
}

// GetPresets returns the saved trial presets (passthrough to SettingService so
// the handler depends only on this service).
func (s *TrialProvisionService) GetPresets(ctx context.Context) []TrialPreset {
	return s.settingService.GetTrialPresets(ctx)
}

// SetPresets validates and persists the trial presets.
func (s *TrialProvisionService) SetPresets(ctx context.Context, presets []TrialPreset) error {
	return s.settingService.SetTrialPresets(ctx, presets)
}

// TrialPlan is the effective configuration applied to every provisioned user.
type TrialPlan struct {
	GroupID      int64
	ValidityDays int
	Balance      float64
	Concurrency  int
	RPMLimit     int
	Rate         *float64 // per-group rate (倍率) override; nil = group default
}

// TrialRecipient is one invitee. Both fields are optional — empty values are
// auto-generated (crypto-random) by the service.
type TrialRecipient struct {
	Email    string
	Password string
}

// ProvisionTrialInput drives a batch invite.
type ProvisionTrialInput struct {
	AdminID    int64
	PresetName string // optional: resolve plan from a saved preset (overrides Plan)
	Plan       TrialPlan
	Recipients []TrialRecipient
	AutoCount  int    // append this many auto-generated recipients
	IssueKey   bool   // issue a "trial" API key per user
	KeyName    string // optional override for the issued key name (default "trial")
}

// TrialCredential is the per-user result, carrying the one-time plaintext
// credentials and the preformatted credential card.
type TrialCredential struct {
	UserID    int64   `json:"user_id"`
	Email     string  `json:"email"`
	Password  string  `json:"password"`
	APIKey    string  `json:"api_key,omitempty"`
	HomeURL   string  `json:"home_url"`
	GroupID   int64   `json:"group_id"`
	GroupName string  `json:"group_name"`
	Balance   float64 `json:"balance"`
	ExpiresAt string  `json:"expires_at,omitempty"`
	CardText  string  `json:"card_text"`
	Error     string  `json:"error,omitempty"`
}

const trialEmailDomainFallback = "trial.local"

// ProvisionTrialUsers creates the batch and returns per-user credential cards.
// Group validity is checked once up front (fail-fast); per-user failures are
// captured in each TrialCredential.Error rather than aborting the whole batch.
func (s *TrialProvisionService) ProvisionTrialUsers(ctx context.Context, input *ProvisionTrialInput) ([]TrialCredential, error) {
	if input == nil {
		return nil, infraerrors.BadRequest("INVALID_TRIAL_INPUT", "missing input")
	}

	plan, err := s.resolvePlan(ctx, input)
	if err != nil {
		return nil, err
	}

	// Fail-fast: the trial's expiry is a group subscription, so the group must
	// exist and be subscription-type before we create anyone.
	group, err := s.groupRepo.GetByID(ctx, plan.GroupID)
	if err != nil {
		return nil, infraerrors.BadRequest("INVALID_TRIAL_GROUP", "trial group not found")
	}
	if !group.IsSubscriptionType() {
		return nil, infraerrors.BadRequest("INVALID_TRIAL_GROUP", "trial group must be a subscription-type group")
	}

	recipients, err := s.buildRecipients(input)
	if err != nil {
		return nil, err
	}

	homeURL := s.homeURL(ctx)
	keyName := strings.TrimSpace(input.KeyName)
	if keyName == "" {
		keyName = "trial"
	}

	results := make([]TrialCredential, 0, len(recipients))
	for _, r := range recipients {
		results = append(results, s.provisionOne(ctx, input.AdminID, plan, group.Name, homeURL, keyName, input.IssueKey, r))
	}
	return results, nil
}

// resolvePlan returns the effective plan: a saved preset when PresetName is set,
// otherwise the inline Plan. Concurrency falls back to the system default.
func (s *TrialProvisionService) resolvePlan(ctx context.Context, input *ProvisionTrialInput) (TrialPlan, error) {
	plan := input.Plan
	if name := strings.TrimSpace(input.PresetName); name != "" {
		var found *TrialPreset
		for i := range s.settingService.GetTrialPresets(ctx) {
			p := s.settingService.GetTrialPresets(ctx)[i]
			if p.Name == name {
				found = &p
				break
			}
		}
		if found == nil {
			return TrialPlan{}, infraerrors.BadRequest("TRIAL_PRESET_NOT_FOUND", "trial preset not found: "+name)
		}
		plan = TrialPlan{
			GroupID:      found.GroupID,
			ValidityDays: found.ValidityDays,
			Balance:      found.Balance,
			Concurrency:  found.Concurrency,
			RPMLimit:     found.RPMLimit,
			Rate:         found.Rate,
		}
	}
	if plan.GroupID <= 0 {
		return TrialPlan{}, infraerrors.BadRequest("INVALID_TRIAL_GROUP", "trial plan must reference a group")
	}
	if plan.ValidityDays <= 0 {
		plan.ValidityDays = 30
	}
	if plan.Concurrency <= 0 && s.settingService != nil {
		plan.Concurrency = s.settingService.GetDefaultConcurrency(ctx)
	}
	if plan.Concurrency <= 0 {
		plan.Concurrency = 1
	}
	return plan, nil
}

// buildRecipients merges explicit recipients with AutoCount auto-generated ones.
func (s *TrialProvisionService) buildRecipients(input *ProvisionTrialInput) ([]TrialRecipient, error) {
	recipients := make([]TrialRecipient, 0, len(input.Recipients)+input.AutoCount)
	for _, r := range input.Recipients {
		r.Email = strings.TrimSpace(r.Email)
		if r.Email == "" && strings.TrimSpace(r.Password) == "" {
			continue // skip fully-blank lines
		}
		recipients = append(recipients, r)
	}
	for i := 0; i < input.AutoCount; i++ {
		recipients = append(recipients, TrialRecipient{})
	}
	if len(recipients) == 0 {
		return nil, infraerrors.BadRequest("NO_TRIAL_RECIPIENTS", "no recipients to provision")
	}
	if len(recipients) > 200 {
		return nil, infraerrors.BadRequest("TOO_MANY_TRIAL_RECIPIENTS", "at most 200 recipients per batch")
	}
	return recipients, nil
}

// provisionOne creates a single trial user and applies the plan. Errors after a
// successful create are captured in the returned credential, not propagated.
func (s *TrialProvisionService) provisionOne(
	ctx context.Context,
	adminID int64,
	plan TrialPlan,
	groupName, homeURL, keyName string,
	issueKey bool,
	r TrialRecipient,
) TrialCredential {
	email := strings.TrimSpace(r.Email)
	if email == "" {
		email = s.generateEmail(ctx)
	}
	password := strings.TrimSpace(r.Password)
	if password == "" {
		password = generateTrialPassword(16)
	}

	cred := TrialCredential{
		Email:     email,
		Password:  password,
		HomeURL:   homeURL,
		GroupID:   plan.GroupID,
		GroupName: groupName,
		Balance:   plan.Balance,
	}

	user := &User{
		Email:         email,
		Role:          RoleUser,
		Balance:       plan.Balance,
		Concurrency:   plan.Concurrency,
		RPMLimit:      plan.RPMLimit,
		Status:        StatusActive,
		AllowedGroups: []int64{plan.GroupID},
	}
	if err := user.SetPassword(password); err != nil {
		cred.Error = "set password: " + err.Error()
		return cred
	}
	if err := s.createTrialUserWithLedger(ctx, user); err != nil {
		cred.Error = "create user: " + err.Error()
		return cred
	}
	cred.UserID = user.ID

	// Per-group rate (倍率) override.
	if plan.Rate != nil {
		if err := s.userGroupRateRepo.SyncUserGroupRates(ctx, user.ID, map[int64]*float64{plan.GroupID: plan.Rate}); err != nil {
			logger.LegacyPrintf("service.admin", "invite-trial: sync group rate failed user_id=%d err=%v", user.ID, err)
		}
	}

	// Subscription = trial expiry (到期时间). Required for the trial to schedule.
	sub, err := s.subscriptionService.AssignSubscription(ctx, &AssignSubscriptionInput{
		UserID:       user.ID,
		GroupID:      plan.GroupID,
		ValidityDays: plan.ValidityDays,
		AssignedBy:   adminID,
		Notes:        "invite-trial",
	})
	if err != nil {
		cred.Error = "assign subscription: " + err.Error()
		cred.CardText = buildTrialCard(homeURL, email, password)
		return cred
	}
	if !sub.ExpiresAt.IsZero() {
		cred.ExpiresAt = sub.ExpiresAt.Format("2006-01-02")
	}

	// Trial API key — created directly so we can echo the plaintext once.
	if issueKey {
		apiKey, err := s.apiKeyService.Create(ctx, user.ID, CreateAPIKeyRequest{Name: keyName})
		if err != nil {
			logger.LegacyPrintf("service.admin", "invite-trial: issue key failed user_id=%d err=%v", user.ID, err)
		} else if apiKey != nil {
			cred.APIKey = apiKey.Key
		}
	}

	cred.CardText = buildTrialCard(homeURL, email, password)
	return cred
}

// buildTrialCard renders the paste-ready credential card, matching the operator's
// existing format exactly (平台/账号/密码).
func buildTrialCard(homeURL, email, password string) string {
	lines := make([]string, 0, 3)
	if homeURL != "" {
		lines = append(lines, "平台 "+homeURL)
	}
	lines = append(lines, "账号 "+email, "密码 "+password)
	return strings.Join(lines, "\n")
}

// homeURL builds "<frontend_url>/home", guarding the empty-config case.
func (s *TrialProvisionService) homeURL(ctx context.Context) string {
	if s.settingService == nil {
		return ""
	}
	base := strings.TrimRight(strings.TrimSpace(s.settingService.GetFrontendURL(ctx)), "/")
	if base == "" {
		return ""
	}
	return base + "/home"
}

// generateEmail builds a unique throwaway login like trial-<8hex>@<domain>.
// Domain derives from the frontend URL host (drops a leading "api." label) so
// generated accounts look on-brand; falls back to trial.local. These accounts
// never receive mail — the email is just a unique login identifier.
func (s *TrialProvisionService) generateEmail(ctx context.Context) string {
	domain := trialEmailDomainFallback
	if s.settingService != nil {
		if host := frontendHost(s.settingService.GetFrontendURL(ctx)); host != "" {
			domain = host
		}
	}
	return "trial-" + randHex(4) + "@" + domain
}

// frontendHost extracts the bare host from a frontend URL, dropping a leading
// "api." label (api.tokenkey.dev → tokenkey.dev).
func frontendHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if host == "" {
		return ""
	}
	return strings.TrimPrefix(host, "api.")
}

// generateTrialPassword returns an n-char crypto-random base62 password.
func generateTrialPassword(n int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	if n <= 0 {
		n = 16
	}
	b := make([]byte, n)
	max := big.NewInt(int64(len(charset)))
	for i := range b {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			// crypto/rand should never fail; fall back to a fixed char rather
			// than panicking inside a request path.
			b[i] = charset[0]
			continue
		}
		b[i] = charset[idx.Int64()]
	}
	return string(b)
}

// randHex returns 2*n lowercase hex chars of crypto randomness.
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "00000000"[:2*n]
	}
	return fmt.Sprintf("%x", b)
}
