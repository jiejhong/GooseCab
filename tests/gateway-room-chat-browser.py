"""Check browser opt-in and restart/rollback using only a synthetic SQLite file."""
import base64
import hashlib
import json
import os
from pathlib import Path
import socket
import sqlite3
import ssl
from contextlib import contextmanager
import subprocess
import sys
import tempfile
import time
from datetime import datetime, timedelta, timezone
from urllib.request import Request, urlopen
from urllib.error import HTTPError

from playwright.sync_api import expect, sync_playwright


def token(seed):
    return base64.urlsafe_b64encode(bytes([seed]) * 32).decode().rstrip("=")


def origin_and_address():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        addr = f"127.0.0.1:{listener.getsockname()[1]}"
    return "http://" + addr, addr


def start(binary, origin, addr, database, policy=None):
    args = [binary, "-addr", addr]
    if policy is not None:
        args += ["-room-chat-config", str(policy)]
    process = subprocess.Popen(args, env={
        "PATH": os.environ["PATH"], "GOOSECAB_DB": str(database),
        "GOOSECAB_AGENT_TOKEN": "synthetic-browser-legacy",
    }, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    for _ in range(120):
        if process.poll() is not None:
            raise AssertionError(process.stderr.read().decode())
        try:
            with urlopen(origin + "/healthz", timeout=0.1):
                return process
        except OSError:
            time.sleep(0.05)
    stop(process)
    raise AssertionError("loopback Gateway did not start")


def stop(process):
    if process.poll() is None:
        process.terminate()
    process.wait(timeout=5)
    process.stderr.close()


@contextmanager
def https_proxy(backend):
    env = {**os.environ, "GOOSECAB_GATEWAY_TEST_BACKEND": backend}
    with subprocess.Popen([sys.argv[3], "-test.run=^TestGatewayHTTPSProxyFixture$"],
                          env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                          text=True) as process:
        try:
            origin, cert = process.stdout.readline().strip(), process.stdout.readline().strip()
            assert origin.startswith("https://127.0.0.1:")
            assert Path(cert).is_file()
            yield origin, cert
        finally:
            process.stdin.close()
            assert process.wait(timeout=10) == 0

def seed_legacy(path):
    now = datetime.now(timezone.utc).isoformat()
    with sqlite3.connect(path) as database:
        database.execute("CREATE TABLE events(event_id TEXT PRIMARY KEY, room_id TEXT NOT NULL,seq INTEGER NOT NULL,type TEXT NOT NULL,payload TEXT,created_at TEXT NOT NULL)")
        database.executemany("INSERT INTO events VALUES (?,?,?,'chat.message',?,?)", [
            (f"prior-{i}", "demo", i, json.dumps({"text": f"prior message {i}"}), now)
            for i in range(1, 23)
        ])


with tempfile.TemporaryDirectory(prefix="goosecab-gateway-browser-") as tmp:
    folder = Path(tmp)
    db_file, policy_file = folder / "legacy.db", folder / "config.json"
    seed_legacy(db_file)
    backend, addr = origin_and_address()
    with https_proxy(backend) as (origin, cert):
        crypto = ssl.create_default_context(cafile=cert)
        policy = {"origin": origin, "trust_loopback_proxy": True, "rooms": [{
            "id": "demo",
            "agents": [{"id": "fixture-agent", "write": True, "token_sha256": hashlib.sha256(token(3).encode()).hexdigest(),
                        "expires_at": (datetime.now(timezone.utc) + timedelta(minutes=3)).isoformat()}],
            "humans": [
                {"id": label, "display_name": label,
                 "token_sha256": hashlib.sha256(token(i).encode()).hexdigest(),
                 "expires_at": (datetime.now(timezone.utc) + timedelta(minutes=3)).isoformat(),
                 "write": writable, "broadcast": broadcast}
                for i, label, writable, broadcast in [(1, "fixture writer", True, True), (2, "fixture reader", False, False)]
            ]
        }]}
        policy_file.write_text(json.dumps(policy))
        policy_file.chmod(0o600)
        process = start(sys.argv[1], backend, addr, db_file, policy_file)
        try:
            with sync_playwright() as playwright:
                browser = playwright.chromium.launch(executable_path="/usr/bin/chromium", headless=True,
                                                      args=["--no-sandbox", "--disable-background-networking"])
                try:
                    context = browser.new_context(ignore_https_errors=True, accept_downloads=True)
                    context.route("**/*", lambda route: route.continue_() if
                                  route.request.url.startswith(origin + "/") else route.abort())
                    page = context.new_page()
                    page.goto(origin + "/room-chat")

                    def sign_in(index, room="demo"):
                        page.get_by_label("房间 ID").fill(room)
                        page.get_by_label("个人令牌").fill(token(index))
                        page.get_by_role("button", name="进入房间").click()

                    sign_in(1, "other")
                    expect(page.locator("#status")).to_contain_text("房间授权已失效")
                    sign_in(1)
                    expect(page.locator("#status")).to_contain_text("fixture writer")
                    expect(page.locator("#events li")).to_have_count(20)
                    expect(page.locator("#events li").first).to_contain_text("prior message 22")
                    page.get_by_role("button", name="查看更早").click()
                    expect(page.locator("#events li")).to_have_count(2)
                    expect(page.locator("#events li").last).to_contain_text("prior message 1")
                    page.get_by_role("button", name="返回最新").click()
                    expect(page.locator("#events li")).to_have_count(20)
                    text = '<img src=x onerror="alert(1)"> receives mention'
                    page.get_by_label("发言", exact=True).fill(text)
                    page.get_by_label("全员点名").check()
                    with page.expect_response(lambda r: "/api/room-chat/messages?" in r.url) as response:
                        page.get_by_role("button", name="发送").click()
                    assert response.value.status == 201
                    event = response.value.json()
                    assert event["actor"] == {"kind": "human", "id": "fixture writer"}
                    expect(page.locator("#events li").first).to_contain_text(text)
                    expect(page.locator("#events img")).to_have_count(0)
                    with page.expect_download() as download:
                        page.get_by_role("button", name="导出历史").click()
                    assert "&lt;img" in download.value.path().read_text()
                    status = page.evaluate("""async () => (await fetch(
                        '/api/events/export.html?room_id=demo',
                        {headers: {Authorization: 'Bearer synthetic-browser-legacy'}})).status""")
                    assert status == 401
                    page.get_by_role("button", name="退出房间").click()
                    sign_in(2)
                    expect(page.locator("#status")).to_contain_text("只读")
                    expect(page.locator("#compose")).to_be_hidden()
                    expect(page.locator("#unread-count")).to_have_text("1")
                    page.get_by_role("button", name="标记本页已读").click()
                    expect(page.locator("#unread-count")).to_have_text("0")
                    assert not context.cookies()
                    assert page.evaluate("localStorage.length + sessionStorage.length") == 0
                finally:
                    browser.close()
            initialize = {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}}
            read = {"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {
                "name": "room_read", "arguments": {"after_seq": 22, "limit": 1}}}
            result = subprocess.run([
                sys.argv[2], "-gateway-room-chat", "-url", origin.replace("https", "wss", 1) + "/api/room-chat/agent",
                "-room", "demo", "-agent", "fixture-agent",
            ], env={"PATH": os.environ["PATH"], "SSL_CERT_FILE": cert, "GOOSECAB_ROOM_CHAT_AGENT_TOKEN": token(3)},
                input=json.dumps(initialize) + "\n" + json.dumps(read) + "\n",
                capture_output=True, text=True, timeout=10, check=True)
            output = json.loads(result.stdout.splitlines()[1])["result"]
            assert not output["isError"]
            page = json.loads(output["content"][0]["text"])
            assert page["events"][0]["event_id"] == event["event_id"]
            assert page["mention_receipt"] == "pending_confirmation"
            with sqlite3.connect(db_file) as database:
                assert database.execute("SELECT COUNT(*) FROM room_chat_unread WHERE recipient='fixture-agent'").fetchone()[0] == 0

            # Use the actual baseline Agent binary with the original handshake
            # and chat command against this candidate's protected endpoint.
            legacy = subprocess.run([
                sys.argv[4], "-mode", "interactive", "-url",
                origin.replace("https", "wss", 1) + "/api/v1/agent/connect",
                "-room", "demo", "-id", "fixture-agent",
            ], env={"PATH": os.environ["PATH"], "SSL_CERT_FILE": cert, "GOOSECAB_AGENT_TOKEN": token(3)},
                input="chat baseline client message\n", capture_output=True, text=True,
                timeout=10, check=True)
            seen = [json.loads(line) for line in legacy.stdout.splitlines()]
            assert any(row.get("event_id") == event["event_id"] for row in seen)
            agent_event = next(row for row in seen if isinstance(row.get("payload"), dict)
                               and row["payload"].get("text") == "baseline client message")
            assert agent_event["actor"] == {"kind": "agent", "id": "fixture-agent"}
        finally:
            stop(process)


        # Dropping the protected configuration with this same SQLite file
        # must stop the binary before it starts serving.
        failed = subprocess.run([sys.argv[1], "-addr", addr], env={
            "PATH": os.environ["PATH"], "GOOSECAB_DB": str(db_file),
            "GOOSECAB_AGENT_TOKEN": "synthetic-browser-legacy",
        }, capture_output=True, text=True, timeout=10)
        assert failed.returncode != 0
        assert "protected room missing" in failed.stderr

        # The additive schema retains the old SQLite columns and committed IDs.
        with sqlite3.connect(db_file) as database:
            records = database.execute(
                "SELECT event_id FROM events WHERE room_id='demo' ORDER BY seq").fetchall()
        assert records[0][0] == "prior-1"
        assert records[-2][0] == event["event_id"]
        assert records[-1][0] == agent_event["event_id"]

        policy["rooms"][0]["read_only"] = True
        policy_file.write_text(json.dumps(policy))
        process = start(sys.argv[1], backend, addr, db_file, policy_file)
        try:
            request = Request(origin + "/api/room-chat/unread?room_id=demo",
                              headers={"Authorization": "GooseCabRoom " + token(2)})
            with urlopen(request, context=crypto, timeout=2) as response:
                assert json.load(response)["unread_count"] == 0
            read = Request(origin + "/api/events?room_id=demo",
                           headers={"Authorization": "GooseCabRoom " + token(2)})
            with urlopen(read, context=crypto, timeout=2) as response:
                assert [row["event_id"] for row in json.load(response)["events"]][-2:] == [
                    event["event_id"], agent_event["event_id"]]
            blocked = Request(origin + "/api/events?room_id=demo",
                              headers={"Authorization": "Bearer synthetic-browser-legacy"})
            try:
                urlopen(blocked, context=crypto, timeout=2)
            except HTTPError as err:
                assert err.code == 401
            else:
                raise AssertionError("rollback returned history to the shared credential")
        finally:
            stop(process)

print("PASS: HTTPS browser paging, plain-text posting and export, room isolation, baseline Agent WS replay and publish, real Chat MCP receipt, and protected read-only restart")
