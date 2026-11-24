"""Native HTTPS and legacy HTTP/WS through one synthetic Gateway listener."""

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import queue
import socket
import sqlite3
import subprocess
import tempfile
import threading
import time
from contextlib import contextmanager
from datetime import datetime, timedelta, timezone
from urllib.error import URLError
from urllib.request import urlopen

from playwright.sync_api import Error, expect, sync_playwright


LEGACY_ROOM = "demo"
PROTECTED_ROOM = "protected-fixture"
LEGACY_EVENT = "legacy-fixture-event"
LEGACY_TEXT = "synthetic legacy history"
LEGACY_REPLY = "synthetic legacy reply"
PROTECTED_TEXT = "synthetic protected browser message"


def run(*args):
    subprocess.run(args, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)


def create_test_pki(root):
    """Issue a loopback server certificate from a fresh, synthetic root."""
    ca, ca_key = root / "ca.pem", root / "ca-key.pem"
    leaf, leaf_key = root / "server.pem", root / "server-key.pem"
    request, extensions = root / "server.csr", root / "server.ext"
    run("openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256",
        "-noenc", "-subj", "/CN=GooseCab synthetic test CA", "-days", "1",
        "-addext", "basicConstraints=critical,CA:TRUE,pathlen:0",
        "-addext", "keyUsage=critical,keyCertSign,cRLSign",
        "-keyout", str(ca_key), "-out", str(ca))
    run("openssl", "req", "-new", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256",
        "-noenc", "-subj", "/CN=localhost", "-keyout", str(leaf_key), "-out", str(request))
    extensions.write_text("basicConstraints=critical,CA:FALSE\n"
                          "keyUsage=critical,digitalSignature\n"
                          "extendedKeyUsage=serverAuth\n"
                          "subjectAltName=IP:127.0.0.1\n")
    run("openssl", "x509", "-req", "-in", str(request), "-CA", str(ca),
        "-CAkey", str(ca_key), "-CAcreateserial", "-days", "1",
        "-extfile", str(extensions), "-out", str(leaf))
    return ca, leaf, leaf_key


def seed_legacy_history(path):
    with sqlite3.connect(path) as database:
        database.execute("""
            CREATE TABLE events(event_id TEXT PRIMARY KEY, room_id TEXT NOT NULL, seq INTEGER NOT NULL,
                                type TEXT NOT NULL, payload TEXT, created_at TEXT NOT NULL)
        """)
        database.execute("INSERT INTO events VALUES (?, ?, 1, 'chat.message', ?, ?)",
                         (LEGACY_EVENT, LEGACY_ROOM, json.dumps({"text": LEGACY_TEXT}),
                          datetime.now(timezone.utc).isoformat()))


@contextmanager
def test_process(args, env, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL):
    with subprocess.Popen(args, env=env, stdin=stdin, stdout=stdout, text=True) as process:
        try:
            yield process
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)


def wait_for_gateway(process, http_origin):
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise AssertionError(f"Gateway exited during startup with status {process.returncode}")
        try:
            with urlopen(http_origin + "/healthz", timeout=0.25) as response:
                assert response.status == 200
                return
        except URLError:
            time.sleep(0.05)
    raise AssertionError("Gateway never accepted HTTP on the synthetic port")


def observe_agent(process):
    frames = queue.Queue()

    def collect():
        try:
            for line in process.stdout:
                frames.put(json.loads(line))
        except (OSError, ValueError) as err:
            frames.put(err)
        finally:
            frames.put(None)

    threading.Thread(target=collect, daemon=True).start()
    return frames


def wait_for_legacy_frame(frames, predicate):
    deadline = time.monotonic() + 8
    while time.monotonic() < deadline:
        try:
            frame = frames.get(timeout=max(deadline - time.monotonic(), 0))
        except queue.Empty:
            break
        if frame is None or isinstance(frame, Exception):
            raise AssertionError(f"Legacy Agent stream ended early: {frame}")
        assert frame["room_id"] == LEGACY_ROOM, frame
        if predicate(frame):
            return
    raise AssertionError("Legacy Agent did not emit the expected frame")


def legacy_history(url):
    with urlopen(url, timeout=3) as response:
        assert response.status == 200
        return response.read().decode()


def launch_chromium(playwright, browser_home, executable):
    # Chromium uses a shared NSS DB on Linux. Give the test browser a
    # dedicated home and configure only that home, leaving host trust alone.
    browser_env = {**os.environ, "HOME": str(browser_home),
                   "XDG_CONFIG_HOME": str(browser_home / ".config"),
                   "XDG_DATA_HOME": str(browser_home / ".local" / "share")}
    return playwright.chromium.launch(channel="chromium" if executable is None else None,
                                      executable_path=executable, headless=True,
                                      args=["--no-proxy-server", "--disable-background-networking"],
                                      env=browser_env)


def browse(playwright, browser_home, executable, ca, origin, token, agent, frames):
    # An existing legacy NSS location is honored by Chromium across versions.
    nss_dir = browser_home / ".pki" / "nssdb"
    nss_dir.mkdir(parents=True)
    nss_database = f"sql:{nss_dir}"
    run("certutil", "-N", "-d", nss_database, "--empty-password")

    with launch_chromium(playwright, browser_home, executable) as browser:
        page = browser.new_context(ignore_https_errors=False).new_page()
        try:
            page.goto(origin + "/room-chat", timeout=8000)
        except Error as err:
            assert "net::ERR_CERT_AUTHORITY_INVALID" in str(err), err
        else:
            raise AssertionError("Chromium accepted the server without trusting the synthetic CA")

    run("certutil", "-A", "-d", nss_database, "-t", "C,,", "-n", "GooseCab synthetic test CA",
        "-i", str(ca))
    with launch_chromium(playwright, browser_home, executable) as browser:
        context = browser.new_context(ignore_https_errors=False, accept_downloads=True)
        context.route("**/*", lambda route: route.continue_()
                      if route.request.url.startswith(origin + "/") else route.abort())
        page = context.new_page()
        response = page.goto(origin + "/room-chat", timeout=8000)
        assert response is not None and response.ok
        security = response.security_details()
        assert security and security["issuer"] == "GooseCab synthetic test CA", security

        def sign_in(room):
            page.get_by_label("房间 ID").fill(room)
            page.get_by_label("个人令牌").fill(token)
            page.get_by_role("button", name="进入房间").click()

        sign_in(LEGACY_ROOM)
        expect(page.locator("#status")).to_contain_text("房间授权已失效")
        sign_in(PROTECTED_ROOM)
        expect(page.locator("#status")).to_contain_text("fixture-writer")
        page.get_by_label("发言", exact=True).fill(PROTECTED_TEXT)
        with page.expect_response(lambda result: "/api/room-chat/messages?" in result.url) as posted:
            page.get_by_role("button", name="发送").click()
        assert posted.value.status == 201
        event = posted.value.json()
        assert event["room_id"] == PROTECTED_ROOM and event["actor"] == {
            "id": "fixture-writer", "kind": "human",
        }, event
        expect(page.locator("#events")).to_contain_text(PROTECTED_TEXT)
        expect(page.locator("#events")).not_to_contain_text(LEGACY_TEXT)
        with page.expect_download() as exported:
            page.get_by_role("button", name="导出历史").click()
        private_export = exported.value.path().read_text(encoding="utf-8")
        assert PROTECTED_TEXT in private_export and LEGACY_TEXT not in private_export

        # Keep the browser session and the originally connected WS Agent alive
        # together. The Agent replies to its legacy room using its old CLI.
        agent.stdin.write(f"chat {LEGACY_REPLY}\n")
        agent.stdin.flush()
        wait_for_legacy_frame(frames, lambda frame: frame.get("payload", {}).get("text") == LEGACY_REPLY)
        with page.expect_response(lambda result: "/api/room-chat/history?" in result.url) as refreshed:
            page.get_by_role("button", name="返回最新").click()
        assert refreshed.value.ok
        events = refreshed.value.json()["events"]
        assert len(events) == 1 and events[0]["event_id"] == event["event_id"], events
        expect(page.locator("#events")).not_to_contain_text(LEGACY_REPLY)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("gateway", type=Path)
    parser.add_argument("baseline_agent", type=Path)
    parser.add_argument("--chromium", help="Use a local executable instead of Playwright's Chromium")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="goosecab-native-browser-") as tmp:
        root = Path(tmp)
        ca, certificate, key = create_test_pki(root)
        database, config = root / "fixture.db", root / "room-policy.json"
        seed_legacy_history(database)
        with socket.socket() as reserved:
            reserved.bind(("127.0.0.1", 0))
            address = f"127.0.0.1:{reserved.getsockname()[1]}"
        http_origin, https_origin = "http://" + address, "https://" + address
        token = base64.urlsafe_b64encode(os.urandom(32)).decode().rstrip("=")
        config.write_text(json.dumps({"origin": https_origin, "rooms": [{
            "id": PROTECTED_ROOM, "humans": [{
                "id": "fixture-writer", "write": True,
                "token_sha256": hashlib.sha256(token.encode()).hexdigest(),
                "expires_at": (datetime.now(timezone.utc) + timedelta(minutes=5)).isoformat(),
            }]}]}))
        config.chmod(0o600)
        child_env = {"PATH": os.environ["PATH"]}
        with test_process([str(args.gateway.resolve()), "-addr", address,
                           "-room-chat-config", str(config), "-room-chat-tls-cert", str(certificate),
                           "-room-chat-tls-key", str(key)],
                          {**child_env, "GOOSECAB_DB": str(database)}) as gateway:
            wait_for_gateway(gateway, http_origin)
            with test_process([str(args.baseline_agent.resolve()), "-mode", "interactive", "-room", LEGACY_ROOM,
                               "-url", "ws://" + address + "/api/v1/agent/connect", "-id", "fixture-legacy-agent"],
                              child_env, stdin=subprocess.PIPE, stdout=subprocess.PIPE) as agent:
                frames = observe_agent(agent)
                wait_for_legacy_frame(frames, lambda frame: frame.get("event_id") == LEGACY_EVENT)
                with sync_playwright() as playwright:
                    browse(playwright, root / "browser-home", args.chromium, ca, https_origin, token, agent, frames)
                for route in ("/api/events", "/api/events/export.html"):
                    public = legacy_history(f"{http_origin}{route}?room_id={LEGACY_ROOM}")
                    assert LEGACY_TEXT in public and LEGACY_REPLY in public and PROTECTED_TEXT not in public
    print("PASS: Chromium rejected the untrusted server, accepted its synthetic CA, and wrote to the protected room. "
          "The old Agent, replay, and anonymous history/export stayed in the legacy room on the same listener.")


if __name__ == "__main__":
    main()
