from __future__ import annotations

import json
import os
from pathlib import Path
from typing import Callable

try:
    from urllib.request import Request, urlopen
    from urllib.error import URLError
except ImportError:
    Request = None
    urlopen = None

APP_DIR = Path(os.environ.get("APPDATA", str(Path.home()))) / "ProjectReboundBrowser"
LAUNCH_DIR = APP_DIR / "launchers"
LOADOUT_EXPORT_PATH = APP_DIR / "loadout-export-v1.json"
LOADOUT_LAUNCH_PATH = LAUNCH_DIR / "loadout-launch-v1.json"

METASERVER_URL = "http://127.0.0.1:8000"
DEFAULT_PLAYER_ID = "76561198211631084"


def set_metaserver_url(url: str) -> None:
    """允许调用方覆盖 metaserver 地址（例如从 browser 配置传入）。"""
    global METASERVER_URL
    METASERVER_URL = url.rstrip("/") if url else "http://127.0.0.1:8000"

DEFAULT_FALLBACK_STATUS = "未找到本地配装快照，将使用游戏默认配装。"


def _strip_transient_fields(snapshot: dict) -> dict:
    """移除快照中不属于配装定义的瞬态字段。"""
    snapshot.pop("selectedRoleId", None)
    return snapshot


def normalize_loadout_snapshot(snapshot: object) -> dict | None:
    if not isinstance(snapshot, dict):
        return None
    _strip_transient_fields(snapshot)
    return snapshot


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


# =====================================================================
#  Metaserver HTTP API
# =====================================================================

def _http_get(path: str) -> dict | None:
    """向 metaserver 发送 GET 请求。"""
    try:
        req = Request(f"{METASERVER_URL}{path}")
        req.add_header("Accept", "application/json")
        with urlopen(req, timeout=3) as resp:
            return json.loads(resp.read().decode("utf-8"))
    except (URLError, OSError, json.JSONDecodeError):
        return None


def _http_put(path: str, data: dict) -> bool:
    """向 metaserver 发送 PUT 请求。"""
    try:
        body = json.dumps(data, ensure_ascii=False).encode("utf-8")
        req = Request(f"{METASERVER_URL}{path}", data=body, method="PUT")
        req.add_header("Content-Type", "application/json")
        with urlopen(req, timeout=5) as resp:
            resp.read()
            return True
    except (URLError, OSError):
        return False


def _metaserver_available() -> bool:
    """检查 metaserver 是否可用。"""
    result = _http_get("/api/health")
    return result is not None and result.get("status") == "ok"


def _load_from_metaserver(player_id: str = DEFAULT_PLAYER_ID) -> dict | None:
    """从 metaserver 加载配装。"""
    result = _http_get(f"/api/loadout/{player_id}")
    if result and "roles" in result:
        # 转换为标准 snapshot 格式
        snapshot = {
            "schemaVersion": 2,
            "source": "metaserver",
            "roles": [],
        }
        roles = result.get("roles", {})
        if isinstance(roles, dict):
            for role_id, role_data in roles.items():
                if isinstance(role_data, dict):
                    snapshot["roles"].append(role_data)
        return snapshot
    return None


def _save_to_metaserver(snapshot: dict, player_id: str = DEFAULT_PLAYER_ID) -> bool:
    """将配装保存到 metaserver。"""
    return _http_put(f"/api/loadout/{player_id}", snapshot)


# =====================================================================
#  本地磁盘降级路径
# =====================================================================

def load_loadout_snapshot() -> dict | None:
    """从本地磁盘加载配装快照（降级路径）。"""
    if not LOADOUT_EXPORT_PATH.exists():
        return None
    try:
        with LOADOUT_EXPORT_PATH.open("r", encoding="utf-8") as file:
            snapshot = json.load(file)
        if isinstance(snapshot, dict):
            snapshot.pop("selectedRoleId", None)
        return normalize_loadout_snapshot(snapshot)
    except (OSError, json.JSONDecodeError):
        return None


def load_current_loadout_snapshot(
    append_log: Callable[[str], None] | None = None,
    set_status: Callable[[str], None] | None = None,
    player_id: str = DEFAULT_PLAYER_ID,
) -> dict | None:
    """加载当前配装 — 优先从 metaserver，降级到本地文件。"""
    # 优先从 metaserver 加载
    if _metaserver_available():
        snapshot = _load_from_metaserver(player_id)
        if snapshot is not None:
            if append_log is not None:
                append_log(f"Loaded loadout snapshot from metaserver ({METASERVER_URL})")
            return snapshot
        if append_log is not None:
            append_log("Metaserver returned no loadout data; falling back to local file.")

    # 降级到本地文件
    snapshot = load_loadout_snapshot()
    if snapshot is None:
        if append_log is not None:
            append_log(
                f"No loadout snapshot found; launch will fall back to the game's default loadout."
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
    player_id: str = DEFAULT_PLAYER_ID,
) -> Path | None:
    """写入启动配装 — 优先上传到 metaserver，降级到本地文件。"""
    normalized = normalize_loadout_snapshot(snapshot)
    if normalized is None:
        # 清空远端和本地
        try:
            LOADOUT_LAUNCH_PATH.unlink()
        except (FileNotFoundError, OSError):
            pass
        return None

    # 尝试上传到 metaserver
    if _metaserver_available():
        if _save_to_metaserver(normalized, player_id):
            if append_log is not None:
                append_log(f"Uploaded loadout snapshot to metaserver ({METASERVER_URL})")
            # 同时保存到本地作为降级备份
            LAUNCH_DIR.mkdir(parents=True, exist_ok=True)
            with LOADOUT_LAUNCH_PATH.open("w", encoding="utf-8") as file:
                json.dump(normalized, file, indent=2, ensure_ascii=False)
            return LOADOUT_LAUNCH_PATH
        if append_log is not None:
            append_log("Metaserver upload failed; falling back to local file.")

    # 降级到本地文件
    LAUNCH_DIR.mkdir(parents=True, exist_ok=True)
    with LOADOUT_LAUNCH_PATH.open("w", encoding="utf-8") as file:
        json.dump(normalized, file, indent=2, ensure_ascii=False)
    return LOADOUT_LAUNCH_PATH


def prepare_launch_loadout_snapshot(
    snapshot: object | None = None,
    append_log: Callable[[str], None] | None = None,
    set_status: Callable[[str], None] | None = None,
    player_id: str = DEFAULT_PLAYER_ID,
) -> Path | None:
    """准备启动配装快照。"""
    written = write_launch_loadout_snapshot(
        coalesce_loadout_snapshot(snapshot, load_loadout_snapshot()),
        append_log=append_log,
        player_id=player_id,
    )
    if written is None:
        if append_log is not None:
            append_log(
                "No launch loadout snapshot prepared; Payload will fall back to the game's default loadout."
            )
        if set_status is not None:
            set_status(DEFAULT_FALLBACK_STATUS)
        return None

    if append_log is not None:
        append_log(f"Prepared launch loadout snapshot: {written}")
    return written
