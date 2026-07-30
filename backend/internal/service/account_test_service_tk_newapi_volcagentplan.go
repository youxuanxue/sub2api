package service

import (
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

func defaultNewAPIAccountTestModel(account *Account) string {
	if isNewAPIVolcEngineAgentPlanAccount(account) {
		return newapiintegration.VolcEngineAgentPlanDefaultTestModel
	}
	return openai.DefaultTestModel
}

func NewAPIAvailableModelPresetIDs(account *Account) []string {
	if account == nil {
		return nil
	}
	if isNewAPIVolcEngineAgentPlanAccount(account) {
		return NewAPIModelMappingPresetIDsForAccount(account)
	}
	return nil
}
