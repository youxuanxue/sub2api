package service

import (
	"context"
	"errors"
)

func loadProtocolExecutionAccount(
	ctx context.Context,
	repo AccountRepository,
	accountID int64,
) (*Account, error) {
	if repo == nil {
		return nil, errors.New("account repository is required")
	}
	return repo.GetByID(ctx, accountID)
}

func (s *OpenAIGatewayService) LoadProtocolExecutionAccount(ctx context.Context, accountID int64) (*Account, error) {
	if s == nil {
		return nil, errors.New("openai gateway service is required")
	}
	return loadProtocolExecutionAccount(ctx, s.accountRepo, accountID)
}

func (s *GatewayService) LoadProtocolExecutionAccount(ctx context.Context, accountID int64) (*Account, error) {
	if s == nil {
		return nil, errors.New("gateway service is required")
	}
	return loadProtocolExecutionAccount(ctx, s.accountRepo, accountID)
}

func (s *OpenAIGatewayService) ValidateProtocolEndpoint(_ context.Context, account *Account, endpoint string) error {
	_, err := s.validateUpstreamBaseURLForAccount(account, endpoint)
	return err
}

func (s *GatewayService) ValidateProtocolEndpoint(_ context.Context, _ *Account, endpoint string) error {
	_, err := s.validateUpstreamBaseURL(endpoint)
	return err
}
