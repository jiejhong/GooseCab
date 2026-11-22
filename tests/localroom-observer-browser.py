import base64
import hashlib
import json
import re
import sqlite3
import subprocess
import sys
import tempfile
from datetime import datetime, timedelta, timezone
from pathlib import Path
from playwright.sync_api import sync_playwright, expect

def token(num):
    return base64.urlsafe_b64encode(bytes([num]) * 32).decode().rstrip("=")

with tempfile.TemporaryDirectory(prefix="goosecab-browser-fixture-") as work:
    work = Path(work)
    policy = work / 'synthetic-policy.json'
    state = work / 'private-state'
    now = datetime.now(timezone.utc)
    expires = (now + timedelta(minutes=5)).isoformat()
    policy.write_text(json.dumps({
        'enabled': True, 'room_id': 'synthetic-browser-room',
        'participants': [
            {'kind': kind, 'id': ident, 'display_name': '测试 ' + ident, 'token_sha256': hashlib.sha256(token(index).encode()).hexdigest(),
             'expires_at': expires, 'read': True, 'write': writable, 'broadcast': index == 1}
            for index, kind, ident, writable in [
                (1, 'user', 'fixture-writer', True),
                (2, 'user', 'fixture-reader', False),
                (3, 'agent', 'fixture-agent', False),
                (4, 'user', 'ordinary-writer', True),
            ] + [(i, 'user', f'page-fixture-{i}', True) for i in range(10, 32)]
        ]
    }))
    policy.chmod(0o600)

    def start():
        service = subprocess.Popen([sys.argv[1], '-config', str(policy), '-state', str(state), '-port', '0'],
                                   stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        address = re.search(r'http://127\.0\.0\.1:\d+', service.stdout.readline())
        if address is None:
            service.kill()
            service.wait(timeout=8)
            raise AssertionError('independent loopback server did not start')
        return service, address.group()

    def stop(service):
        if service.poll() is None:
            service.terminate()
        assert service.wait(timeout=8) == 0

    service, address = start()
    try:
        with sync_playwright() as p:
            browser = p.chromium.launch(executable_path='/usr/bin/chromium', headless=True, args=[
                '--no-sandbox', '--disable-background-networking'])
            try:
                context = browser.new_context()
                active_address = address
                context.route('**/*', lambda route: route.continue_() if
                              route.request.url.startswith(active_address + '/') else route.abort())
                page = context.new_page()
                page_errors = []
                page.on('pageerror', lambda err: page_errors.append(err))
                page.goto(address)
                expect(page.get_by_role('heading', level=1)).to_have_text('GooseCab 本地房间')
                expect(page.locator('#broadcast')).to_have_count(0)
                scenario = [None]

                def intercepted_history(route):
                    if scenario[0] is None:
                        route.continue_()
                        return
                    actual = route.fetch()
                    body = actual.json()
                    body.update(scenario[0])
                    route.fulfill(response=actual, json=body)

                page.route('**/api/events', intercepted_history)

                def login(index):
                    page.get_by_label('房间访问令牌').fill(token(index))
                    page.get_by_role('button', name='进入房间').click()

                login(1)
                expect(page.get_by_role('heading', level=1)).to_have_text('GooseCab · synthetic-browser-room')
                expect(page.locator('#room-status')).to_contain_text('测试 fixture-writer（人类 fixture-writer）')
                expect(page.locator('#access-token')).to_have_value('')
                forbidden_markup = '<img src=x onerror="document.body.dataset.ran=1">'
                page.get_by_label('发言', exact=True).fill(forbidden_markup)
                with page.expect_response(lambda response: response.url == address + '/api/messages') as sent:
                    page.get_by_role('button', name='发送').click()
                assert sent.value.status == 201
                human_event = sent.value.json()
                assert human_event['actor'] == {'kind': 'human', 'id': 'fixture-writer'}
                spoofed = context.request.post(address + '/api/messages',
                    headers={'Authorization': 'GooseCabLocal ' + token(1), 'Origin': address},
                    data={'text': 'spoofed author', 'actor': {'kind': 'human', 'id': 'ordinary-writer'}})
                assert spoofed.status == 400
                expect(page.locator('#send-status')).to_contain_text('已保存发言 #1')
                expect(page.get_by_text(forbidden_markup, exact=True)).to_be_visible()
                expect(page.locator('#events img')).to_have_count(0)
                expect(page.locator('#events li').first).to_contain_text('测试 fixture-writer（人类 fixture-writer）')
                assert page.evaluate("document.body.dataset.ran") is None

                page.get_by_label('发言', exact=True).fill('fixture direct mention')
                page.get_by_label('点名对象 ID').fill('fixture-agent')
                page.get_by_role('button', name='发送').click()
                expect(page.locator('#send-status')).to_contain_text('已保存发言 #2')
                expect(page.locator('#events li').first).to_contain_text('点名 Agent fixture-agent')
                page.get_by_label('发言', exact=True).fill('fixture room mention')
                page.get_by_label('点名本房间所有人').check()
                page.get_by_role('button', name='发送').click()
                expect(page.locator('#send-status')).to_contain_text('已保存发言 #3')
                expect(page.locator('#events li').first).to_contain_text('全员点名')
                expect(page.locator('#mention-count')).to_have_text('未读点名 0 条')
                assert not context.cookies()
                assert page.evaluate('localStorage.length + sessionStorage.length') == 0

                page.get_by_role('button', name='退出房间').click()
                expect(page.locator('#events li')).to_have_count(0)
                expect(page.locator('#compose')).to_be_hidden()
                login(2)
                expect(page.locator('#room-status')).to_contain_text('只读')
                expect(page.locator('#compose')).to_be_hidden()
                expect(page.locator('#events li')).to_have_count(3)
                expect(page.locator('#mention-count')).to_have_text('未读点名 1 条')
                expect(page.locator('#mention-events li')).to_have_count(1)
                expect(page.locator('#mention-events li').first).to_contain_text('全员点名')
                page.get_by_role('button', name='标记本页已读').click()
                expect(page.locator('#mention-count')).to_have_text('未读点名 0 条')
                page.reload()
                expect(page.locator('#events li')).to_have_count(0)
                login(3)
                expect(page.locator('#room-status')).to_contain_text('访问被拒绝')

                stop(service)
                # Load the format persisted by the earlier v2 candidate, with
                # the same event ID and its original author kind.
                path = state / 'room.gcdb'
                marker, raw_state = path.read_bytes().split(b'\n', 1)
                assert marker == b'GooseCab LocalRoom v2'
                durable = json.loads(raw_state)
                assert durable['events'][0]['event_id'] == human_event['event_id']
                assert durable['events'][0]['actor'] == human_event['actor']
                durable['events'][0]['actor']['kind'] = 'user'
                legacy_v2 = marker + b'\n' + json.dumps(durable).encode()
                path.write_bytes(legacy_v2)

                service, address = start()
                active_address = address
                page.goto(address)
                login(1)
                expect(page.locator('#events li')).to_have_count(3)
                expect(page.locator('#events li').first).to_contain_text('全员点名')
                expect(page.locator('#events li').last).to_contain_text('测试 fixture-writer（人类 fixture-writer）')
                assert path.read_bytes() == legacy_v2
                page.get_by_role('button', name='退出房间').click()
                login(2)
                expect(page.locator('#mention-count')).to_have_text('未读点名 0 条')
                # Switch away from the room owner to a writer without broadcast.
                page.get_by_role('button', name='退出房间').click()
                expect(page.locator('#broadcast')).to_have_count(0)
                login(4)
                expect(page.locator('#compose')).to_be_visible()
                expect(page.locator('#broadcast')).to_have_count(0)
                denied = context.request.post(address + '/api/messages',
                    headers={'Authorization': 'GooseCabLocal ' + token(4), 'Origin': address},
                    data={'text': 'must not broadcast', 'mention_all': True})
                assert denied.status == 403
                for index in range(10, 32):
                    stamped = context.request.post(address + '/api/messages',
                        headers={'Authorization': 'GooseCabLocal ' + token(index), 'Origin': address},
                        data={'text': f'page message {index}'})
                    assert stamped.status == 201

                expect(page.locator('#events li')).to_have_count(20)
                expect(page.locator('#events li').first).to_contain_text('page message 31')
                with page.expect_response(lambda response: '/api/events?before=' in response.url) as older:
                    page.get_by_role('button', name='查看更早发言').click()
                old_event = older.value.json()['events'][-1]
                assert old_event['event_id'] == human_event['event_id']
                assert old_event['actor'] == human_event['actor']
                expect(page.locator('#events li')).to_have_count(5)
                expect(page.locator('#events li').first).to_contain_text('page message 11')
                expect(page.locator('#events li').last).to_contain_text(forbidden_markup)
                expect(page.locator('#mention-count')).to_have_text('未读点名 1 条')
                page.get_by_role('button', name='返回最新发言').click()
                expect(page.locator('#events li').first).to_contain_text('page message 31')
                page.get_by_role('button', name='退出房间').click()
                login(1)
                expect(page.locator('#broadcast')).to_have_count(1)
                page.get_by_label('点名本房间所有人').check()
                page.get_by_label('发言', exact=True).fill('unsent broadcast draft')
                scenario[0] = {'can_broadcast': False}
                expect(page.locator('#broadcast')).to_have_count(0)
                expect(page.get_by_label('发言', exact=True)).to_have_value('')
                assert not page.locator('#mention-id').is_disabled()
                scenario[0] = {'room_id': 'changed-room'}
                expect(page.get_by_role('button', name='进入房间')).to_be_visible()
                expect(page.locator('#broadcast')).to_have_count(0)
                expect(page.locator('#events li')).to_have_count(0)
                expect(page.locator('#mention-events li')).to_have_count(0)
                assert page_errors == []
            finally:
                browser.close()
    finally:
        stop(service)

    data_path = state / 'room.gcdb'
    before = data_path.read_bytes()
    try:
        with sqlite3.connect(f'file:{data_path}?mode=rw', uri=True) as legacy:
            legacy.execute('PRAGMA busy_timeout=5000')
            legacy.execute('PRAGMA journal_mode=WAL').fetchall()
            legacy.execute('SELECT name FROM sqlite_master').fetchall()
    except sqlite3.DatabaseError as err:
        assert 'not a database' in str(err)
    else:
        raise AssertionError('SQLite read the independent room file')
    assert data_path.read_bytes() == before

print('PASS: human actor via browser, spoof rejection, old v2 history and rendering, negative broadcast UI and request, permission loss and room change cleanup, stable browser paging, real browser login, plain text, direct and granted broadcast mentions, acknowledgement and read persistence, writer receipt, read-only session, logout/reload, denied Agent browser login, restart persistence, SQLite format isolation')
