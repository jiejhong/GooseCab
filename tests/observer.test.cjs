// Minimal dependency-free DOM smoke test; run with Node.js 18+.
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const assert = require('assert');

class Node {
  constructor(tag = 'div') {
    this.tagName = tag;
    this.children = [];
    this.listeners = {};
    this.value = '';
    this.textContent = '';
  }
  append(...nodes) { this.children.push(...nodes); }
  replaceChildren(...nodes) { this.children = nodes; }
  get childNodes() { return this.children; }
  setAttribute(key, value) { this[key] = value; }
  addEventListener(event, handler) { this.listeners[event] = handler; }
  remove() {}
  click() { if (this.listeners.click) this.listeners.click(); }
}

const nodes = {};
const document = {
  getElementById: id => nodes[id] ?? (nodes[id] = new Node()),
  createElement: tag => new Node(tag),
  createDocumentFragment: () => new Node('fragment'),
  body: new Node('body')
};
const requests = [];
const context = {
  document, location: { search: '' }, URLSearchParams, URL, AbortController,
  Date, Set, Number, JSON, String, Boolean, console, TextEncoder,
  clearTimeout: () => {}, setTimeout: () => 1,
  fetch: async (url, options = {}) => {
    requests.push({ url, headers: options.headers });
    if (url === '/api/status') {
      return { ok: true, json: async () => ({ service: 'ok', database: '/secret/path' }) };
    }
    const query = new URL(url, 'http://test').searchParams;
    const seq = Number(query.get('after_seq') || 0) + 1;
    return {
      ok: true,
      json: async () => ({
        room_id: '[已隐藏]', redacted: true,
        snapshot_seq: seq > 2 ? 3 : 2, snapshot_cursor: seq > 2 ? '3' : '2',
        total: seq > 2 ? 3 : 2, has_more: seq === 1,
        next_seq: seq, next_cursor: String(seq),
        events: [{
          // Distinct original IDs may redact identically; none may disappear.
          event_id: '[已隐藏]', room_id: '[已隐藏]', seq,
          type: 'chat.message', actor: { id: 'worker' },
          payload: { text: '<img onerror=evil>' }
        }]
      })
    };
  }
};
vm.createContext(context);
const source = fs.readFileSync(path.join(__dirname, '../internal/gateway/web/index.html'), 'utf8')
  .split('<script>')[1].split('</script>')[0];
vm.runInContext(source, context);
const wait = () => new Promise(resolve => setImmediate(resolve));
const all = node => [node, ...node.children.flatMap(all)];

(async () => {
  await wait();
  assert.equal(nodes.shown.textContent, '1');
  assert(all(nodes.events).some(node => node.textContent === '<img onerror=evil>'));
  assert(!all(nodes.events).some(node => node.tagName === 'img'));
  assert(!all(nodes.config).some(node => String(node.textContent).includes('/secret')));
  await vm.runInContext('load()', context);
  assert.equal(nodes.shown.textContent, '2');
  assert(requests.at(-1).url.includes('through_cursor=2'));
  assert(vm.runInContext('pollEnabled && pollTimer === 1', context));
  await vm.runInContext('load(false, true)', context);
  assert(!requests.at(-1).url.includes('through_seq'));
  assert(!requests.at(-1).url.includes('through_cursor'));
  assert.equal(nodes.shown.textContent, '3');
  assert(requests.some(request => request.url.includes('after_cursor=2')));

  nodes.room.value = 'new';
  nodes.token.value = 'secret';
  nodes.room.listeners.input();
  assert.equal(nodes.shown.textContent, '0');
  assert.equal(vm.runInContext('pollEnabled', context), false);
  await vm.runInContext('load(true)', context);
  assert.equal(nodes.shown.textContent, '1');
  assert(requests.some(request => request.headers?.Authorization === 'Bearer secret'));
  assert(!requests.some(request => request.url.includes('secret')));

  nodes.room.value = '中'.repeat(86);
  await vm.runInContext('load(true)', context);
  assert(nodes.state.textContent.includes('256'));
  assert.equal(vm.runInContext('pollEnabled', context), false);
  console.log('PASS: safe text, status whitelist, sanitized IDs, snapshot paging, polling, auth, room reset, UTF-8 limit');
})().catch(error => { console.error(error); process.exitCode = 1; });
