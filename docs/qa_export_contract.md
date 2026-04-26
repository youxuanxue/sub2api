# QA export contract (synth session / self-contained zip)

This document is the canonical consumer contract for user-initiated QA exports (`POST /api/v1/users/me/qa/export`). It aligns with GitHub issue #79 and the implementation in `internal/observability/qa`.

## E1 Session keys

- **`synth_session_id`**: Logical session id from the caller (e.g. header `X-Synth-Session`), stored on each `qa_record`. When present in the export request body, the export is scoped to rows where `synth_session_id` matches exactly (still subject to `user_id = authenticated user`).
- **`synth_role`**: Optional sub-scope within a session. When set together with `synth_session_id`, only rows with both fields matching are exported.
- **Time window**: When `synth_session_id` is **empty**, the server applies a bounded default window on `created_at` (see handler). When `synth_session_id` is **set**, `since` / `until` from the client are ignored so a long session is not truncated by the default window.

## E2 Ordering

- Rows are ordered by **`created_at ASC`**, then **`request_id ASC`** as a stable secondary key when timestamps collide.

## E3 Row semantics

- **`qa_records.jsonl`**: one JSON object per line. Each line corresponds to **one HTTP capture** (one gateway request/response pair). Multiple lines represent multiple turns, tool loops, or retries in the same `synth_session_id`.

## E4 Payload placement

Every export zip contains:

- **`qa_records.jsonl`**: metadata per capture (Ent row fields in snake_case, plus export-specific fields below).
- **`manifest.json`**: version and aggregate flags (see E6).

When **`synth_session_id` is set** (session export), the zip is **self-contained**:

- For each line, **`capture_archive_path`** is a zip-internal relative path (under `blobs/`) to the **zstd-compressed JSON** capture payload (same on-disk format as live QA blobs: JSON object with `request`, `response`, `stream`, `redactions`, etc.).
- **`capture_encoding`** is always **`application/zstd+json`** for these embedded files.
- **`blob_uri` is omitted** from jsonl lines in this mode so consumers do not depend on external object storage.

When **`synth_session_id` is empty** (time-window export), **`manifest.includes_blobs` is `false`**. Jsonl lines keep **`blob_uri`** for backward compatibility with operators who can still reach the configured blob store; embedded `blobs/` entries are not added.

## E5 Completeness (`incomplete` vs hard failure)

- **`manifest.incomplete`**: `true` if **any** exported row has the tag **`body_truncated`** (response buffer hit `qa_capture.body_max_bytes` at capture time). Redaction is always applied at capture; the presence of `"redactions":["logredact"]` in the blob payload documents that policy — it does not by itself set `incomplete`.
- **Session exports** (`synth_session_id` set): if **any** included row is incomplete as above, **or** the blob cannot be read from storage, **or** `blob_uri` is missing / uses an unsupported scheme, the **entire export fails** with an error (no zip is written). This is the “fail closed” branch of issue #79 E5.
- **Time-window exports**: incomplete rows still produce a zip; **`manifest.incomplete`** is `true` and the HTTP JSON response sets **`export_incomplete: true`** when applicable.

## E6 Manifest

`manifest.json` fields:

| Field | Type | Meaning |
| ----- | ---- | ------- |
| `export_format_version` | string | Constant `qa_export_v1` (bump only when breaking zip/jsonl/manifest shape). |
| `includes_blobs` | bool | `true` when session export embedded `blobs/`; `false` for time-window export. |
| `record_count` | int | Number of lines in `qa_records.jsonl`. |
| `incomplete` | bool | See E5. |

The HTTP **`export_format_version`** field mirrors `manifest.export_format_version` whenever a manifest is present.

## Consumer algorithm (minimal)

1. Unzip; read **`manifest.json`**; verify **`export_format_version`**.
2. Stream **`qa_records.jsonl`** in order.
3. If **`includes_blobs`**, for each line open **`capture_archive_path`**, decompress zstd, parse JSON — that object is the full capture.
