package handler

import (
	"context"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (h *AuthHandler) ensureBackendModeAllowsUser(ctx context.Context, user *service.User) error {
	if user == nil {
		return infraerrors.Unauthorized("INVALID_USER", "user not found")
	}
	if h == nil || !h.isBackendModeEnabled(ctx) || user.IsAdmin() {
		return nil
	}
	return infraerrors.Forbidden("BACKEND_MODE_ADMIN_ONLY", "Backend mode is active. Only admin login is allowed.")
}

func (h *AuthHandler) ensureBackendModeAllowsNewUserLogin(ctx context.Context) error {
	if h == nil || !h.isBackendModeEnabled(ctx) {
		return nil
	}
	return infraerrors.Forbidden("BACKEND_MODE_ADMIN_ONLY", "Backend mode is active. Only admin login is allowed.")
}

func (h *AuthHandler) isBackendModeEnabled(ctx context.Context) bool {
	if h == nil || h.settingSvc == nil {
		return false
	}
	settings, err := h.settingSvc.GetPublicSettings(ctx)
	if err == nil && settings != nil {
		return settings.BackendModeEnabled
	}
	return h.settingSvc.IsBackendModeEnabled(ctx)
}
