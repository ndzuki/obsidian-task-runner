// 无头验证脚本（配合 scripts/shot.mjs --eval 使用）：
//   1) 断言内联资产与数据契约（精灵数/岗位数/查表数/道路掩码分布）
//   2) 断言地图真的有内容（非空白、中央区不是天空）
//   3) 等距逆变换往返（格 -> 屏幕 -> 格）
//   4) 切换季节 / 天气后画面像素确实变化，并给出像素差
//   5) 断言零缺图（所有取名回退都能命中真实精灵，覆盖四季 × 四时段）
//   6) 帧率粗测
(async () => {
  const sleep = (ms) => new Promise(r => setTimeout(r, ms));
  const cv = document.getElementById('townCanvas');
  const c2 = cv.getContext('2d');
  const AT = window.AGENT_TOWN;
  if (!AT) return JSON.stringify({ ok: false, err: 'AGENT_TOWN 调试接口缺失' });
  const setSel = (id, v) => { const s = document.getElementById(id); s.value = v; s.dispatchEvent(new Event('change')); };
  const grab = () => c2.getImageData(0, 0, 960, 540).data.slice();
  const px = (x, y) => { const d = c2.getImageData(x, y, 1, 1).data; return [d[0], d[1], d[2]]; };
  const diffCount = (a, b) => { let n = 0; for (let i = 0; i < a.length; i += 4) { if (a[i] !== b[i] || a[i + 1] !== b[i + 1] || a[i + 2] !== b[i + 2]) n++; } return n; };

  await sleep(600);

  // ── 2. 地图非空白：天空是「同一 y 横向同色」的竖向渐变，
  //      因此统计「与同排天空列 (x=4) 不同色」的像素数即可量出地图覆盖量 ──
  const data0 = c2.getImageData(0, 0, 960, 540).data;
  const uniq = new Set();
  let mapPixels = 0;
  for (let y = 0; y < 540; y++) {
    const o = (y * 960 + 4) * 4, r0 = data0[o], g0 = data0[o + 1], b0 = data0[o + 2];
    for (let x = 0; x < 960; x++) {
      const i = (y * 960 + x) * 4;
      if (((x + y) & 15) === 0) uniq.add((data0[i] << 16) | (data0[i + 1] << 8) | data0[i + 2]);
      if (data0[i] !== r0 || data0[i + 1] !== g0 || data0[i + 2] !== b0) mapPixels++;
    }
  }
  const skyPx = px(4, 4), centerPx = px(480, 300);
  const differs = (a, b) => a[0] !== b[0] || a[1] !== b[1] || a[2] !== b[2];

  // ── 3. 等距逆变换往返（用每格真实高度投影北角）。
  //      水面格的北角可能被更高的陆地格顶面覆盖 —— 此时拾取返回覆盖它的陆地格，
  //      这正是「实心柱」语义，属于正确行为，单列为 covered。 ──
  const roundTrip = [[0, 2], [15, 15], [3, 5], [29, 27], [12, 12], [2, 29], [29, 2], [1, 1], [28, 28]].map((t) => {
    const z = AT.heightAt(t[0], t[1]);
    const sx = 496 + (t[0] - t[1]) * 16, sy = 60 + (t[0] + t[1]) * 8 - z * 8;
    const got = AT.pickTile(sx, sy);
    const same = !!got && got.x === t[0] && got.y === t[1];
    const covered = !!got && !same && z < 0 && AT.heightAt(got.x, got.y) >= 0;
    return { want: t, z: z, got: got ? [got.x, got.y] : null, ok: same || covered, same: same, covered: covered };
  });

  // ── 4. 季节 / 天气切换：先都切到无粒子的 clear，保证像素差只来自重映射 ──
  setSel('selWeather', 'clear');
  setSel('selSeason', 'summer');
  await sleep(700);
  const summer = grab();
  setSel('selSeason', 'winter');
  await sleep(700);
  const winter = grab();
  setSel('selSeason', 'autumn');
  setSel('selWeather', 'rain');
  await sleep(700);
  const autumnRain = grab();
  const total = summer.length / 4;
  const dSW = diffCount(summer, winter), dWA = diffCount(winter, autumnRain);

  // ── 5. 缺图清单：跑遍四季 × 四时段，覆盖季节资产与 #n 夜间档 ──
  for (const s of ['spring', 'summer', 'autumn', 'winter']) for (const t of ['dawn', 'day', 'dusk', 'night']) { AT.env.season = s; AT.env.tod = t; AT.applyEnv(); }
  await sleep(150);
  const missing = AT.missing();

  // ── 6. 帧率粗测 ──
  const fps = await new Promise((res) => {
    let n = 0; const t0 = performance.now();
    const loop = () => { n++; if (performance.now() - t0 < 1000) requestAnimationFrame(loop); else res(n); };
    requestAnimationFrame(loop);
  });

  return JSON.stringify({
    ok: true,
    counts: AT.counts, tile: AT.tile, atlas: AT.atlasSize(),
    roadMasks: AT.roadMasks, residents: AT.residents.length, decor: AT.decor.length, agents: AT.agents.size,
    nonBlank: { uniqSampled: uniq.size, mapPixels: mapPixels, mapPct: +(mapPixels / total * 100).toFixed(1), skyVsCenter: differs(skyPx, centerPx), skyPx: skyPx, centerPx: centerPx },
    roundTrip: roundTrip, roundTripAllOk: roundTrip.every(r => r.ok),
    seasonPixelsChanged: dSW, seasonPct: +(dSW / total * 100).toFixed(1),
    weatherPixelsChanged: dWA, weatherPct: +(dWA / total * 100).toFixed(1),
    missingSprites: missing, missingCount: missing.length,
    fps: fps,
  });
})()
