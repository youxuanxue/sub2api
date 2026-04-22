package bridge

import (
	"strconv"
	"time"

	newapicommon "github.com/QuantumNous/new-api/common"
	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
)

// ChannelContextInput carries New API relay context keys derived from a Sub2API account.
// Zero values are skipped where appropriate.
type ChannelContextInput struct {
	ChannelType int
	ChannelID   int
	BaseURL     string
	APIKey      string
	// ModelMappingJSON is optional JSON object for New API model_mapping (same as New API Gin key "model_mapping").
	ModelMappingJSON string
	// Organization is optional OpenAI Organization header value; New API relay handlers read
	// it from Gin key "channel_organization" to set OpenAI-Organization on outbound requests.
	Organization string
	// StatusCodeMappingJSON is an optional JSON object that remaps upstream HTTP status
	// codes per New API's status_code_mapping contract (e.g. {"404":"500"}). It is read
	// downstream by every relay handler in new-api/relay/* via c.GetString("status_code_mapping"),
	// so accounts that need to mask transient upstream errors as 5xx (or vice versa) MUST
	// have this populated here. Empty / "{}" disables remapping.
	StatusCodeMappingJSON string
	// UserID is optional; used for RelayInfo / diagnostics (Sub2API billing does not use New API quota).
	UserID int
	// UserGroup / UsingGroup optional, for affinity/logging alignment.
	UserGroup  string
	UsingGroup string
}

// PopulateContextKeys sets New API constant context keys on the Gin context so GenRelayInfo / InitChannelMeta work.
func PopulateContextKeys(c *gin.Context, in ChannelContextInput) {
	if c == nil {
		return
	}
	newapicommon.SetContextKey(c, newapiconstant.ContextKeyChannelType, in.ChannelType)
	newapicommon.SetContextKey(c, newapiconstant.ContextKeyChannelId, in.ChannelID)
	newapicommon.SetContextKey(c, newapiconstant.ContextKeyChannelBaseUrl, in.BaseURL)
	newapicommon.SetContextKey(c, newapiconstant.ContextKeyChannelKey, in.APIKey)
	if in.UserID > 0 {
		newapicommon.SetContextKey(c, newapiconstant.ContextKeyUserId, in.UserID)
	}
	if in.UserGroup != "" {
		newapicommon.SetContextKey(c, newapiconstant.ContextKeyUserGroup, in.UserGroup)
	}
	if in.UsingGroup != "" {
		newapicommon.SetContextKey(c, newapiconstant.ContextKeyUsingGroup, in.UsingGroup)
	}
	if in.Organization != "" {
		c.Set("channel_organization", in.Organization)
	}
	newapicommon.SetContextKey(c, newapiconstant.ContextKeyRequestStartTime, time.Now())

	if in.ModelMappingJSON != "" && in.ModelMappingJSON != "{}" {
		c.Set("model_mapping", in.ModelMappingJSON)
	}
	if in.StatusCodeMappingJSON != "" && in.StatusCodeMappingJSON != "{}" {
		c.Set("status_code_mapping", in.StatusCodeMappingJSON)
	}
}

// SetOriginalModel sets the model name used by GenRelayInfo / model mapping helpers.
func SetOriginalModel(c *gin.Context, model string) {
	if c == nil {
		return
	}
	newapicommon.SetContextKey(c, newapiconstant.ContextKeyOriginalModel, model)
}

// SetRequestID sets a stable request id for RelayInfo when missing.
func SetRequestID(c *gin.Context, id string) {
	if c == nil || id == "" {
		return
	}
	c.Set(newapicommon.RequestIdKey, id)
}

// NewRequestID builds a fallback id when the client did not provide one.
func NewRequestID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 10)
}
