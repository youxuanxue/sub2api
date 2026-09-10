package service

import "context"

type candidateDiscoveryPreparedPath struct {
	ctx     context.Context
	model   string
	channel ChannelMappingResult
	err     error
}

// A discovery shape uses one immutable request and account set. Prepare each
// group's request policy once; execution and subsequent requests stay fresh.
func candidateDiscoveryPathPreparer(request *CandidateRequest) func(context.Context, *Group) (context.Context, string, ChannelMappingResult, error) {
	prepared := make(map[int64]candidateDiscoveryPreparedPath)
	return func(ctx context.Context, group *Group) (context.Context, string, ChannelMappingResult, error) {
		result, ok := prepared[group.ID]
		if !ok {
			result.ctx, result.model, result.channel, result.err = request.pathContext(ctx, group)
			if result.err == nil {
				if routing, routed := result.ctx.Value(protocolRoutingContextKey{}).(protocolRoutingContextValue); routed {
					routing.immutableAccounts = true
					result.ctx = context.WithValue(result.ctx, protocolRoutingContextKey{}, routing)
				}
			}
			prepared[group.ID] = result
		}
		return result.ctx, result.model, result.channel, result.err
	}
}

type candidateDiscoveryAccountResult struct {
	account *Account
	err     error
}

// Only metadata billing-policy comparisons use this repository. A shadow's
// parent is read once per discovery, including failed reads, never across calls.
type candidateDiscoveryAccountRepository struct {
	AccountRepository
	loaded map[int64]candidateDiscoveryAccountResult
}

func (r *candidateDiscoveryAccountRepository) GetByID(ctx context.Context, id int64) (*Account, error) {
	result, ok := r.loaded[id]
	if !ok {
		result.account, result.err = r.AccountRepository.GetByID(ctx, id)
		r.loaded[id] = result
	}
	return result.account, result.err
}
