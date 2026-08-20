#!/usr/bin/env python3
"""Launch or attach to Boundary and record the read-only Frida armory probe."""

from __future__ import annotations

import argparse
import ctypes
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
import time
from typing import Any

import frida


GAME_PROCESS = "ProjectBoundarySteam-Win64-Shipping.exe"
EXPECTED_GAME_SHA256 = "181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843"
DEFAULT_STARTGAME = Path(
    r"C:\Steam\steamapps\common\Boundary\ProjectBoundary\Binaries\Win64\startgame.ps1"
)


def default_output_directory() -> Path:
    local_app_data = os.environ.get("LOCALAPPDATA")
    base = Path(local_app_data) if local_app_data else Path.home() / "AppData" / "Local"
    timestamp = dt.datetime.now().strftime("%Y%m%d-%H%M%S")
    return base / "ProjectRebound" / "frida-captures" / timestamp


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Observe Boundary QueryAssets, OwnedItems, and native HasItem decisions."
    )
    parser.add_argument("--pid", type=int, help="Attach to an exact process ID.")
    parser.add_argument(
        "--process-name", default=GAME_PROCESS, help="Executable name to wait for."
    )
    parser.add_argument(
        "--launch", action="store_true", help="Launch startgame.ps1 before attaching."
    )
    parser.add_argument(
        "--startgame", type=Path, default=DEFAULT_STARTGAME, help="Path to startgame.ps1."
    )
    parser.add_argument(
        "--output", type=Path, default=None, help="Capture directory (outside the repo by default)."
    )
    parser.add_argument(
        "--attach-timeout", type=float, default=90.0, help="Seconds to wait for the game process."
    )
    parser.add_argument(
        "game_args", nargs=argparse.REMAINDER, help="Arguments forwarded to startgame.ps1 after --."
    )
    args = parser.parse_args()
    if args.game_args and args.game_args[0] == "--":
        args.game_args = args.game_args[1:]
    return args


def enumerate_matching_pids(device: frida.core.Device, process_name: str) -> list[int]:
    expected = process_name.casefold()
    return [
        process.pid
        for process in device.enumerate_processes()
        if process.name.casefold() == expected
    ]


def wait_for_process(
    device: frida.core.Device,
    process_name: str,
    timeout: float,
    excluded_pids: set[int],
) -> int:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        matches = enumerate_matching_pids(device, process_name)
        new_matches = [pid for pid in matches if pid not in excluded_pids]
        if new_matches:
            return new_matches[-1]
        if matches and not excluded_pids:
            return matches[-1]
        time.sleep(0.2)
    raise TimeoutError(f"process {process_name!r} did not appear within {timeout:.0f}s")


def process_image_path(pid: int) -> Path:
    """Resolve a Windows process image without optional third-party modules."""
    process_query_limited_information = 0x1000
    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel32.OpenProcess.argtypes = [ctypes.c_uint32, ctypes.c_bool, ctypes.c_uint32]
    kernel32.OpenProcess.restype = ctypes.c_void_p
    kernel32.QueryFullProcessImageNameW.argtypes = [
        ctypes.c_void_p,
        ctypes.c_uint32,
        ctypes.c_wchar_p,
        ctypes.POINTER(ctypes.c_uint32),
    ]
    kernel32.QueryFullProcessImageNameW.restype = ctypes.c_bool
    kernel32.CloseHandle.argtypes = [ctypes.c_void_p]
    handle = kernel32.OpenProcess(process_query_limited_information, False, pid)
    if not handle:
        raise OSError(ctypes.get_last_error(), f"OpenProcess({pid}) failed")
    try:
        capacity = 32768
        buffer = ctypes.create_unicode_buffer(capacity)
        length = ctypes.c_uint32(capacity)
        if not kernel32.QueryFullProcessImageNameW(handle, 0, buffer, ctypes.byref(length)):
            raise OSError(ctypes.get_last_error(), "QueryFullProcessImageNameW failed")
        return Path(buffer.value)
    finally:
        kernel32.CloseHandle(handle)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(4 * 1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def compact_console_line(payload: dict[str, Any]) -> str | None:
    event = str(payload.get("event", ""))
    if event in {
        "probe.ready",
        "probe.error",
        "rpc.query_assets",
        "rpc.player_archive",
        "rpc.role_archive_update",
        "rpc.weapon_archive_update",
        "rpc.payload_capture",
        "armory.manager_found",
        "armory.changed",
        "armory.snapshot",
        "armory.has_item",
        "fieldmod.manager_found",
        "fieldmod.native_call",
        "fieldmod.snapshot",
        "persistent_user.snapshot",
        "progression.player_level_table",
        "progression.character_level_table",
        "progression.data_statistics",
        "progression.career_manager_found",
        "progression.career_snapshot",
        "progression.career_monitor_ready",
        "progression.career_monitor_skipped",
        "progression.career_memory_access",
        "progression.query_native_dispatch",
        "progression.local_player_vtable",
        "unreal.lifecycle",
    }:
        return json.dumps(payload, ensure_ascii=False, separators=(",", ":"))
    return None


def read_varint(data: bytes, offset: int) -> tuple[int, int]:
    value = 0
    shift = 0
    for _ in range(10):
        if offset >= len(data):
            raise ValueError("truncated varint")
        current = data[offset]
        offset += 1
        value |= (current & 0x7F) << shift
        if current & 0x80 == 0:
            return value, offset
        shift += 7
    raise ValueError("invalid varint")


def parse_proto(data: bytes) -> dict[int, list[tuple[int, Any]]]:
    fields: dict[int, list[tuple[int, Any]]] = {}
    offset = 0
    while offset < len(data):
        tag, offset = read_varint(data, offset)
        field_number, wire_type = tag >> 3, tag & 7
        if field_number == 0:
            raise ValueError("invalid field number 0")
        if wire_type == 0:
            value, offset = read_varint(data, offset)
        elif wire_type == 1:
            end = offset + 8
            if end > len(data):
                raise ValueError("truncated fixed64")
            value, offset = data[offset:end], end
        elif wire_type == 2:
            length, offset = read_varint(data, offset)
            end = offset + length
            if end > len(data):
                raise ValueError("truncated length-delimited field")
            value, offset = data[offset:end], end
        elif wire_type == 5:
            end = offset + 4
            if end > len(data):
                raise ValueError("truncated fixed32")
            value, offset = data[offset:end], end
        else:
            raise ValueError(f"unsupported protobuf wire type {wire_type}")
        fields.setdefault(field_number, []).append((wire_type, value))
    return fields


def proto_varint(fields: dict[int, list[tuple[int, Any]]], number: int, default: int = 0) -> int:
    values = fields.get(number, [])
    return int(values[0][1]) if values and values[0][0] == 0 else default


def proto_bytes(fields: dict[int, list[tuple[int, Any]]], number: int) -> bytes:
    values = fields.get(number, [])
    return bytes(values[0][1]) if values and values[0][0] == 2 else b""


def proto_repeated_bytes(fields: dict[int, list[tuple[int, Any]]], number: int) -> list[bytes]:
    return [bytes(value) for wire, value in fields.get(number, []) if wire == 2]


def proto_text(fields: dict[int, list[tuple[int, Any]]], number: int) -> str:
    return proto_bytes(fields, number).decode("utf-8", errors="replace")


def decode_captured_payload(rpc_path: str, direction: str, data: bytes) -> dict[str, Any]:
    fields = parse_proto(data)
    method = rpc_path.rsplit("/", 1)[-1]
    if method == "GetPlayerArchiveV2" and direction == "send":
        return {
            "kind": "player_archive_request",
            "role_ids": [
                value.decode("utf-8", errors="replace")
                for value in proto_repeated_bytes(fields, 1)
            ],
        }
    if method == "QueryAssets" and direction == "recv":
        rows = proto_repeated_bytes(fields, 2)
        ids: list[str] = []
        counts = {"zero": 0, "positive": 0, "negative": 0}
        for row in rows:
            item = parse_proto(row)
            ids.append(proto_text(item, 1))
            # QueryAssets fields 2-4 are sint32 metadata, not ownership counts.
            raw_count = proto_varint(item, 2)
            signed_count = (raw_count >> 1) ^ -(raw_count & 1)
            counts["zero" if signed_count == 0 else "positive" if signed_count > 0 else "negative"] += 1
        return {
            "kind": "query_assets_response",
            "declared_item_count": proto_varint(fields, 1),
            "rows": len(rows),
            "unique_ids": len(set(ids)),
            "empty_ids": sum(not item_id for item_id in ids),
            "field2_distribution": counts,
            "sample_ids": ids[:4] + ids[-4:] if len(ids) > 8 else ids,
        }
    if method == "GetPlayerArchiveV2" and direction == "recv":
        roles: list[dict[str, Any]] = []
        for encoded_role in proto_repeated_bytes(fields, 1):
            role = parse_proto(encoded_role)
            roles.append(
                {
                    "role_id": proto_text(role, 1),
                    "left_pylon": proto_text(role, 2),
                    "right_pylon": proto_text(role, 3),
                    "mobility_module": proto_text(role, 4),
                    "melee_weapon": proto_text(role, 5),
                    "primary_weapon": proto_text(role, 6),
                    "second_weapon": proto_text(role, 7),
                    "unknown_fields": sorted(number for number in role if number > 7),
                }
            )
        return {
            "kind": "player_archive_response",
            "player_level": proto_varint(fields, 2),
            "role_count": len(roles),
            "roles": roles,
        }
    if method == "GetDataStatisticsInfo" and direction == "send":
        return {
            "kind": "data_statistics_request",
            "player_id_bytes": len(proto_bytes(fields, 1)),
        }
    if method == "GetDataStatisticsInfo" and direction == "recv":
        datapoints: list[dict[str, Any]] = []
        for encoded_datapoint in proto_repeated_bytes(fields, 2):
            datapoint = parse_proto(encoded_datapoint)
            datapoints.append(
                {
                    "key": proto_text(datapoint, 1),
                    "value": proto_varint(datapoint, 2),
                }
            )
        return {
            "kind": "data_statistics_response",
            "status_code": proto_varint(fields, 1),
            "datapoint_count": len(datapoints),
            "datapoints": datapoints,
        }
    if method == "UpdateRoleArchiveV2" and direction == "send":
        return {
            "kind": "update_role_request",
            "operation": proto_varint(fields, 1),
            "role_id": proto_text(fields, 2),
            "item_id": proto_text(fields, 3),
            "skin_data_bytes": len(proto_bytes(fields, 4)),
        }
    if method == "UpdateWeaponArchiveV2" and direction == "send":
        return {
            "kind": "update_weapon_request",
            "role_id": proto_text(fields, 1),
            "weapon_archive_bytes": len(proto_bytes(fields, 3)),
        }
    if method in {"UpdateRoleArchiveV2", "UpdateWeaponArchiveV2"} and direction == "recv":
        return {
            "kind": "archive_update_response",
            "status_field_present": 1 in fields,
            "status_code": proto_varint(fields, 1) if 1 in fields else None,
        }
    return {"kind": "unclassified", "top_level_fields": sorted(fields)}


def main() -> int:
    args = parse_args()
    script_path = Path(__file__).with_name("armory_probe.js")
    if not script_path.is_file():
        raise FileNotFoundError(script_path)

    output_directory = (args.output or default_output_directory()).resolve()
    output_directory.mkdir(parents=True, exist_ok=True)
    event_path = output_directory / "events.jsonl"
    metadata_path = output_directory / "capture.json"
    payload_directory = output_directory / "payloads"

    device = frida.get_local_device()
    existing_pids = set(enumerate_matching_pids(device, args.process_name))
    launcher: subprocess.Popen[Any] | None = None
    preverified_image_path: Path | None = None
    preverified_image_sha256: str | None = None

    if args.launch:
        startgame = args.startgame.resolve()
        if not startgame.is_file():
            raise FileNotFoundError(startgame)
        # Hash the version-pinned image while startgame authenticates and
        # starts MetaTunnel. Re-hashing the 100 MB executable after process
        # creation misses the earliest native RPCs on fast machines.
        candidate_image = (startgame.parent / args.process_name).resolve()
        if not candidate_image.is_file():
            raise FileNotFoundError(candidate_image)
        preverified_image_path = candidate_image
        preverified_image_sha256 = sha256_file(candidate_image)
        if preverified_image_sha256.casefold() != EXPECTED_GAME_SHA256:
            raise RuntimeError(
                "Boundary executable hash mismatch: "
                f"expected {EXPECTED_GAME_SHA256}, got {preverified_image_sha256} "
                f"({candidate_image})"
            )
        command = [
            "powershell.exe",
            "-NoProfile",
            "-ExecutionPolicy",
            "Bypass",
            "-File",
            str(startgame),
            *args.game_args,
        ]
        print(f"[LAUNCH] {' '.join(command)}", flush=True)
        launcher = subprocess.Popen(command, cwd=startgame.parent)

    if args.pid:
        pid = args.pid
    else:
        excluded = existing_pids if args.launch else set()
        pid = wait_for_process(device, args.process_name, args.attach_timeout, excluded)

    image_path = process_image_path(pid).resolve()
    if (
        preverified_image_path is not None
        and os.path.normcase(str(preverified_image_path))
        == os.path.normcase(str(image_path))
    ):
        image_sha256 = str(preverified_image_sha256)
    else:
        image_sha256 = sha256_file(image_path)
    if image_sha256.casefold() != EXPECTED_GAME_SHA256:
        raise RuntimeError(
            "Boundary executable hash mismatch: "
            f"expected {EXPECTED_GAME_SHA256}, got {image_sha256} ({image_path})"
        )

    stop_event = threading.Event()
    session: frida.core.Session | None = None

    def request_stop(*_: Any) -> None:
        stop_event.set()

    signal.signal(signal.SIGINT, request_stop)
    if hasattr(signal, "SIGTERM"):
        signal.signal(signal.SIGTERM, request_stop)

    metadata = {
        "created_at": dt.datetime.now(dt.timezone.utc).isoformat(),
        "pid": pid,
        "process_name": args.process_name,
        "process_image": str(image_path),
        "process_image_sha256": image_sha256,
        "probe": str(script_path.resolve()),
        "frida_version": frida.__version__,
        "mode": "native_archive_read_only",
    }
    metadata_path.write_text(
        json.dumps(metadata, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )

    print(f"[ATTACH] PID {pid}", flush=True)
    print(f"[OUTPUT] {output_directory}", flush=True)
    with event_path.open("a", encoding="utf-8", buffering=1) as event_file:
        payload_counter = 0
        try:
            session = device.attach(pid)
            session.on("detached", lambda *_: stop_event.set())
            script = session.create_script(script_path.read_text(encoding="utf-8"))

            def on_message(message: dict[str, Any], data: bytes | None) -> None:
                nonlocal payload_counter
                record: dict[str, Any] = {
                    "host_timestamp": dt.datetime.now(dt.timezone.utc).isoformat(),
                    "frida_message": message,
                }
                if message.get("type") == "send" and isinstance(message.get("payload"), dict):
                    payload = message["payload"]
                    record = dict(payload)
                    record["host_timestamp"] = dt.datetime.now(dt.timezone.utc).isoformat()
                    line = compact_console_line(payload)
                    if data and payload.get("event") == "rpc.payload_capture":
                        payload_counter += 1
                        payload_directory.mkdir(parents=True, exist_ok=True)
                        method = str(payload.get("rpc_path", "rpc")).rsplit("/", 1)[-1]
                        safe_method = "".join(
                            character if character.isalnum() else "_" for character in method
                        )
                        filename = (
                            f"{payload_counter:04d}-{payload.get('direction', 'unknown')}-"
                            f"{safe_method or 'rpc'}.bin"
                        )
                        payload_path = payload_directory / filename
                        payload_path.write_bytes(data)
                        record["binary_path"] = str(payload_path)
                        try:
                            record["decoded"] = decode_captured_payload(
                                str(payload.get("rpc_path", "")),
                                str(payload.get("direction", "")),
                                data,
                            )
                        except Exception as error:
                            record["decode_error"] = f"{type(error).__name__}: {error}"
                        print(
                            json.dumps(record, ensure_ascii=False, separators=(",", ":")),
                            flush=True,
                        )
                    elif line:
                        print(line, flush=True)
                elif message.get("type") == "error":
                    print(json.dumps(message, ensure_ascii=False), file=sys.stderr, flush=True)
                if data:
                    record["binary_bytes"] = len(data)
                event_file.write(
                    json.dumps(record, ensure_ascii=False, separators=(",", ":")) + "\n"
                )

            script.on("message", on_message)
            script.load()
            while not stop_event.wait(0.5):
                if launcher is not None and launcher.poll() is not None:
                    # The Frida session's detached event is authoritative, but do not wait forever
                    # if startgame terminates before the game starts correctly.
                    try:
                        device.get_process(pid)
                    except frida.ProcessNotFoundError:
                        break
        finally:
            if session is not None:
                try:
                    session.detach()
                except frida.InvalidOperationError:
                    pass
            if launcher is not None and launcher.poll() is None and stop_event.is_set():
                print("[NOTE] Probe detached; startgame/game were left running.", flush=True)

    print(f"[DONE] {event_path}", flush=True)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as error:
        print(f"[ERROR] {error}", file=sys.stderr)
        raise
