from __future__ import annotations

import json
import os
from pathlib import Path
from typing import Callable


APP_DIR = Path(os.environ.get("APPDATA", str(Path.home()))) / "ProjectReboundBrowser"
LAUNCH_DIR = APP_DIR / "launchers"
LOADOUT_EXPORT_PATH = APP_DIR / "loadout-export-v1.json"
LOADOUT_LAUNCH_PATH = LAUNCH_DIR / "loadout-launch-v1.json"

DEFAULT_FALLBACK_STATUS = "未找到本地配装快照，将使用游戏默认配装。"


def normalize_loadout_snapshot(snapshot: object) -> dict | None:
    return snapshot if isinstance(snapshot, dict) else None


def coalesce_loadout_snapshot(*snapshots: object) -> dict | None:
    for snapshot in snapshots:
        normalized = normalize_loadout_snapshot(snapshot)
        if normalized is not None:
            return normalized
    return None


def with_loadout_snapshot(payload: dict[str, object], snapshot: object) -> dict[str, object]:
    enriched = dict(payload)
    normalized = normalize_loadout_snapshot(snapshot)
    if normalized is not None:
        enriched["loadoutSnapshot"] = normalized
    return enriched


def load_loadout_snapshot() -> dict | None:
    if not LOADOUT_EXPORT_PATH.exists():
        return None
    try:
        with LOADOUT_EXPORT_PATH.open("r", encoding="utf-8") as file:
            return normalize_loadout_snapshot(json.load(file))
    except (OSError, json.JSONDecodeError):
        return None


def load_current_loadout_snapshot(
    append_log: Callable[[str], None] | None = None,
    set_status: Callable[[str], None] | None = None,
) -> dict | None:
    snapshot = load_loadout_snapshot()
    if snapshot is None:
        if append_log is not None:
            append_log(
                f"No local loadout snapshot found at {LOADOUT_EXPORT_PATH}; launch will fall back to the game's default loadout."
            )
        if set_status is not None:
            set_status(DEFAULT_FALLBACK_STATUS)
    else:
        if append_log is not None:
            append_log(f"Loaded local loadout snapshot from {LOADOUT_EXPORT_PATH}")
    return snapshot


def write_launch_loadout_snapshot(
    snapshot: object | None,
    append_log: Callable[[str], None] | None = None,
) -> Path | None:
    normalized = normalize_loadout_snapshot(snapshot)
    if normalized is None:
        try:
            LOADOUT_LAUNCH_PATH.unlink()
        except FileNotFoundError:
            pass
        except OSError:
            if append_log is not None:
                append_log(f"Failed to clear stale launch loadout snapshot: {LOADOUT_LAUNCH_PATH}")
        return None

    LAUNCH_DIR.mkdir(parents=True, exist_ok=True)
    with LOADOUT_LAUNCH_PATH.open("w", encoding="utf-8") as file:
        json.dump(normalized, file, indent=2, ensure_ascii=False)
    return LOADOUT_LAUNCH_PATH


def prepare_launch_loadout_snapshot(
    snapshot: object | None = None,
    append_log: Callable[[str], None] | None = None,
    set_status: Callable[[str], None] | None = None,
) -> Path | None:
    written = write_launch_loadout_snapshot(
        coalesce_loadout_snapshot(snapshot, load_loadout_snapshot()),
        append_log=append_log,
    )
    if written is None:
        if append_log is not None:
            append_log("No launch loadout snapshot prepared; Payload will fall back to the game's default loadout.")
        if set_status is not None:
            set_status(DEFAULT_FALLBACK_STATUS)
        return None

    if append_log is not None:
        append_log(f"Prepared launch loadout snapshot: {written}")
    return written
