'use strict';

const $ = id => document.getElementById(id);
const heading = $('heading');
const signIn = $('sign-in');
const signOut = $('sign-out');
const token = $('access-token');
const compose = $('compose');
const writeFields = $('write-fields');
const message = $('message');
const kind = $('mention-kind');
const target = $('mention-id');
const broadcastHost = $('broadcast-controls');
const mentions = $('mentions');
const mentionCount = $('mention-count');
const mentionStatus = $('mention-status');
const mentionEvents = $('mention-events');
const mentionFirst = $('mention-first');
const mentionNext = $('mention-next');
const mentionAck = $('mention-ack');
const roomStatus = $('room-status');
const sendStatus = $('send-status');
const timeline = $('events');
const historySection = $('history');
const historyStatus = $('history-status');
const historyFirst = $('history-first');
const historyNext = $('history-next');
let broadcast;
let session;

function leave(note) {
  if (session) {
    session.controller.abort();
    clearTimeout(session.timer);
  }
  session = undefined;
  token.value = '';
  message.value = '';
  target.value = '';
  kind.value = 'agent';
  sendStatus.textContent = '';
  roomStatus.textContent = note;
  heading.textContent = 'GooseCab 本地房间';
  timeline.replaceChildren();
  mentionEvents.replaceChildren();
  mentionCount.textContent = '';
  mentionStatus.textContent = '';
  mentions.hidden = true;
  updateComposer(false, false);
  historySection.hidden = true;
  historyStatus.textContent = '';
  historyFirst.disabled = true;
  historyNext.disabled = true;
  signIn.hidden = false;
  signOut.hidden = true;
  compose.hidden = true;
  writeFields.disabled = true;
}

function request(active, path, method, body) {
  const headers = { Authorization: 'GooseCabLocal ' + active.token };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  return fetch(path, {
    method, headers, body, mode: 'same-origin', credentials: 'omit',
    cache: 'no-store', redirect: 'error', signal: active.controller.signal
  });
}

function renderEvents(destination, events) {
  const rows = document.createDocumentFragment();
  for (const event of events) {
    const row = document.createElement('li');
    const author = document.createElement('div');
    author.textContent = identityLabel(event.actor, event.payload.author_display_name) +
      ' · ' + new Date(event.ts).toLocaleString();
    const body = document.createElement('p');
    body.textContent = event.payload.text;
    row.append(author, body);
    if (event.payload.mention_all) {
      const mention = document.createElement('div');
      mention.textContent = '全员点名';
      row.append(mention);
    } else if (event.payload.mention) {
      const mention = document.createElement('div');
      const recipient = event.payload.mention;
      mention.textContent = '点名 ' + (recipient.kind === 'agent' ? 'Agent ' : '人类 ') + recipient.id;
      row.append(mention);
    }
    rows.append(row);
  }
  destination.replaceChildren(rows);
}

function identityLabel(actor, name) {
  const prefix = actor.kind === 'human' ? '人类 ' : actor.kind === 'agent' ? 'Agent ' : '';
  const role = prefix + actor.id;
  return name && name !== actor.id ? name + '（' + role + '）' : role;
}

function setBroadcast(checked) {
  if (broadcast) broadcast.checked = checked;
  target.disabled = checked;
  kind.disabled = checked;
}

function updateComposer(writable, allowed, active = session) {
  const canBroadcast = writable && allowed;
  if ((active?.canWrite && !writable) || (broadcast && !canBroadcast)) {
    message.value = '';
    target.value = '';
    kind.value = 'agent';
    sendStatus.textContent = '';
  }
  if (canBroadcast && !broadcast) {
    const label = document.createElement('label');
    broadcast = document.createElement('input');
    broadcast.type = 'checkbox';
    broadcast.id = 'broadcast';
    broadcast.addEventListener('change', () => setBroadcast(broadcast.checked));
    label.append(broadcast, ' 点名本房间所有人');
    broadcastHost.replaceChildren(label);
  } else if (!canBroadcast && broadcast) {
    setBroadcast(false);
    broadcastHost.replaceChildren();
    broadcast = undefined;
  }
  broadcastHost.hidden = !canBroadcast;
  if (active) active.canWrite = writable;
  compose.hidden = !writable;
  writeFields.disabled = !writable || !!active?.sending;
}

function updateMentionControls(active) {
  const busy = active.mentionLoading || active.acknowledging;
  mentionFirst.disabled = busy || !active.before;
  mentionNext.disabled = busy || !active.nextBefore;
  mentionAck.disabled = busy || !active.visibleIDs?.length;
}

async function loadMentions(active, before = active.before || '') {
  if (active.mentionLoading || active.acknowledging) return;
  active.mentionLoading = true;
  updateMentionControls(active);
  const path = '/api/mentions' + (before ? '?before=' + encodeURIComponent(before) : '');
  try {
    const response = await request(active, path, 'GET');
    if (session !== active) return;
    if (response.status === 401) {
      leave('访问被拒绝或令牌已到期。');
      return;
    }
    if (!response.ok) throw new Error('Unread mentions unavailable');
    const data = await response.json();
    if (session !== active) return;
    active.before = before;
    active.nextBefore = data.next_before;
    active.visibleIDs = data.events.map(event => event.event_id);
    renderEvents(mentionEvents, data.events);
    mentionCount.textContent = '未读点名 ' + data.unread_count + ' 条';
    mentions.hidden = false;
  } catch {
    if (session !== active) return;
    active.nextBefore = undefined;
    active.visibleIDs = [];
    mentionEvents.replaceChildren();
    mentionCount.textContent = '暂时无法读取未读点名。';
  } finally {
    active.mentionLoading = false;
    if (session === active) updateMentionControls(active);
  }
}

mentionFirst.addEventListener('click', () => {
  if (session) loadMentions(session, '');
});
mentionNext.addEventListener('click', () => {
  if (session?.nextBefore) loadMentions(session, session.nextBefore);
});
mentionAck.addEventListener('click', async () => {
  const active = session;
  if (!active?.visibleIDs?.length || active.acknowledging || active.mentionLoading) return;
  const eventIDs = [...active.visibleIDs];
  active.acknowledging = true;
  updateMentionControls(active);
  try {
    const response = await request(active, '/api/mentions/read', 'POST', JSON.stringify({ event_ids: eventIDs }));
    if (session !== active) return;
    if (response.status === 401) {
      leave('访问被拒绝或令牌已到期。');
      return;
    }
    if (response.status !== 204) throw new Error('Acknowledgement unconfirmed');
    mentionStatus.textContent = '已将本页点名标记为已读。';
  } catch {
    if (session === active) mentionStatus.textContent = '标记结果待确认，请查看未读列表。';
  } finally {
    active.acknowledging = false;
    if (session === active) await loadMentions(active);
  }
});

function updateHistoryControls(active) {
  historyFirst.disabled = active.historyLoading || !active.historyBefore;
  historyNext.disabled = active.historyLoading || !active.historyNext;
}

async function loadHistory(active, before) {
  if (active.historyLoading) return;
  active.historyLoading = true;
  updateHistoryControls(active);
  try {
    const path = '/api/events' + (before ? '?before=' + encodeURIComponent(before) : '');
    const response = await request(active, path, 'GET');
    if (session !== active) return;
    if (response.status === 401 || response.status === 403) {
      leave('访问被拒绝或令牌已到期。');
      return;
    }
    if (!response.ok) throw new Error('Room unavailable');
    const data = await response.json();
    if (session !== active) return;
    const identity = JSON.stringify([data.room_id, data.actor.kind, data.actor.id]);
    if (active.identity && active.identity !== identity) {
      leave('房间或登录身份已变更，请重新认证。');
      return;
    }
    active.identity = identity;
    heading.textContent = 'GooseCab · ' + data.room_id;
    roomStatus.textContent = '已进入房间 · ' + identityLabel(data.actor, data.display_name) +
      (data.can_write ? '' : ' · 只读');
    updateComposer(data.can_write === true, data.can_broadcast === true, active);
    if (before !== undefined || !active.historyBefore) {
      active.historyBefore = before || '';
      active.historyNext = data.next_before;
      renderEvents(timeline, data.events);
      historyStatus.textContent = before ? '正在查看较早的发言。' : '';
    }
    historySection.hidden = false;
  } catch {
    if (session !== active) return;
    updateComposer(false, false, active);
    roomStatus.textContent = '暂时无法读取房间，正在重试。';
    active.historyBefore = '';
    active.historyNext = undefined;
    timeline.replaceChildren();
    historyStatus.textContent = '';
    historySection.hidden = true;
  } finally {
    active.historyLoading = false;
    if (session === active) updateHistoryControls(active);
  }
}

historyFirst.addEventListener('click', () => {
  if (session) loadHistory(session, '');
});
historyNext.addEventListener('click', () => {
  if (session?.historyNext) loadHistory(session, session.historyNext);
});

async function poll(active) {
  await loadHistory(active);
  if (session === active && active.identity) await loadMentions(active);
  if (session === active) active.timer = setTimeout(() => poll(active), 2500);
}

signIn.addEventListener('submit', event => {
  event.preventDefault();
  if (!token.value.trim() || session) return;
  const active = { token: token.value.trim(), controller: new AbortController() };
  session = active;
  token.value = '';
  signIn.hidden = true;
  signOut.hidden = false;
  roomStatus.textContent = '正在验证访问权限。';
  poll(active);
});
signOut.addEventListener('click', () => leave('已退出房间。'));
window.addEventListener('pagehide', () => leave(''));

compose.addEventListener('submit', async event => {
  event.preventDefault();
  const active = session;
  if (!active?.canWrite || active.sending) return;
  const text = message.value;
  if (!text.trim() || new TextEncoder().encode(text).length > 4096) {
    sendStatus.textContent = '请输入不超过 4096 字节的发言。';
    return;
  }
  const chat = { text };
  if (broadcast?.checked) chat.mention_all = true;
  else if (target.value !== '') chat.mention = { kind: kind.value, id: target.value };
  active.sending = true;
  writeFields.disabled = true;
  sendStatus.textContent = '正在发送。';
  try {
    const response = await request(active, '/api/messages', 'POST', JSON.stringify(chat));
    if (session !== active) return;
    if (response.status === 401) {
      leave('访问被拒绝或令牌已到期。');
      return;
    }
    if (response.status === 201) {
      const receipt = await response.json();
      if (session !== active) return;
      message.value = '';
      target.value = '';
      setBroadcast(false);
      sendStatus.textContent = '已保存发言 #' + receipt.seq + '。';
    } else if (response.status === 409) {
      sendStatus.textContent = '房间记录已满，发言未保存。';
    } else if (response.status === 403) {
      updateComposer(false, false, active);
      roomStatus.textContent = '当前身份没有此次发言权限。';
    } else if (response.status === 429) {
      sendStatus.textContent = '发送过快，请稍后再试。';
    } else if (response.status === 400) {
      sendStatus.textContent = '请检查发言或本房间点名对象。';
    } else {
      sendStatus.textContent = '发送未确认，请检查时间线。';
    }
  } catch {
    if (session === active) sendStatus.textContent = '发送未确认，请检查时间线。';
  } finally {
    active.sending = false;
    if (session === active) writeFields.disabled = !active.canWrite;
  }
});
