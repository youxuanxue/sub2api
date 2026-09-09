package service

import (
	"context"
	"fmt"
	"strings"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
)

func bindProtocolPlanToNewAPIBridge(
	ctx context.Context,
	account *Account,
	body []byte,
	userID int64,
	groupLabel string,
	format newapitypes.RelayFormat,
) ([]byte, bridge.ChannelContextInput, error) {
	plan, planned := ProtocolExecutionPlan(ctx)
	if !planned {
		return body, newAPIBridgeChannelInputForBody(account, userID, groupLabel, body), nil
	}
	target := protocolrouter.ProtocolChatCompletions
	if format == newapitypes.RelayFormatOpenAIResponses {
		target = protocolrouter.ProtocolResponses
	}
	if plan.TargetProtocol() != target {
		return nil, bridge.ChannelContextInput{}, fmt.Errorf("%w: newapi %s dispatcher received %s plan", protocolrouter.ErrStalePlan, target, plan.TargetProtocol())
	}
	body = ReplaceModelInBody(body, plan.ResolvedModel())
	in := newAPIBridgeChannelInputForBody(account, userID, groupLabel, body).WithoutModelMapping()
	endpoint, err := bridge.ResolveTextEndpoint(in, format, plan.ResolvedModel())
	if err != nil {
		return nil, bridge.ChannelContextInput{}, fmt.Errorf("resolve newapi execution endpoint: %w", err)
	}
	if strings.TrimSpace(endpoint) != strings.TrimSpace(plan.Endpoint()) {
		return nil, bridge.ChannelContextInput{}, fmt.Errorf("%w: newapi endpoint %q differs from plan %q", protocolrouter.ErrStalePlan, endpoint, plan.Endpoint())
	}
	return body, in, nil
}
