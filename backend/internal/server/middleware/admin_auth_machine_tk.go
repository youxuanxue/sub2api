package middleware

import (
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const machineAdminContextKey = "tk_machine_admin_identity"

func GetMachineAdminIdentity(c *gin.Context) (*service.MachineAdminKey, bool) {
	value, exists := c.Get(machineAdminContextKey)
	identity, ok := value.(*service.MachineAdminKey)
	return identity, exists && ok && identity != nil && c.GetString("auth_method") == service.AuditAuthMethodMachineAdminKey
}

func validateMachineAdminKey(c *gin.Context, token string, settings *service.SettingService, users *service.UserService) bool {
	if settings == nil || users == nil {
		AbortWithError(c, 503, "ADMIN_AUTH_UNAVAILABLE", "Admin authentication unavailable")
		return false
	}
	identity, err := settings.AuthenticateMachineAdminKey(c.Request.Context(), token)
	if err != nil {
		if errors.Is(err, service.ErrInvalidMachineAdminKey) {
			AbortWithError(c, 401, "INVALID_ADMIN_KEY", "Invalid admin API key")
		} else {
			AbortWithError(c, 503, "ADMIN_AUTH_UNAVAILABLE", "Admin authentication unavailable")
		}
		return false
	}
	// Recheck the issuing administrator on every request; never impersonate
	// whichever account happens to be returned by GetFirstAdmin.
	owner, err := users.GetByIDForAuth(c.Request.Context(), identity.OwnerUserID)
	if err != nil || owner == nil || !owner.IsActive() || !owner.IsAdmin() {
		AbortWithError(c, 401, "INVALID_ADMIN_KEY", "Invalid admin API key")
		return false
	}
	c.Set(string(ContextKeyUser), AuthSubject{UserID: owner.ID, Concurrency: owner.Concurrency})
	c.Set(string(ContextKeyUserRole), owner.Role)
	c.Set(ContextKeyAuthEmail, owner.Email)
	c.Set("auth_method", service.AuditAuthMethodMachineAdminKey)
	c.Set(machineAdminContextKey, identity)
	SetAuditExtra(c, map[string]any{"machine_key_id": identity.ID})
	return true
}

// Authorization lives inside the shared admin authentication boundary, so it
// also protects separately registered admin groups (e.g. payment) and future
// consumers. Rejections happen before handler/body processing.
func authorizeMachineAdminRequest(c *gin.Context, audit *service.AuditLogService) bool {
	identity, ok := GetMachineAdminIdentity(c)
	if ok && service.MachineAdminAllows(identity.Scopes, c.Request.Method, c.FullPath()) {
		return true
	}
	AbortWithError(c, 403, "MACHINE_ADMIN_SCOPE_REQUIRED", "Machine credential is not authorized for this operation")
	// The regular audit middleware runs after authentication. Record scope
	// denials here too, without capturing a credential-bearing request body.
	if ok && audit != nil {
		uid := identity.OwnerUserID
		audit.Record(&service.AuditLog{
			CreatedAt: time.Now().UTC(), Action: "admin.machine_scope.denied",
			ActorUserID: &uid, ActorEmail: c.GetString(ContextKeyAuthEmail), ActorRole: service.RoleAdmin,
			AuthMethod: service.AuditAuthMethodMachineAdminKey, Method: c.Request.Method,
			Path: c.FullPath(), StatusCode: 403, ClientIP: SecurityClientIP(c),
			Extra: map[string]any{"machine_key_id": identity.ID},
		})
	}
	return false
}

// RequireHumanAdmin prevents all machine credentials, including the legacy
// global key, from minting/revoking scoped credentials. The admin auth
// middleware must precede this guard; sensitive writes additionally use MFA
// according to the existing step-up setting.
func RequireHumanAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, ok := GetUserRoleFromContext(c)
		if !ok || role != service.RoleAdmin || c.GetString("auth_method") != service.AuditAuthMethodJWT {
			AbortWithError(c, 403, "HUMAN_ADMIN_REQUIRED", "An administrator session is required")
			return
		}
		c.Next()
	}
}
