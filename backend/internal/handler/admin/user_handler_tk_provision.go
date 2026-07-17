package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// TokenKey: Invite-to-Trial admin endpoints — one-step batch user provisioning
// that returns ready-to-paste credential cards, plus CRUD for the reusable
// "试用方案" presets. Kept in a dedicated handler (not on UserHandler) so the
// large AdminService interface / its stubs are untouched (CLAUDE.md §5).
type TrialProvisionHandler struct {
	service *service.TrialProvisionService
}

// NewTrialProvisionHandler constructs the handler.
func NewTrialProvisionHandler(svc *service.TrialProvisionService) *TrialProvisionHandler {
	return &TrialProvisionHandler{service: svc}
}

type trialPlanRequest struct {
	GroupID      int64    `json:"group_id"`
	ValidityDays int      `json:"validity_days"`
	Balance      float64  `json:"balance"`
	Concurrency  int      `json:"concurrency"`
	RPMLimit     int      `json:"rpm_limit"`
	Rate         *float64 `json:"rate"`
}

type trialRecipientRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type inviteTrialRequest struct {
	PresetName string                  `json:"preset_name"`
	Plan       *trialPlanRequest       `json:"plan"`
	Recipients []trialRecipientRequest `json:"recipients"`
	AutoCount  int                     `json:"auto_count"`
	IssueKey   *bool                   `json:"issue_key"` // default true
	KeyName    string                  `json:"key_name"`
}

// InviteTrial provisions a batch of trial users and returns credential cards.
// POST /admin/users/invite-trial
func (h *TrialProvisionHandler) InviteTrial(c *gin.Context) {
	var req inviteTrialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}

	issueKey := true
	if req.IssueKey != nil {
		issueKey = *req.IssueKey
	}

	input := &service.ProvisionTrialInput{
		AdminID:    getAdminIDFromContext(c),
		PresetName: req.PresetName,
		AutoCount:  req.AutoCount,
		IssueKey:   issueKey,
		KeyName:    req.KeyName,
	}
	if req.Plan != nil {
		input.Plan = service.TrialPlan{
			GroupID:      req.Plan.GroupID,
			ValidityDays: req.Plan.ValidityDays,
			Balance:      req.Plan.Balance,
			Concurrency:  req.Plan.Concurrency,
			RPMLimit:     req.Plan.RPMLimit,
			Rate:         req.Plan.Rate,
		}
	}
	for _, r := range req.Recipients {
		input.Recipients = append(input.Recipients, service.TrialRecipient{
			Email:    r.Email,
			Password: r.Password,
		})
	}

	results, err := h.service.ProvisionTrialUsers(c.Request.Context(), input)
	if err != nil {
		if !response.ErrorFrom(c, err) {
			response.BadRequest(c, err.Error())
		}
		return
	}
	response.Success(c, gin.H{"results": results, "count": len(results)})
}

// GetTrialPresets returns the saved trial presets.
// GET /admin/users/trial-presets
func (h *TrialProvisionHandler) GetTrialPresets(c *gin.Context) {
	presets := h.service.GetPresets(c.Request.Context())
	if presets == nil {
		presets = []service.TrialPreset{}
	}
	response.Success(c, gin.H{"presets": presets})
}

// SetTrialPresets replaces the saved trial presets.
// PUT /admin/users/trial-presets
func (h *TrialProvisionHandler) SetTrialPresets(c *gin.Context) {
	var req struct {
		Presets []service.TrialPreset `json:"presets"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}
	if err := h.service.SetPresets(c.Request.Context(), req.Presets); err != nil {
		if !response.ErrorFrom(c, err) {
			response.BadRequest(c, err.Error())
		}
		return
	}
	response.Success(c, gin.H{"presets": req.Presets})
}
