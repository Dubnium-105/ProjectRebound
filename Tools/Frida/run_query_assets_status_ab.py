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


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--pid", required=True, type=int)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--script", default="query_assets_status_ab.js")
    args = parser.parse_args()

    script_path = Path(__file__).with_name(args.script)
    if script_path.name != args.script or not script_path.is_file():
        raise ValueError("--script must name a probe file next to this controller")
    args.output.parent.mkdir(parents=True, exist_ok=True)
    stop = threading.Event()
    signal.signal(signal.SIGINT, lambda *_: stop.set())
    if hasattr(signal, "SIGTERM"):
        signal.signal(signal.SIGTERM, lambda *_: stop.set())

    device = frida.get_local_device()
    session = device.attach(args.pid)
    session.on("detached", lambda *_: stop.set())
    script = session.create_script(script_path.read_text(encoding="utf-8"))

    with args.output.open("a", encoding="utf-8", buffering=1) as output:
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
