#!/usr/bin/env python3
"""Compatibility wrapper for ops/pricing/modelops.py.

Keep old runbooks working while the canonical operator entry converges on
`modelops.py`.
"""

from __future__ import annotations

import importlib.util
import sys
from pathlib import Path


MODEL_OPS = Path(__file__).with_name("modelops.py")


def _load_modelops():
    spec = importlib.util.spec_from_file_location("tk_modelops", MODEL_OPS)
    if spec is None or spec.loader is None:
        raise SystemExit(f"cannot load {MODEL_OPS}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


if __name__ == "__main__":
    sys.exit(_load_modelops().main())
