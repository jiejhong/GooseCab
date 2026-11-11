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
let emptyReads = 0;
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
    if (query.get('room_id') === 'empty') {
      if (query.has('after_cursor') && query.get('after_cursor') === '') return {ok:false,status:400};
      const arrived = ++emptyReads > 2;
      return {ok:true,json:async()=>({room_id:'empty',redacted:true,snapshot_seq:arrived?1:0,snapshot_cursor:arrived?'1':'0',total:arrived?1:0,has_more:false,next_seq:arrived?1:0,next_cursor:arrived?'1':'',events:arrived?[{room_id:'empty',seq:1,type:'chat.message',payload:{text:'first arrival'}}]:[]})};
    }
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
  assert(requests.some(request => request.url === '/api/status' && request.headers?.Authorization === 'Bearer secret'));

  nodes.room.value = 'empty';
  await vm.runInContext('load(true)', context);
  await vm.runInContext('load(false, true)', context);
  assert(!new URL(requests.at(-1).url, 'http://test').searchParams.has('after_cursor'));
  assert.equal(vm.runInContext('pollEnabled', context), true);
  await vm.runInContext('load(false, true)', context);
  assert.equal(nodes.shown.textContent, '1');
  assert(all(nodes.events).some(node => node.textContent === 'first arrival'));
  assert.equal(vm.runInContext('pollEnabled', context), true);

  // Task cards must never conflate execution completion with a retrievable result.
  const emit = event => vm.runInContext(`recordTask(${JSON.stringify(event)}, 'fixture-${event.seq}'); taskFreshness='current'; renderTasks();`, context);
  const cardText = () => all(nodes.tasks).map(node => String(node.textContent)).join('\n');
  const task = id => vm.runInContext(`tasks.get('request:'+${JSON.stringify(id)})`, context);
  emit({type:'capability.invoke',seq:1,ts:'2026-11-10T01:02:03Z',correlation_id:'req-1',payload:{request_id:'req-1',capability_id:'cap.text.uppercase',target_agent_id:'worker-a',payload:{text:'hello'}}});
  assert.equal(nodes['task-count'].textContent, '1 个调用');
  assert(cardText().includes('将文本转换为大写'));
  assert(cardText().includes('worker-a'));
  assert(cardText().includes('hello'));
  assert.equal(task('req-1').updatedAt, '2026-11-10T01:02:03Z');
  emit({type:'capability.progress',seq:2,ts:'2026-11-10T01:02:04Z',actor:{id:'worker-a'},payload:{request_id:'req-1',status:'running',percent:0}});
  assert(cardText().includes('执行中'));
  emit({type:'capability.progress',seq:3,ts:'2026-11-10T01:02:05Z',payload:{request_id:'req-1',status:'completed',percent:100}});
  assert(cardText().includes('执行完成'));
  assert(cardText().includes('结果尚不可取'));
  assert.equal(task('req-1').resultAvailable, false);
  assert(!all(nodes.tasks).some(node => node.className === 'task-output'));
  emit({type:'capability.result',seq:4,ts:'2026-11-10T01:02:06Z',actor:{id:'worker-a'},payload:{request_id:'req-1',status:'completed',result_available:true,output:{text:'<img onerror=evil>',media_type:'text/plain'}}});
  assert.equal(nodes['task-count'].textContent, '1 个调用');
  assert(cardText().includes('结果可取（文本）'));
  assert(all(nodes.tasks).some(node => node.className === 'task-output' && node.textContent === '<img onerror=evil>'));
  assert(!all(nodes.tasks).some(node => node.tagName === 'img'));
  assert(all(nodes.tasks).some(node => node.tagName === 'a' && node.href === '#fixture-4'));
  const resultDetails = all(nodes.tasks).find(node => node.className === 'task-result');
  resultDetails.open = true;
  resultDetails.listeners.toggle();
  vm.runInContext('renderTasks()', context);
  assert(all(nodes.tasks).find(node => node.className === 'task-result').open);
  // A duplicate invoke/progress after completion cannot restart or hide the result.
  emit({type:'capability.invoke',seq:5,payload:{request_id:'req-1',target_agent_id:'different-worker',payload:{text:'different'}}});
  emit({type:'capability.progress',seq:6,actor:{id:'wrong-agent'},payload:{request_id:'req-1',status:'running'}});
  assert.equal(task('req-1').status, 'completed');
  assert.equal(task('req-1').agent, 'worker-a');
  assert.equal(task('req-1').input, 'hello');
  assert.equal(task('req-1').resultAvailable, true);

  // Missing, false and non-string output availability must not expose a result.
  for (const [id,extra] of Object.entries({legacy:{output_ref:'file:///local/result.txt'},missing:{result_available:true},false_flag:{result_available:false,output:{text:'hello'}},invalid:{result_available:true,output:{text:{unsafe:'object'}}}})) {
    emit({type:'capability.result',seq:10,payload:{request_id:id,status:'completed',...extra}});
    assert.equal(task(id).resultAvailable, false);
  }
  assert(!all(nodes.tasks).some(node => node.tagName === 'a' && node.href.startsWith('file:')));
  emit({type:'capability.result',seq:11,payload:{request_id:'empty-text',status:'completed',result_available:true,output:{text:'',media_type:'text/plain'}}});
  assert.equal(task('empty-text').resultAvailable, true);
  assert(cardText().includes('（空文本结果）'));
  emit({type:'capability.result',seq:12,actor:{id:'worker-b'},payload:{request_id:'failed',status:'failed',error:'Input exceeds limit',result_available:false}});
  assert(cardText().includes('执行失败'));
  assert(cardText().includes('Input exceeds limit'));
  assert(cardText().includes('确认任务未继续执行'));
  emit({type:'capability.result',seq:13,payload:{request_id:'unknown',status:'unknown',error:'connection lost',result_available:false}});
  assert(cardText().includes('执行结果未知'));
  assert(cardText().includes('不要自动重试'));
  emit({type:'capability.result',seq:13,payload:{request_id:'unexpected',status:'constructor'}});
  assert(cardText().includes('状态未识别'));
  // Old P1 lifecycle shares the same aggregation without inventing result availability.
  emit({type:'capability.invoke.request',seq:14,correlation_id:'p1',payload:{id:'p1',capability_id:'cap.echo',target_agent_id:'worker-c'}});
  emit({type:'capability.invoke.progress',seq:15,correlation_id:'p1',payload:{percent:50}});
  assert.equal(task('p1').status, 'running');
  emit({type:'capability.invoke.result',seq:16,correlation_id:'p1',payload:{status:'completed',output_ref:'[已隐藏]'}});
  assert.equal(task('p1').status, 'completed');
  assert.equal(task('p1').resultAvailable, false);
  // Redacted IDs can collide. They must not join distinct tasks or become DOM IDs.
  const beforeRedacted = vm.runInContext('tasks.size', context);
  emit({type:'capability.invoke',seq:17,correlation_id:'[已隐藏]',payload:{request_id:'[已隐藏]'}});
  emit({type:'capability.result',seq:18,correlation_id:'[已隐藏]',payload:{request_id:'[已隐藏]',status:'completed'}});
  assert.equal(vm.runInContext('tasks.size', context), beforeRedacted + 2);
  assert(cardText().includes('关联 ID 已隐藏或缺失'));
  vm.runInContext('hasMore=true; renderTasks()', context);
  assert(nodes['task-summary'].textContent.includes('可能不是最新状态'));

  // Poll failure preserves confirmed output, but leaves active tasks unconfirmed.
  emit({type:'capability.invoke',seq:19,payload:{request_id:'active',capability_id:'cap.echo'}});
  const normalFetch = context.fetch;
  context.fetch = async () => { throw new Error('Failed to fetch'); };
  await vm.runInContext('load(false, true)', context);
  assert.equal(vm.runInContext('taskFreshness', context), 'disconnected');
  assert.equal(vm.runInContext('pollEnabled', context), false);
  assert(cardText().includes('状态待确认'));
  assert(cardText().includes('手动刷新并核对 Agent'));
  assert.equal(task('req-1').resultAvailable, true);
  assert(cardText().includes('结果可取（文本）'));
  context.fetch = normalFetch;

  nodes.room.value = 'another-room';
  nodes.room.listeners.input();
  assert.equal(nodes['task-count'].textContent, '0 个调用');
  assert(!cardText().includes('worker-a'));
  assert(!cardText().includes('<img onerror=evil>'));

  // A full HTTP page-to-poll lifecycle updates the same card and safe local anchors.
  let taskReads = 0;
  context.fetch = async url => {
    if (url === '/api/status') return {ok:true,json:async()=>({service:'ok'})};
    const result = ++taskReads > 1;
    const event = {room_id:'task-room',seq:result?2:1,type:result?'capability.result':'capability.invoke',payload:result?{request_id:'web-task',status:'completed',result_available:true,output:{text:'WEB RESULT'}}:{request_id:'web-task',capability_id:'cap.text.uppercase',target_agent_id:'web-agent',payload:{text:'web input'}}};
    return {ok:true,json:async()=>({room_id:'task-room',redacted:true,snapshot_seq:event.seq,snapshot_cursor:String(event.seq),total:event.seq,has_more:false,next_seq:event.seq,next_cursor:String(event.seq),events:[event]})};
  };
  nodes.room.value = 'task-room';
  await vm.runInContext('load(true)', context);
  assert.equal(nodes['task-count'].textContent,'1 个调用');
  assert(cardText().includes('等待 Agent 接收'));
  await vm.runInContext('load(false, true)', context);
  assert.equal(nodes['task-count'].textContent,'1 个调用');
  assert(cardText().includes('WEB RESULT'));
  assert(all(nodes.tasks).some(node=>node.tagName==='a'&&node.href==='#event-2'));
  assert(all(nodes.events).some(node=>node.id==='event-2'));
  context.fetch = normalFetch;

  nodes.room.value = '中'.repeat(86);
  await vm.runInContext('load(true)', context);
  assert(nodes.state.textContent.includes('256'));
  assert.equal(vm.runInContext('pollEnabled', context), false);
  console.log('PASS: safe text, status whitelist, sanitized IDs, snapshot paging, empty-room arrival, polling, authenticated status, room reset, UTF-8 limit, task lifecycle, result availability, safe output, duplicate events, redacted IDs, disconnected state');
})().catch(error => { console.error(error); process.exitCode = 1; });
