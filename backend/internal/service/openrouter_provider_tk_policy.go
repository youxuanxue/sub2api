package service

import (
	"context"
	"fmt"
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

// PublicGroupForbiddenAggregatorChannelTypes lists new-api aggregator channel types
// that must never appear on public (non-exclusive) groups. Scheme C for OpenRouter
// provider loop prevention: universal keys can route to any public group, so public
// groups must not host OpenRouter/Coze/Submodel upstream relays.
func PublicGroupForbiddenAggregatorChannelTypes() []int {
	return []int{
		newapiconstant.ChannelTypeOpenRouter,
		newapiconstant.ChannelTypeCoze,
		newapiconstant.ChannelTypeSubmodel,
	}
}

func isPublicGroupForbiddenAggregatorAccount(platform string, channelType int, credentials map[string]any) bool {
	if strings.TrimSpace(platform) != PlatformNewAPI {
		return false
	}
	for _, ct := range PublicGroupForbiddenAggregatorChannelTypes() {
		if channelType == ct {
			return true
		}
	}
	if credentials == nil {
		return false
	}
	baseURL, _ := credentials["base_url"].(string)
	lower := strings.ToLower(strings.TrimSpace(baseURL))
	return strings.Contains(lower, "openrouter.ai")
}

func forbiddenAggregatorChannelLabel(channelType int) string {
	switch channelType {
	case newapiconstant.ChannelTypeOpenRouter:
		return "OpenRouter"
	case newapiconstant.ChannelTypeCoze:
		return "Coze"
	case newapiconstant.ChannelTypeSubmodel:
		return "Submodel"
	default:
		return fmt.Sprintf("channel_type=%d", channelType)
	}
}

// PublicGroupAggregatorChannelError is returned when an aggregator upstream account
// would be (or already is) bound to a public group.
type PublicGroupAggregatorChannelError struct {
	GroupID       int64
	GroupName     string
	AccountID     int64
	AccountName   string
	ChannelType   int
	ChannelLabel  string
	OperationHint string
}

func (e *PublicGroupAggregatorChannelError) Error() string {
	if e == nil {
		return "public group aggregator channel policy violation"
	}
	group := e.GroupName
	if group == "" {
		group = fmt.Sprintf("group %d", e.GroupID)
	}
	account := e.AccountName
	if account == "" && e.AccountID > 0 {
		account = fmt.Sprintf("account %d", e.AccountID)
	}
	label := e.ChannelLabel
	if label == "" {
		label = forbiddenAggregatorChannelLabel(e.ChannelType)
	}
	msg := fmt.Sprintf(
		"public group %q must not contain %s aggregator upstream (%s)",
		group, label, account,
	)
	if strings.TrimSpace(e.OperationHint) != "" {
		msg += ": " + e.OperationHint
	}
	return msg
}

func (s *adminServiceImpl) checkPublicGroupAggregatorChannelPolicy(
	ctx context.Context,
	currentAccountID int64,
	currentAccountName string,
	currentPlatform string,
	currentChannelType int,
	currentCredentials map[string]any,
	groupIDs []int64,
) error {
	if len(groupIDs) == 0 || s.groupRepo == nil || s.accountRepo == nil {
		return nil
	}

	currentForbidden := isPublicGroupForbiddenAggregatorAccount(
		currentPlatform, currentChannelType, currentCredentials,
	)

	for _, groupID := range groupIDs {
		group, err := s.groupRepo.GetByID(ctx, groupID)
		if err != nil {
			return fmt.Errorf("get group %d: %w", groupID, err)
		}
		if group == nil || group.IsExclusive {
			continue
		}

		groupName := group.Name
		if groupName == "" {
			groupName = fmt.Sprintf("Group %d", groupID)
		}

		if currentForbidden {
			return &PublicGroupAggregatorChannelError{
				GroupID:       groupID,
				GroupName:     groupName,
				AccountID:     currentAccountID,
				AccountName:   currentAccountName,
				ChannelType:   currentChannelType,
				ChannelLabel:  forbiddenAggregatorChannelLabel(currentChannelType),
				OperationHint: "bind this account only to exclusive groups or use a direct upstream channel",
			}
		}

		accounts, err := s.accountRepo.ListByGroup(ctx, groupID)
		if err != nil {
			return fmt.Errorf("list accounts in group %d: %w", groupID, err)
		}
		for _, account := range accounts {
			if currentAccountID > 0 && account.ID == currentAccountID {
				continue
			}
			if !isPublicGroupForbiddenAggregatorAccount(account.Platform, account.ChannelType, account.Credentials) {
				continue
			}
			return &PublicGroupAggregatorChannelError{
				GroupID:       groupID,
				GroupName:     groupName,
				AccountID:     account.ID,
				AccountName:   account.Name,
				ChannelType:   account.ChannelType,
				ChannelLabel:  forbiddenAggregatorChannelLabel(account.ChannelType),
				OperationHint: "remove aggregator accounts from public groups (scheme C)",
			}
		}
	}
	return nil
}
