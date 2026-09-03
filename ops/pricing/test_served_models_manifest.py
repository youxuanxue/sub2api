#!/usr/bin/env python3
from __future__ import annotations

import json
import sys
import unittest
from pathlib import Path


sys.path.insert(0, str(Path(__file__).resolve().parent))
import served_models_manifest as manifest


class ServedModelsManifestTest(unittest.TestCase):
    def valid_document(self) -> dict:
        return {
            "schema_version": 3,
            "entries": {
                "shown-model": {"channel_type": 17, "display": True},
                "hidden-model": {
                    "scopes": [
                        {
                            "channel_type": 46,
                            "base_url": "https://qianfan.baidubce.com",
                        }
                    ],
                    "price_owner": "shown-model",
                    "display": False,
                },
            },
        }

    def test_repository_projection_matches_pre_consolidation_algorithm(self) -> None:
        raw = json.loads(manifest.DEFAULT_MANIFEST_PATH.read_text(encoding="utf-8"))
        old_projection = {
            model_id: {
                "model_id": model_id,
                "channel_type": entry.get("channel_type"),
                "scopes": entry.get("scopes", []),
                "price_owner": entry.get("price_owner", model_id),
                "display": entry.get("display", False),
            }
            for model_id, entry in raw["entries"].items()
            if isinstance(entry, dict)
        }

        parsed = manifest.parse_manifest_document(raw)
        new_projection = {
            entry.model_id: entry.projection() for entry in parsed.entries
        }

        self.assertEqual(new_projection, old_projection)
        self.assertEqual(parsed.model_ids(), set(old_projection))
        self.assertEqual(
            parsed.displayed_model_ids(),
            {model_id for model_id, entry in old_projection.items() if entry["display"]},
        )

    def test_rejects_unsupported_schema_and_top_level_residue(self) -> None:
        for mutation in (
            lambda data: data.update(schema_version=2),
            lambda data: data.update(generated_at="2026-09-03"),
        ):
            with self.subTest(mutation=mutation):
                data = self.valid_document()
                mutation(data)
                with self.assertRaises(manifest.ManifestError):
                    manifest.parse_manifest_document(data)

    def test_rejects_entry_shape_drift(self) -> None:
        mutations = (
            lambda entry: entry.update(notes="history"),
            lambda entry: entry.update(display="true"),
            lambda entry: entry.update(channel_type=True),
        )
        for mutation in mutations:
            with self.subTest(mutation=mutation):
                data = self.valid_document()
                mutation(data["entries"]["shown-model"])
                with self.assertRaises(manifest.ManifestError):
                    manifest.parse_manifest_document(data)

    def test_rejects_unknown_or_duplicate_property_scope(self) -> None:
        data = self.valid_document()
        scope = data["entries"]["hidden-model"]["scopes"][0]
        data["entries"]["hidden-model"]["scopes"].append(dict(scope))
        with self.assertRaisesRegex(manifest.ManifestError, "duplicate property scope"):
            manifest.parse_manifest_document(data)

        data = self.valid_document()
        data["entries"]["hidden-model"]["scopes"][0]["base_url"] += "/"
        with self.assertRaisesRegex(manifest.ManifestError, "supported normalized"):
            manifest.parse_manifest_document(data)

    def test_requires_a_route_scope(self) -> None:
        data = self.valid_document()
        data["entries"]["shown-model"].pop("channel_type")
        with self.assertRaisesRegex(manifest.ManifestError, "at least one channel_type"):
            manifest.parse_manifest_document(data)


if __name__ == "__main__":
    unittest.main()
