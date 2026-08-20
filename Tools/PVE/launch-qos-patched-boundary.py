#!/usr/bin/env python3
"""Launch the pinned Boundary client and patch its retired QoS URL before login."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import subprocess
import sys
import threading
import time
from pathlib import Path
from urllib.parse import urlparse

import frida


EXPECTED_EXE_SHA256 = "181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843"
MODULE_NAME = "ProjectBoundarySteam-Win64-Shipping.exe"
OVERSEA_DISCOVERY_FSTRING_RVA = 0x5C63C88
OVERSEA_DISCOVERY_INITIALIZER_RVA = 0x68ADE0


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--executable", required=True)
    parser.add_argument("--url", required=True)
    parser.add_argument("--ready-file")
    parser.add_argument("--timeout-seconds", type=float, default=15.0)
    parser.add_argument("game_arguments", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    if args.game_arguments[:1] == ["--"]:
        args.game_arguments = args.game_arguments[1:]
    return args


def validate_discovery_url(value: str) -> str:
    parsed = urlparse(value)
    if (
        parsed.scheme != "http"
        or parsed.hostname != "127.0.0.1"
        or parsed.username is not None
        or parsed.password is not None
        or parsed.port is None
        or not 1 <= parsed.port <= 65535
        or parsed.path != "/servers"
        or parsed.params
        or parsed.query
        or parsed.fragment
    ):
        raise ValueError("QoS discovery URL must be http://127.0.0.1:<port>/servers")
    return value


def build_agent(discovery_url: str) -> str:
    url_literal = json.dumps(discovery_url)
    return f"""
'use strict';
const module = Process.getModuleByName({json.dumps(MODULE_NAME)});
const discoveryState = module.base.add(0x{OVERSEA_DISCOVERY_FSTRING_RVA:x});
let initializerHook = null;
let finished = false;

function reportError(message) {{
  if (finished) return;
  finished = true;
  send({{ event: 'error', reason: String(message) }});
}}

function applyPatch() {{
  if (finished) return true;
  const data = discoveryState.readPointer();
  const capacity = discoveryState.add(12).readS32();
  const url = {url_literal};
  if (data.isNull() || capacity <= 0) return false;
  if (url.length + 1 > capacity) {{
    reportError('local URL exceeds the initialized FString capacity');
    return true;
  }}
  data.writeUtf16String(url);
  discoveryState.add(8).writeS32(url.length + 1);
  const verified = data.readUtf16String(url.length);
  if (verified !== url) {{
    reportError('QoS URL read-back mismatch');
    return true;
  }}
  finished = true;
  send({{
    event: 'patched',
    module_base: module.base.toString(),
    string_capacity: capacity,
    url_length: url.length
  }});
  if (initializerHook !== null) initializerHook.detach();
  return true;
}}

try {{
  if (!applyPatch()) {{
    initializerHook = Interceptor.attach(
      module.base.add(0x{OVERSEA_DISCOVERY_INITIALIZER_RVA:x}),
      {{ onLeave() {{ applyPatch(); }} }}
    );
    send({{ event: 'armed' }});
  }}
}} catch (error) {{
  reportError(error.stack || error);
}}
"""


def main() -> int:
    args = parse_args()
    executable = Path(args.executable).resolve()
    if not executable.is_file():
        raise FileNotFoundError(f"Boundary executable not found: {executable}")
    actual_hash = sha256_file(executable)
    if actual_hash.lower() != EXPECTED_EXE_SHA256:
        raise RuntimeError(
            f"Boundary executable SHA-256 mismatch: expected {EXPECTED_EXE_SHA256}, got {actual_hash}"
        )
    discovery_url = validate_discovery_url(args.url)

    device = frida.get_local_device()
    launched_process: subprocess.Popen[bytes] | None = None
    launched_pid: int | None = None
    session = None
    script = None
    patch_event = threading.Event()
    outcome: dict[str, object] = {}

    try:
        environment = dict(os.environ)
        loopback_bypass = "127.0.0.1,localhost,::1"
        inherited_bypass = environment.get("NO_PROXY", "").strip()
        effective_bypass = (
            f"{inherited_bypass},{loopback_bypass}" if inherited_bypass else loopback_bypass
        )
        environment["NO_PROXY"] = effective_bypass
        environment["no_proxy"] = effective_bypass

        # Frida's spawn path changes enough of the Steam client's process
        # bootstrap that this pinned build can stall at platform login. Launch
        # normally, then attach while the splash/movie sequence is still
        # running. The discovery FString is not consumed until the login UI
        # starts QoS, so patching after CreateProcess is early enough without
        # changing the game's native creation path.
        launched_process = subprocess.Popen(
            [str(executable), *args.game_arguments],
            cwd=executable.parent,
            env=environment,
        )
        launched_pid = launched_process.pid
        attach_deadline = time.monotonic() + args.timeout_seconds
        while True:
            if launched_process.poll() is not None:
                raise RuntimeError(
                    f"Boundary exited before the QoS patch attached (exit {launched_process.returncode})"
                )
            try:
                session = device.attach(launched_pid)
                break
            except (frida.ProcessNotFoundError, frida.PermissionDeniedError):
                if time.monotonic() >= attach_deadline:
                    raise TimeoutError("Boundary was not attachable before the startup timeout")
                time.sleep(0.025)
        script = session.create_script(build_agent(discovery_url))

        def on_message(message: dict[str, object], data: bytes | None) -> None:
            del data
            if message.get("type") == "error":
                outcome.update(event="error", reason=str(message.get("description", "agent error")))
                patch_event.set()
                return
            payload = message.get("payload")
            if not isinstance(payload, dict):
                return
            event = payload.get("event")
            if event in {"patched", "error"}:
                outcome.update(payload)
                patch_event.set()

        script.on("message", on_message)
        script.load()

        if not patch_event.wait(args.timeout_seconds):
            raise TimeoutError("QoS patch did not complete before the startup timeout")
        if outcome.get("event") != "patched":
            raise RuntimeError(str(outcome.get("reason", "QoS patch agent failed")))

        script.unload()
        script = None
        session.detach()
        session = None
        ready_json = json.dumps(
            {
                "event": "ready",
                "pid": launched_pid,
                "exe_sha256": actual_hash.upper(),
                "qos_url_length": outcome.get("url_length"),
            },
            separators=(",", ":"),
        )
        if args.ready_file:
            ready_path = Path(args.ready_file).resolve()
            temporary_path = ready_path.with_name(f"{ready_path.name}.{os.getpid()}.tmp")
            temporary_path.write_text(ready_json, encoding="utf-8")
            os.replace(temporary_path, ready_path)
        else:
            print(ready_json, flush=True)
        return 0
    except Exception:
        if launched_process is not None and launched_process.poll() is None:
            try:
                launched_process.terminate()
                launched_process.wait(timeout=5)
            except Exception:
                try:
                    launched_process.kill()
                except Exception:
                    pass
        raise
    finally:
        if script is not None:
            try:
                script.unload()
            except Exception:
                pass
        if session is not None:
            try:
                session.detach()
            except Exception:
                pass


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(f"QoS launch failed: {exc}", file=sys.stderr, flush=True)
        raise SystemExit(1)
