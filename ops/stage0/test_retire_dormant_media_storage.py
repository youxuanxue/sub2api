#!/usr/bin/env python3
import unittest
from retire_dormant_media_storage import clean, LEGACY


class MediaCleanupTests(unittest.TestCase):
    def test_remove_only_known_dormant_defaults_preserving_other_secrets_and_flags(self):
        unrelated = "JWT_SECRET=synthetic-secret\nIMAGE_STORAGE_ENABLED=false\nMEDIA_STORAGE_IMAGE_OFFLOAD_ENABLED=false\n"
        old = unrelated + "\n".join(f"{key}={value}" for key, value in LEGACY.items()) + "\n"
        self.assertEqual(clean(old), unrelated)
        self.assertEqual(clean(clean(old)), unrelated)

    def test_enabled_offload_custom_bucket_and_partial_config_require_review(self):
        for text in ["MEDIA_STORAGE_IMAGE_OFFLOAD_ENABLED=true\n",
                     "MEDIA_STORAGE_BUCKET=custom\n", "MEDIA_STORAGE_DRIVER=s3\n"]:
            with self.subTest(text=text):
                with self.assertRaises(RuntimeError):
                    clean(text)


if __name__ == "__main__":
    unittest.main()
