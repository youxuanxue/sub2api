package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	entgroup "github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/internal/integration/cursor"
)

const CursorSourceExtraKey = "upstream_provider"
const CursorModelParametersKey = "cursor_model_parameters"
const CursorWireModelsKey = "cursor_wire_models"

func (a *Account) IsCursor() bool {
	return a != nil && a.Platform == PlatformNewAPI && a.Type == AccountTypeAPIKey &&
		a.ChannelType == newapiconstant.ChannelTypeAnthropic && a.Extra[CursorSourceExtraKey] == "cursor"
}

type CursorAccountInput struct {
	SessionID string  `json:"session_id"`
	Name      string  `json:"name"`
	GroupIDs  []int64 `json:"group_ids"`
	AccountID int64   `json:"account_id,omitempty"`
}

type cursorAccountAdmin interface {
	GetAccount(context.Context, int64) (*Account, error)
	GetGroup(context.Context, int64) (*Group, error)
	SaveCursorAccount(context.Context, *CreateAccountInput, *UpdateAccountInput, int64) (*Account, error)
}

// Keep credential replacement and group binding atomic before consuming the
// authorization claim. Both mutations continue to use the existing owners.
func (s *adminServiceImpl) SaveCursorAccount(ctx context.Context, create *CreateAccountInput, update *UpdateAccountInput, accountID int64) (*Account, error) {
	if s.entClient == nil {
		return nil, errors.New("cursor account transaction is unavailable")
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	opCtx := dbent.NewTxContext(ctx, tx)
	var groupIDs []int64
	if create != nil {
		groupIDs = create.GroupIDs
	} else if update != nil && update.GroupIDs != nil {
		groupIDs = *update.GroupIDs
	}
	if len(groupIDs) != 1 {
		return nil, errors.New("cursor requires exactly one dedicated service group")
	}
	groups, err := tx.Group.Query().Where(entgroup.IDIn(groupIDs...)).Order(dbent.Asc(entgroup.FieldID)).ForUpdate().All(opCtx)
	if err != nil {
		return nil, err
	}
	for _, group := range groups {
		if group.Platform != PlatformNewAPI {
			return nil, errors.New("cursor requires a newapi service group")
		}
		cfg := group.MessagesDispatchModelConfig
		_, registered := tkMessagesDispatchFamilyForGroup(group.Name)
		if registered || cfg.OpusMappedModel != "" || cfg.SonnetMappedModel != "" || cfg.HaikuMappedModel != "" || len(cfg.ExactModelMappings) > 0 {
			return nil, errors.New("cursor requires a group without cross-family model remapping")
		}
	}
	for _, id := range groupIDs {
		accounts, listErr := s.accountRepo.ListByGroup(opCtx, id)
		if listErr != nil {
			return nil, listErr
		}
		for _, candidate := range accounts {
			if candidate.ID != accountID && !candidate.IsCursor() {
				return nil, errors.New("cursor requires a dedicated service group")
			}
		}
	}
	var account *Account
	if accountID > 0 {
		// Expiry and an operator pause share schedulable=false. Replacing a key
		// cannot establish why the account was paused, so preserve that state.
		account, err = s.UpdateAccount(opCtx, accountID, update)
	} else {
		account, err = s.CreateAccount(opCtx, create)
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.Group.Update().Where(entgroup.IDIn(groupIDs...)).SetAllowMessagesDispatch(true).Save(opCtx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	if s.authCacheInvalidator != nil {
		for _, id := range groupIDs {
			s.authCacheInvalidator.InvalidateAuthCacheByGroupID(ctx, id)
		}
	}
	return account, nil
}

type cursorAuthorizationClient interface {
	Claim(context.Context, string, string) (cursor.CredentialClaim, error)
	Settle(context.Context, string, string, string, bool) error
}

func ImportCursorAccount(ctx context.Context, admin cursorAccountAdmin, client cursorAuthorizationClient, owner string, input CursorAccountInput) (*Account, error) {
	if strings.TrimSpace(input.Name) == "" || len(input.Name) > 100 {
		return nil, errors.New("cursor account name is required (maximum 100 characters)")
	}
	if len(input.GroupIDs) != 1 {
		return nil, errors.New("select exactly one Cursor service group")
	}
	for _, id := range input.GroupIDs {
		group, err := admin.GetGroup(ctx, id)
		if err != nil {
			return nil, err
		}
		if group.Platform != PlatformNewAPI {
			return nil, errors.New("cursor requires a newapi service group")
		}
		cfg := group.MessagesDispatchModelConfig
		_, registered := tkMessagesDispatchFamilyForGroup(group.Name)
		if registered || cfg.OpusMappedModel != "" || cfg.SonnetMappedModel != "" || cfg.HaikuMappedModel != "" || len(cfg.ExactModelMappings) > 0 {
			return nil, errors.New("cursor requires a group without cross-family model remapping")
		}
	}
	var existing *Account
	if input.AccountID > 0 {
		var err error
		existing, err = admin.GetAccount(ctx, input.AccountID)
		if err != nil {
			return nil, err
		}
		if !existing.IsCursor() {
			return nil, errors.New("only Cursor accounts can be reconnected")
		}
	}
	claim, err := client.Claim(ctx, owner, input.SessionID)
	if err != nil {
		return nil, err
	}
	saved := false
	defer func() {
		settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if settleErr := client.Settle(settleCtx, owner, input.SessionID, claim.Claim, saved); settleErr != nil {
			slog.Warn("cursor_authorization_settlement_failed", "saved", saved)
		}
	}()
	if claim.APIKey == "" || !claim.KeyExpiresAt.After(time.Now()) {
		return nil, errors.New("cursor authorization returned no valid credential")
	}
	mapping := make(map[string]any)
	parameters := make(map[string]any)
	wireModels := make(map[string]any)
	for _, model := range claim.Models {
		if model.ID == "" || model.ID == "default" || model.ID == "auto" {
			continue
		}
		selected := cursor.DefaultParameters(model)
		for _, parameter := range selected {
			if parameter.ID == "fast" && parameter.Value == "true" {
				return nil, errors.New("cursor catalog does not offer a regular-speed variant for " + model.ID)
			}
		}
		mapping[model.ID] = model.ID
		parameters[model.ID] = selected
		wireModel, err := cursor.AgentVariantWireModel(model, selected)
		if err != nil {
			return nil, err
		}
		wireModels[model.ID] = wireModel
	}
	if len(mapping) == 0 {
		return nil, errors.New("cursor account has no fixed models")
	}
	credentials := map[string]any{}
	extra := map[string]any{}
	if existing != nil {
		maps.Copy(credentials, existing.Credentials)
		maps.Copy(extra, existing.Extra)
	}
	credentials["api_key"] = claim.APIKey
	credentials["model_mapping"] = mapping
	credentials[CursorModelParametersKey] = parameters
	credentials[CursorWireModelsKey] = wireModels
	applyExclusiveSupplierProtocolEndpoints(credentials, cursor.AgentBaseURL, newapiconstant.ChannelTypeAnthropic)
	extra[CursorSourceExtraKey] = "cursor"
	expires := claim.KeyExpiresAt.Unix()
	pause := true
	var account *Account
	if existing == nil {
		account, err = admin.SaveCursorAccount(ctx, &CreateAccountInput{
			Name: strings.TrimSpace(input.Name), Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
			ChannelType: newapiconstant.ChannelTypeAnthropic, Credentials: credentials, Extra: extra,
			GroupIDs: input.GroupIDs, Concurrency: 4, Priority: 50, ExpiresAt: &expires, AutoPauseOnExpired: &pause,
			AccountEmail: claim.Email, SkipDefaultGroupBind: true,
		}, nil, 0)
	} else {
		account, err = admin.SaveCursorAccount(ctx, nil, &UpdateAccountInput{
			Name: strings.TrimSpace(input.Name), Credentials: credentials, Extra: extra, GroupIDs: &input.GroupIDs,
			ExpiresAt: &expires, AutoPauseOnExpired: &pause, AccountEmail: &claim.Email,
		}, existing.ID)
	}
	if err != nil {
		return nil, fmt.Errorf("save Cursor account: %w", err)
	}
	saved = true
	return account, nil
}
