package service

import (
	"context"
	"log/slog"
	"strings"
)

type accountCreateOptions struct {
	allowSupplierReservedExtra bool
	initialSchedulable         *bool
}

func (s *adminServiceImpl) createAccount(ctx context.Context, input *CreateAccountInput, options accountCreateOptions) (*Account, error) {
	if input == nil {
		return nil, ErrAccountNilInput
	}
	if !options.allowSupplierReservedExtra {
		if err := ValidateSupplierReservedAccountExtra(input.Extra); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(input.AccountEmail) != "" {
		var err error
		input.Extra, input.Credentials, err = ApplyAccountEmail(input.Extra, input.Credentials, input.AccountEmail)
		if err != nil {
			return nil, err
		}
	}

	accountExtra, err := normalizeOpenAILongContextBillingExtra(input.Platform, input.Extra)
	if err != nil {
		return nil, err
	}
	accountExtra, err = normalizeGrokMediaEligibilityExtra(input.Platform, accountExtra)
	if err != nil {
		return nil, err
	}

	groupIDs := input.GroupIDs
	if len(groupIDs) == 0 && !input.SkipDefaultGroupBind {
		defaultGroupName := input.Platform + "-default"
		groups, listErr := s.groupRepo.ListActiveByPlatform(ctx, input.Platform)
		if listErr == nil {
			for _, group := range groups {
				if group.Name == defaultGroupName {
					groupIDs = []int64{group.ID}
					break
				}
			}
		}
	}
	if len(groupIDs) > 0 && !input.SkipMixedChannelCheck {
		if err := s.checkMixedChannelRisk(ctx, 0, input.Platform, groupIDs); err != nil {
			return nil, err
		}
	}
	if len(groupIDs) > 0 {
		if err := s.checkPublicGroupAggregatorChannelPolicy(ctx, 0, input.Name, input.Platform, input.ChannelType, input.Credentials, groupIDs); err != nil {
			return nil, err
		}
	}
	if err := NormalizeHeaderOverrideCredentials(input.Credentials); err != nil {
		return nil, err
	}
	// Never persist ephemeral SSO/password secrets after OAuth conversion.
	input.Credentials = SanitizeStoredCredentials(input.Platform, input.Credentials)
	input.Credentials = FinalizeAccountCredentials(input.Credentials, input.ChannelType)

	account, err := buildAccountForCreate(input, accountExtra)
	if err != nil {
		return nil, err
	}
	if options.initialSchedulable != nil {
		account.Schedulable = *options.initialSchedulable
	}
	if err := resolveNewAPIMoonshotBaseURLOnSave(ctx, account); err != nil {
		return nil, err
	}
	if err := resolveGrokTokenOnSave(ctx, account); err != nil {
		return nil, err
	}
	SeedOfficialSupportedProtocols(account)
	if err := s.accountRepo.Create(ctx, account); err != nil {
		return nil, err
	}

	// 绑定分组
	if len(groupIDs) > 0 {
		if err := s.accountRepo.BindGroups(ctx, account.ID, groupIDs); err != nil {
			return nil, err
		}
		account.GroupIDs = append([]int64(nil), groupIDs...)
	}

	// OAuth 账号：创建后异步设置隐私。
	// 使用 Ensure（幂等）而非 Force：新建账号 Extra 为空时效果相同，但更安全。
	if account.Type == AccountTypeOAuth {
		switch account.Platform {
		case PlatformOpenAI:
			go func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("create_account_openai_privacy_panic", "account_id", account.ID, "recover", r)
					}
				}()
				s.EnsureOpenAIPrivacy(context.Background(), account)
			}()
		case PlatformAntigravity:
			go func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("create_account_antigravity_privacy_panic", "account_id", account.ID, "recover", r)
					}
				}()
				s.EnsureAntigravityPrivacy(context.Background(), account)
			}()
		}
	}

	return account, nil
}
