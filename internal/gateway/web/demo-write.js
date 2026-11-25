'use strict';

function bindGooseCabDemoComposer(form, status, onSent = () => {}, canSend = () => true) {
  const message = form.querySelector('textarea');
  const send = form.querySelector('button[type="submit"], button:not([type])');
  let pending;

  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (send.disabled || !canSend() || !message.value.trim()) return;
    if (!pending || pending.text !== message.value) {
      pending = {text: message.value, request_id: crypto.randomUUID()};
    }
    send.disabled = true;
    message.readOnly = true;
    try {
      const response = await fetch('/api/demo-write/messages', {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(pending), mode: 'same-origin', credentials: 'omit',
        cache: 'no-store', redirect: 'error'
      });
      if (response.status !== 201) {
        status.textContent = response.status === 429 ? '发言过快，请稍后重试。' : '发言未确认，草稿已保留。';
        return;
      }
      const receipt = await response.json();
      if (receipt.room_id !== 'demo' || typeof receipt.event_id !== 'string' ||
          !Number.isSafeInteger(receipt.seq) || receipt.seq < 1) {
        throw new Error('invalid receipt');
      }
      pending = undefined;
      message.value = '';
      status.textContent = '已发送 #' + receipt.seq + '。';
      onSent();
    } catch {
      status.textContent = '发言未确认，草稿已保留。';
    } finally {
      message.readOnly = false;
      send.disabled = false;
    }
  });
}

const standalone = document.getElementById('compose');
if (standalone) bindGooseCabDemoComposer(standalone, document.getElementById('status'));
