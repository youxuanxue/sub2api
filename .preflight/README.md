# Preflight

`./scripts/preflight.sh` is the shared local/CI entry point. It runs the common
dev-rules checks, then selects project checks from `gates.json`. The existing
shell sections remain the only implementation of each check. Successful sections
print one line; failures include diagnostics and the final line links the full log.
Section times measure foreground execution/wait time; independent jobs overlap.

| Command | Scope |
|---|---|
| `./scripts/preflight.sh` | Local: staged files if present, otherwise pending worktree changes, otherwise branch. CI: PR branch; other events full. |
| `./scripts/preflight.sh --staged` | Current commit index, including Git's alternate index. Partially staged files must match the working copy being tested. |
| `./scripts/preflight.sh --worktree` | Staged, unstaged and untracked files. |
| `./scripts/preflight.sh --branch` | `PREFLIGHT_BASE...HEAD` (default `origin/main`) plus pending changes. Use before PR/review/push. |
| `./scripts/preflight.sh --full` | All gates, without CI skip/delegation flags. Use for release validation. |
| Add `--plan` | Print selected gates and reasons without running checks. |

Missing Git baselines and invalid manifests fail rather than produce an empty
selection. Empty changes, unknown input paths, shared dependency pins, rules,
checkers and preflight changes select full validation. The initial declared
surfaces cover backend/frontend/deploy and the listed ops domains; other inputs
remain full until their dependencies are declared. Local full lint is selected
only for backend inputs or full validation; CI retains its dedicated lint job.

`input_sets` groups dependencies, `gates` binds section names to those groups,
`backgrounds` binds jobs to the same gates, and `requires` keeps dependent sections
together. An empty input set means always run. Unregistered sections/jobs default
to execution. `sentinel_inputs` reads paths from the existing sentinel registries;
do not copy their owner lists here. Isolated leaf exclusions apply only to the
listed inputs, so adding another changed helper restores its directory checks.

The billing-watch gate owns its test module. The directory runner excludes that
module, and selecting the observability suite also selects its focused gate.
The common SQL soft-delete, script-reference and orphan checks still run for a
billing probe edit. Add routing regression cases in `scripts/test_preflight_scope.py`
when changing these boundaries. To inspect raw output, set `PREFLIGHT_RAW_OUTPUT=1`;
this changes presentation only.
