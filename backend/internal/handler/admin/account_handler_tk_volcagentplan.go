package admin

import (
	"sort"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// tkRespondNewAPIAgentPlanAvailableModelsWhenMappingEmpty returns true when
// Agent Plan accounts with empty model_mapping were answered from manifest
// served_on presets.
func tkRespondNewAPIAgentPlanAvailableModelsWhenMappingEmpty(c *gin.Context, account *service.Account) bool {
	agentPlanIDs := service.NewAPIAvailableModelPresetIDs(account)
	if len(agentPlanIDs) == 0 {
		return false
	}
	sort.Strings(agentPlanIDs)
	response.Success(c, tkAdminModelOptionsForIDs(agentPlanIDs))
	return true
}
