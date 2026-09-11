package service

import "slices"

// MachineAdminPermission is the sole registry for machine administrator access.
// Routes are exact HTTP method + Gin FullPath pairs, never URL prefixes. Adding
// an admin endpoint does not silently grant it to existing machine credentials.
type MachineAdminPermission struct {
	Scope       string   `json:"scope"`
	Description string   `json:"description"`
	Routes      []string `json:"routes"`
}

var machineAdminPermissions = []MachineAdminPermission{
	{"accounts:read", "读取账号配置及用量；沿用现有 DTO 脱敏，扩展字段及备注仍可能敏感", []string{
		"GET /api/v1/admin/accounts", "GET /api/v1/admin/accounts/:id",
		"GET /api/v1/admin/accounts/:id/stats", "GET /api/v1/admin/accounts/:id/usage",
		"GET /api/v1/admin/accounts/:id/today-stats", "POST /api/v1/admin/accounts/today-stats/batch",
		"POST /api/v1/admin/accounts/usage/batch", "GET /api/v1/admin/accounts/:id/temp-unschedulable",
		"GET /api/v1/admin/accounts/:id/models", "GET /api/v1/admin/accounts/antigravity/default-model-mapping",
	}},
	{"accounts:import", "创建及导入账号（可绑定已有分组和代理）；数据包导入还需 proxies:write", []string{
		"POST /api/v1/admin/accounts", "POST /api/v1/admin/accounts/batch",
		"POST /api/v1/admin/accounts/data", "POST /api/v1/admin/accounts/import/codex-session",
		"POST /api/v1/admin/accounts/import/antigravity-oauth",
	}},
	{"accounts:write", "修改账号、凭据、路由及调度并刷新凭据；具有影响流量及凭据外送的能力", []string{
		"PUT /api/v1/admin/accounts/:id", "POST /api/v1/admin/accounts/bulk-update",
		"POST /api/v1/admin/accounts/batch-update-credentials",
		"POST /api/v1/admin/accounts/:id/apply-oauth-credentials",
		"POST /api/v1/admin/accounts/:id/refresh", "POST /api/v1/admin/accounts/batch-refresh",
		"POST /api/v1/admin/accounts/:id/clear-error", "POST /api/v1/admin/accounts/batch-clear-error",
		"POST /api/v1/admin/accounts/:id/clear-rate-limit", "POST /api/v1/admin/accounts/:id/reset-quota",
		"DELETE /api/v1/admin/accounts/:id/temp-unschedulable", "POST /api/v1/admin/accounts/:id/schedulable",
		"POST /api/v1/admin/accounts/:id/set-privacy", "POST /api/v1/admin/accounts/:id/refresh-tier",
		"POST /api/v1/admin/accounts/batch-refresh-tier",
	}},
	{"accounts:export", "导出上游凭据原文；账号数据包同时要求 proxies:export", []string{
		"GET /api/v1/admin/accounts/data",
	}},
	{"accounts:delete", "删除账号（包含批量删除）", []string{
		"DELETE /api/v1/admin/accounts/:id", "POST /api/v1/admin/accounts/batch-delete",
	}},
	{"proxies:read", "读取代理配置，包含代理用户名及密码", []string{
		"GET /api/v1/admin/proxies", "GET /api/v1/admin/proxies/all", "GET /api/v1/admin/proxies/:id",
	}},
	{"proxies:write", "创建、导入及修改代理；数据包导入可修改已有代理", []string{
		"POST /api/v1/admin/proxies", "POST /api/v1/admin/proxies/batch",
		"POST /api/v1/admin/proxies/data", "PUT /api/v1/admin/proxies/:id",
		"POST /api/v1/admin/accounts/data",
	}},
	{"proxies:export", "导出代理凭据原文（含账号数据包中的代理）", []string{
		"GET /api/v1/admin/proxies/data", "GET /api/v1/admin/accounts/data",
	}},
	{"groups:read", "读取分组配置以选择账号绑定；不包含用户 API key", []string{
		"GET /api/v1/admin/groups", "GET /api/v1/admin/groups/all", "GET /api/v1/admin/groups/:id",
	}},
}

// MachineAdminPermissions returns a copy so callers cannot mutate the policy.
func MachineAdminPermissions() []MachineAdminPermission {
	result := slices.Clone(machineAdminPermissions)
	for i := range result {
		result[i].Routes = slices.Clone(result[i].Routes)
	}
	return result
}

func validMachineAdminScope(scope string) bool {
	return slices.ContainsFunc(machineAdminPermissions, func(p MachineAdminPermission) bool { return p.Scope == scope })
}

// MachineAdminAllows requires ALL scopes registered for a route. For example,
// account bundles can both carry proxy passwords and create/update proxies.
func MachineAdminAllows(scopes []string, method, fullPath string) bool {
	known := false
	for _, permission := range machineAdminPermissions {
		if slices.Contains(permission.Routes, method+" "+fullPath) {
			known = true
			if !slices.Contains(scopes, permission.Scope) {
				return false
			}
		}
	}
	return known
}

// MachineAdminAllowsStepUp is deliberately narrower than general access.
// Machine export authorization replaces human MFA only on these reviewed
// exports; a future conditional step-up cannot accidentally inherit a bypass.
func MachineAdminAllowsStepUp(scopes []string, method, fullPath string) bool {
	return method == "GET" && (fullPath == "/api/v1/admin/accounts/data" || fullPath == "/api/v1/admin/proxies/data") &&
		MachineAdminAllows(scopes, method, fullPath)
}
