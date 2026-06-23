#!/usr/bin/env bash
set -euo pipefail

OUT="${1:-/tmp/kiro-tokenkey-fields.txt}"

python3 - "${OUT}" <<'PY'
import glob
import json
import os
import sys
from pathlib import Path

out_path = Path(sys.argv[1])
cache_dir = Path.home() / ".aws" / "sso" / "cache"
token_path = cache_dir / "kiro-auth-token.json"

if not token_path.exists():
    raise SystemExit(
        "找不到 Kiro token cache。请先打开 Kiro 并完成登录，再重新执行导出命令。"
    )

with token_path.open(encoding="utf-8") as handle:
    token = json.load(handle)

auth_method = "social" if str(token.get("authMethod", "")).lower() == "social" else "idc"
credentials = {
    "access_token": token.get("accessToken", ""),
    "refresh_token": token.get("refreshToken", ""),
    "region": token.get("region", "us-east-1"),
    "auth_method": auth_method,
    "profile_arn": token.get("profileArn", ""),
}

if auth_method == "idc":
    registrations = []
    for raw_path in glob.glob(str(cache_dir / "*.json")):
        path = Path(raw_path)
        if path.name == "kiro-auth-token.json":
            continue
        try:
            with path.open(encoding="utf-8") as handle:
                payload = json.load(handle)
        except Exception:
            continue
        if not payload.get("clientId") or not payload.get("clientSecret"):
            continue
        registrations.append((path.stat().st_mtime, path, payload))

    if not registrations:
        raise SystemExit(
            "找不到 Kiro IdC Client ID / Client Secret。请重新走一遍 Kiro 组织 SSO 登录。"
        )

    registrations.sort(key=lambda item: item[0], reverse=True)
    registration = registrations[0][2]
    credentials["client_id"] = registration.get("clientId", "")
    credentials["client_secret"] = registration.get("clientSecret", "")
else:
    credentials["client_id"] = ""
    credentials["client_secret"] = ""

required = ("access_token", "refresh_token", "region", "auth_method")
missing = [key for key in required if not str(credentials.get(key) or "").strip()]
if missing:
    raise SystemExit("Kiro credentials 缺少字段：" + ", ".join(missing))

if auth_method == "idc":
    missing_idc = [
        key for key in ("client_id", "client_secret")
        if not str(credentials.get(key) or "").strip()
    ]
    if missing_idc:
        raise SystemExit("Kiro IdC credentials 缺少字段：" + ", ".join(missing_idc))

lines = [
    "TokenKey Kiro OAuth 新建账号填写字段",
    "",
    "请只把下面字段填写到 TokenKey 后台。不要转发、截图或提交本文件。",
    "",
    "平台: Kiro",
    "类型: OAuth",
    "",
    f"Access Token: {credentials.get('access_token', '')}",
    f"Refresh Token: {credentials.get('refresh_token', '')}",
    f"Region: {credentials.get('region', '')}",
    f"认证方式: {credentials.get('auth_method', '')}",
    f"Client ID: {credentials.get('client_id', '')}",
    f"Client Secret: {credentials.get('client_secret', '')}",
    f"Profile ARN: {credentials.get('profile_arn', '')}",
    "",
    "接受 Kiro 服务条款: 勾选",
    "",
    "后台保存成功后，请删除本文件。",
]

out_path.parent.mkdir(parents=True, exist_ok=True)
out_path.write_text("\n".join(lines) + "\n", encoding="utf-8")
os.chmod(out_path, 0o600)
print(str(out_path))
PY

echo
echo "已生成 TokenKey 后台填写字段：${OUT}"
echo "打开查看：open ${OUT}"
echo "后台保存后删除：rm -f ${OUT}"
