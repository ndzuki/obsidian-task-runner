// ISO-CITY '98 — 入口。
//
// 架构：Go 离线预渲染管线产出图集与 manifest；这里只负责
//   1) 用精灵图合成等距世界（整图缓存 + 视口 blit）
//   2) 同年代风格的 UI 与编辑工具
//   3) CRT 后处理，把整屏压回有限色板

import { Assets } from './assets.js';
import { City, TERRAIN, ROADS, generateIsland, MAX_HEIGHT } from './city.js';
import { WorldRenderer, TILE_W, TILE_H, PX_SCALE, configureTile } from './render.js';
import { View, VIEW, pickCache } from './view.js';
import { UI, LAYOUT, UI_SCALE, px, TOOLS, STATUS_BUTTONS, hitTest, hoverIndices, itemsForTool, categoryList, configureLayout } from './ui.js';
import { Font } from './font.js';
import { CRT } from './crt.js';
import { ToolRunner } from './tools.js';

// 内部帧缓冲尺寸随像素密度变化（1x = 640x400，2x = 1280x800）
const BASE_W = 640, BASE_H = 400;
let W = BASE_W, H = BASE_H;
const SAVE_KEY = 'isocity98.save.v1';
const OPT_KEY = 'isocity98.opt.v1';

const state = {
  toolIndex: 0,
  itemIndex: 0,
  itemPage: 0,
  categoryIndex: 0,
  tabsActive: false,
  items: [],
  selectedItem: null,
  toolHint: '',
  toolName: '',
  night: false,
  autoNight: true,
  growth: true,
  speed: 1,
  clock: 8 * 60, // 分钟
  day: 1,
  fps: 0,
  messages: [],
  intro: 2.2,
  dialog: null,
  showGrid: false,
  brush: 1,
};

let canvas, displayCtx, scene, sceneCtx, assets, pal, city, world, view, ui, font, crt, runner;
let dragging = false;
let dragButton = 0;
let panning = false;
let panLast = null;
let scaleFactor = 1;
let lastTime = 0;
let fpsAccum = 0;
let fpsFrames = 0;
const keys = new Set();

main().catch((err) => {
  console.error(err);
  const boot = document.getElementById('boot');
  if (boot) {
    boot.classList.remove('done');
    boot.innerHTML = `<div class="logo">BOOT FAILURE</div><div class="err">${escapeHtml(String(err && err.message || err))}</div>`;
  }
});

async function main() {
  canvas = document.getElementById('screen');
  displayCtx = canvas.getContext('2d', { alpha: false });
  scene = document.createElement('canvas');
  scene.width = W;
  scene.height = H;
  // 帧缓冲始终不透明：用 alpha:false 让 canvas 走更快的合成路径
  sceneCtx = scene.getContext('2d', { alpha: false });

  const bootbar = document.getElementById('bootbar');
  const bootmsg = document.getElementById('bootmsg');
  assets = await Assets.load(document.baseURI, (p, msg) => {
    if (bootbar) bootbar.style.width = `${Math.round(p * 100)}%`;
    if (bootmsg && msg) bootmsg.textContent = msg;
  });
  pal = assets.pal;

  // 像素密度由离线管线写进 manifest：这里据此配置瓦片尺寸、界面布局与帧缓冲
  const S = assets.scale || 1;
  configureTile(assets.tile, S);
  configureLayout(S);
  W = BASE_W * S;
  H = BASE_H * S;
  canvas.width = W;
  canvas.height = H;
  scene.width = W;
  scene.height = H;
  sceneCtx.imageSmoothingEnabled = false;

  font = new Font(pal, { size: 12 * S });
  ui = new UI(pal, font, assets);
  crt = new CRT(pal);
  crt.onDegrade = (ms) => pushMessage(
    `后处理耗时 ${ms.toFixed(0)}ms，已切到「仅扫描线」；按 C 可切回全效果`);
  // 全效果档的逐像素循环开销与帧缓冲像素数成正比：
  // 1x/2x（≤1.05M 像素）约 3~11ms，很划算；3x（2.3M）要 ~21ms，会明显压帧。
  // 所以在高密度下默认用「仅扫描线」（纯 canvas 合成，约 1.6ms），按 C 可随时切回。
  const autoMode = (W * H) <= 1_400_000 ? 2 : 1;
  world = new WorldRenderer(assets);

  const saved = localStorage.getItem(SAVE_KEY);
  city = saved ? City.fromJSON(JSON.parse(saved)) : generateIsland(64, 64, 20250911);
  world.assets = assets;
  runner = new ToolRunner(city, assets);
  runner.onMessage = pushMessage;
  runner.onChange = () => {};
  runner.onBeforeEdit = snapshot;

  view = new View();
  const c0 = city.center();
  world.ensure(city);
  view.centerOn(world, c0.x, c0.y);

  // 首次进入（无存档）时先给一座小镇：沙盒的第一印象不该是一片空地
  if (!saved) {
    buildDemoTown();
    view.centerOn(world, c0.x, c0.y);
  }

  loadOptions();
  // 分辨率变了就重新自动选档；用户显式切过（同分辨率下）则尊重用户选择
  if (state.optLoaded.scale !== S || !state.optLoaded.crtMode) crt.mode = autoMode;
  selectTool(0, true);
  if (crt.mode !== 2) {
    pushMessage(`像素密度 ${S}x：默认「仅扫描线」后处理；按 C 切换全效果 CRT`);
  }

  window.addEventListener('resize', resize);
  resize();
  bindInput();

  const boot = document.getElementById('boot');
  if (boot) boot.classList.add('done');

  requestAnimationFrame(frame);
}

/**
 * 显示缩放。
 *
 * 两个要点：
 *  1. 按**物理像素**取整：devicePixelRatio = 1.25/1.5 这类屏幕上若按 CSS 像素取整，
 *     物理缩放就是小数，浏览器会重采样 → 整屏发虚；
 *  2. 放不下时**按比例缩小到刚好放下**，而不是硬撑 1:1 把画面裁掉
 *     （3x 的内部帧缓冲是 1920x1200，小窗口必须能完整显示）。
 *     整数倍用最近邻（锐），缩小时用平滑（否则会丢像素、闪烁）。
 */
function resize() {
  const dpr = window.devicePixelRatio || 1;
  const availW = window.innerWidth * dpr;
  const availH = window.innerHeight * dpr;
  let k = Math.min(availW / W, availH / H);
  const crisp = k >= 1;
  if (crisp) k = Math.floor(k);
  k = Math.max(k, 0.1);
  scaleFactor = k / dpr; // CSS 像素 / 帧缓冲像素
  canvas.style.width = `${(W * k) / dpr}px`;
  canvas.style.height = `${(H * k) / dpr}px`;
  canvas.style.imageRendering = crisp ? 'pixelated' : 'auto';
}

function bindInput() {
  canvas.addEventListener('contextmenu', (e) => e.preventDefault());
  canvas.addEventListener('mousedown', onDown);
  window.addEventListener('mouseup', onUp);
  canvas.addEventListener('mousemove', onMove);
  canvas.addEventListener('wheel', onWheel, { passive: false });
  canvas.addEventListener('mouseleave', () => { view.hover = null; });
  window.addEventListener('keydown', onKeyDown);
  window.addEventListener('keyup', (e) => keys.delete(e.key.toLowerCase()));
}

/** 客户端坐标 → 640x400 帧缓冲坐标。 */
function toFrame(e) {
  const r = canvas.getBoundingClientRect();
  return {
    x: Math.floor((e.clientX - r.left) / scaleFactor),
    y: Math.floor((e.clientY - r.top) / scaleFactor),
  };
}

function onDown(e) {
  const p = toFrame(e);
  if (state.intro > 0) { state.intro = 0; return; }
  if (state.dialog) { handleDialogClick(p); return; }
  const hit = hitTest(state, p.x, p.y);
  state.lastDown = { at: { x: p.x, y: p.y }, kind: hit.kind, tile: null };
  if (hit.kind === 'tool') { selectTool(hit.index); return; }
  if (hit.kind === 'daynight') { state.night = !state.night; assets.night = state.night; state.autoNight = false; return; }
  if (hit.kind === 'status' && hit.index >= 0) { onStatusButton(hit.index); return; }
  if (hit.kind === 'tab') { state.categoryIndex = hit.index; state.itemPage = 0; refreshItems(); return; }
  if (hit.kind === 'item') { selectItem(hit.index); return; }
  if (hit.kind === 'status') { onStatusClick(p); return; }
  if (hit.kind === 'panel') {
    const t = ui.mapHit(city, p.x, p.y);
    if (t) view.centerOn(world, t.x, t.y);
    return;
  }
  // 视口内
  if (p.x < LAYOUT.view.x || p.y < LAYOUT.view.y || p.y >= LAYOUT.view.y + LAYOUT.view.h) return;
  dragging = true;
  dragButton = e.button;
  if (e.button === 1 || (e.button === 0 && keys.has(' '))) {
    panning = true; panLast = p; dragging = false; return;
  }
  const tile = view.pick(city, p.x, p.y);
  // 供自动化脚本定位「点为什么没生效」：记录最近一次按下的命中类型与格子
  state.lastDown = { at: { x: p.x, y: p.y }, kind: 'view', tile };
  runner.begin(tile, { erase: e.button === 2 });
}

function onUp() {
  if (dragging) runner.end();
  dragging = false;
  panning = false;
  panLast = null;
}

function onMove(e) {
  const p = toFrame(e);
  state.mouse = p;
  if (state.intro > 0) return;
  const hov = hoverIndices(state, p.x, p.y);
  ui.hoverTool = hov.tool;
  ui.hoverTab = hov.tab;
  ui.hoverItem = hov.item;
  ui.hoverStatus = hov.status;

  if (panning && panLast) {
    view.pan((panLast.x - p.x) / view.zoom, (panLast.y - p.y) / view.zoom);
    view.clamp(world);
    panLast = p;
    return;
  }
  const inView = p.x >= LAYOUT.view.x && p.y >= LAYOUT.view.y && p.y < LAYOUT.view.y + LAYOUT.view.h;
  view.hover = inView ? view.pick(city, p.x, p.y) : null;
  if (dragging && view.hover) runner.drag(view.hover);
}

function onWheel(e) {
  const p = toFrame(e);
  e.preventDefault();
  const hit = hitTest(state, p.x, p.y);
  if (hit.kind === 'item' || hit.kind === 'panel') {
    const perPage = LAYOUT.items.cols * LAYOUT.items.rows;
    const pages = Math.max(1, Math.ceil(state.items.length / perPage));
    state.itemPage = (state.itemPage + (e.deltaY > 0 ? 1 : pages - 1)) % pages;
    return;
  }
  if (e.ctrlKey || e.shiftKey) {
    view.setZoom(view.zoom + (e.deltaY > 0 ? -1 : 1), world, p.x - VIEW.x, p.y - VIEW.y);
  } else {
    view.pan(0, e.deltaY > 0 ? 24 : -24);
    view.clamp(world);
  }
}

function onStatusButton(i) {
  const id = STATUS_BUTTONS[i] && STATUS_BUTTONS[i].id;
  if (id === 'new') { ISO.newMap(); pushMessage('已生成新的岛屿地图'); }
  else if (id === 'demo') { buildDemoTown(); }
  else if (id === 'help') { showHelp(); }
}

function onKeyDown(e) {
  const k = e.key.toLowerCase();
  if (state.intro > 0) { state.intro = 0; return; }
  if (state.dialog) {
    if (k === 'escape' || k === 'enter') state.dialog = null;
    return;
  }
  keys.add(k);
  const step = e.shiftKey ? 12 : 4;
  if (k === 'arrowleft' || k === 'a') { view.pan(-step, 0); view.clamp(world); e.preventDefault(); }
  else if (k === 'arrowright' || k === 'd') { view.pan(step, 0); view.clamp(world); e.preventDefault(); }
  else if (k === 'arrowup' || k === 'w') { view.pan(0, -step); view.clamp(world); e.preventDefault(); }
  else if (k === 'arrowdown' || k === 's') { view.pan(0, step); view.clamp(world); e.preventDefault(); }
  else if (k === '+' || k === '=') view.setZoom(view.zoom + 1, world);
  else if (k === '-' || k === '_') view.setZoom(view.zoom - 1, world);
  else if (k === ' ') { state.night = !state.night; assets.night = state.night; e.preventDefault(); }
  else if (k === 'g') state.showGrid = !state.showGrid;
  else if (k === 'n') { state.autoNight = !state.autoNight; pushMessage(state.autoNight ? '自动昼夜 开' : '自动昼夜 关'); }
  else if (k === 'p') { state.growth = !state.growth; pushMessage(state.growth ? '城市成长 开' : '城市成长 关'); }
  else if (k === 'c') {
    crt.mode = (crt.mode + 1) % 3;
    pushMessage(`CRT 后处理：${['关闭', '仅扫描线', '全效果'][crt.mode]}`);
  }
  else if (k === 'f1') { showHelp(); e.preventDefault(); }
  else if (k === 'escape') { state.dialog = null; }
  else if ((e.ctrlKey || e.metaKey) && k === 'z') { undo(); e.preventDefault(); }
  else if ((e.ctrlKey || e.metaKey) && k === 's') { save(); e.preventDefault(); }
  else if ((e.ctrlKey || e.metaKey) && k === 'o') { loadSave(); e.preventDefault(); }
  else if (k === '[') { state.brush = Math.max(0, state.brush - 1); runner.brush = state.brush; }
  else if (k === ']') { state.brush = Math.min(4, state.brush + 1); runner.brush = state.brush; }
  else if (k >= '1' && k <= '9') { selectTool(+k - 1); }
  else if (k === 'tab') {
    state.categoryIndex = (state.categoryIndex + 1) % categoryList().length;
    state.itemPage = 0; refreshItems(); e.preventDefault();
  }
  else if (k === 'q') { pageItems(-1); }
  else if (k === 'e') { pageItems(1); }
}

function pageItems(dir) {
  const perPage = LAYOUT.items.cols * LAYOUT.items.rows;
  const pages = Math.max(1, Math.ceil(state.items.length / perPage));
  state.itemPage = (state.itemPage + dir + pages) % pages;
}

// ---------------------------------------------------------------- 工具与目录

function selectTool(i, silent = false) {
  state.toolIndex = i;
  const t = TOOLS[i];
  state.toolName = t.name;
  state.toolHint = t.hint;
  state.tabsActive = t.id === 'build';
  if (!silent) state.itemIndex = 0;
  refreshItems();
  runner.setTool(t.id, state.selectedItem);
  if (!silent) pushMessage(`${t.name} — ${t.hint}`);
}

function refreshItems() {
  state.items = itemsForTool(TOOLS[state.toolIndex].id, assets, state.categoryIndex, state.itemPage);
  const perPage = LAYOUT.items.cols * LAYOUT.items.rows;
  const pages = Math.max(1, Math.ceil(state.items.length / perPage));
  if (state.itemPage >= pages) state.itemPage = 0;
  if (state.itemIndex >= state.items.length) state.itemIndex = 0;
  state.selectedItem = state.items[state.itemIndex] || null;
  runner.setTool(TOOLS[state.toolIndex].id, state.selectedItem);
}

function selectItem(i) {
  state.itemIndex = i;
  state.selectedItem = state.items[i] || null;
  runner.setTool(TOOLS[state.toolIndex].id, state.selectedItem);
  if (state.selectedItem) pushMessage(`${state.selectedItem.label}`);
}

function pushMessage(text) {
  state.messages.push({ text, t: 4.5 });
  if (state.messages.length > 4) state.messages.shift();
}

// ---------------------------------------------------------------- 存档 / 撤销

const undoStack = [];
let undoLock = false;

function snapshot() {
  if (undoLock) return;
  undoStack.push(city.toJSON());
  if (undoStack.length > 16) undoStack.shift();
}

function undo() {
  const s = undoStack.pop();
  if (!s) { pushMessage('没有可撤销的操作'); return; }
  undoLock = true;
  city = City.fromJSON(s);
  runner.setCity(city);
  world.version = -1;
  undoLock = false;
  pushMessage('已撤销');
}

function save() {
  try {
    localStorage.setItem(SAVE_KEY, JSON.stringify(city.toJSON()));
    pushMessage('已保存到浏览器本地存储');
  } catch (err) {
    pushMessage('保存失败：' + err.message);
  }
}

function loadSave() {
  const raw = localStorage.getItem(SAVE_KEY);
  if (!raw) { pushMessage('没有找到存档'); return; }
  city = City.fromJSON(JSON.parse(raw));
  runner.setCity(city);
  world.version = -1;
  const c = city.center();
  world.ensure(city);
  view.centerOn(world, c.x, c.y);
  pushMessage('已读取存档');
}

function loadOptions() {
  state.optLoaded = {};
  try {
    const o = JSON.parse(localStorage.getItem(OPT_KEY) || '{}');
    state.optLoaded = o;
    if (o.crtMode !== undefined) crt.mode = o.crtMode;
    if (o.autoNight !== undefined) state.autoNight = o.autoNight;
    if (o.growth !== undefined) state.growth = o.growth;
    if (o.zoom) view.zoom = o.zoom;
  } catch { /* 忽略损坏的本地设置 */ }
}

function saveOptions() {
  try {
    localStorage.setItem(OPT_KEY, JSON.stringify({
      crtMode: crt.mode, scale: PX_SCALE,
      autoNight: state.autoNight, growth: state.growth, zoom: view.zoom,
    }));
  } catch { /* 忽略 */ }
}

// ---------------------------------------------------------------- 对话与帮助

function showHelp() {
  state.dialog = {
    title: '操作说明',
    w: 420, h: 208,
    lines: [
      '左键 施工 / 右键 拆除      滚轮 平移   Shift+滚轮 缩放',
      'WASD 或方向键 平移          + / - 缩放',
      '1-9 选择工具                [ ] 调整笔刷半径',
      '空格 昼夜切换               N 自动昼夜   P 城市成长',
      'G 网格   C 切换 CRT 效果     Q / E 目录翻页   TAB 切换分类',
      'Ctrl+Z 撤销   Ctrl+S 保存   Ctrl+O 读档   F1 本说明',
      '',
      '玩法：只保留地形编辑与城市建造，没有财政与分区压力。',
      '人口与岗位只作为观测量；打开「城市成长」后，临街的地块会',
      '逐步升级成更高密度的建筑。',
    ],
    buttons: [
      { label: '存档', action: () => save() },
      { label: '读档', action: () => loadSave() },
      { label: '关 闭', action: () => { state.dialog = null; } },
    ],
  };
}

function handleDialogClick(p) {
  const d = state.dialog;
  for (const b of d.buttons || []) {
    if (b.rect && p.x >= b.rect.x && p.x < b.rect.x + b.rect.w && p.y >= b.rect.y && p.y < b.rect.y + b.rect.h) {
      const keep = d.persist;
      if (!keep) state.dialog = null;
      b.action();
      return;
    }
  }
  state.dialog = null;
}

// ---------------------------------------------------------------- 轻量模拟

let simAccum = 0;
let growthAccum = 0;

function simulate(dt) {
  if (state.speed === 0) return;
  const mult = state.speed;
  state.clock += dt * 60 * mult * 0.6; // 1 秒 ≈ 36 游戏分钟
  if (state.clock >= 1440) { state.clock -= 1440; state.day++; }
  const hour = state.clock / 60;
  if (state.autoNight) {
    const want = hour < 6 || hour >= 19;
    if (want !== state.night) { state.night = want; assets.night = want; }
  }
  simAccum += dt;
  if (state.growth) {
    growthAccum += dt * mult;
    if (growthAccum > 1.6) {
      growthAccum = 0;
      tryGrowth();
    }
  }
}

const CHAINS = {
  住宅: ['house.small', 'house.suburban', 'house.townhouse', 'apartment.walkup', 'apartment.tower'],
  商业: ['shop.corner', 'cafe', 'shop.row', 'supermarket', 'office.block', 'department', 'hotel', 'office.tower'],
  工业: ['warehouse', 'sawmill', 'factory', 'refinery', 'powerplant'],
};

/** 城市成长：临街建筑按等级链升档，纯观感变化，不影响玩法平衡。 */
function tryGrowth() {
  const cands = city.objects.filter((o) => CHAINS[o.category]);
  if (!cands.length) return;
  for (let attempt = 0; attempt < 3; attempt++) {
    const o = cands[Math.floor(Math.random() * cands.length)];
    const chain = CHAINS[o.category];
    const idx = chain.indexOf(o.name);
    if (idx < 0 || idx >= chain.length - 1) continue;
    const nextName = chain[idx + 1];
    const nextVariants = assets.variants(nextName);
    if (!nextVariants.length) continue;
    if (!hasRoadNear(o)) continue;
    const def = assets.get(nextVariants[0]);
    const fp = def.fp || [1, 1];
    // 占地变化时，先看新占地是否放得下（含旧建筑自身占的格）
    const freed = new Set();
    for (let dy = 0; dy < o.fh; dy++) for (let dx = 0; dx < o.fw; dx++) freed.add(`${o.x + dx},${o.y + dy}`);
    let fits = true;
    for (let dy = 0; dy < fp[1] && fits; dy++) {
      for (let dx = 0; dx < fp[0]; dx++) {
        const x = o.x + dx, y = o.y + dy;
        if (!city.inside(x, y)) { fits = false; break; }
        const i = city.idx(x, y);
        const ref = city.occ[i];
        const owner = ref > 0 ? city.objects[ref - 1] : null;
        if (owner && owner !== o) { fits = false; break; }
        if (city.road[i] > 0) { fits = false; break; }
        if (city.height[i] !== city.height[city.idx(o.x, o.y)]) { fits = false; break; }
      }
    }
    if (!fits) continue;
    const pos = { x: o.x, y: o.y };
    city.removeAt(pos.x, pos.y);
    city.place({
      name: nextName, sprite: assets.pickVariant(nextName), label: def.label, category: def.category, fp,
      level: def.level || 1, pop: def.pop || 0, jobs: def.jobs || 0, cost: def.cost || 0,
    }, pos.x, pos.y);
    return;
  }
}

function hasRoadNear(o) {
  for (let dy = -1; dy <= o.fh; dy++) {
    for (let dx = -1; dx <= o.fw; dx++) {
      const x = o.x + dx, y = o.y + dy;
      if (!city.inside(x, y)) continue;
      if (city.road[city.idx(x, y)] > 0) return true;
    }
  }
  return false;
}

// ---------------------------------------------------------------- 主循环

function frame(now) {
  const dt = Math.min(0.1, (now - lastTime) / 1000 || 0.016);
  lastTime = now;
  const tFrame = performance.now();
  fpsAccum += dt;
  fpsFrames++;
  if (fpsAccum > 0.5) {
    state.fps = fpsFrames / fpsAccum;
    fpsAccum = 0;
    fpsFrames = 0;
  }

  if (state.intro > 0) state.intro -= dt;
  simulate(dt);
  world.animateWater(city, dt * 1000 * (state.speed || 1));
  for (const m of state.messages) m.t -= dt;
  while (state.messages.length && state.messages[0].t <= 0) state.messages.shift();

  const tCompose = performance.now();
  compose(dt);
  const tPost = performance.now();
  crt.present(scene, displayCtx, W, H);
  const tEnd = performance.now();
  state.simMs = tCompose - tFrame;
  state.composeMs = tPost - tCompose;
  state.postMs = tEnd - tPost;
  state.frameMs = tEnd - tFrame;

  if (Math.floor(now / 4000) !== Math.floor((now - dt * 1000) / 4000)) saveOptions();
  requestAnimationFrame(frame);
}

function compose() {
  const s = sceneCtx;
  s.imageSmoothingEnabled = false;
  s.fillStyle = '#05060a';
  s.fillRect(0, 0, W, H);

  const fp = state.selectedItem && (TOOLS[state.toolIndex].id === 'build' || TOOLS[state.toolIndex].id === 'prop')
    ? (state.selectedItem.fp || [1, 1]) : [1, 1];
  let valid = true;
  if (view.hover && (state.toolIndex === 6 || state.toolIndex === 7)) {
    const allowWater = !!(state.selectedItem && state.selectedItem.def && state.selectedItem.def.water);
    valid = city.canPlace(view.hover.x, view.hover.y, fp[0], fp[1], { allowWater });
  }
  view.draw(s, city, world, { footprint: fp, valid });

  if (state.showGrid) drawGrid(s);

  composeMessages(s);
  composeStatusAndPanel(s);
  composeTooltip(s);
  if (state.dialog) ui.dialog(s, state.dialog);
  if (state.intro > 0) drawIntro(s);
}

function composeStatusAndPanel(s) {
  const sel = state.selectedItem;
  const stats = city.stats();
  ui.drawStatus(s, {
    ...stats,
    night: state.night,
    day: state.day,
    fps: state.fps,
    toolName: state.toolName,
    selectionText: sel ? `${TOOLS[state.toolIndex].name} · ${sel.label}` : state.toolName,
    clock: `${String(Math.floor(state.clock / 60)).padStart(2, '0')}:${String(Math.floor(state.clock % 60)).padStart(2, '0')}`,
  });
  ui.drawPanel(s, {
    city,
    toolIndex: state.toolIndex,
    tabsActive: state.tabsActive,
    categoryIndex: state.categoryIndex,
    items: state.items,
    itemIndex: state.itemIndex,
    itemPage: state.itemPage,
    selectedItem: sel,
    toolHint: state.toolHint,
  });
  // 视口内的相机取景框（小地图）
  const A = (view.camX - world.originX) / (TILE_W / 2);
  const B = (view.camY - world.originY) / (TILE_H / 2);
  const corners = [[0, 0], [VIEW.w / view.zoom, 0], [0, VIEW.h / view.zoom], [VIEW.w / view.zoom, VIEW.h / view.zoom]];
  let minX = 1e9, minY = 1e9, maxX = -1e9, maxY = -1e9;
  for (const [px, py] of corners) {
    const a = A + px / (TILE_W / 2);
    const b = B + py / (TILE_H / 2);
    const tx = (b + a) / 2, ty = (b - a) / 2;
    minX = Math.min(minX, tx); maxX = Math.max(maxX, tx);
    minY = Math.min(minY, ty); maxY = Math.max(maxY, ty);
  }
  ui.drawMinimap(s, city, { x: minX, y: minY, w: maxX - minX, h: maxY - minY });
}

/** 悬浮提示：给按钮和目录条目补上名字/造价（信息框放不下的部分）。 */
function composeTooltip(s) {
  const m = state.mouse;
  if (!m || state.dialog) return;
  let text = null;
  if (ui.hoverItem >= 0 && ui.hoverItem < state.items.length) {
    const it = state.items[ui.hoverItem];
    text = it.label || it.name;
    if (it.cost) text += `  $${it.cost}`;
    if (it.pop) text += `  人口+${it.pop}`;
    if (it.jobs) text += `  岗位+${it.jobs}`;
  } else if (ui.hoverTool >= 0) {
    const t = TOOLS[ui.hoverTool];
    text = `${t.name}  ${t.hint}`;
  } else if (ui.hoverStatus >= 0) {
    text = { new: '生成新的岛屿地图', demo: '在当前地图上生成示例城镇', help: '操作说明 (F1)' }[STATUS_BUTTONS[ui.hoverStatus].id];
  }
  if (text) ui.tooltip(s, text, m.x, m.y);
}

function composeMessages(s) {
  let y = VIEW.y + px(4);
  for (const m of state.messages) {
    const w = font.textWidth(m.text) + px(8);
    s.fillStyle = `rgba(10,10,14,${Math.min(1, m.t) * 0.72})`;
    s.fillRect(VIEW.x + px(4), y, w, font.lineHeight + px(3));
    s.fillStyle = 'rgba(47,191,176,0.5)';
    s.fillRect(VIEW.x + px(4), y, px(2), font.lineHeight + px(3));
    font.draw(s, m.text, VIEW.x + px(9), y + px(2), ui.C.text);
    y += font.lineHeight + px(4);
  }
}

function drawGrid(s) {
  const z = view.zoom;
  if (z < 2) return;
  s.save();
  s.beginPath();
  s.rect(VIEW.x, VIEW.y, VIEW.w, VIEW.h);
  s.clip();
  s.fillStyle = 'rgba(159,232,224,0.16)';
  const hover = view.hover;
  if (hover) {
    for (let dy = -2; dy <= 2; dy++) {
      for (let dx = -2; dx <= 2; dx++) {
        const x = hover.x + dx, y = hover.y + dy;
        if (!city.inside(x, y)) continue;
        const p = view.toScreen(world, x, y, city.heightAt(x, y));
        s.fillRect(Math.round(p.x - px(1)), Math.round(p.y + TILE_H / 2 * z - px(1)), px(2), px(2));
      }
    }
  }
  s.restore();
}

function drawIntro(s) {
  // t 从 1（刚启动）降到 0：开头快速淡入、中段保持、结尾淡出
  const t = Math.min(1, Math.max(0, state.intro / 2.2));
  const a = t > 0.92 ? (1 - t) / 0.08 : t < 0.22 ? t / 0.22 : 1;
  s.fillStyle = `rgba(5,6,10,${Math.min(1, a * 1.1)})`;
  s.fillRect(0, 0, W, H);
  if (a < 0.35) return;
  // 标题卡：色板条 + 标题，模仿当年的启动画面
  const S = UI_SCALE;
  ui.bevel(s, 96 * S, 116 * S, 448 * S, 158 * S, true, ui.C.faceDark);
  font.drawCenter(s, 'ISO-CITY 98', W / 2, 128 * S, ui.C.accent);
  font.drawCenter(s, 'PRE-RENDERED ISOMETRIC CITY SANDBOX', W / 2, 146 * S, ui.C.textDim);
  // 母板色条：一行 48 格，直观展示「整屏就这些颜色」
  for (let i = 0; i < pal.colors.length; i++) {
    const c = pal.colors[i];
    s.fillStyle = `rgb(${c[0]},${c[1]},${c[2]})`;
    s.fillRect((116 + (i % 48) * 8) * S, (168 + Math.floor(i / 48) * 10) * S, 7 * S, 9 * S);
  }
  const barRows = Math.ceil(pal.colors.length / 48);
  font.drawCenter(s, `${pal.colors.length} COLOR MASTER PALETTE`, W / 2, (168 + barRows * 10 + 6) * S, ui.C.textDim);
  font.drawCenter(s, '点击或按任意键开始', W / 2, 246 * S, ui.C.text);
}

function escapeHtml(s) {
  return s.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
}

// 暴露给控制台做调试（也方便自动化截图断言）
window.ISO = {
  state, get city() { return city; }, get world() { return world; }, get view() { return view; },
  get assets() { return assets; }, get crt() { return crt; },
  // 供自动化脚本按其真实布局计算点击坐标，避免把布局常量抄进测试
  layout: LAYOUT, tools: TOOLS,
  get scale() { return PX_SCALE; },
  get frame() { return { w: W, h: H }; },
  get postMs() { return state.postMs || 0; },
  get composeMs() { return state.composeMs || 0; },
  get frameMs() { return state.frameMs || 0; },
  get runner() { return runner; },
  get font() { return font; },
  crtMode(m) { crt.mode = m; },
  /** 帧缓冲内容指纹：用于验证「整图缓存」与「逐帧直绘」两条路径画面一致。 */
  frameHash() {
    const d = sceneCtx.getImageData(0, 0, W, H).data;
    let h = 2166136261 >>> 0;
    for (let i = 0; i < d.length; i += 17) {
      h ^= d[i];
      h = Math.imul(h, 16777619) >>> 0;
    }
    return h.toString(16);
  },

  toolButton(i) {
    const L = LAYOUT.tools, c = i % L.cols, r = Math.floor(i / L.cols);
    return { x: L.x + c * (L.bw + L.gap) + L.bw / 2, y: L.y + r * (L.bh + L.gap) + L.bh / 2 };
  },
  itemButton(i) {
    const L = LAYOUT.items, c = i % L.cols, r = Math.floor(i / L.cols);
    return { x: L.x + c * (L.bw + L.gap) + L.bw / 2, y: L.y + r * (L.bh + L.gap) + L.bh / 2 };
  },
  newMap(seed) {
    city = generateIsland(64, 64, seed || Math.floor(Math.random() * 1e9));
    runner.setCity(city);
    world.version = -1;
    const c = city.center();
    world.ensure(city);
    view.centerOn(world, c.x, c.y);
  },
  demo() { buildDemoTown(); },
  help: showHelp,
  setTool(i) { selectTool(i); },
  ready: true,
};

/** 一键生成一座示例小城：先整平场地，再铺路、落建筑（等价于玩家的正常操作顺序）。 */
function buildDemoTown() {
  const c = city.center();
  // 1) 统计场地内的常见海拔，作为整平目标
  const hist = new Map();
  for (let dy = -10; dy <= 10; dy++) {
    for (let dx = -10; dx <= 10; dx++) {
      const x = c.x + dx, y = c.y + dy;
      if (!city.inside(x, y) || city.terrainAt(x, y) === 9) continue;
      const h = city.heightAt(x, y);
      hist.set(h, (hist.get(h) || 0) + 1);
    }
  }
  let target = 2;
  let best = -1;
  for (const [h, n] of hist) if (n > best) { best = n; target = h; }
  target = Math.max(1, Math.min(3, target));
  // 2) 整平场地（保留水面）
  for (let dy = -10; dy <= 10; dy++) {
    for (let dx = -10; dx <= 10; dx++) {
      const x = c.x + dx, y = c.y + dy;
      if (!city.inside(x, y) || city.terrainAt(x, y) === 9) continue;
      city.setHeight(x, y, target);
      if (city.terrainAt(x, y) === 3 && Math.abs(dx) + Math.abs(dy) > 6) continue;
    }
  }
  // 场地铺装：把整平区改成草地，靠近中心的地块铺成硬化地面
  for (let dy = -10; dy <= 10; dy++) {
    for (let dx = -10; dx <= 10; dx++) {
      const x = c.x + dx, y = c.y + dy;
      if (!city.inside(x, y) || city.terrainAt(x, y) === 9) continue;
      const m = Math.max(Math.abs(dx), Math.abs(dy));
      city.setTerrain(x, y, m <= 5 ? 7 : 0);
    }
  }
  // 3) 主干道（十字 + 一条支路）
  for (let i = -9; i <= 9; i++) {
    const along = [[c.x + i, c.y], [c.x, c.y + i]];
    for (const [x, y] of along) {
      if (!city.inside(x, y) || city.terrainAt(x, y) === 9) continue;
      city.setRoad(x, y, 1);
    }
  }
  for (let i = 3; i <= 9; i++) {
    const x = c.x + i, y = c.y - 4;
    if (city.inside(x, y) && city.terrainAt(x, y) !== 9) city.setRoad(x, y, 1);
  }
  for (let i = -9; i <= -5; i++) {
    const x = c.x + i, y = c.y - 4;
    if (city.inside(x, y) && city.terrainAt(x, y) !== 9) city.setRoad(x, y, 1);
  }
  // 4) 落建筑：绕开道路、贴着自己的地界摆放
  const put = (base, x, y) => {
    const variants = assets.variants(base);
    if (!variants.length) return false;
    const def = assets.get(variants[0]);
    const fp = def.fp || [1, 1];
    if (!city.canPlace(x, y, fp[0], fp[1], {})) return false;
    city.place({
      name: base, sprite: assets.pickVariant(base), label: def.label, category: def.category, fp,
      level: def.level || 1, pop: def.pop || 0, jobs: def.jobs || 0, cost: def.cost || 0,
    }, x, y);
    return true;
  };
  const spots = [
    // 市中心
    ['office.tower', -4, -4], ['office.block', 2, -4], ['bank', -1, -6], ['hotel', 5, -8],
    ['shop.row', 2, -1], ['shop.corner', -3, -2], ['shop.corner', 4, -3],
    ['department', -6, -2], ['cinema', -8, -7], ['cafe', 7, -3],
    // 市政
    ['townhall', -6, 2], ['hospital', 6, -6], ['museum', -8, -4], ['library', 4, -7],
    ['church', -3, 4], ['police', -6, 5], ['firestation', -1, 5], ['school', 2, 6],
    ['stadium', 7, 5],
    // 住宅
    ['apartment.tower', 3, 2], ['apartment.walkup', -4, 3],
    ['house.small', -8, -3], ['house.suburban', -8, -6], ['house.townhouse', 5, -6],
    ['house.villa', 6, 3], ['house.small', -8, 1], ['house.suburban', 6, -8],
    ['apartment.walkup', -8, 6],
    // 公园与景观
    ['plaza', -1, 2], ['park.small', -2, 4], ['statue', 1, 2], ['playground', 4, 2],
    ['parking', 8, 0], ['avenue', -6, -1],
    // 工业与基建
    ['warehouse', 5, 5], ['factory', -6, 6], ['powerplant', 9, -6], ['tankfarm', 9, 3],
    ['refinery', 10, 7], ['watertower', -9, -1], ['freight', 9, -2], ['sawmill', -10, 4],
    ['supermarket', -10, -8], ['gasstation', 8, -8],
  ];
  let placed = 0;
  for (const [n, dx, dy] of spots) if (put(n, c.x + dx, c.y + dy)) placed++;
  // 5) 绿化
  for (let i = 0; i < 44; i++) {
    const dx = Math.floor(Math.random() * 21) - 10;
    const dy = Math.floor(Math.random() * 21) - 10;
    const x = c.x + dx, y = c.y + dy;
    if (!city.inside(x, y) || city.terrainAt(x, y) === 9) continue;
    if (city.road[city.idx(x, y)] > 0 || city.objectAt(x, y)) continue;
    put('tree.oak', x, y);
  }
  // 把镜头对准城镇几何中心并略微上移，保证高楼不被视口顶边裁掉
  const objs = city.objects;
  if (objs.length) {
    let cx0 = 1e9, cy0 = 1e9, cx1 = -1e9, cy1 = -1e9;
    for (const o of objs) {
      cx0 = Math.min(cx0, o.x); cy0 = Math.min(cy0, o.y);
      cx1 = Math.max(cx1, o.x + o.fw - 1); cy1 = Math.max(cy1, o.y + o.fh - 1);
    }
    view.centerOn(world, (cx0 + cx1) / 2, (cy0 + cy1) / 2);
    view.pan(0, 26);
    view.clamp(world);
  }
  pushMessage(`已生成示例城镇（${placed} 座建筑）`);
}
