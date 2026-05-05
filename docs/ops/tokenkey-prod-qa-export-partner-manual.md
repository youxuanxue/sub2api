# TokenKey 生产环境 QA 全量导出与清理（运营合伙人简版）

适用对象：需在 **AWS prod（Stage-0）** 做 **QA evidence 数据** 全量备份、随后在云上 **释放 PG 与磁盘占用** 的 TokenKey 运营合伙人。
技术细节与风险见 `deploy/aws/README.md` 中「Prod QA 全量导出与清理」一节。

---

## 你将得到什么

- **全量 QA**：表 `qa_records` 的 JSON Lines 导出 + 磁盘上 **`qa_blobs`** 整树（含捕获的请求/响应/流式片段等；**正文为已脱敏、`logredact` 处理后的可分析内容**，适用于含 tools 的 JSON 请求体等）。
- **云上清理（可选但通常需要）**：清空 `qa_records`、清空 **`/var/lib/tokenkey/app/qa_blobs`**（及必要时 `qa_dlq`），并删除用于传递 tar 包的 **S3 暂存对象**，避免 prod EBS 与 staging 桶长期占用。

说明：**未脱敏的原始请求**不会出现在本管道中；若业务上需要，须另议其他渠道，**不在本文范围**。

---

## 前置条件

| 条件 | 说明 |
|------|------|
| AWS 权限 | 能对 prod 实例做 **SSM Run Command**（`ssm:SendCommand` / `GetCommandInvocation`）；能对 **staging 桶**做 **GetObject + PutObject**（用于预签名上传/本机下载）。 |
| 工具 | 本机已装 `aws`、`jq`、`curl`、`python3`；可选 `zstd`（读 blob）。 |
| Staging 桶 | 环境变量 **`QA_DUMP_S3_BUCKET`** 指向**专用或共用的私有桶**（仅作 tar 中转，用完脚本会删对象）。**桶名属基础设施配置**，由团队提供；若未设置，脚本会报错退出。 |
| 时间窗口 | TRUNCATE `qa_records` 会短暂锁表；建议在**低峰**操作。清理前确保**已有一份校验通过的本地导出**。 |

---

## 一次性配置（本机 shell）

```bash
export AWS_REGION=us-east-1
export QA_DUMP_S3_BUCKET="<由团队提供的 staging 桶名>"
# 可选：导出落盘目录（默认 ./.dump_qa），建议带日期便于区分
export OUT_DIR="./.dump_qa/prod-export-$(date -u +%Y%m%d)"
```

---

## 只做「全量导出」（不删云上数据）

脚本：`scripts/fetch-prod-qa-dump.sh`

```bash
bash scripts/fetch-prod-qa-dump.sh
```

流程摘要：经 SSM 在 prod 实例打包 → 预签名 **PUT** 到 `QA_DUMP_S3_BUCKET` → 本机 **下载并解压** → 生成 **`$OUT_DIR/.last-prod-qa-export.json`**（行数、校验和、时间戳等）。

自检（不连 AWS 跑逻辑，只检工具与变量）：

```bash
bash scripts/fetch-prod-qa-dump.sh --check
```

---

## 「导出 + 清理云上占用」（推荐用于释放 prod 资源）

脚本：`scripts/prod-qa-export-and-purge.sh`

该脚本顺序为：**先完整导出并本地校验** → （可选行数门禁）→ **远端 TRUNCATE + 清空 blob 目录** → **删除 S3 暂存 tar**。破坏性步骤前必须有合法确认串。

### 演练（不删库、不删盘、不删 S3）

用于确认本机与权限无误；仍会执行**完整导出**（与线上读一致）。

```bash
export PROD_QA_PURGE_CONFIRM=yes-delete-prod-qa-data
export PROD_QA_PURGE_DRY_RUN=1   # 或使用参数 --dry-run
bash scripts/prod-qa-export-and-purge.sh --dry-run
```

结束时日志会出现 **dry-run: skipping remote purge**，且 **不会** TRUNCATE。

### 真实清理（会永久删除 prod 侧 QA 数据）

1. 确认 **`unset PROD_QA_PURGE_DRY_RUN`**（环境中若曾设为 `1`，会导致误跑演练）。
2. **`PROD_QA_PURGE_CONFIRM`** 必须为字面 **`yes-delete-prod-qa-data`**。
3. 建议使用 **`PURGE_MAX_EXTRA_ROWS=0`**：在清空前再拉一次 prod 行数，与导出 manifest 行数对齐，避免「导出快照后有新写入却被删掉」的窗口问题。

```bash
unset PROD_QA_PURGE_DRY_RUN
export PROD_QA_PURGE_CONFIRM=yes-delete-prod-qa-data
export QA_DUMP_S3_BUCKET="<staging 桶>"
export OUT_DIR="./.dump_qa/prod-export-$(date -u +%Y%m%d)"
export PURGE_MAX_EXTRA_ROWS=0
# 若希望保留解压后同目录下的 .tar.gz 副本：
export KEEP_LOCAL_TAR_AFTER_PURGE=1

bash scripts/prod-qa-export-and-purge.sh
```

成功时日志中应出现 **`purge_ok`**，并提示已删除 S3 暂存对象。若任一步失败，**以脚本退出码与日志为准**，不要假设 prod 已清空或本地已完整。

---

## 本地导出目录里有什么

以 **`$OUT_DIR`** 为根目录（例如 `.dump_qa/prod-export-20260428/`）。

| 路径 / 文件 | 含义 |
|---------------|------|
| **`.last-prod-qa-export.json`** | **以该文件为准**：对应哪次 tar、时间戳、`qa_records` 行数、本地 blob 文件数、校验和等。同一目录下若留有多个 `tokenkey-qa-dump-*Z.tar.gz`，**与 manifest 里 `tarball` 字段一致的那份**为与当前 `metadata/` / `qa_blobs/` 匹配的一次导出。 |
| **`metadata/qa_records.jsonl`** | 每张表行一条 JSON；含 `request_id`、`inbound_endpoint`、`tool_calls_present`、各类 **`*_blob_uri`** 等。**不含**完整对话正文（正文在 blob）。 |
| **`qa_blobs/YYYY/MM/DD/<2 字符>/<request_id>.json.zst`** | **线上自动捕获写入的 blob**：路径按「捕获日期 + request_id」组织；内容为 **zstd 压缩 JSON**。 |
| **`qa_blobs/exports/<user_id>/<nanos>.zip`** | **用户自助导出**生成的 zip（与上面「按日上链式」的捕获 **不是同一批文件的两种摆法**）。 |
| **`qa_blobs/traj-exports/...`** | **历史专项导出** zip（若存在；保留路径名兼容旧导出，不代表当前目标追随外部 traj 标准）。 |

---

## 如何阅读「人类可读的脱敏 QA」（含 tools）

1. **在 `qa_records.jsonl` 中找到目标请求**（如按 `request_id`、时间、`tool_calls_present` 等筛选）。
2. 根据 **`blob_uri` / `request_blob_uri` / `response_blob_uri` / `stream_blob_uri`** 定位 **`qa_blobs/` 下同名相对路径** 的 **`*.json.zst`**。
3. **解压为 JSON** 后浏览（需已安装 `zstd`/`zstd-cli`）：

```bash
zstd -dc qa_blobs/2026/04/28/ab/<request_id>.json.zst | jq .
```

关注点（均为 **已脱敏** 字段）：

- **`request.body`**：脱敏后的请求体；OpenAI 兼容形态下 **tools** 等多在此。
- **`response.body`**：脱敏后的响应体。
- **`stream.chunks[].raw_b64`**：流式分段，需 **base64 解码** 后为可读片段。

 **`metadata/qa_records.jsonl` 单行只能看元数据；要看 tools / 正文，必须解压对应 `.json.zst`。**

---

## 清单（发同事前自检）

- [ ] `QA_DUMP_S3_BUCKET` 已配置且本账号可读写 staging。
- [ ] AWS 能通过 SSM 连上 prod 实例。
- [ ] 若执行清理：**已 dry-run 或已书面确认**，且真实跑前 **`unset PROD_QA_PURGE_DRY_RUN`**。
- [ ] 清理后抽查：`.last-prod-qa-export.json` 与磁盘上的 `qa_records.jsonl` / `qa_blobs` 是否一致；业务上按需再验证 prod。

---

## 脚本与权威文档索引

| 资源 | 路径 |
|------|------|
| 仅导出 | `scripts/fetch-prod-qa-dump.sh` |
| 导出 + 清理 | `scripts/prod-qa-export-and-purge.sh` |
| AWS 侧说明与迁移注意 | `deploy/aws/README.md`（Prod QA 小节） |

本文仅服务运营合伙人日常操作；**发版 / 镜像部署**与本文无关，请参阅 `deploy/aws/README.md` 中升级与 release 章节。
