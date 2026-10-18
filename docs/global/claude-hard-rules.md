# Project hard rules (CLAUDE.md overflow)

Canonical **session-level** hard rules stay as short bullets in root [`CLAUDE.md`](../../CLAUDE.md).
This file holds the long-form detail for the same numbered sections so Claude Code
does not auto-load essays every turn. Read the matching § when touching that area.

## §4 Cross-repo dependency: New API

`backend/go.mod` contains:

```
replace github.com/QuantumNous/new-api => ../../new-api
```

**Required layout:** `new-api` must sit next to `sub2api` under the same parent
directory. The `../../new-api` path resolves from `sub2api/backend/` up two levels
to the parent, then into `new-api/`.

**Pinned commit (`.new-api-ref` is the single source of truth):**

The repo-root file `.new-api-ref` records the exact `QuantumNous/new-api` commit
SHA used by both local dev and CI. `scripts/upstream/sync-new-api.sh` and the two
workflows (`release.yml`, `backend-ci.yml`) both read it, so the release Docker
image is bit-identical to what is tested locally.

```bash
bash scripts/upstream/sync-new-api.sh           # pull sibling clone to the pinned SHA
bash scripts/upstream/sync-new-api.sh --check   # CI-style drift check; exit 1 if mismatch
bash scripts/upstream/sync-new-api.sh --bump <sha>   # update .new-api-ref + sync
```

**Bumping the pin:** `--bump <sha>` → `make test` → `git add .new-api-ref` →
commit. **NEVER** hand-edit hardcoded SHAs in workflows.

**Docker build:** From the parent of `sub2api/`, run
`docker build -f sub2api/Dockerfile -t sub2api:local .`, or from `deploy/`:
`docker compose -f docker-compose.dev.yml build`. See `Dockerfile` header.

**Constraints:**

- Import only stateless packages: `relay/channel/`*, `relay/common/*`, `dto/*`,
  `constant/*`, `types/*`, `service/` (affinity). **NEVER** call GORM DB
  operations from New API code.
- New API integration logic lives in `internal/integration/newapi/`. Keep it there.
- When upstream changes break compilation, fix the bridge — do NOT modify New API
  from this repo.
- New-api packages may register top-level `flag.Bool` (e.g. `-version`) in their
  `init()`; check `flag.Lookup` before defining your own to avoid `flag redefined`
  panics at startup. See `backend/cmd/server/main.go`.

**Worktrees + the `../../new-api` sibling (turnkey bootstrap):**

Default to an **isolated git worktree** for any commit-bearing task — sharing the
primary checkout's single mutable HEAD/index with a parallel agent lets one
`git checkout` land commits on the wrong branch. A worktree created at a deep path
(`.claude/worktrees/<name>/`) breaks `replace … => ../../new-api` resolution.

- Run `bash dev-rules/templates/worktree-bootstrap.sh <worktree_dir>` after
  creating a worktree. It inits the `dev-rules` submodule and runs
  `scripts/worktree-bootstrap-hook.sh`, which symlinks the deep-path-resolved
  `new-api` location to the real sibling clone so `go build` / preflight work.
- Sibling-placed worktrees resolve `../../new-api` natively — the hook is a no-op.
- The real sibling clone is still located/synced by
  `scripts/upstream/sync-new-api.sh` (`.new-api-ref`); the hook only fixes path
  resolution, never the pin.

## §5.x Deletion discipline — default = keep, override; never silent-delete

**Default assumption: an upstream feature stays compiled in.** TokenKey almost
always wants to **change defaults / wire new behavior**, not strip community
capabilities. Quietly deleting upstream files is the highest-risk form of
divergence because:

1. It silently regresses functionality operators may rely on.
2. It guarantees recurring merge conflicts at every upstream change to deleted
   call sites.
3. It loses upstream tests + docs for that feature.

**Rules:**

- **NEVER** delete an upstream-owned file/method/route to "clean up" — discuss
  instead.
- If TK truly does not want a feature, prefer in order:
  1. **Override the default** via migration or `InitializeDefaultSettings`.
  2. **Add an admin-toggleable setting** and a `*_tk_*.go` companion that
     short-circuits at the call site.
  3. Last resort, **comment out the registration** with
     `// TK: disabled because <ticket>` — easier to re-enable than a deletion.
- Any PR that net-deletes upstream symbols MUST: (a) link the upstream commit
  being reverted, (b) state the regression cost, (c) list which upstream tests
  are now skipped or removed.
- Drift signal: `git diff --diff-filter=D upstream/main..HEAD -- backend/` — if
  non-empty, the next merge will fight; re-evaluate.

Forward-looking history, merge modes, and mechanical gates:
[`upstream-merge-discipline.md`](upstream-merge-discipline.md).

## §9 Release discipline (ARM + tag triggers)

Production (`api.tokenkey.dev`) runs on **AWS Tokyo EC2 `c7g.xlarge` (`arm64`)**
（2026-10-17 起）。活舰队 SSOT：
[`deploy/hetzner/README.md`](../../deploy/hetzner/README.md)（prod-only）。Release workflow is triggered by `tags: v*`。镜像仍须 multi-arch arm64。

### §9.1 `simple_release` MUST stay `false`

`.github/workflows/release.yml` `workflow_dispatch` input `simple_release`
**DEFAULT MUST REMAIN `false`.**

- `simple_release=true` → GoReleaser builds **`linux/amd64` only**, then
  **overwrites** shared tags `:latest`, `:X`, `:X.Y`, `:X.Y.Z`.
- ARM hosts pulling those tags crash with `exec format error`.
- **NEVER** flip the default to `true`, **NEVER** dispatch with
  `simple_release=true` unless every consumer is verified amd64.
- Accidental dispatch: re-dispatch the **same** tag with
  `simple_release=false` immediately.
- Workflow already prints `::warning::` + Step Summary when true — treat as
  stop-the-line.

### §9.2 `VERSION` bump commits MUST NOT carry skip-ci markers

Release is triggered by tag push, but GitHub evaluates skip-ci markers against
the **commit message the tag points at**.

```
git commit -m "chore: bump VERSION to X.Y.Z [skip ci]"   # BAD
git tag vX.Y.Z
git push origin main vX.Y.Z                              # release.yml silently SKIPPED
```

**Rule:** hand-bumped `backend/cmd/server/VERSION` commits MUST NOT contain
`[skip ci]` / `[ci skip]`. **Discussing the marker counts as carrying it.**
The only commits that may include those bracketed forms are auto-generated
`sync-version-file` writebacks from `release.yml`.

**Mechanical enforcement:** `bash scripts/release-tag.sh vX.Y.Z` (not raw
`git tag`). Merge/`main` PR shape workflows enforce the same on landing
commits (imported upstream messages exempt).

**Discussion-of-marker discipline:** PR title/body/commit messages that land on
`main` MUST use unbracketed forms when discussing these markers —
`skip-ci`, `ci-skip`, or `skip ci` (no brackets). Root `CLAUDE.md` text is
exempt (file contents never reach commit-message context).
`main-ancestry-guard.yml` Check 3 enforces this.

## Host patching / 宿主机打补丁 (detail)

**宿主机不自动打补丁。补丁是一次蓝绿发版。**

Ubuntu 镜像出厂启用 `apt-daily-upgrade.timer`（`OnCalendar=*-*-* 6:00` +
`RandomizedDelaySec=60m`）和 `APT::Periodic::Unattended-Upgrade "1"`。这两处都不是
本仓写的，是装机镜像带进来的（`/etc/apt/apt.conf.d/20auto-upgrades` mtime 停在
2024-02-12，比现有节点出生早八个月；全仓 grep `apt-daily` 零命中）。不是误配，是遗漏。

对网关主机，这个默认值恰好最差：

1. 06:00–07:00 UTC 窗口换成 CST 是 **14:00–15:00**，正好压在流量高峰。
2. `libc6` 的 postinst 会跑 `systemctl daemon-reexec`
   （`/var/lib/dpkg/info/libc6:arm64.postinst`），systemd 重新 exec 连带重启
   systemd-networkd / resolved / timesyncd、eth0 重走 DHCP。**容器自己不重启**，
   但容器内在飞的上游 TLS 连接被整片打断——它们共享同一条 eth0 出口。
   2026-10-11 06:46:05 UTC prod 实测：九分钟内约 56 次用户可见 502，横跨
   newapi / openai / anthropic 三个平台。
3. `Unattended-Upgrade::Automatic-Reboot` 为 `false`（这个默认值本身是对的，自动重启
   生产网关更糟），于是主机装了却从不激活：glibc 和内核在磁盘上已更新，长驻进程仍
   映射旧版本。**付了可用性代价，没拿到安全收益。**

因此生产 bootstrap（`deploy/hetzner/render-prod-bootstrap.sh`）
在首次 apt 调用前 `mask` 掉该 timer，并用 `/etc/apt/apt.conf.d/99tokenkey-no-auto-upgrade`
固定 `APT::Periodic::Unattended-Upgrade "0"`。用 `mask` 不用 `disable`——后者会被包升级和
systemd preset 静默重开；写进 `99-` 新文件而不是改 `20auto-upgrades`——后者是装机器产物，
apt 按字典序后读者胜。改完必须重跑 renderer 让 generated user-data 同步。

`apt-daily.timer` **故意保留**：它只刷包列表、不重启任何 unit，留着才能让补丁债可见；
一起关掉等于让自己瞎掉。

不要折中。把 `libc6` 加黑名单、或把窗口挪到半夜，都只改了"什么时候挨刀"，没改"挨了刀
还不管用"；而真正需要紧急修的包（glibc、kernel、openssl、openssh）恰好全是激活必须重启
的那批。"自动装、从不重启"是合规幻觉。

**门禁：** preflight `host auto-upgrade disabled`
（`scripts/checks/host-auto-upgrade-disabled.py`，`.preflight/gates.json` → `deploy` 作用域）
同时校验 renderer 与 generated 产物，带 `--selftest`。新增 fleet bootstrap 必须进该脚本的
`BOOTSTRAPS`，否则它静默继承 Ubuntu 默认。

**打补丁流程：** 零流量待机侧 `full-upgrade` + **真重启** + 健康检查 → 切流 → 另一侧重复。
只读盘点每节点债务：

```bash
bash ops/observability/run-probe.sh --target prod \
  --script ops/observability/probe-host-patch-debt.sh
```

报 `reboot_required` / `upgradable_count` / 运行内核 vs 已装内核 / `daemon_reexec_7d`，
并复核 live 主机是否仍符合 bootstrap 契约（期望 `timer_apt_daily_upgrade=masked`、
`periodic_unattended=0`）。debt 非零**不阻塞发版**，只决定下次蓝绿要不要顺带还债。

**已在跑的主机**（bootstrap 只管新建）用
`ops/observability/remediate-disable-host-auto-upgrade.sh` 对齐契约：默认 dry-run，
需 `APPLY=yes-disable-host-auto-upgrade`，幂等。它只做止血——mask timer + 固定
`Periodic=0`，**不装不卸不升级、不重启任何服务或容器**，因此把主机冻结在
"磁盘已打补丁、内存仍旧"的状态；`patch_debt=unchanged` 会在 verify 输出里显式标出。
还债仍然只能走蓝绿。已于 2026-10-11 对 live 五节点执行完毕。
关掉自动升级之后真正的风险是"再也不打补丁而没人发现"，这个探测就是对冲。

## Account Usage SSOT (detail)

- NewAPI 可恢复配额窗口的解析、Extra 快照和用量投影归
  `backend/internal/service/newapi_usage_window_tk.go`；冷却/到期资格继续消费
  `SetRateLimited` / `Account.IsSchedulable`，不改手动暂停状态。
- 本地窗口由 `buildLocalWindowUsageFromStats` 提供；
  `UsageProgress.utilization_unknown` 区分未观测配额与已知 0%。过期耗尽快照回到未知。
- 今日和窗口统计统一由 `frontend/src/components/account/UsageStatsRow.vue` 展示；
  `TodayStatsBadges` 只接今日标签，`UsageProgressBar` 负责已知配额进度，
  `UpstreamQuotaSummary` 仅补充未重复的配额维度。
- Ali Token Plan 迁移别名归 `newAPIAliTokenPlanModelAliases`，预设、账号 floor
  与生成 bundle 同源；通用 Ali PAYG 不消费这些别名。
