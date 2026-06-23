#!/usr/bin/env python3
"""Verify the release GoReleaser Docker config stays multi-arch and v2-based.

Release speed depends on GoReleaser's dockers_v2 path: one buildx invocation
publishes the shared multi-arch tags directly. Reintroducing the legacy
`dockers` + `docker_manifests` split silently adds extra registry pushes and
manifest operations back into every release. Dropping arm64 is worse: prod and
Edge Stage0 hosts are Graviton.
"""

from __future__ import annotations

import argparse
import pathlib
import sys

try:
    import yaml
except ImportError:
    print(
        "  err: PyYAML not installed (required to parse GoReleaser configs).\n"
        "       fix: python3 -m pip install --user pyyaml",
        flush=True,
    )
    sys.exit(2)

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]

MULTIARCH_CONFIGS = [
    REPO_ROOT / ".goreleaser.yaml",
    REPO_ROOT / ".goreleaser.full.yaml",
]
SIMPLE_CONFIG = REPO_ROOT / ".goreleaser.simple.yaml"
DOCKERFILE = REPO_ROOT / "Dockerfile.goreleaser"


def _rel(path: pathlib.Path) -> str:
    return str(path.relative_to(REPO_ROOT))


def _load(path: pathlib.Path) -> dict:
    if not path.is_file():
        raise ValueError(f"{_rel(path)} not found")
    try:
        doc = yaml.safe_load(path.read_text())
    except yaml.YAMLError as exc:
        raise ValueError(f"{_rel(path)} is not valid YAML: {exc}") from exc
    if not isinstance(doc, dict):
        raise ValueError(f"{_rel(path)} must be a YAML mapping")
    return doc


def _check_no_legacy(path: pathlib.Path, doc: dict) -> list[str]:
    errors: list[str] = []
    if "dockers" in doc:
        errors.append(
            f"{_rel(path)} defines legacy `dockers`; use `dockers_v2` so release "
            "does not return to per-arch image pushes."
        )
    if "docker_manifests" in doc:
        errors.append(
            f"{_rel(path)} defines legacy `docker_manifests`; `dockers_v2` should "
            "publish shared tags directly."
        )
    return errors


def _single_docker_config(path: pathlib.Path, doc: dict) -> dict:
    dockers = doc.get("dockers_v2")
    if not isinstance(dockers, list) or not dockers:
        raise ValueError(f"{_rel(path)} must define at least one `dockers_v2` entry")
    if len(dockers) != 1:
        raise ValueError(
            f"{_rel(path)} must keep one `dockers_v2` entry; multiple entries add "
            "extra buildx/push work to every release"
        )
    cfg = dockers[0]
    if not isinstance(cfg, dict):
        raise ValueError(f"{_rel(path)} dockers_v2[0] must be a mapping")
    return cfg


def _check_multiarch(path: pathlib.Path, doc: dict) -> list[str]:
    errors = _check_no_legacy(path, doc)
    try:
        cfg = _single_docker_config(path, doc)
    except ValueError as exc:
        return errors + [str(exc)]

    platforms = cfg.get("platforms")
    if platforms != ["linux/amd64", "linux/arm64"]:
        errors.append(
            f"{_rel(path)} dockers_v2[0].platforms = {platforms!r}; expected "
            "['linux/amd64', 'linux/arm64'] for Graviton-safe release tags."
        )

    tags = cfg.get("tags")
    required_tags = {"{{ .Version }}", "latest", "{{ .Major }}.{{ .Minor }}", "{{ .Major }}"}
    if not isinstance(tags, list) or not required_tags.issubset(set(tags)):
        errors.append(
            f"{_rel(path)} dockers_v2[0].tags must include version/latest/major.minor/major "
            "shared tags."
        )

    if cfg.get("dockerfile") != "Dockerfile.goreleaser":
        errors.append(f"{_rel(path)} dockers_v2[0].dockerfile must be Dockerfile.goreleaser")

    if "deploy/docker-entrypoint.sh" not in (cfg.get("extra_files") or []):
        errors.append(f"{_rel(path)} must pass deploy/docker-entrypoint.sh via extra_files")

    if cfg.get("ids") != ["sub2api"]:
        errors.append(f"{_rel(path)} dockers_v2[0].ids must be ['sub2api']")

    return errors


def _check_simple(path: pathlib.Path, doc: dict) -> list[str]:
    errors = _check_no_legacy(path, doc)
    try:
        cfg = _single_docker_config(path, doc)
    except ValueError as exc:
        return errors + [str(exc)]

    if cfg.get("platforms") != ["linux/amd64"]:
        errors.append(f"{_rel(path)} must stay explicit linux/amd64-only")
    if cfg.get("ids") != ["sub2api"]:
        errors.append(f"{_rel(path)} dockers_v2[0].ids must be ['sub2api']")
    if cfg.get("dockerfile") != "Dockerfile.goreleaser":
        errors.append(f"{_rel(path)} dockers_v2[0].dockerfile must be Dockerfile.goreleaser")
    if "deploy/docker-entrypoint.sh" not in (cfg.get("extra_files") or []):
        errors.append(f"{_rel(path)} must pass deploy/docker-entrypoint.sh via extra_files")
    return errors


def _check_dockerfile() -> list[str]:
    if not DOCKERFILE.is_file():
        return [f"{_rel(DOCKERFILE)} not found"]
    text = DOCKERFILE.read_text()
    errors: list[str] = []
    if "ARG TARGETPLATFORM" not in text:
        errors.append("Dockerfile.goreleaser must declare ARG TARGETPLATFORM for dockers_v2")
    if (
        "COPY --chown=sub2api:sub2api --chmod=0755 ${TARGETPLATFORM}/sub2api /app/sub2api"
        not in text
    ):
        errors.append(
            "Dockerfile.goreleaser must copy ${TARGETPLATFORM}/sub2api from the "
            "dockers_v2 build context with final owner/mode"
        )
    if "COPY sub2api /app/sub2api" in text:
        errors.append("Dockerfile.goreleaser still copies legacy root-level sub2api")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--quiet", action="store_true", help="suppress success output")
    args = parser.parse_args()

    errors: list[str] = []
    for path in MULTIARCH_CONFIGS:
        try:
            errors.extend(_check_multiarch(path, _load(path)))
        except ValueError as exc:
            errors.append(str(exc))
    try:
        errors.extend(_check_simple(SIMPLE_CONFIG, _load(SIMPLE_CONFIG)))
    except ValueError as exc:
        errors.append(str(exc))
    errors.extend(_check_dockerfile())

    if errors:
        for error in errors:
            print(f"  err: {error}", flush=True)
        return 1

    if not args.quiet:
        print("  ok: GoReleaser Docker config uses dockers_v2 with safe platforms")
    return 0


if __name__ == "__main__":
    sys.exit(main())
