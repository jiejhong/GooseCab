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
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit
from urllib.request import urlopen

from playwright.sync_api import Error, expect, sync_playwright


LEGACY_ROOM = "demo"
LEGACY_AGENT = "fixture-legacy-agent"
PROTECTED_ROOM = "protected-fixture"
LEGACY_EVENT = "legacy-fixture-event"
LEGACY_TEXT = "synthetic legacy history"
LEGACY_REPLY = "synthetic legacy reply"
PROTECTED_TEXT = "synthetic protected browser message"
INLINE_TEXT = "synthetic inline demo message"


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
            return frame
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


def browse(playwright, browser_home, executable, ca, origin, token, agent, frames, demo_compat):
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

        sign_in("other-fixture" if demo_compat else LEGACY_ROOM)
        expect(page.locator("#status")).to_contain_text("房间授权已失效")
        target_room = LEGACY_ROOM if demo_compat else PROTECTED_ROOM
        sign_in(target_room)
        expect(page.locator("#status")).to_contain_text("fixture-writer")
        expect(page.locator("#room-online")).to_have_text("在线 Agent（WebSocket） · " + (LEGACY_AGENT if demo_compat else "无"))
        expect(page.locator("#recent-read-status")).to_have_text("最近 5 分钟已认证的读取")
        expect(page.locator("#room-recent-reads")).to_contain_text("fixture-writer · 人类")
        if demo_compat:
            expect(page.locator("#legacy-audience")).to_be_visible()
            page.get_by_label("点名对象", exact=False).fill(LEGACY_AGENT)
        else:
            expect(page.locator("#legacy-audience")).to_be_hidden()
        page.get_by_label("发言", exact=True).fill(PROTECTED_TEXT)
        with page.expect_response(lambda result: "/api/room-chat/messages?" in result.url) as posted:
            page.get_by_role("button", name="发送").click()
        assert posted.value.status == 201
        event = posted.value.json()
        assert event["room_id"] == target_room and event["actor"] == {
            "id": "fixture-writer", "kind": "human",
        }, event
        expect(page.locator("#events")).to_contain_text(PROTECTED_TEXT)
        if not demo_compat:
            expect(page.locator("#events")).not_to_contain_text(LEGACY_TEXT)
        with page.expect_download() as exported:
            page.get_by_role("button", name="导出历史").click()
        private_export = exported.value.path().read_text(encoding="utf-8")
        assert PROTECTED_TEXT in private_export
        assert (LEGACY_TEXT in private_export) == demo_compat
        if demo_compat:
            legacy_event = wait_for_legacy_frame(frames, lambda frame: frame.get("event_id") == event["event_id"])
            assert legacy_event["payload"] == {"text": PROTECTED_TEXT}, legacy_event

        # Keep the browser session and the originally connected WS Agent alive
        # together. The Agent replies to its legacy room using its old CLI.
        agent.stdin.write(f"chat {LEGACY_REPLY}\n")
        agent.stdin.flush()
        wait_for_legacy_frame(frames, lambda frame: frame.get("payload", {}).get("text") == LEGACY_REPLY)
        with page.expect_response(lambda result: "/api/room-chat/history?" in result.url) as refreshed:
            page.get_by_role("button", name="返回最新").click()
        assert refreshed.value.ok
        events = refreshed.value.json()["events"]
        if demo_compat:
            posted = next(row for row in events if row["event_id"] == event["event_id"])
            assert posted["payload"] == {"text": PROTECTED_TEXT}, posted
            assert any(row.get("payload", {}).get("text") == LEGACY_REPLY for row in events)
        else:
            assert len(events) == 1 and events[0]["event_id"] == event["event_id"], events
            expect(page.locator("#events")).not_to_contain_text(LEGACY_REPLY)
        return event


def read_agent_page(binary, address, ca, token, after):
    command = [str(binary.resolve()), "-gateway-room-chat", "-url",
               f"wss://{address}/api/room-chat/agent", "-room", LEGACY_ROOM, "-agent", LEGACY_AGENT]
    initialize = {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}}
    read = {"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {
        "name": "room_read", "arguments": {"after_seq": after, "limit": 1}}}
    process = subprocess.run(command, env={"PATH": os.environ["PATH"], "SSL_CERT_FILE": str(ca),
                                           "GOOSECAB_ROOM_CHAT_AGENT_TOKEN": token},
                             input=json.dumps(initialize) + "\n" + json.dumps(read) + "\n",
                             capture_output=True, text=True, timeout=10, check=True)
    reply = json.loads(process.stdout.splitlines()[1])["result"]
    assert not reply.get("isError"), reply
    return json.loads(reply["content"][0]["text"])


def exercise_allowlisted_writer(playwright, executable, ca, origin):
    browser_home = ca.parent / "browser-home"
    nss_dir = browser_home / ".pki" / "nssdb"
    nss_dir.mkdir(parents=True)
    nss_database = f"sql:{nss_dir}"
    run("certutil", "-N", "-d", nss_database, "--empty-password")
    run("certutil", "-A", "-d", nss_database, "-t", "C,,", "-n", "GooseCab synthetic test CA", "-i", str(ca))
    with launch_chromium(playwright, browser_home, executable) as browser:
        context = browser.new_context(ignore_https_errors=False)
        context.route("**/*", lambda route: route.continue_()
                      if route.request.url.startswith(origin + "/") else route.abort())
        page = context.new_page()
        response = page.goto(origin + "/demo-write", timeout=8000)
        assert response is not None and response.ok
        expect(page.get_by_role("heading", name="demo 房间发言")).to_be_visible()
        expect(page.locator('input[type="password"]')).to_have_count(0)
        expect(page.get_by_text("点名", exact=False)).to_have_count(0)
        page.get_by_label("发言", exact=True).fill("synthetic allowlisted browser message")
        with page.expect_response(lambda result: result.url == origin + "/api/demo-write/messages") as sent:
            page.get_by_role("button", name="发送").click()
        assert sent.value.status == 201 and "authorization" not in sent.value.request.all_headers()
        event = sent.value.json()
        assert event["room_id"] == LEGACY_ROOM and event["actor"] == {"kind": "guest", "id": "白名单访客"}, event
        expect(page.get_by_role("status")).to_contain_text("已发送")
        time.sleep(0.5)  # The same writer also serves the standalone page.
        inline = exercise_inline_writer(browser, context, origin)
        return event, inline


def exercise_inline_writer(browser, context, origin):
    denied = browser.new_context(ignore_https_errors=False)
    denied.route(origin + "/demo-write.js", lambda route: route.fulfill(status=403))
    denied_page = denied.new_page()
    with denied_page.expect_response(lambda res: res.url == origin + "/demo-write.js") as blocked:
        denied_page.goto(origin + "/?room_id=demo")
    assert blocked.value.status == 403
    expect(denied_page.locator("#demo-compose")).to_be_hidden()
    denied.close()

    page = context.new_page()

    def small_history_page(route):
        url = urlsplit(route.request.url)
        query = dict(parse_qsl(url.query))
        query["limit"] = "1"
        route.continue_(url=urlunsplit(url._replace(query=urlencode(query))))

    page.route(origin + "/api/events?*", small_history_page)
    page.goto(origin + "/?room_id=demo")
    form = page.locator("#demo-compose")
    expect(form).to_be_visible()
    expect(form.get_by_text("白名单访客", exact=False)).to_be_visible()
    expect(page.locator(".presence")).to_contain_text("在线 Agent 1（WebSocket）")
    more = page.get_by_role("button", name="加载更早记录")
    more.click()
    expect(page.locator("#shown")).to_have_text("2")

    posted = []
    write_url = origin + "/api/demo-write/messages"
    page.on("request", lambda req: posted.append(req.post_data_json) if req.url == write_url else None)
    page.evaluate("""() => {
        const fetchOriginal = window.fetch;
        window.fetch = async (...args) => {
            const response = await fetchOriginal(...args);
            if (args[0] === '/api/demo-write/messages') {
                window.fetch = fetchOriginal;
                throw new TypeError('synthetic lost receipt after commit');
            }
            return response;
        };
    }""")
    draft = form.get_by_label("发言", exact=True)
    draft.fill(INLINE_TEXT)
    form.evaluate("element => { element.requestSubmit(); element.requestSubmit(); }")
    expect(form.get_by_role("status")).to_contain_text("草稿已保留")
    expect(draft).to_have_value(INLINE_TEXT)
    assert len(posted) == 1, posted

    room = page.get_by_label("房间 ID")
    room.fill("other")
    expect(form).to_be_hidden()
    page.get_by_role("button", name="刷新记录").click()
    expect(page.locator("#room-name")).to_have_text("other")
    expect(form).to_be_hidden()
    room.fill(LEGACY_ROOM)
    expect(form).to_be_hidden()
    page.get_by_role("button", name="刷新记录").click()
    expect(form).to_be_visible()
    expect(draft).to_have_value(INLINE_TEXT)
    more.click()
    expect(page.locator("#shown")).to_have_text("2")
    with page.expect_response(lambda res: res.url == write_url) as retried:
        form.get_by_role("button", name="发送").click()
    event = retried.value.json()
    assert retried.value.status == 201 and event["actor"] == {"kind": "guest", "id": "白名单访客"}, event
    assert event["room_id"] == LEGACY_ROOM and len(posted) == 2 and posted[0] == posted[1], posted
    expect(draft).to_have_value("")
    expect(form.get_by_role("status")).to_contain_text("已发送")
    expect(page.locator("#events").get_by_text(INLINE_TEXT, exact=True)).to_have_count(1)
    expect(page.locator("#shown")).to_have_text("2")
    return event


def check_allowlisted_writer(args, ca, certificate, key):
    fixture_env = {**os.environ, "GOOSECAB_TEST_DEMO_CERT": str(certificate), "GOOSECAB_TEST_DEMO_KEY": str(key)}
    command = [str(args.allowlisted_demo_fixture.resolve()), "-test.run=^TestDemoWriterBrowserFixture$", "-test.timeout=45s"]
    with test_process(command, fixture_env, stdin=subprocess.PIPE, stdout=subprocess.PIPE) as fixture:
        try:
            origin = fixture.stdout.readline().strip()
            backend = fixture.stdout.readline().strip()
            assert origin.startswith("https://127.0.0.1:") and backend.startswith("http://127.0.0.1:")
            with test_process([str(args.baseline_agent.resolve()), "-mode", "interactive", "-room", LEGACY_ROOM,
                               "-url", backend.replace("http://", "ws://", 1) + "/api/v1/agent/connect",
                               "-id", LEGACY_AGENT], {"PATH": os.environ["PATH"]},
                              stdin=subprocess.PIPE, stdout=subprocess.PIPE) as agent:
                frames = observe_agent(agent)
                wait_for_legacy_frame(frames, lambda frame: frame.get("event_id") == "before-a")
                with sync_playwright() as playwright:
                    standalone, inline = exercise_allowlisted_writer(playwright, args.chromium, ca, origin)
                for written, body in [(standalone, "synthetic allowlisted browser message"), (inline, INLINE_TEXT)]:
                    live = wait_for_legacy_frame(frames, lambda frame: frame.get("event_id") == written["event_id"])
                    assert live["payload"] == {"text": body}
                agent.stdin.write(f"chat {LEGACY_REPLY}\n")
                agent.stdin.flush()
                wait_for_legacy_frame(frames, lambda frame: frame.get("payload", {}).get("text") == LEGACY_REPLY)
                history = legacy_history(f"{backend}/api/events/export.html?room_id={LEGACY_ROOM}")
                assert "prior conversation" in history and "synthetic allowlisted browser message" in history
                events = json.loads(legacy_history(f"{backend}/api/events?room_id={LEGACY_ROOM}"))["events"]
                assert [row["event_id"] for row in events if row.get("payload", {}).get("text") == INLINE_TEXT] == [inline["event_id"]]
        finally:
            fixture.stdin.close()
            assert fixture.wait(timeout=10) == 0
    print("PASS: Chromium exercised inline and standalone allowlisted posting, history paging, room switching, lost-receipt retry and one committed event. The old Agent and export continued in demo.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("gateway", type=Path)
    parser.add_argument("baseline_agent", type=Path)
    parser.add_argument("--chromium", help="Use a local executable instead of Playwright's Chromium")
    parser.add_argument("--demo-chat-mcp", type=Path, help="Test explicit demo compatibility with this Chat MCP binary")
    parser.add_argument("--allowlisted-demo-fixture", type=Path, help="Exercise the Unix-backed demo writer fixture")
    args = parser.parse_args()
    if args.demo_chat_mcp and args.allowlisted_demo_fixture:
        parser.error("select exactly one demo mode")
    demo_compat = args.demo_chat_mcp is not None
    with tempfile.TemporaryDirectory(prefix="goosecab-native-browser-") as tmp:
        root = Path(tmp)
        ca, certificate, key = create_test_pki(root)
        if args.allowlisted_demo_fixture:
            check_allowlisted_writer(args, ca, certificate, key)
            return
        database, config = root / "fixture.db", root / "room-policy.json"
        seed_legacy_history(database)
        with socket.socket() as reserved:
            reserved.bind(("127.0.0.1", 0))
            address = f"127.0.0.1:{reserved.getsockname()[1]}"
        http_origin, https_origin = "http://" + address, "https://" + address
        token = base64.urlsafe_b64encode(os.urandom(32)).decode().rstrip("=")
        expires = (datetime.now(timezone.utc) + timedelta(minutes=5)).isoformat()
        room = {"id": LEGACY_ROOM if demo_compat else PROTECTED_ROOM, "humans": [{
            "id": "fixture-writer", "write": True, "token_sha256": hashlib.sha256(token.encode()).hexdigest(),
            "expires_at": expires,
        }]}
        if demo_compat:
            agent_token = base64.urlsafe_b64encode(os.urandom(32)).decode().rstrip("=")
            room["legacy_readers_may_read_new_messages"] = True
            room["agents"] = [{"id": LEGACY_AGENT, "expires_at": expires,
                               "token_sha256": hashlib.sha256(agent_token.encode()).hexdigest()}]
        config.write_text(json.dumps({"origin": https_origin, "rooms": [room]}))
        config.chmod(0o600)
        child_env = {"PATH": os.environ["PATH"]}
        with test_process([str(args.gateway.resolve()), "-addr", address,
                           "-room-chat-config", str(config), "-room-chat-tls-cert", str(certificate),
                           "-room-chat-tls-key", str(key)],
                          {**child_env, "GOOSECAB_DB": str(database)}) as gateway:
            wait_for_gateway(gateway, http_origin)
            with test_process([str(args.baseline_agent.resolve()), "-mode", "interactive", "-room", LEGACY_ROOM,
                               "-url", "ws://" + address + "/api/v1/agent/connect", "-id", LEGACY_AGENT],
                              child_env, stdin=subprocess.PIPE, stdout=subprocess.PIPE) as agent:
                frames = observe_agent(agent)
                wait_for_legacy_frame(frames, lambda frame: frame.get("event_id") == LEGACY_EVENT)
                with sync_playwright() as playwright:
                    written = browse(playwright, root / "browser-home", args.chromium, ca,
                                     https_origin, token, agent, frames, demo_compat)
                for route in ("/api/events", "/api/events/export.html"):
                    public = legacy_history(f"{http_origin}{route}?room_id={LEGACY_ROOM}")
                    assert LEGACY_TEXT in public and LEGACY_REPLY in public
                    assert (PROTECTED_TEXT in public) == demo_compat
                if demo_compat:
                    disclosed = next(row for row in json.loads(legacy_history(
                        f"{http_origin}/api/events?room_id={LEGACY_ROOM}"))["events"]
                        if row["event_id"] == written["event_id"])
                    assert disclosed["payload"] == {"text": PROTECTED_TEXT}, disclosed
                    page = read_agent_page(args.demo_chat_mcp, address, ca, agent_token, written["seq"] - 1)
                    assert page["private_mention_event_ids"] == [written["event_id"]], page
                    assert page["mention_receipt"] == "pending_confirmation", page
                    page_after = read_agent_page(args.demo_chat_mcp, address, ca, agent_token, written["seq"] - 1)
                    assert page_after["events"][0]["event_id"] == written["event_id"]
                    assert "private_mention_event_ids" not in page_after and "mention_receipt" not in page_after, page_after
    if demo_compat:
        print("PASS: Chromium used verified TLS for the original demo; legacy live/history/export got plain text. "
              "Only the separately authenticated Chat MCP obtained a personal mention and acknowledged it.")
    else:
        print("PASS: Chromium rejected the untrusted server, accepted its synthetic CA, and wrote to the protected room. "
              "The old Agent, replay, and anonymous history/export stayed in the legacy room on the same listener.")


if __name__ == "__main__":
    main()
