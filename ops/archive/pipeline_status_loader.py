"""Load ops/archive/pipeline_status.yaml (SSOT consumer for repo archive pipeline)."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Any

DEFAULT_PIPELINE = Path(__file__).resolve().parent / "pipeline_status.yaml"


@dataclass(frozen=True)
class EvidenceLayout:
    evidence_dir: Path
    cleanup_hold_glob: str
    cleanup_release_receipt_glob: str
    export_ledger_template: str
    promote_ledger_template: str
    tail_export_ledger_template: str
    tail_promote_ledger_template: str
    closeout_receipt_template: str
    table_slugs: dict[str, str]

    @property
    def tables(self) -> tuple[str, ...]:
        return tuple(sorted(self.table_slugs))

    def export_ledger_name(self, table: str) -> str:
        return self.export_ledger_template.format(table_slug=self.table_slugs[table])

    def promote_ledger_name(self, table: str) -> str:
        return self.promote_ledger_template.format(table_slug=self.table_slugs[table])

    def tail_export_ledger_name(self, table: str) -> str:
        return self.tail_export_ledger_template.format(table_slug=self.table_slugs[table])

    def tail_promote_ledger_name(self, table: str) -> str:
        return self.tail_promote_ledger_template.format(table_slug=self.table_slugs[table])

    def closeout_receipt_name(self, table: str) -> str:
        return self.closeout_receipt_template.format(table_slug=self.table_slugs[table])


def _repo_root(pipeline_path: Path) -> Path:
    return pipeline_path.resolve().parents[2]


def _resolve_evidence_dir(repo_root: Path, spec: str) -> Path:
    raw = spec.strip()
    if not raw or raw.startswith(("/", "\\")) or ".." in raw.replace("\\", "/").split("/"):
        raise ValueError("pipeline_status.yaml evidence_attachments.evidence_dir must be a safe repo-relative path")
    resolved = (repo_root / raw).resolve()
    if resolved != repo_root and repo_root not in resolved.parents:
        raise ValueError("pipeline_status.yaml evidence_attachments.evidence_dir escapes repo root")
    return resolved

def load_pipeline_status(path: Path | None = None) -> dict[str, Any]:
    path = DEFAULT_PIPELINE if path is None else path
    try:
        import yaml
    except ImportError as exc:
        raise RuntimeError("PyYAML required") from exc
    payload = yaml.safe_load(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError(f"{path} must be a mapping")
    return payload


def load_evidence_layout(path: Path | None = None) -> EvidenceLayout:
    pipeline_path = DEFAULT_PIPELINE if path is None else path
    data = load_pipeline_status(pipeline_path)
    evidence = data.get("evidence_attachments")
    if not isinstance(evidence, dict):
        raise ValueError("pipeline_status.yaml evidence_attachments must be a mapping")
    slugs = evidence.get("table_slugs")
    if not isinstance(slugs, dict) or not slugs:
        raise ValueError("pipeline_status.yaml evidence_attachments.table_slugs required")
    for key in (
        "evidence_dir",
        "cleanup_hold_glob",
        "cleanup_release_receipt_glob",
        "export_ledger_template",
        "promote_ledger_template",
        "tail_export_ledger_template",
        "tail_promote_ledger_template",
        "closeout_receipt_template",
    ):
        value = evidence.get(key)
        if not isinstance(value, str) or not value.strip():
            raise ValueError(f"pipeline_status.yaml evidence_attachments.{key} required")
    normalized_slugs = {str(table): str(slug) for table, slug in slugs.items()}
    repo_root = _repo_root(pipeline_path)
    return EvidenceLayout(
        evidence_dir=_resolve_evidence_dir(repo_root, str(evidence["evidence_dir"])),
        cleanup_hold_glob=str(evidence["cleanup_hold_glob"]),
        cleanup_release_receipt_glob=str(evidence["cleanup_release_receipt_glob"]),
        export_ledger_template=str(evidence["export_ledger_template"]),
        promote_ledger_template=str(evidence["promote_ledger_template"]),
        tail_export_ledger_template=str(evidence["tail_export_ledger_template"]),
        tail_promote_ledger_template=str(evidence["tail_promote_ledger_template"]),
        closeout_receipt_template=str(evidence["closeout_receipt_template"]),
        table_slugs=normalized_slugs,
    )
