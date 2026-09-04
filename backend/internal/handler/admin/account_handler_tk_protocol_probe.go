package admin

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

type protocolCapabilityProbeScheduler interface {
	ProbeAccountProtocolCapabilitiesBatch(ctx context.Context, accountIDs []int64)
}

type protocolCapabilityProbeRunner interface {
	ProbeAccountProtocolCapabilitiesNow(ctx context.Context, accountID int64) (service.ProtocolProbeRunResult, error)
}

type protocolCapabilityProbeResponse struct {
	CapabilityKey        string     `json:"capability_key"`
	SupportedProtocols   []string   `json:"supported_protocols"`
	Revision             int64      `json:"revision"`
	LastProbedAt         *time.Time `json:"last_probed_at"`
	AffectedAccountCount int        `json:"affected_account_count"`
}

func protocolCapabilityProbeProjection(result service.ProtocolProbeRunResult) *protocolCapabilityProbeResponse {
	if result.Capability == nil {
		return nil
	}
	protocols, err := service.NormalizeSupportedProtocols(result.Capability.SupportedProtocols)
	if err != nil {
		protocols = nil
	}
	supportedProtocols := make([]string, len(protocols))
	for i, protocol := range protocols {
		supportedProtocols[i] = string(protocol)
	}
	return &protocolCapabilityProbeResponse{
		CapabilityKey:        result.Capability.CapabilityKey,
		SupportedProtocols:   supportedProtocols,
		Revision:             result.Capability.Revision,
		LastProbedAt:         result.Capability.LastProbedAt,
		AffectedAccountCount: result.AffectedAccountCount,
	}
}

func (h *AccountHandler) scheduleProtocolCapabilityProbes(account *service.Account) {
	if account == nil {
		return
	}
	if h.protocolProbeScheduler == nil {
		return
	}
	if len(service.ProtocolProbeCandidates(account)) == 0 {
		return
	}
	h.scheduleProtocolCapabilityProbeBatch([]int64{account.ID})
}

func (h *AccountHandler) scheduleProtocolCapabilityProbeBatch(accountIDs []int64) {
	if h == nil || h.protocolProbeScheduler == nil || len(accountIDs) == 0 {
		return
	}
	ids := append([]int64(nil), accountIDs...)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("protocol_capability_probe_panic", "account_count", len(ids), "recover", r)
			}
		}()
		h.protocolProbeScheduler.ProbeAccountProtocolCapabilitiesBatch(context.Background(), ids)
	}()
}

func protocolCapabilityProbeRequiredForUpdate(req UpdateAccountRequest) bool {
	if req.Type != "" || req.ChannelType != nil || len(req.Credentials) > 0 {
		return true
	}
	return protocolCapabilityExtraChanged(req.Extra)
}

func protocolCapabilityExtraChanged(extra map[string]any) bool {
	if len(extra) == 0 {
		return false
	}
	_, customBaseURLChanged := extra["custom_base_url"]
	_, customBaseURLEnabledChanged := extra["custom_base_url_enabled"]
	return customBaseURLChanged || customBaseURLEnabledChanged
}

// ProbeProtocols synchronously re-tests the native text protocols for one
// account and returns the refreshed account snapshot.
// POST /api/v1/admin/accounts/:id/protocol-probe
func (h *AccountHandler) ProbeProtocols(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}

	account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if h.protocolProbeScheduler == nil {
		response.ErrorWithDetails(c, http.StatusServiceUnavailable, "Protocol probe unavailable", "protocol_probe_unavailable", nil)
		return
	}
	if len(service.ProtocolProbeCandidates(account)) == 0 {
		response.Success(c, gin.H{
			"account": h.buildAccountResponseWithRuntime(c.Request.Context(), account),
			"outcome": service.ProtocolProbeRunNotApplicable,
			"reason":  "no_protocol_probe_candidates",
		})
		return
	}
	runner, ok := h.protocolProbeScheduler.(protocolCapabilityProbeRunner)
	if !ok {
		response.ErrorWithDetails(c, http.StatusServiceUnavailable, "Protocol probe unavailable", "protocol_probe_unavailable", nil)
		return
	}
	result, err := runner.ProbeAccountProtocolCapabilitiesNow(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, fmt.Errorf("protocol probe account %d: %w", accountID, err))
		return
	}
	account, err = h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{
		"account":    h.buildAccountResponseWithRuntime(c.Request.Context(), account),
		"capability": protocolCapabilityProbeProjection(result),
		"outcome":    result.Outcome,
		"reason":     result.Reason,
	})
}
