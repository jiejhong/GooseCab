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
    list.append(row);
  }
  el(id).replaceChildren(list);
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

function poll(active) {
  active.timer = setTimeout(async () => {
    if (session !== active) return;
    if (!active.older) await history(active, null, true);
    if (session === active) await mentions(active);
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
  if (active.broadcast?.checked) body.mention_all = true;
  else if (target.value.trim()) body.mention = {kind: kind.value, id: target.value.trim()};
  el('send').disabled = true;
  try {
    const result = await api(active, 'messages', {}, body);
    if (session !== active) return;
    message.value = target.value = '';
    if (active.broadcast) active.broadcast.checked = false;
    status.textContent = '已保存发言 #' + result.seq + '。';
    await history(active, null);
  } catch (error) {
    if (session === active) status.textContent = error.message;
  } finally {
    if (session === active) el('send').disabled = false;
  }
});
