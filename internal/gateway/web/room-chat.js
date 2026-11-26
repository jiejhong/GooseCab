'use strict';

const el = id => document.getElementById(id);
const login = el('login'), compose = el('compose'), token = el('token');
const message = el('message'), target = el('target'), kind = el('kind');
const status = el('status'), broadcastControls = el('broadcast-controls');
let session;

function leave(note) {
  if (session) {
    session.abort.abort();
    clearTimeout(session.timer);
  }
  session = undefined;
  token.value = message.value = target.value = '';
  kind.value = 'agent';
  broadcastControls.replaceChildren();
  el('private-mentions').hidden = true;
  el('private-notices').replaceChildren();
  setReply(undefined);
  el('room-online').textContent = '';
  el('recent-read-status').textContent = '';
  el('room-recent-reads').replaceChildren();
  el('mentions').replaceChildren();
  el('events').replaceChildren();
  el('legacy-audience').hidden = true;
  el('room-view').hidden = true;
  el('logout').hidden = true;
  login.hidden = false;
  status.textContent = note;
}

async function request(active, path, params, body) {
  const query = new URLSearchParams({room_id: active.room, ...params});
  const headers = {Authorization: 'GooseCabRoom ' + active.token};
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  const response = await fetch(path + '?' + query, {
    method: body === undefined ? 'GET' : 'POST', headers, body: body === undefined ? undefined : JSON.stringify(body),
    mode: 'same-origin', credentials: 'omit', cache: 'no-store', redirect: 'error', signal: active.abort.signal
  });
  if (session !== active) throw new Error('会话已结束。');
  if (response.status === 401) leave('房间授权已失效。');
  if (!response.ok) throw new Error(response.status === 403 ? '缺少本房间所需权限。' : '请求未确认。');
  return response;
}

async function api(active, path, params = {}, body) {
  const response = await request(active, '/api/room-chat/' + path, params, body);
  if (response.status !== 204) return response.json();
}

function showEvents(id, events) {
  const list = document.createDocumentFragment();
  for (const event of events) {
    const row = document.createElement('li');
    const payload = event.payload || {};
    const actor = event.actor || {};
    const author = payload.author_display_name || actor.id || '系统';
    const body = typeof payload === 'string' ? payload : (payload.text || payload.message || event.type);
    const mention = payload.mention_all ? ' · 全员点名' : payload.mention ? ' · 点名 ' + payload.mention.id : '';
    row.textContent = author + (actor.id && author !== actor.id ? '（' + actor.id + '）' : '') +
      ' · #' + event.seq + ' · ' + String(body) + mention;
    if (id === 'events' && session?.trackMentions && session.actor.id === actor.id && session.actor.kind === actor.kind &&
        /^[0-9a-f-]{36}$/.test(event.event_id)) {
      const active = session;
      const check = document.createElement('button');
      check.type = 'button';
      check.textContent = '查看点名回执';
      const view = document.createElement('span');
      view.dataset.mentionStatus = event.event_id;
      view.textContent = active.receipts.get(event.event_id) || '';
      check.addEventListener('click', async () => {
        try {
          const {recipients} = await api(active, 'mentions/status', {event_id: event.event_id});
          if (session !== active) return;
          const labels = {pending: '待送达', received: '已收到', replied: '已回复'};
          view.textContent = recipients.length ? recipients.map(result =>
            result.recipient.id + '（' + (result.recipient.kind === 'agent' ? 'Agent' : '人类') + '）' + labels[result.state]).join('、') : '无点名回执';
          active.receipts.set(event.event_id, view.textContent);
          const current = el('events').querySelector('[data-mention-status="' + event.event_id + '"]');
          if (current) current.textContent = view.textContent;
        } catch (error) {
          if (session === active) view.textContent = error.message;
        }
      });
      row.append(' ', check, ' ', view);
    }
    list.append(row);
  }
  el(id).replaceChildren(list);
}

function setReply(id) {
  if (session) session.replyTo = id;
  el('replying').hidden = el('cancel-reply').hidden = !id;
  el('replying').textContent = id ? '回复点名 ' + id : '';
}

el('cancel-reply').addEventListener('click', () => setReply(undefined));

async function privateMentions(active) {
  if (!active.trackMentions) return;
  const list = el('private-notices');
  try {
    const {items} = await api(active, 'mentions');
    if (session !== active) return;
    const rows = document.createDocumentFragment();
    for (const {event, proof} of items) {
      const row = document.createElement('li');
      row.textContent = (event.actor?.id || '成员') + ' · ' + String(event.payload?.text || '');
      const controls = active.write ? [['确认收到', false], ['确认收到并回复', true]] : [['确认收到', false]];
      for (const [label, reply] of controls) {
        const button = document.createElement('button');
        button.type = 'button';
        button.textContent = label;
        button.disabled = !active.ack;
        button.addEventListener('click', async () => {
          button.disabled = true;
          try {
            await api(active, 'mentions/ack', {}, {event_id: event.event_id, proof});
            if (session !== active) return;
            if (reply) {
              setReply(event.event_id);
              message.focus();
            }
            await privateMentions(active);
          } catch (error) {
            if (session === active) status.textContent = error.message;
          } finally {
            button.disabled = !active.ack;
          }
        });
        row.append(' ', button);
      }
      rows.append(row);
    }
    list.replaceChildren(rows);
  } catch (error) {
    if (session === active) status.textContent = error.message;
  }
}

async function history(active, before, passive = false) {
  if (active.loading) return;
  active.loading = true;
  const params = {order: 'desc', limit: 20};
  if (before) {
    params.before_seq = before.next_seq;
    params.before_cursor = before.next_cursor;
    params.through_seq = before.snapshot_seq;
    params.through_cursor = before.snapshot_cursor;
  }
  try {
    const page = await api(active, 'history', params);
    if (session !== active || (passive && active.older)) return;
    active.older = !!before;
    active.next = page.has_more ? page : null;
    el('older').disabled = !active.next;
    showEvents('events', page.events);
  } catch (error) {
    if (session === active) status.textContent = error.message;
  } finally {
    active.loading = false;
  }
}

async function mentions(active) {
  try {
    const page = await api(active, 'unread');
    if (session !== active) return;
    active.visible = page.events.map(event => event.event_id);
    el('unread-count').textContent = String(page.unread_count);
    el('ack').disabled = !active.ack || !active.visible.length;
    showEvents('mentions', page.events);
  } catch (error) {
    if (session === active) status.textContent = error.message;
  }
}

async function activity(active) {
  try {
    const {online_agents: agents, recent_reads: reads} = await api(active, 'activity');
    if (session !== active) return;
    const rows = document.createDocumentFragment();
    for (const {actor, at} of reads) {
      const row = document.createElement('li');
      row.textContent = actor.id + ' · ' + (actor.kind === 'agent' ? 'Agent' : '人类') + ' · ' +
        new Date(at).toLocaleString('zh-CN', {hour12: false});
      rows.append(row);
    }
    el('room-online').textContent = '在线 Agent（WebSocket）' + (agents.length ? ' · ' + agents.join('、') : ' · 无');
    el('recent-read-status').textContent = reads.length ? '最近 5 分钟已认证的读取' : '最近 5 分钟暂无已认证的读取';
    el('room-recent-reads').replaceChildren(rows);
  } catch {
    if (session !== active) return;
    el('room-online').textContent = '连接与读取状态暂不可用。';
    el('recent-read-status').textContent = '';
    el('room-recent-reads').replaceChildren();
  }
}

function poll(active) {
  active.timer = setTimeout(async () => {
    if (session !== active) return;
    if (!active.older) await history(active, null, true);
    if (session === active) await mentions(active);
    if (session === active) await activity(active);
    if (session === active) await privateMentions(active);
    if (session === active) poll(active);
  }, 5000);
}

login.addEventListener('submit', async event => {
  event.preventDefault();
  const active = {room: el('room').value.trim(), token: token.value.trim(), abort: new AbortController()};
  if (session) leave('');
  token.value = '';
  session = active;
  try {
    const user = await api(active, 'session');
    if (session !== active) return;
    login.hidden = true;
    el('logout').hidden = el('room-view').hidden = false;
    compose.hidden = !user.write;
    el('legacy-audience').hidden = !user.legacy_audience;
    active.ack = user.ack;
    active.actor = user.actor;
    active.trackMentions = user.track_mentions;
    active.write = user.write;
    active.receipts = new Map();
    el('private-mentions').hidden = !active.trackMentions;
    el('send').disabled = false;
    broadcastControls.replaceChildren();
    if (user.broadcast) {
      const label = document.createElement('label');
      const toggle = document.createElement('input');
      toggle.type = 'checkbox';
      label.append(toggle, ' 全员点名');
      broadcastControls.append(label);
      active.broadcast = toggle;
    }
    status.textContent = active.room + ' · 人类 ' + (user.display_name || user.actor.id) + (user.write ? '' : ' · 只读');
    await history(active, null);
    if (session === active) await mentions(active);
    if (session === active) await activity(active);
    if (session === active) await privateMentions(active);
    if (session === active) poll(active);
  } catch (error) {
    if (session === active) leave(error.message);
  }
});

el('logout').addEventListener('click', () => leave('已退出房间。'));
window.addEventListener('pagehide', () => leave(''));
el('recent').addEventListener('click', () => { if (session) history(session, null); });
el('older').addEventListener('click', () => { if (session?.next) history(session, session.next); });
el('export').addEventListener('click', async () => {
  const active = session;
  if (!active) return;
  try {
    const response = await request(active, '/api/room-chat/export.html', {});
    const data = await response.blob();
    if (session !== active) return;
    const url = URL.createObjectURL(data);
    const link = document.createElement('a');
    link.href = url;
    link.download = 'goosecab-history.html';
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  } catch (error) {
    if (session === active) status.textContent = error.message;
  }
});
el('ack').addEventListener('click', async () => {
  const active = session;
  if (!active?.visible?.length) return;
  const ids = [...active.visible];
  el('ack').disabled = true;
  try {
    await api(active, 'unread/ack', {}, {event_ids: ids});
  } catch (error) {
    if (session === active) status.textContent = error.message;
  }
  if (session === active) await mentions(active);
});

compose.addEventListener('submit', async event => {
  event.preventDefault();
  const active = session;
  if (!active || el('send').disabled) return;
  const body = {text: message.value};
  if (active.replyTo) body.reply_to_event_id = active.replyTo;
  else if (active.broadcast?.checked) body.mention_all = true;
  else if (target.value.trim()) body.mention = {kind: kind.value, id: target.value.trim()};
  el('send').disabled = true;
  try {
    const result = await api(active, 'messages', {}, body);
    if (session !== active) return;
    message.value = target.value = '';
    if (active.broadcast) active.broadcast.checked = false;
    setReply(undefined);
    status.textContent = '已保存发言 #' + result.seq + '。';
    await history(active, null);
  } catch (error) {
    if (session === active) status.textContent = error.message;
  } finally {
    if (session === active) el('send').disabled = false;
  }
});
