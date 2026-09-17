## 6) 已知失败模式与定位

| 现象 | 根因候选 | 第一步 |
|---|---|---|
| OIDC `AssumeRoleWithWebIdentity` / `Not authorized`，provision 未开始 | 新 `edge-<id>` Environment 未加入 `cicd-oidc` AllowedSubjects，或 stack 未 `--parameter-overrides` | §1.5a：查 IAM role trust `sub` 列表 |
| 本机 `provision-edge.sh` → `iam:PassRole` denied | 普通 IAM user 无 Hybrid activation 权限 | 改走 GHA workflow，不要本地 provision |
| "EDGE_MAIN_GATEWAY_ALLOWED_CIDR not set" | Environment 漏配 | 加到 `edge-<edge_id>`；不要给默认值 |
| `provision` 步骤 fail，managed instance 未注册 | SSM Hybrid activation expired / 网络不通 | Lightsail 浏览器 SSH 看 `/var/log/tokenkey-lightsail-bootstrap.log`；`BOOTSTRAP_FAIL: ` 行即根因 |
| GHA `smoke` / SSM 步骤失败，本机同 tag 却正常 | **`aws` / workflow 用了错的 `--region`**（误用 `ec2_equivalent_region`）或 job **工作目录**与脚本相对路径不一致 | 用 `python3 ops/stage0/edge_ssm_execution.py --repo-root . --edge-id <id> --format env` 核对输出的 **`REGION`**；workflow 里凡 SSM 调用必须与之一致；检查 `defaults.run.working-directory` / `cd` |
| `external_health` 报 5xx | Caddy 还在签证书 / docker compose 起不来 | `ssh` 进实例 `docker compose -f /var/lib/tokenkey/docker-compose.yml ps` |
| 公网 `curl https://api-<id>.tokenkey.dev` **连接超时** | Lightsail 防火墙 **缺 TCP 443**（基线 TCP 443 + 8443 + UDP 34567；80 应关，双用途 edge 的 22 仅允许一个公网 IPv4 /32） | §2.2 `verify-edge-lightsail-network.sh --enforce-ports` |
| DNS 已指 Static IP 但 **TLS handshake 失败** | provision 时 DNS 为 NXDOMAIN，ACME 未签成功 | §3 `--renew-cert` 重启 `tokenkey-caddy` |
| Static IP 已分配但 attach 失败 | 旧 instance 还在持有该 Static IP | `aws lightsail detach-static-ip` 再重 attach；或 `recreate=true` 重来 |
| GHCR pull 401 | PAT 过期 / 写错 SSM 路径 | 用 1.4 重新 put-parameter |
| Squash 合并后 `git branch -d feature/...` 拒绝删除 | Squash 不产生「分支 tip 是 main 祖先」关系 | `git checkout main && git pull --ff-only` 后 `git branch -D feature/...`；`git remote prune origin` |
