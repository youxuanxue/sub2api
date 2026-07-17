-- TK: per-user "可导出对话记录(traj)" 授权开关。默认 false（关闭）。
-- 管理员在用户编辑面板为某用户开启后，该用户每个 API Key 行出现导出入口，
-- 可独立导出其经网关捕获的对话轨迹（qa traj v2 导出器，见 #685）。
-- 后端导出端点 POST /api/v1/users/me/qa/traj/export 亦据此字段做 403 兜底，
-- 防止绕过前端可见性闸。纯能力授权，不影响计费/转发/捕获本身。
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS traj_export_enabled BOOLEAN NOT NULL DEFAULT false;

COMMENT ON COLUMN users.traj_export_enabled IS
    'Admin-granted switch allowing the user to export each API key''s captured conversation trajectory (qa traj v2). Default false. Gates both the UI export entry and the self-export endpoint.';
