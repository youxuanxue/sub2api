package service

import (
	"strconv"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

func defaultNewAPIAccountTestModel(account *Account) string {
	if account != nil &&
		account.ChannelType == newapiconstant.ChannelTypeVolcEngine &&
		newapiintegration.IsVolcEngineAgentPlanBaseURL(account.ChannelType, account.GetBaseURL()) {
		return newapiintegration.VolcEngineAgentPlanDefaultTestModel
	}
	return openai.DefaultTestModel
}

func NewAPIAvailableModelPresetIDs(account *Account) []string {
	if account == nil {
		return nil
	}
	if account.Platform == PlatformNewAPI &&
		account.ChannelType == newapiconstant.ChannelTypeVolcEngine &&
		newapiintegration.IsVolcEngineAgentPlanBaseURL(account.ChannelType, account.GetBaseURL()) {
		return tkServedModelsManifestPresetIDsForAccount(strconv.FormatInt(account.ID, 10))
	}
	return nil
}
