// 端到端行为验证（配合 stub agent-server 使用）：
//   * 有 agent 时：/agents 轮询 → roster chips → 画布拾取 → 详情面板
//   * 点击 canvas 命中 agent（等距拾取），断言详情面板内容
//   * 加油 / 定位 / 问答（POST /agent/chat）三条交互链路
//   * agent 会沿 A* 路径走向岗位（位置随时间变化、且在可通行格上）
//   * 完工计数来自响应头 x-agents-finished
(async () => {
  const sleep = (ms) => new Promise(r => setTimeout(r, ms));
  const AT = window.AGENT_TOWN;
  if (!AT) return JSON.stringify({ ok: false, err: 'AGENT_TOWN 缺失' });
  const out = { ok: true, steps: {} };

  await sleep(2600);   // 等首轮 /agents 轮询

  out.agents = AT.agents.size;
  out.finished = document.getElementById('finished').textContent;
  out.count = document.getElementById('count').textContent;
  out.rosterItems = document.querySelectorAll('#roster .roster-item').length;
  out.kbLoaded = !document.querySelector('#kbStats .empty');

  // 等 agent 走到岗位（速度 ~30px/s，最长路径约 20 格）
  const snap = () => Array.from(AT.agents.values()).map(a => ({ sid: a.sid.slice(-6), x: +a.x.toFixed(2), y: +a.y.toFixed(2), st: a.state, dir: a.dir, frame: a.frame }));
  const p0 = snap();
  await sleep(3000);
  const p1 = snap();
  const moved = p0.filter((a, i) => Math.abs(a.x - p1[i].x) > 0.01 || Math.abs(a.y - p1[i].y) > 0.01).length;
  out.steps.movedAgents = moved;
  out.before = p0; out.after = p1;
  out.states = p1.map(a => a.st);
  out.frames = p1.map(a => a.frame);
  // 所有 agent 都在陆地上
  out.allOnLand = Array.from(AT.agents.values()).every(a => AT.heightAt(Math.round(a.x), Math.round(a.y)) >= 0);

  // 画布拾取：把每个 agent 的锚点投影成画布坐标，反查应命中它自己
  const picks = [];
  for (const a of AT.agents.values()) {
    const sx = 496 + (a.x - a.y) * 16, sy = 60 + (a.x + a.y) * 8 - AT.heightAt(Math.round(a.x), Math.round(a.y)) * 8;
    const hit = AT.pickResident(sx, sy - 8);   // 取躯干位置
    picks.push({ sid: a.sid.slice(-6), hit: hit ? (hit.kind + ':' + (hit.sid || hit.stage || '').slice(-6)) : null, ok: !!hit && hit.sid === a.sid });
  }
  out.steps.picks = picks;
  out.steps.pickAllOk = picks.every(p => p.ok);

  // 详情面板：直接调 showDetail（roster 内联 onclick 用的同一入口）
  const first = Array.from(AT.agents.values())[0];
  window.showDetail(first.sid);
  await sleep(120);
  out.steps.detailShown = document.getElementById('detail').classList.contains('show');
  out.steps.detailTitle = document.getElementById('dTitle').textContent;
  out.steps.detailKv = Array.from(document.querySelectorAll('#dKv dt')).map(d => d.textContent);
  out.steps.detailText = document.getElementById('dKv').textContent.replace(/\s+/g, ' ').slice(0, 220);

  // 加油 / 定位
  document.getElementById('dCheer').click();
  await sleep(60);
  out.steps.cheerToast = document.getElementById('toast').textContent;
  document.getElementById('dLocate').click();
  await sleep(60);
  out.steps.locateToast = document.getElementById('toast').textContent;

  // 问答：填写 provider/model 后发送，走 POST /agent/chat（stub）
  window.showDetail(first.sid);
  document.getElementById('dGrill').click();
  await sleep(80);
  document.getElementById('cProvider').value = 'stub-provider';
  document.getElementById('cModel').value = 'stub-model';
  document.getElementById('cInput').value = '等距版支持哪些天气？';
  document.getElementById('cSend').click();
  await sleep(500);
  out.steps.chatOpen = !document.getElementById('chat').classList.contains('hidden');
  out.steps.chatMsgs = Array.from(document.querySelectorAll('#cBody .msg')).map(m => m.className + ': ' + m.textContent.slice(0, 60));
  document.getElementById('cClose').click();

  // 空态：无 agent 时小镇仍完整（这里只验证元素存在与文案）
  out.steps.emptyText = document.getElementById('empty').textContent;
  out.steps.legendItems = document.querySelectorAll('#legend span').length;
  return JSON.stringify(out);
})()
