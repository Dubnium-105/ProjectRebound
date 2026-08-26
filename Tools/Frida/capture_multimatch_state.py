#!/usr/bin/env python3
"""Capture one read-only multi-match state snapshot from an exact game PID."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import queue
from typing import Any

import frida

from capture_armory import EXPECTED_GAME_SHA256, process_image_path


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest().upper()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--pid", required=True, type=int)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()

    image_path = process_image_path(args.pid).resolve()
    image_sha256 = sha256_file(image_path)
    if image_sha256.casefold() != EXPECTED_GAME_SHA256.casefold():
        raise RuntimeError(
            "Boundary executable hash mismatch: "
            f"expected {EXPECTED_GAME_SHA256}, got {image_sha256} ({image_path})"
        )

    script_path = Path(__file__).with_name("multimatch_state_probe.js")
    messages: queue.Queue[dict[str, Any]] = queue.Queue()
    device = frida.get_local_device()
    session = device.attach(args.pid)
    script = session.create_script(script_path.read_text(encoding="utf-8"))

    def on_message(message: dict[str, Any], data: bytes | None) -> None:
        del data
        messages.put(message.get("payload", message))

    script.on("message", on_message)
    script.load()

    records: list[dict[str, Any]] = [{
        "source": "project-rebound-frida-controller",
        "event": "probe.target_verified",
        "pid": args.pid,
        "process_image": str(image_path),
        "process_image_sha256": image_sha256,
        "probe": str(script_path.resolve()),
    }]
    while True:
        record = messages.get(timeout=10.0)
        records.append(record)
        if record.get("event") == "probe.complete":
            break

    session.detach()
    args.output.parent.mkdir(parents=True, exist_ok=True)
    with args.output.open("w", encoding="utf-8") as output:
        for record in records:
            output.write(json.dumps(record, ensure_ascii=False, separators=(",", ":")) + "\n")
            print(json.dumps(record, ensure_ascii=False, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
