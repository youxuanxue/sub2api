#!/usr/bin/env python3
"""Gemini Web Worker build identity — single owner of the digest contract.

The Worker deploys on its own cadence and its image tags are hand-typed, so a
tag is a claim, not evidence. This module derives identity from the bytes the
image actually ships, which lets a fleet check prove every edge runs one build
even when the tags disagree.

Imported by worker.py at runtime and run standalone by CI to tag the image, so
both sides cannot drift. Deliberately dependency-free: CI computes the digest
without installing curl_cffi / Pillow.

    python3 ops/gemini-web/build_digest.py
"""
import hashlib
import os

# Every shipped file whose contents change behaviour, this module included.
# requirements.txt counts: identical code on different pinned dependency
# versions is a different build. So does the Dockerfile: it owns the base image,
# the non-root user and the read-only filesystem, so identical sources built
# under a changed Dockerfile are a different container. Leaving it out meant a
# security-contract change published to an unchanged tag, where the host's
# `docker pull` finds nothing new and the deploy's digest gate still passes.
DIGEST_FILES = (
    'worker.py',
    'session_contract.py',
    'requirements.txt',
    'build_digest.py',
    'Dockerfile',
)
DIGEST_LENGTH = 12
UNKNOWN = 'unknown'


def build_digest(directory=None):
    """Return 12 hex chars identifying the shipped sources, or 'unknown'.

    'unknown' never masquerades as a real build: a fleet check treats a missing
    or unreadable source as unidentified rather than converged.
    """
    directory = directory or os.path.dirname(os.path.abspath(__file__))
    digest = hashlib.sha256()
    for name in DIGEST_FILES:
        try:
            with open(os.path.join(directory, name), 'rb') as handle:
                digest.update(handle.read())
        except OSError:
            return UNKNOWN
    return digest.hexdigest()[:DIGEST_LENGTH]


BUILD_DIGEST = build_digest()

if __name__ == '__main__':
    print(BUILD_DIGEST)
