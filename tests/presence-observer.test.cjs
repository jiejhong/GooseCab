// Executes the embedded observer presence script with a controlled DOM and clock.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

class Element {
  constructor(tag, text) {
    this.tag = tag;
    this.textContent = text ?? '';
    this.children = [];
    this.listeners = new Map();
    this.value = '';
  }
  append(...children) { this.children.push(...children); }
  replaceChildren(fragment) { this.children = fragment.children; }
  querySelector() { return this.children[0]; }
  insertBefore(node, sibling) { this.children.splice(this.children.indexOf(sibling), 0, node); }
  setAttribute(name, value) { this[name] = value; }
  addEventListener(event, handler) { this.listeners.set(event, handler); }
  dispatch(event) { this.listeners.get(event)(); }
}

const controls = Object.fromEntries(['room', 'token', 'room-form'].map(id => [id, new Element('input')]));
controls.room.value = 'first room';
const sidebar = new Element('aside');
sidebar.append(new Element('p'));
const document = new Element('document');
document.querySelector = () => sidebar;
document.createDocumentFragment = () => new Element('fragment');

const requests = [];
const timers = new Map();
let timerID = 0;
const context = {
  document, URLSearchParams, AbortController, TextEncoder,
  $: id => controls[id],
  node: (tag, _cls, text) => new Element(tag, text),
  setTimeout: (callback, delay) => {
    assert.equal(delay, 5000);
    timers.set(++timerID, callback);
    return timerID;
  },
  clearTimeout: id => timers.delete(id),
  fetch: (url, options) => new Promise((resolve, reject) => requests.push({ url, options, resolve, reject }))
};
const html = fs.readFileSync(path.join(__dirname, '../internal/gateway/web/index.html'), 'utf8');
const source = html.match(/<script id="room-presence-script">([\s\S]*?)<\/script>/)[1];
vm.runInNewContext(source, context);

const [heading, status, activity, list] = sidebar.children[0].children;
const wait = () => new Promise(resolve => setImmediate(resolve));
const reply = (request, room_id, agents, last_activity_at = null, activity_available = true) =>
  request.resolve({ ok: true, json: async () => ({ room_id, agents, last_activity_at, activity_available }) });
const worker = (agent_id, connections = 1, last_activity_at = null) => ({ agent_id, connections, last_activity_at });
function fireTimer() {
  assert.equal(timers.size, 1);
  const [[id, run]] = timers;
  timers.delete(id);
  run();
  return requests.at(-1);
}

(async () => {
  assert.equal(heading.textContent, '房间在线状态');
  assert.equal(status.role, 'status');
  assert.equal(requests[0].url, '/api/rooms/presence?room_id=first+room');
  assert.equal(requests[0].options.cache, 'no-store');
  const at = '2020-01-02T03:04:05Z';
  const label = '本房间最近发言 ' + new Date(at).toLocaleString('zh-CN', { hour12: false });
  reply(requests[0], 'first room', [worker('<img onerror=evil>', 2)], at);
  await wait();
  assert.equal(status.textContent, '在线 Agent 1');
  assert.equal(activity.textContent, label);
  assert.equal(list.children[0].tag, 'li');
  assert.equal(list.children[0].textContent, '<img onerror=evil> · 2 个连接 · 暂无活动');

  reply(fireTimer(), 'first room', [], at);
  await wait();
  assert.equal(status.textContent, '在线 Agent 0');
  assert.equal(activity.textContent, label);

  fireTimer().resolve({ ok: false, status: 503 });
  await wait();
  assert.equal(status.textContent, '在线状态暂不可用');
  assert.equal(activity.hidden, true);
  assert.equal(list.children.length, 0);

  const old = fireTimer();
  controls.room.value = 'new room';
  controls.room.dispatch('input');
  controls.token.value = 'fixture-token';
  controls.token.dispatch('input');
  assert.equal(old.options.signal.aborted, true);
  assert.equal(status.textContent, '刷新后查看当前房间在线状态');
  controls['room-form'].dispatch('submit');
  const current = requests.at(-1);
  assert.equal(current.url, '/api/rooms/presence?room_id=new+room');
  assert.equal(current.options.headers.Authorization, 'Bearer fixture-token');
  reply(old, 'first room', [worker('stale')], at);
  reply(current, 'new room', []);
  await wait();
  assert.equal(status.textContent, '在线 Agent 0');
  assert.equal(activity.textContent, '暂无可确认时间的房间发言');
  assert.equal(list.children.length, 0);

  reply(fireTimer(), 'new room', [worker('current', 1, at)], at, false);
  await wait();
  assert.equal(status.textContent, '在线 Agent 1');
  assert.equal(activity.textContent, '本房间最近发言时间暂不可用');
  assert.equal(list.children[0].textContent, 'current · 最近活跃 ' + new Date(at).toLocaleString('zh-CN', { hour12: false }));
  reply(fireTimer(), 'new room', [], 'bad timestamp');
  await wait();
  assert.equal(activity.textContent, '本房间最近发言时间暂不可用');

  document.hidden = true;
  document.dispatch('visibilitychange');
  assert.equal(timers.size, 0);
  assert.equal(status.textContent, '页面可见时更新在线状态');
  document.hidden = false;
  document.dispatch('visibilitychange');
  reply(requests.at(-1), 'first room', [worker('wrong-room')]);
  await wait();
  assert.equal(status.textContent, '在线状态暂不可用');

  const hanging = fireTimer();
  hanging.options.signal.addEventListener('abort', () => hanging.reject(new Error('timeout')));
  fireTimer();
  await wait();
  assert.equal(hanging.options.signal.aborted, true);
  assert.equal(status.textContent, '在线状态暂不可用');
  assert.equal(list.children.length, 0);
  console.log('PASS: room presence, per-Agent activity, independent room chat time, auth, room changes, visibility and timeout');
})().catch(error => { console.error(error); process.exitCode = 1; });
