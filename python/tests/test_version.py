"""Tests that ``__version__`` matches the source-of-truth files."""

from __future__ import annotations

import pathlib

import mantyx


def test_version_matches_root_VERSION() -> None:
    repo_root = pathlib.Path(__file__).resolve().parents[2]
    root_version = (repo_root / "VERSION").read_text(encoding="utf-8").strip()
    assert mantyx.__version__ == root_version
    assert mantyx.SDK_VERSION == root_version


def test_version_matches_sdk_version_txt() -> None:
    sdk_version = (
        (pathlib.Path(__file__).resolve().parents[1] / "sdk-version.txt")
        .read_text(encoding="utf-8")
        .strip()
    )
    assert mantyx.__version__ == sdk_version
