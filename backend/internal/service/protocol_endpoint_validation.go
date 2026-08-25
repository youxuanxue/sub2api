package service

import "context"

func (s *OpenAIGatewayService) ValidateProtocolEndpoint(_ context.Context, account *Account, endpoint string) error {
	_, err := s.validateUpstreamBaseURLForAccount(account, endpoint)
	return err
}

func (s *GatewayService) ValidateProtocolEndpoint(_ context.Context, _ *Account, endpoint string) error {
	_, err := s.validateUpstreamBaseURL(endpoint)
	return err
}
