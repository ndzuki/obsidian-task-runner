// 端到端交互冒烟：用真实鼠标事件驱动 UI，验证「工具 → 模型 → 渲染」整条链路。
//
//   node scripts/e2e.mjs --url http://127.0.0.1:8098/ --out build/e2e.png
//
// 断言的是模型状态（格子高度/建筑数/道路数），不是截图相似度，
// 因此既能在 CI 里跑，也能在改玩法逻辑时立刻发现回归。

import { spawn } from 'node:child_process';
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const argv = process.argv.slice(2);
const arg = (n, d) => { const i = argv.indexOf(`--${n}`); return i >= 0 ? argv[i + 1] : d; };
const url = arg('url', 'http://127.0.0.1:8098/');
const out = arg('out', 'build/e2e.png');
const port = Number(arg('port', 9334));
const chrome = arg('chrome', 'google-chrome-stable');
const W = 1280, H = 800;

const profile = mkdtempSync(join(tmpdir(), 'isocity-e2e-'));
const child = spawn(chrome, [
  '--headless=new', '--disable-gpu', '--no-sandbox', '--no-first-run',
  '--disable-dev-shm-usage', '--hide-scrollbars', '--mute-audio',
  `--user-data-dir=${profile}`, `--window-size=${W},${H}`,
  `--remote-debugging-port=${port}`, 'about:blank',
], { stdio: ['ignore', 'ignore', 'ignore'] });

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const results = [];
let failed = 0;

function check(name, ok, detail = '') {
  results.push(`${ok ? '  PASS' : '  FAIL'}  ${name}${detail ? '  — ' + detail : ''}`);
  if (!ok) failed++;
}

let ws;
try {
  let page = null;
  for (let i = 0; i < 60 && !page; i++) {
    try {
      const list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
      page = list.find((t) => t.type === 'page');
    } catch { /* 等 Chrome 起来 */ }
    if (!page) await sleep(120);
  }
  if (!page) throw new Error('Chrome 调试端口未就绪');

  ws = new WebSocket(page.webSocketDebuggerUrl);
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
  let id = 0;
  const pending = new Map();
  const consoleErrors = [];
  ws.onmessage = (ev) => {
    const m = JSON.parse(ev.data);
    if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); return; }
    if (m.method === 'Runtime.exceptionThrown') {
      consoleErrors.push(m.params.exceptionDetails.exception?.description || m.params.exceptionDetails.text);
    }
    if (m.method === 'Runtime.consoleAPICalled' && m.params.type === 'error') {
      consoleErrors.push((m.params.args || []).map((a) => a.value).join(' '));
    }
  };
  const send = (method, params = {}) => new Promise((res) => {
    const mid = ++id;
    pending.set(mid, res);
    ws.send(JSON.stringify({ id: mid, method, params }));
  });
  const evalJs = async (expression) => {
    const r = await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true });
    if (r.result?.exceptionDetails) throw new Error(r.result.exceptionDetails.text + ' ' + (r.result.exceptionDetails.exception?.description || ''));
    return r.result?.result?.value;
  };
  // 帧缓冲坐标 → 客户端坐标。帧缓冲尺寸与显示倍率都由页面决定
  // （像素密度、devicePixelRatio、整数倍缩放都会影响），所以直接问页面要。
  let mapping = { left: 0, top: 0, kx: 1, ky: 1 };
  const refreshMapping = async () => {
    const m = JSON.parse(await evalJs(`(()=>{const c=document.getElementById('screen');
      const r=c.getBoundingClientRect(); const f=ISO.frame;
      return JSON.stringify({left:r.left, top:r.top, kx:r.width/f.w, ky:r.height/f.h});})()`));
    mapping = m;
  };
  const cx = (x) => mapping.left + x * mapping.kx + mapping.kx / 2;
  const cy = (y) => mapping.top + y * mapping.ky + mapping.ky / 2;
  const mouse = async (type, x, y, button = 'left', clickCount = 1) => {
    await send('Input.dispatchMouseEvent', {
      type, x: cx(x), y: cy(y), button, buttons: type === 'mouseReleased' ? 0 : 1,
      clickCount, modifiers: 0,
    });
  };
  const click = async (x, y, button = 'left') => {
    await mouse('mouseMoved', x, y, 'none');
    await mouse('mousePressed', x, y, button);
    await mouse('mouseReleased', x, y, button);
    await sleep(120);
  };
  const drag = async (x0, y0, x1, y1) => {
    await mouse('mouseMoved', x0, y0, 'none');
    await mouse('mousePressed', x0, y0);
    const steps = 6;
    for (let i = 1; i <= steps; i++) {
      await mouse('mouseMoved', x0 + ((x1 - x0) * i) / steps, y0 + ((y1 - y0) * i) / steps);
      await sleep(30);
    }
    await mouse('mouseReleased', x1, y1);
    await sleep(220);
  };

  await send('Runtime.enable');
  await send('Page.enable');
  await send('Emulation.setDeviceMetricsOverride', { width: W, height: H, deviceScaleFactor: 1, mobile: false });
  await send('Page.navigate', { url });
  await sleep(3500);

  const ready = await evalJs('!!(window.ISO && ISO.ready)');
  check('页面启动且 ISO 就绪', ready === true);
  if (!ready) throw new Error('页面未就绪');
  await refreshMapping();

  await evalJs("ISO.newMap(7); ISO.state.intro = 0; 'ok'");
  await sleep(300);

  // ---- 1. 抬高工具：点按钮 + 在视口拖拽 ----
  const toolBtn = async (i) => {
    const b = await evalJs(`JSON.stringify(ISO.toolButton(${i}))`);
    return JSON.parse(b);
  };
  const itemBtn = async (i) => {
    const b = await evalJs(`JSON.stringify(ISO.itemButton(${i}))`);
    return JSON.parse(b);
  };
  const clickTool = async (i) => { const b = await toolBtn(i); await click(b.x, b.y); };
  const clickItem = async (i) => { const b = await itemBtn(i); await click(b.x, b.y); };

  await clickTool(1); // 抬高
  const tool0 = await evalJs('ISO.state.toolIndex');
  check('点击「抬高」按钮切换了工具', tool0 === 1, `toolIndex=${tool0}`);

  const before = await evalJs('(()=>{const c=ISO.city;let s=0;for(const h of c.height)s+=h;return s;})()');
  // 落点从城市中心反算，避免像素密度/镜头变化后点到海面上
  const dragFrom = JSON.parse(await evalJs(`(()=>{const c=ISO.city;const t=c.center();
    const p=ISO.view.toScreen(ISO.world,t.x,t.y,c.heightAt(t.x,t.y));
    return JSON.stringify({x:Math.round(p.x),y:Math.round(p.y)});})()`));
  await drag(dragFrom.x, dragFrom.y, dragFrom.x + 40, dragFrom.y + 10);
  const after = await evalJs('(()=>{const c=ISO.city;let s=0;for(const h of c.height)s+=h;return s;})()');
  check('拖拽抬升了地形', after > before, `高度总和 ${before} → ${after}`);

  const maxH = await evalJs('(()=>{let m=0;for(const h of ISO.city.height)if(h>m)m=h;return m;})()');
  check('地形高度未越界（<=6）', maxH <= 6, `max=${maxH}`);

  // ---- 2. 地表涂抹 ----
  await clickTool(4); // 地表
  const toolT = await evalJs('ISO.state.toolIndex');
  check('切换到「地表」工具', toolT === 4, `toolIndex=${toolT}`);
  const itemsT = await evalJs('ISO.state.items.length');
  check('地表工具有可选条目', itemsT > 0, `items=${itemsT}`);
  await clickItem(0);
  const picked = await evalJs('ISO.state.selectedItem && ISO.state.selectedItem.name');
  await click(dragFrom.x + 60, dragFrom.y);
  await click(dragFrom.x + 80, dragFrom.y + 10);
  const painted = await evalJs('(()=>{const c=ISO.city;let n=0;for(const t of c.terrain)if(t!==9&&t!==0)n++;return n;})()');
  check('涂抹地表改变了地形类型', painted > 0, `非草地陆地格 ${painted}（选中 ${picked}）`);

  // ---- 3. 道路工具：拖拽铺路 ----
  await clickTool(5); // 道路
  const toolR = await evalJs('ISO.state.toolIndex');
  check('切换到「道路」工具', toolR === 5, `toolIndex=${toolR}`);
  await clickItem(0);
  const roadSel = await evalJs('JSON.stringify(ISO.state.selectedItem && {n:ISO.state.selectedItem.name, id:ISO.state.selectedItem.roadId})');
  const roadsBefore = await evalJs('(()=>{let n=0;for(const r of ISO.city.road)if(r>0)n++;return n;})()');
  await drag(dragFrom.x - 20, dragFrom.y + 40, dragFrom.x + 100, dragFrom.y + 40);
  const roadsAfter = await evalJs('(()=>{let n=0;for(const r of ISO.city.road)if(r>0)n++;return n;})()');
  const roadHover = await evalJs('JSON.stringify(ISO.view.hover)');
  const lastDown = await evalJs('JSON.stringify(ISO.state.lastDown)');
  const terrAt = await evalJs('JSON.stringify(ISO.state.lastDown && ISO.state.lastDown.tile ? {t:ISO.city.terrainAt(ISO.state.lastDown.tile.x,ISO.state.lastDown.tile.y), h:ISO.city.heightAt(ISO.state.lastDown.tile.x,ISO.state.lastDown.tile.y)} : null)');
  check('拖拽铺出了道路', roadsAfter > roadsBefore,
    `道路数 ${roadsBefore} → ${roadsAfter}；选中=${roadSel} 落点=${lastDown} 地表=${terrAt} dragFrom=${JSON.stringify(dragFrom)}`);
  const maskOk = await evalJs('(()=>{const c=ISO.city;for(let i=0;i<c.road.length;i++){if(c.road[i]>0)return typeof c.roadMask(i%c.w, (i/c.w)|0) === "number";}return false;})()');
  check('道路连通掩码可计算', maskOk === true);

  // ---- 4. 建筑工具：选条目 + 放置 ----
  await clickTool(6); // 建筑
  const toolB = await evalJs('ISO.state.toolIndex');
  check('切换到「建筑」工具', toolB === 6, `toolIndex=${toolB}`);
  const tabs = await evalJs('ISO.state.tabsActive');
  check('建筑工具会显示分类页签', tabs === true);
  const bItems = await evalJs('ISO.state.items.length');
  check('住宅分类下有可建条目', bItems > 0, `items=${bItems}`);
  // 挑一个 1x1 的建筑：多格建筑要求占地内地面完全等高，而前面的抬高步骤
  // 恰好把附近堆成了台地，1x1 才是与地形无关的稳定用例。
  const oneIdx = await evalJs('ISO.state.items.findIndex((i) => i.fp && i.fp[0] === 1 && i.fp[1] === 1)');
  check('住宅分类里有 1x1 建筑可选', oneIdx >= 0, `index=${oneIdx}`);
  await clickItem(oneIdx >= 0 ? oneIdx : 0);
  const selName = await evalJs('ISO.state.selectedItem && ISO.state.selectedItem.name');
  const bBefore = await evalJs('ISO.city.objects.length');
  let placed = false;
  let usedSpot = null;
  const spots = [[0, -80], [-60, -80], [60, -80], [120, -60], [-120, -60], [0, -140], [80, 60], [-80, 60]];
  for (const [dx, dy] of spots) {
    await click(dragFrom.x + dx, dragFrom.y + dy);
    const n = await evalJs('ISO.city.objects.length');
    if (n > bBefore) { placed = true; usedSpot = [dx, dy]; break; }
  }
  check('放置建筑成功', placed, `选中 ${selName}，对象数 ${bBefore} → ${await evalJs('ISO.city.objects.length')}（落点偏移 ${JSON.stringify(usedSpot)}）`);

  // ---- 5. 右键拆除 ----
  const target = await evalJs('(()=>{const o=ISO.city.objects[ISO.city.objects.length-1];return o?JSON.stringify({x:o.x,y:o.y}):null;})()');
  if (target) {
    const t = JSON.parse(target);
    const scr = await evalJs(`(()=>{const c=ISO.city;ISO.view.centerOn(ISO.world,${t.x},${t.y});const p=ISO.view.toScreen(ISO.world,${t.x},${t.y},c.heightAt(${t.x},${t.y}));return JSON.stringify({x:p.x+8,y:p.y+8});})()`);
    const s = JSON.parse(scr);
    const n0 = await evalJs('ISO.city.objects.length');
    await click(Math.round(s.x), Math.round(s.y), 'right');
    const n1 = await evalJs('ISO.city.objects.length');
    check('右键拆除建筑', n1 < n0, `对象数 ${n0} → ${n1}`);
  } else {
    check('右键拆除建筑', false, '找不到可拆除的目标');
  }

  // ---- 6. 撤销：应当把上一步「拆除」恢复回来 ----
  await evalJs("ISO.state.dialog=null; 'ok'");
  const u0 = await evalJs('ISO.city.objects.length');
  await send('Input.dispatchKeyEvent', { type: 'keyDown', key: 'z', code: 'KeyZ', windowsVirtualKeyCode: 90, modifiers: 2 });
  await send('Input.dispatchKeyEvent', { type: 'keyUp', key: 'z', code: 'KeyZ', windowsVirtualKeyCode: 90, modifiers: 2 });
  await sleep(400);
  const u1 = await evalJs('ISO.city.objects.length');
  check('Ctrl+Z 恢复了被拆除的建筑', u1 > u0, `对象数 ${u0} → ${u1}`);

  // ---- 7. 昼夜切换 ----
  await evalJs("ISO.state.autoNight=false;ISO.state.night=true;ISO.assets.night=true;ISO.world.version=-1;'ok'");
  await sleep(600);
  const nightOk = await evalJs('ISO.assets.night === true && ISO.world.night === true');
  check('夜间档切换后重建了世界缓存', nightOk === true);

  // ---- 8. 缩放 ----
  await evalJs("ISO.view.setZoom(2, ISO.world); 'ok'");
  await sleep(300);
  const z = await evalJs('ISO.view.zoom');
  check('缩放可用', z === 2, `zoom=${z}`);

  await evalJs("ISO.view.setZoom(1, ISO.world); ISO.state.night=false; ISO.assets.night=false; ISO.world.version=-1; 'ok'");
  await sleep(500);

  check('全程无未捕获异常', consoleErrors.length === 0, consoleErrors.slice(0, 3).join(' | '));

  const shot = await send('Page.captureScreenshot', { format: 'png' });
  if (shot.result?.data) writeFileSync(out, Buffer.from(shot.result.data, 'base64'));
} catch (err) {
  check('测试脚本执行完成', false, String(err && err.message || err));
} finally {
  try { ws?.close(); } catch { /* ignore */ }
  child.kill('SIGKILL');
  await sleep(200);
  try { rmSync(profile, { recursive: true, force: true }); } catch { /* ignore */ }
}

console.log(results.join('\n'));
console.log(`\n${failed === 0 ? '全部通过' : failed + ' 项失败'}（共 ${results.length} 项）`);
if (failed) process.exitCode = 1;
