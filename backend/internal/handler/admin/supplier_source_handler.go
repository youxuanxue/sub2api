package admin

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type SupplierSourceHandler struct {
	service *service.SupplierSourceService
}

func NewSupplierSourceHandler(svc *service.SupplierSourceService) *SupplierSourceHandler {
	return &SupplierSourceHandler{service: svc}
}

type supplierSourceRequest struct {
	SupplierName       string                       `json:"supplier_name" binding:"required,max=120"`
	ChannelName        string                       `json:"channel_name" binding:"required,max=120"`
	ChannelType        int                          `json:"channel_type" binding:"required,gt=0"`
	Endpoint           string                       `json:"endpoint" binding:"required,max=500"`
	Credential         string                       `json:"credential" binding:"max=8192"`
	BasePriority       *int                         `json:"base_priority"`
	AccountConcurrency *int                         `json:"account_concurrency"`
	Notes              string                       `json:"notes" binding:"max=4000"`
	Models             []supplierSourceModelRequest `json:"models" binding:"max=100,dive"`
}

type supplierSourceModelRequest struct {
	ClientModelID   string   `json:"client_model_id" binding:"required,max=200"`
	UpstreamModelID string   `json:"upstream_model_id" binding:"required,max=200"`
	PurchaseRatio   *float64 `json:"purchase_ratio" binding:"omitempty,gt=0,lte=1"`
}

type supplierSourceResponse struct {
	ID                 int64                         `json:"id"`
	SupplierName       string                        `json:"supplier_name"`
	ChannelName        string                        `json:"channel_name"`
	ChannelType        int                           `json:"channel_type"`
	Endpoint           string                        `json:"endpoint"`
	BasePriority       int                           `json:"base_priority"`
	AccountConcurrency int                           `json:"account_concurrency"`
	Models             []supplierSourceModelResponse `json:"models"`
	Notes              string                        `json:"notes"`
	CreatedAt          any                           `json:"created_at"`
	UpdatedAt          any                           `json:"updated_at"`
}

type supplierSourceModelResponse struct {
	ClientModelID   string   `json:"client_model_id"`
	UpstreamModelID string   `json:"upstream_model_id"`
	PurchaseRatio   *float64 `json:"purchase_ratio"`
}

type supplierPriorityPreviewResponse struct {
	Entries  []supplierPriorityPreviewEntryResponse   `json:"entries"`
	Warnings []supplierPriorityPreviewWarningResponse `json:"warnings"`
}

type supplierPriorityPreviewEntryResponse struct {
	SourceID         int64    `json:"source_id"`
	SupplierName     string   `json:"supplier_name"`
	ChannelName      string   `json:"channel_name"`
	DiscountBand     int      `json:"discount_band"`
	DiscountPriority int      `json:"discount_priority"`
	Priority         int      `json:"priority"`
	ClientModelIDs   []string `json:"client_model_ids"`
}

type supplierPriorityPreviewWarningResponse struct {
	Code      string  `json:"code"`
	Priority  int     `json:"priority"`
	SourceIDs []int64 `json:"source_ids"`
}

func (r supplierSourceRequest) toInput() service.SupplierSourceInput {
	models := make([]service.SupplierSourceModelInput, 0, len(r.Models))
	for _, model := range r.Models {
		models = append(models, service.SupplierSourceModelInput{
			ClientModelID: model.ClientModelID, UpstreamModelID: model.UpstreamModelID, PurchaseRatio: model.PurchaseRatio,
		})
	}
	return service.SupplierSourceInput{
		SupplierName: r.SupplierName, ChannelName: r.ChannelName, ChannelType: r.ChannelType,
		Endpoint:   r.Endpoint,
		Credential: r.Credential, BasePriority: r.BasePriority, AccountConcurrency: r.AccountConcurrency,
		Notes: r.Notes, Models: models,
	}
}

func supplierSourceToResponse(source *service.SupplierSource) *supplierSourceResponse {
	if source == nil {
		return nil
	}
	models := make([]supplierSourceModelResponse, 0, len(source.Models))
	for _, model := range source.Models {
		models = append(models, supplierSourceModelResponse{
			ClientModelID: model.ClientModelID, UpstreamModelID: model.UpstreamModelID, PurchaseRatio: model.PurchaseRatio,
		})
	}
	return &supplierSourceResponse{
		ID: source.ID, SupplierName: source.SupplierName, ChannelName: source.ChannelName,
		ChannelType: source.ChannelType, Endpoint: source.Endpoint, BasePriority: source.BasePriority,
		AccountConcurrency: source.AccountConcurrency, Models: models, Notes: source.Notes,
		CreatedAt: source.CreatedAt, UpdatedAt: source.UpdatedAt,
	}
}

func supplierPriorityPreviewToResponse(preview *service.SupplierPriorityPreview) *supplierPriorityPreviewResponse {
	if preview == nil {
		return nil
	}
	result := &supplierPriorityPreviewResponse{
		Entries:  make([]supplierPriorityPreviewEntryResponse, 0, len(preview.Entries)),
		Warnings: make([]supplierPriorityPreviewWarningResponse, 0, len(preview.Warnings)),
	}
	for _, entry := range preview.Entries {
		result.Entries = append(result.Entries, supplierPriorityPreviewEntryResponse{
			SourceID: entry.SourceID, SupplierName: entry.SupplierName, ChannelName: entry.ChannelName,
			DiscountBand: entry.DiscountBand, DiscountPriority: entry.DiscountPriority,
			Priority: entry.Priority, ClientModelIDs: entry.ClientModelIDs,
		})
	}
	for _, warning := range preview.Warnings {
		result.Warnings = append(result.Warnings, supplierPriorityPreviewWarningResponse{
			Code: warning.Code, Priority: warning.Priority, SourceIDs: warning.SourceIDs,
		})
	}
	return result
}

func (h *SupplierSourceHandler) List(c *gin.Context) {
	sources, err := h.service.List(c.Request.Context())
	if err != nil {
		writeSupplierSourceError(c, err)
		return
	}
	items := make([]*supplierSourceResponse, 0, len(sources))
	for index := range sources {
		items = append(items, supplierSourceToResponse(&sources[index]))
	}
	response.Success(c, items)
}

func (h *SupplierSourceHandler) Get(c *gin.Context) {
	id, ok := supplierSourceID(c)
	if !ok {
		return
	}
	source, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		writeSupplierSourceError(c, err)
		return
	}
	response.Success(c, supplierSourceToResponse(source))
}

func (h *SupplierSourceHandler) Create(c *gin.Context) {
	var req supplierSourceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidRequest(c)
		return
	}
	source, err := h.service.Create(c.Request.Context(), req.toInput())
	if err != nil {
		writeSupplierSourceError(c, err)
		return
	}
	response.Created(c, supplierSourceToResponse(source))
}

func (h *SupplierSourceHandler) Update(c *gin.Context) {
	id, ok := supplierSourceID(c)
	if !ok {
		return
	}
	var req supplierSourceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidRequest(c)
		return
	}
	source, err := h.service.Update(c.Request.Context(), id, req.toInput())
	if err != nil {
		writeSupplierSourceError(c, err)
		return
	}
	response.Success(c, supplierSourceToResponse(source))
}

func (h *SupplierSourceHandler) PriorityPreview(c *gin.Context) {
	preview, err := h.service.PriorityPreview(c.Request.Context())
	if err != nil {
		writeSupplierSourceError(c, err)
		return
	}
	response.Success(c, supplierPriorityPreviewToResponse(preview))
}

func (h *SupplierSourceHandler) Sync(c *gin.Context) {
	id, ok := supplierSourceID(c)
	if !ok {
		return
	}
	result, err := h.service.Sync(c.Request.Context(), id)
	if err != nil {
		writeSupplierSourceSyncError(c, result, err)
		return
	}
	response.Success(c, result)
}

func (h *SupplierSourceHandler) Probe(c *gin.Context) {
	id, ok := supplierSourceID(c)
	if !ok {
		return
	}
	result, err := h.service.Probe(c.Request.Context(), id)
	if err != nil {
		writeSupplierSourceProbeError(c, result, err)
		return
	}
	response.Success(c, result)
}

func (h *SupplierSourceHandler) GetProbeJob(c *gin.Context) {
	id, ok := supplierSourceID(c)
	if !ok {
		return
	}
	jobID := strings.TrimSpace(c.Param("job_id"))
	if jobID == "" {
		response.InvalidRequest(c)
		return
	}
	result, err := h.service.GetSupplierProbeJob(c.Request.Context(), id, jobID)
	if err != nil {
		writeSupplierSourceProbeError(c, result, err)
		return
	}
	response.Success(c, result)
}

func writeSupplierSourceProbeError(c *gin.Context, result *service.SupplierSourceProbeResult, err error) {
	statusCode, message, reason, metadata := supplierSourceHTTPError(err)
	failedStep := ""
	if result != nil {
		failedStep = result.FailedStep
	}
	slog.Warn("supplier_source_probe_failed",
		"status_code", statusCode,
		"failed_step", failedStep,
		"message", message,
		"err", err.Error(),
	)
	c.JSON(statusCode, response.Response{
		Code: statusCode, Message: message, Reason: reason, Metadata: metadata, Data: result,
	})
}

func writeSupplierSourceSyncError(c *gin.Context, result *service.SupplierSourceSyncResult, err error) {
	statusCode, message, reason, metadata := supplierSourceHTTPError(err)
	c.JSON(statusCode, response.Response{
		Code: statusCode, Message: message, Reason: reason, Metadata: metadata, Data: result,
	})
}

func supplierSourceID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.InvalidRequest(c)
		return 0, false
	}
	return id, true
}

func writeSupplierSourceError(c *gin.Context, err error) {
	statusCode, message, reason, metadata := supplierSourceHTTPError(err)
	response.ErrorWithDetails(c, statusCode, message, reason, metadata)
}

func supplierSourceHTTPError(err error) (int, string, string, map[string]string) {
	var syncErr *service.UpstreamModelSyncError
	switch {
	case errors.Is(err, service.ErrSupplierSourceInvalidInput),
		errors.Is(err, service.ErrSupplierSourceInvalidPurchaseRatio),
		errors.Is(err, service.ErrSupplierSourceDuplicateClientModel):
		return http.StatusBadRequest, err.Error(), "", nil
	case errors.Is(err, service.ErrSupplierSourceNotFound):
		return http.StatusNotFound, err.Error(), "", nil
	case errors.Is(err, service.ErrSupplierSourceIdentityConflict),
		errors.Is(err, service.ErrSupplierSourceMultipleMatches):
		return http.StatusConflict, err.Error(), "", nil
	case errors.Is(err, service.ErrSupplierSourceProbeFailed),
		errors.Is(err, service.ErrSupplierProjectionProtocolNotReady):
		status := infraerrors.FromError(err)
		return int(status.Code), status.Message, status.Reason, status.Metadata
	case errors.As(err, &syncErr):
		switch syncErr.Kind {
		case service.UpstreamModelSyncErrorConfiguration, service.UpstreamModelSyncErrorUnsupported:
			return http.StatusBadRequest, syncErr.SafeMessage(), "", nil
		default:
			return http.StatusBadGateway, syncErr.SafeMessage(), "", nil
		}
	default:
		return http.StatusInternalServerError, "supplier source operation failed", "", nil
	}
}
