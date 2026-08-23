package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"go.uber.org/zap"
)

// tkSelectionFailureLogFields appends ops_system_log_skip when the OpenAI-compat
// scheduler already classified the failure (service-layer diagnostics own the
// ops_system_logs row; handler warn echoes are for stdout/file only).
func tkSelectionFailureLogFields(err error, fields ...zap.Field) []zap.Field {
	if service.OpenAICompatSelectionFailureOpsSystemLogRedundant(err) {
		fields = append(fields, zap.Bool(logger.OpsSystemLogSkipField, true))
	}
	return fields
}
