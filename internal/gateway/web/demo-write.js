'use strict';

const form = document.getElementById('compose');
const message = document.getElementById('message');
const send = document.getElementById('send');
const status = document.getElementById('status');

form.addEventListener('submit', async event => {
  event.preventDefault();
  if (send.disabled) return;
  send.disabled = true;
  try {
    const response = await fetch('/api/demo-write/messages', {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({text: message.value}), mode: 'same-origin',
      cache: 'no-store', credentials: 'same-origin', redirect: 'error'
    });
    if (response.status !== 201) {
      status.textContent = response.status === 429 ? '发言过快，请稍后重试。' : '发言未确认。';
      return;
    }
    const receipt = await response.json();
    message.value = '';
    status.textContent = '已发送 #' + receipt.seq + '。';
  } catch (error) {
    status.textContent = '发言未确认。';
  } finally {
    send.disabled = false;
  }
});
