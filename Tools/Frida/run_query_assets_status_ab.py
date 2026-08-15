#!/usr/bin/env python3
"""Attach the opt-in QueryAssets field-1 A/B probe to an exact game PID."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import signal
import threading
from typing import Any

import frida

from capture_armory import EXPECTED_GAME_SHA256, process_image_path, sha256_file


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--pid", required=True, type=int)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--script", default="query_assets_status_ab.js")
    parser.add_argument(
        "--target-item",
        help="Override the probe's default ItemID for a discriminating A/B run.",
    )
    parser.add_argument(
        "--top-level-value",
        choices=(0, 1),
        type=int,
        help="Override QueryAssets field 1 while preserving its three-byte width.",
    )
    parser.add_argument(
        "--target-player-level",
        choices=range(0, 128),
        type=int,
        help="Override GetPlayerArchiveV2 level for player_archive_level_ab.js.",
    )
    args = parser.parse_args()

    script_path = Path(__file__).with_name(args.script)
    if script_path.name != args.script or not script_path.is_file():
        raise ValueError("--script must name a probe file next to this controller")
    args.output.parent.mkdir(parents=True, exist_ok=True)
    stop = threading.Event()
    signal.signal(signal.SIGINT, lambda *_: stop.set())
    if hasattr(signal, "SIGTERM"):
        signal.signal(signal.SIGTERM, lambda *_: stop.set())

    image_path = process_image_path(args.pid).resolve()
    image_sha256 = sha256_file(image_path)
    if image_sha256.casefold() != EXPECTED_GAME_SHA256:
        raise RuntimeError(
            "Boundary executable hash mismatch: "
            f"expected {EXPECTED_GAME_SHA256}, got {image_sha256} ({image_path})"
        )

    device = frida.get_local_device()
    session = device.attach(args.pid)
    session.on("detached", lambda *_: stop.set())
    script_source = script_path.read_text(encoding="utf-8")
    if args.target_item:
        target_assignment = (
            "globalThis.__PROJECT_REBOUND_TARGET_ITEM__ = "
            + json.dumps(args.target_item, ensure_ascii=False)
            + ";\n"
        )
        script_source = target_assignment + script_source
    if args.top_level_value is not None:
        value_assignment = (
            "globalThis.__PROJECT_REBOUND_TOP_LEVEL_VALUE__ = "
            + str(args.top_level_value)
            + ";\n"
        )
        script_source = value_assignment + script_source
    if args.target_player_level is not None:
        level_assignment = (
            "globalThis.__PROJECT_REBOUND_TARGET_PLAYER_LEVEL__ = "
            + str(args.target_player_level)
            + ";\n"
        )
        script_source = level_assignment + script_source
    script = session.create_script(script_source)

    with args.output.open("a", encoding="utf-8", buffering=1) as output:
        identity = {
            "source": "project-rebound-frida-controller",
            "event": "probe.target_verified",
            "pid": args.pid,
            "process_image": str(image_path),
            "process_image_sha256": image_sha256,
            "probe": str(script_path.resolve()),
        }
        output.write(json.dumps(identity, ensure_ascii=False, separators=(",", ":")) + "\n")
        print(json.dumps(identity, ensure_ascii=False, separators=(",", ":")), flush=True)

        def on_message(message: dict[str, Any], data: bytes | None) -> None:
            record = message.get("payload", message)
            output.write(json.dumps(record, ensure_ascii=False, separators=(",", ":")) + "\n")
            print(json.dumps(record, ensure_ascii=False, separators=(",", ":")), flush=True)

        script.on("message", on_message)
        script.load()
        while not stop.wait(0.5):
            pass

    try:
        session.detach()
    except frida.InvalidOperationError:
        pass
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
