// 无头浏览器验证脚本（零依赖，只用 Node 内置的 WebSocket / fetch）。
//
//   node scripts/shot.mjs --url http://127.0.0.1:8098/ --out build/shot.png \
//        --wait 4000 --eval "ISO.demo()" --w 1280 --h 800
//
// 它做三件事：收集控制台报错与未捕获异常、执行可选的注入脚本、截图。
// 关掉浏览器前会把所有日志打到 stdout，方便 CI 断言「零 console 错误」。

import { spawn } from 'node:child_process';
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const argv = process.argv.slice(2);
function arg(name, def) {
  const i = argv.indexOf(`--${name}`);
  return i >= 0 ? argv[i + 1] : def;
}
const url = arg('url', 'http://127.0.0.1:8098/');
const out = arg('out', 'build/shot.png');
const wait = Number(arg('wait', 4000));
const W = Number(arg('w', 1280));
const H = Number(arg('h', 800));
const inject = arg('eval', '');
const move = arg('move', ''); // "x,y" 帧缓冲坐标：截图前把鼠标移过去（用于验证悬浮提示）
const port = Number(arg('port', 9333));
const chrome = arg('chrome', 'google-chrome-stable');

const profile = mkdtempSync(join(tmpdir(), 'isocity-chrome-'));
const child = spawn(chrome, [
  '--headless=new',
  '--disable-gpu',
  '--no-sandbox',
  '--no-first-run',
  '--disable-dev-shm-usage',
  '--hide-scrollbars',
  '--mute-audio',
  `--user-data-dir=${profile}`,
  `--window-size=${W},${H}`,
  `--remote-debugging-port=${port}`,
  'about:blank',
], { stdio: ['ignore', 'ignore', 'ignore'] });

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function targets() {
  for (let i = 0; i < 60; i++) {
    try {
      const res = await fetch(`http://127.0.0.1:${port}/json/list`);
      const list = await res.json();
      const page = list.find((t) => t.type === 'page');
      if (page) return page;
    } catch { /* 还没起来 */ }
    await sleep(120);
  }
  throw new Error('Chrome 调试端口未就绪');
}

const logs = [];
let ws;
try {
  const page = await targets();
  ws = new WebSocket(page.webSocketDebuggerUrl);
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
  let id = 0;
  const pending = new Map();
  ws.onmessage = (ev) => {
    const msg = JSON.parse(ev.data);
    if (msg.id && pending.has(msg.id)) { pending.get(msg.id)(msg); pending.delete(msg.id); return; }
    if (msg.method === 'Runtime.consoleAPICalled') {
      const text = (msg.params.args || []).map(a => a.value ?? a.description ?? a.type).join(' ');
      logs.push(`[${msg.params.type}] ${text}`);
    }
    if (msg.method === 'Runtime.exceptionThrown') {
      const d = msg.params.exceptionDetails;
      logs.push(`[exception] ${d.text} ${d.exception?.description || ''}`.trim());
    }
    if (msg.method === 'Log.entryAdded') {
      logs.push(`[${msg.params.entry.level}] ${msg.params.entry.text}`);
    }
  };
  const send = (method, params = {}) => new Promise((res) => {
    const mid = ++id;
    pending.set(mid, res);
    ws.send(JSON.stringify({ id: mid, method, params }));
  });

  await send('Runtime.enable');
  await send('Log.enable');
  await send('Page.enable');
  await send('Emulation.setDeviceMetricsOverride', { width: W, height: H, deviceScaleFactor: 1, mobile: false });
  await send('Page.navigate', { url });
  await sleep(wait);

  if (inject) {
    const r = await send('Runtime.evaluate', { expression: inject, awaitPromise: true, returnByValue: true });
    if (r.result?.exceptionDetails) logs.push(`[eval] ${r.result.exceptionDetails.text}`);
    else logs.push(`[eval-ok] ${JSON.stringify(r.result?.result?.value)}`);
  }
  if (move) {
    const [fx, fy] = move.split(',').map(Number);
    const m = await send('Runtime.evaluate', {
      expression: `(()=>{const c=document.getElementById('screen');const r=c.getBoundingClientRect();
        const f=(window.ISO&&ISO.frame)||{w:640,h:400};
        return JSON.stringify({left:r.left, top:r.top, kx:r.width/f.w, ky:r.height/f.h});})()`,
      returnByValue: true,
    });
    const g = JSON.parse(m.result.result.value);
    const cx = g.left + fx * g.kx + g.kx / 2;
    const cy = g.top + fy * g.ky + g.ky / 2;
    await send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: cx, y: cy, button: 'none', buttons: 0 });
    logs.push(`[move] frame(${fx},${fy}) → client(${cx},${cy})`);
  }
  await sleep(600);

  const shot = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
  if (shot.result?.data) {
    const { writeFileSync: wf } = await import('node:fs');
    wf(out, Buffer.from(shot.result.data, 'base64'));
    logs.push(`[shot] ${out}`);
  } else {
    logs.push('[shot] 失败');
  }
} finally {
  try { ws?.close(); } catch { /* ignore */ }
  child.kill('SIGKILL');
  await sleep(200);
  try { rmSync(profile, { recursive: true, force: true }); } catch { /* ignore */ }
}

const bad = logs.filter((l) => l.startsWith('[exception]') || l.startsWith('[error]') || l.startsWith('[eval]'));
console.log(logs.join('\n'));
if (bad.length) {
  console.log(`\n== ${bad.length} 条错误 ==`);
  process.exitCode = 1;
} else {
  console.log('\n== 无 console 错误 ==');
}
void writeFileSync;
