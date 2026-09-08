"""Local E2E-11 harness: synthetic loopback control plane, real Toolbox client and real MetaTunnel binary."""

import argparse
import asyncio
import datetime
import http.server
import json
import os
from pathlib import Path
import socket
import hashlib
import shutil
import subprocess
import tempfile
import threading
import time

from websockets.legacy.server import serve


TOKENS = {
    "old": "synthetic-old-access-000000000000000000000000",
    "new": "synthetic-new-access-111111111111111111111111",
    "logout_old": "synthetic-logout-old-222222222222222222222",
    "switch_old": "synthetic-switch-old-333333333333333333333",
    "switch_new": "synthetic-switch-new-444444444444444444444",
}
REFRESH_TOKENS = {
    "phase_a": "synthetic-old-refresh-000000000000000000000",
    "logout": "synthetic-logout-refresh-222222222222222",
    "switch": "synthetic-switch-refresh-333333333333333",
}
REPLACEMENTS = {
    "phase_a": (TOKENS["new"], "synthetic-new-refresh-111111111111111111111"),
    "logout": ("synthetic-logout-new-555555555555555555555", "synthetic-logout-new-refresh-55555555555"),
    "switch": (TOKENS["switch_new"], "synthetic-switch-new-refresh-44444444444"),
}


class State:
    def __init__(self, root: Path):
        self.root = root
        self.lock = threading.Lock()
        self.refresh_serial = threading.Lock()
        self.counts = {
            "old_401": 0,
            "protected_success": 0,
            "refresh": 0,
            "logout": 0,
            "meta_old": 0,
            "meta_new": 0,
            "ws_old": 0,
            "ws_new": 0,
            "logic_connections": 0,
        }
        self.refresh_by_phase = {"phase_a": 0, "logout": 0, "switch": 0}

    def phase(self):
        try:
            return (self.root / "phase").read_text(encoding="utf-8").strip()
        except FileNotFoundError:
            return "phase_a"

    def mark(self, name: str):
        (self.root / name).write_text("ok", encoding="utf-8")

    def refresh_count(self, phase: str) -> int:
        with self.lock:
            return self.refresh_by_phase[phase]

    def increment_refresh(self, phase: str):
        with self.lock:
            self.refresh_by_phase[phase] += 1
            self.counts["refresh"] += 1
            value = self.refresh_by_phase[phase]
        self.mark(f"refresh-started-{phase}")
        return value

    def snapshot(self):
        with self.lock:
            return dict(self.counts)


def wait_file(path: Path, timeout: float = 60.0):
    deadline = time.monotonic() + timeout
    while not path.exists() and time.monotonic() < deadline:
        time.sleep(0.01)


class AuthHandler(http.server.BaseHTTPRequestHandler):
    state: State = None

    def log_message(self, *_args):
        pass

    def respond(self, status, value):
        body = json.dumps(value, separators=(",", ":")).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def bearer(self):
        value = self.headers.get("Authorization", "")
        return value[7:] if value.startswith("Bearer ") else ""

    def do_GET(self):
        state = self.state
        if self.path != "/test/protected":
            self.respond(404, {})
            return
        phase = state.phase()
        token = self.bearer()
        old = {"phase_a": TOKENS["old"], "logout": TOKENS["logout_old"], "switch": TOKENS["switch_old"]}[phase]
        new = {"phase_a": TOKENS["new"], "logout": "synthetic-logout-new-555555555555555555555", "switch": TOKENS["switch_new"]}[phase]
        if token == old:
            with state.lock:
                state.counts["old_401"] += 1
                old_count = state.counts["old_401"]
            if phase == "phase_a" and old_count == 20:
                state.mark("old401-20")
            self.respond(401, {"error": {"code": "TOKEN_EXPIRED", "message": "synthetic expiry"}, "request_id": "e2e11-old"})
            return
        if token == new:
            with state.lock:
                state.counts["protected_success"] += 1
            self.respond(200, {"data": {"ok": True}, "request_id": "e2e11-new"})
            return
        self.respond(403, {"error": {"code": "WRONG_TEST_CREDENTIAL", "message": "synthetic credential rejected"}})

    def do_POST(self):
        state = self.state
        length = int(self.headers.get("Content-Length", "0"))
        payload = self.rfile.read(length)
        path = self.path
        if path == "/v1/auth/logout":
            with state.lock:
                state.counts["logout"] += 1
            self.respond(200, {"data": {"logged_out": True}})
            return
        if path != "/v1/auth/refresh":
            self.respond(404, {})
            return
        phase = state.phase()
        expected = REFRESH_TOKENS[phase]
        try:
            supplied = json.loads(payload).get("refresh_token")
        except Exception:
            supplied = None
        if supplied != expected:
            self.respond(401, {"error": {"code": "REFRESH_REUSED", "message": "synthetic refresh rejected"}})
            return
        with state.refresh_serial:
            if state.increment_refresh(phase) != 1:
                self.respond(401, {"error": {"code": "REFRESH_REUSED", "message": "synthetic refresh already consumed"}})
                return
            wait_file(state.root / f"release-refresh-{phase}")
            access, refresh = REPLACEMENTS[phase]
            expiry = (datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(hours=1)).isoformat()
            self.respond(200, {"data": {"session": {"access_token": access, "refresh_token": refresh, "access_token_expires_at": expiry, "refresh_token_expires_at": expiry}, "capabilities": []}})


class MetaHandler(http.server.BaseHTTPRequestHandler):
    state: State = None

    def log_message(self, *_args):
        pass

    def respond(self, status, value):
        body = json.dumps(value, separators=(",", ":")).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path not in ("/v1/users/me/meta-profile", "/users/me/meta-profile"):
            self.respond(404, {})
            return
        value = self.headers.get("Authorization", "")
        token = value[7:] if value.startswith("Bearer ") else ""
        if token == TOKENS["old"]:
            with self.state.lock:
                self.state.counts["meta_old"] += 1
            self.state.mark("meta-old")
            self.respond(401, {"error": {"code": "TOKEN_EXPIRED", "message": "synthetic meta expiry"}})
            return
        if token == TOKENS["new"]:
            with self.state.lock:
                self.state.counts["meta_new"] += 1
            self.state.mark("meta-new")
            self.respond(200, {"data": {"revision": 8, "status": "ok"}})
            return
        self.respond(401, {"error": {"code": "UNAUTHORIZED", "message": "synthetic meta credential rejected"}})


def start_logic(state: State):
    listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    listener.bind(("127.0.0.1", 0))
    listener.listen(4)

    def worker():
        try:
            while True:
                conn, _ = listener.accept()
                with state.lock:
                    state.counts["logic_connections"] += 1
                state.mark("logic-hit")
                with conn:
                    data = conn.recv(4096)
                    if data:
                        conn.sendall(data)
        except OSError:
            pass

    thread = threading.Thread(target=worker, daemon=True)
    thread.start()
    return listener, thread


def start_ws(state: State):
    ready = threading.Event()
    holder = {}
    loop = asyncio.new_event_loop()

    async def handler(websocket, _path=None):
        value = websocket.request_headers.get("Authorization", "")
        token = value[7:] if value.startswith("Bearer ") else ""
        if token == TOKENS["old"]:
            with state.lock:
                state.counts["ws_old"] += 1
                count = state.counts["ws_old"]
            state.mark("ws-old")
            if count == 1:
                while state.refresh_count("phase_a") < 1:
                    await asyncio.sleep(0.01)
            await websocket.close()
            return
        if token == TOKENS["new"]:
            with state.lock:
                state.counts["ws_new"] += 1
            state.mark("ws-new")
            await websocket.send(json.dumps({"type": "room.snapshot", "payload": {"session_epoch": 7, "token_version": "new"}}))
            await asyncio.sleep(0.2)
            await websocket.close()
            return
        await websocket.close()

    async def run():
        server = await serve(handler, "127.0.0.1", 0)
        holder["server"] = server
        holder["port"] = server.sockets[0].getsockname()[1]
        ready.set()
        await holder["stop"].wait()
        server.close()
        await server.wait_closed()

    def thread_main():
        asyncio.set_event_loop(loop)
        holder["stop"] = asyncio.Event()
        loop.run_until_complete(run())
        loop.close()

    thread = threading.Thread(target=thread_main, daemon=True)
    thread.start()
    if not ready.wait(10):
        raise RuntimeError("WebSocket server did not start")
    return holder["port"], loop, holder["stop"], thread


def redact(text: str):
    for token in list(TOKENS.values()) + list(REFRESH_TOKENS.values()) + [v for pair in REPLACEMENTS.values() for v in pair]:
        text = text.replace(token, "<redacted-token>")
    return text


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--driver", type=Path, required=True)
    parser.add_argument("--config-writer", type=Path, required=True)
    parser.add_argument("--result", type=Path, required=True)
    args = parser.parse_args()
    if not args.config_writer.is_file():
        raise SystemExit(f"config writer does not exist: {args.config_writer}")
    with tempfile.TemporaryDirectory(prefix="rebound-e2e11-") as temporary:
        root = Path(temporary)
        state = State(root)
        (root / "phase").write_text("phase_a", encoding="utf-8")
        AuthHandler.state = state
        MetaHandler.state = state
        auth_server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), AuthHandler)
        meta_server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), MetaHandler)
        auth_thread = threading.Thread(target=auth_server.serve_forever, daemon=True)
        meta_thread = threading.Thread(target=meta_server.serve_forever, daemon=True)
        auth_thread.start()
        meta_thread.start()
        logic_listener, logic_thread = start_logic(state)
        ws_port, ws_loop, ws_stop, ws_thread = start_ws(state)
        env = dict(os.environ)
        env.update({
            "LOCALAPPDATA": str(root),
            "E2E11_STATE_DIR": str(root),
            "E2E11_REALTIME_URL": f"ws://127.0.0.1:{ws_port}/v1/realtime/connect",
            "PROJECT_REBOUND_LAB_API_ORIGIN": f"http://127.0.0.1:{auth_server.server_port}",
            "PROJECT_REBOUND_LAB_META_HTTP_ORIGIN": f"http://127.0.0.1:{meta_server.server_port}",
            "PROJECT_REBOUND_LAB_META_LOGIC_ADDRESS": f"127.0.0.1:{logic_listener.getsockname()[1]}",
            "NO_PROXY": "127.0.0.1,localhost",
            "no_proxy": "127.0.0.1,localhost",
            "E2E11_CONFIG_WRITER": str(args.config_writer.resolve()),
        })
        # Preinstall the already-verified embedded asset into the isolated lab
        # runtime root. This keeps the E2E run from exercising MoveFileEx on a
        # temporary profile; the Toolbox still validates the pinned bytes and
        # manifest before starting the real child.
        origin = env["PROJECT_REBOUND_LAB_API_ORIGIN"].rstrip("/").lower()
        scope = hashlib.sha256(origin.encode()).hexdigest()
        runtime_root = root / "com.projectrebound.toolbox" / "lab" / scope / "runtime" / "meta-tunnel" / "6623003757bfbb93abe1a7fc33491b3d1f3697bd909957527634775322728b91"
        runtime_root.mkdir(parents=True, exist_ok=True)
        source_runtime = Path(r"C:\wksp\ProjectReboundToolbox\res\metatunnel\runtime")
        shutil.copy2(source_runtime / "meta-tunnel.exe", runtime_root / "meta-tunnel.exe")
        shutil.copy2(source_runtime / "meta-tunnel-runtime-manifest.json", runtime_root / "meta-tunnel-runtime-manifest.json")
        try:
            completed = subprocess.run(
                [str(args.driver.resolve(strict=True))],
                env=env,
                capture_output=True,
                text=True,
                encoding="utf-8",
                errors="replace",
                timeout=150,
                creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
            )
            # Release any server-side waiters before teardown even on a failure.
            for phase in ("phase_a", "logout", "switch"):
                (root / f"release-refresh-{phase}").write_text("cleanup", encoding="utf-8")
            (root / "old401-release").write_text("cleanup", encoding="utf-8")
            result = {
                "status": "PASS" if completed.returncode == 0 else "FAIL",
                "driver_exit_code": completed.returncode,
                "counts": state.snapshot(),
                "markers": sorted(p.name for p in root.iterdir() if p.is_file() and p.name != "phase"),
                "driver_stdout": redact((completed.stdout or "").strip()),
                "driver_stderr": redact((completed.stderr or "").strip()),
            }
        except subprocess.TimeoutExpired as error:
            for phase in ("phase_a", "logout", "switch"):
                (root / f"release-refresh-{phase}").write_text("cleanup", encoding="utf-8")
            (root / "old401-release").write_text("cleanup", encoding="utf-8")
            result = {"status": "TIMEOUT", "driver_exit_code": None, "counts": state.snapshot(), "error": redact(str(error))}
        finally:
            auth_server.shutdown()
            meta_server.shutdown()
            auth_server.server_close()
            meta_server.server_close()
            try:
                logic_listener.close()
            except OSError:
                pass
            ws_loop.call_soon_threadsafe(ws_stop.set)
            ws_thread.join(timeout=5)
            auth_thread.join(timeout=5)
            meta_thread.join(timeout=5)
            logic_thread.join(timeout=2)
        args.result.parent.mkdir(parents=True, exist_ok=True)
        args.result.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(result, ensure_ascii=False, indent=2))
        raise SystemExit(0 if result["status"] == "PASS" else 1)


if __name__ == "__main__":
    main()
