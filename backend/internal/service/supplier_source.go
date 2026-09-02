package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var errSupplierSourceInvalidChannelType = fmt.Errorf("%w: channel_type", ErrSupplierSourceInvalidInput)

type SupplierProbeStatus string

const (
	SupplierProbeStatusPassed              SupplierProbeStatus = "passed"
	SupplierProbeStatusFailed              SupplierProbeStatus = "failed"
	SupplierProbeStatusAuthFailed          SupplierProbeStatus = "auth_failed"
	SupplierProbeStatusModelUnsupported    SupplierProbeStatus = "model_unsupported"
	SupplierProbeStatusProtocolUnsupported SupplierProbeStatus = "protocol_unsupported"
)

var (
	ErrSupplierSourceNotFound             = errors.New("supplier source not found")
	ErrSupplierSourceInvalidPurchaseRatio = errors.New("supplier source purchase ratio must be a number greater than 0 and at most 1")
	ErrSupplierSourceDuplicateClientModel = errors.New("supplier source client model ids must be unique")
	ErrSupplierSourceInvalidInput         = errors.New("invalid supplier source input")
	ErrSupplierSourceProbeFailed          = infraerrors.New(http.StatusUnprocessableEntity, "SUPPLIER_SOURCE_PROBE_FAILED", "one or more supplier models failed validation")
	ErrSupplierSourceValidateRequired     = infraerrors.New(http.StatusUnprocessableEntity, "SUPPLIER_SOURCE_VALIDATE_REQUIRED", "supplier models must be validated before projection")
	ErrSupplierSourceMultipleMatches      = errors.New("supplier source matched multiple accounts")
	ErrSupplierSourceIdentityConflict     = errors.New("supplier source identity already exists")
	ErrSupplierProjectionReadbackMismatch = errors.New("supplier projection readback mismatch")
	ErrSupplierProjectionProtocolNotReady = infraerrors.New(http.StatusUnprocessableEntity, "SUPPLIER_PROTOCOL_NOT_READY", "supplier account protocol capability is not ready")
	ErrSupplierProjectionReaderMissing    = errors.New("supplier projection reader unavailable")
	ErrSupplierProjectionUpdaterMissing   = errors.New("supplier projection updater unavailable")
	ErrSupplierReservedAccountExtra       = infraerrors.BadRequest("SUPPLIER_RESERVED_ACCOUNT_EXTRA", "supplier_source_id and supplier_discount_band are reserved for supplier-source sync")
)

const (
	SupplierSourceIDExtraKey                = "supplier_source_id"
	SupplierDiscountBandExtraKey            = "supplier_discount_band"
	SupplierSourceDefaultAccountConcurrency = 1000
	SupplierSourceMaxAccountConcurrency     = 1<<31 - 1
)

type SupplierSource struct {
	ID                    int64
	SupplierName          string
	ChannelName           string
	ChannelType           int
	Endpoint              string
	EncryptedCredential   string
	Notes                 string
	BasePriority          int
	AccountConcurrency    int
	CredentialFingerprint string
	Models                []SupplierSourceModel
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type SupplierSourceModel struct {
	ClientModelID   string   `json:"client_model_id"`
	UpstreamModelID string   `json:"upstream_model_id"`
	PurchaseRatio   *float64 `json:"purchase_ratio"`
}

type SupplierSourceInput struct {
	SupplierName       string
	ChannelName        string
	ChannelType        int
	Endpoint           string
	Credential         string
	BasePriority       *int
	AccountConcurrency *int
	Notes              string
	Models             []SupplierSourceModelInput
}

type SupplierSourceModelInput struct {
	ClientModelID   string
	UpstreamModelID string
	PurchaseRatio   *float64
}

type SupplierProbeResult struct {
	ClientModelID   string              `json:"client_model_id"`
	UpstreamModelID string              `json:"upstream_model_id"`
	Status          SupplierProbeStatus `json:"status"`
	Protocol        string              `json:"protocol,omitempty"`
	Detail          string              `json:"detail,omitempty"`
}

func (i *SupplierSourceInput) Normalize() error {
	if i == nil {
		return ErrSupplierSourceInvalidInput
	}
	i.SupplierName = strings.TrimSpace(i.SupplierName)
	i.ChannelName = strings.TrimSpace(i.ChannelName)
	i.Notes = strings.TrimSpace(i.Notes)
	endpoint, err := NormalizeSupplierEndpoint(i.Endpoint)
	if err != nil {
		return err
	}
	i.Endpoint = endpoint
	for index := range i.Models {
		i.Models[index].ClientModelID = strings.TrimSpace(i.Models[index].ClientModelID)
		i.Models[index].UpstreamModelID = strings.TrimSpace(i.Models[index].UpstreamModelID)
	}
	return nil
}

func (i SupplierSourceInput) Validate() error {
	if err := (&i).Normalize(); err != nil {
		return err
	}
	if i.SupplierName == "" || i.ChannelName == "" || i.Endpoint == "" {
		return ErrSupplierSourceInvalidInput
	}
	if err := validateSupplierChannelType(i.ChannelType); err != nil {
		return err
	}
	if i.BasePriority != nil {
		if _, err := SupplierAccountPriority(*i.BasePriority, 6); err != nil {
			return err
		}
	}
	if i.AccountConcurrency != nil {
		if *i.AccountConcurrency <= 0 || *i.AccountConcurrency > SupplierSourceMaxAccountConcurrency {
			return ErrSupplierSourceInvalidInput
		}
	}
	seen := make(map[string]struct{}, len(i.Models))
	for _, model := range i.Models {
		if model.ClientModelID == "" || model.ClientModelID == "*" || strings.Contains(model.ClientModelID, "全系列") ||
			model.UpstreamModelID == "" || model.UpstreamModelID == "*" || strings.Contains(model.UpstreamModelID, "全系列") {
			return ErrSupplierSourceInvalidInput
		}
		key := strings.ToLower(model.ClientModelID)
		if _, ok := seen[key]; ok {
			return ErrSupplierSourceDuplicateClientModel
		}
		seen[key] = struct{}{}
		if model.PurchaseRatio != nil && (math.IsNaN(*model.PurchaseRatio) || math.IsInf(*model.PurchaseRatio, 0) ||
			*model.PurchaseRatio <= 0 || *model.PurchaseRatio > 1) {
			return ErrSupplierSourceInvalidPurchaseRatio
		}
	}
	return nil
}

func NormalizeSupplierEndpoint(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("%w: invalid endpoint", ErrSupplierSourceInvalidInput)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", fmt.Errorf("%w: endpoint scheme", ErrSupplierSourceInvalidInput)
	}
	parsed.Fragment = ""
	parsed.RawQuery = ""
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	// Qianfan BaiduV2 accounts require the bare host root; strip accidental /v1 or /v2 suffixes.
	if strings.EqualFold(parsed.Hostname(), "qianfan.baidubce.com") {
		return newapiintegration.QianfanBaseURL, nil
	}
	return parsed.String(), nil
}

func ResolveSupplierSourceAccountConcurrency(value int) int {
	if value <= 0 {
		return SupplierSourceDefaultAccountConcurrency
	}
	return value
}

type SupplierSourceRepository interface {
	Create(ctx context.Context, source *SupplierSource) error
	Update(ctx context.Context, source *SupplierSource) error
	Get(ctx context.Context, id int64) (*SupplierSource, error)
	List(ctx context.Context) ([]SupplierSource, error)
}
