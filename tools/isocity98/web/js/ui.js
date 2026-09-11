// 同年代风格的界面层：DOS/VGA 式凸凹镶边窗口、网点填充、状态栏、图标按钮。
// 全部用色板取色，与精灵图完全同源，因此 UI 与场景在观感上是「一套东西」。

import { TERRAIN, ROADS } from './city.js';

/**
 * 界面布局。
 *
 * BASE 是「基础单位」下的布局（640x400，即 PC-98 分辨率）；LAYOUT 是可变的运行时副本，
 * 由 configureLayout(scale) 就地按像素密度放大。所有绘制与命中测试都读 LAYOUT，
 * 因此提高分辨率时不需要改任何一处布局代码。
 */
const BASE = {
  W: 640, H: 400,
  status: { x: 0, y: 0, w: 640, h: 16 },
  view: { x: 0, y: 16, w: 640, h: 288 },
  panel: { x: 0, y: 304, w: 640, h: 96 },
  tools: { x: 4, y: 309, cols: 3, rows: 3, bw: 44, bh: 27, gap: 2 },
  tabs: { x: 142, y: 309, bw: 44, bh: 17, gap: 1 },
  items: { x: 142, y: 329, cols: 7, rows: 2, bw: 44, bh: 32, gap: 1 },
  info: { x: 462, y: 309, w: 94, h: 87 },
  map: { x: 560, y: 309, w: 72, h: 87 },
  // 状态栏右侧的小按钮（新图/示例/帮助 + 昼夜切换）
  sbtn: { x: 440, y: 2, w: 34, h: 12, gap: 2 },
  dayBtn: { x: 550, y: 2, w: 32, h: 12 },
  clockBox: { x: 586, y: 2, w: 50, h: 12 },
};

/** 当前像素密度（1 = 640x400 内部分辨率）。 */
export let UI_SCALE = 1;

/** 把基础单位换算成当前像素密度下的像素数。 */
export function px(n) { return n * UI_SCALE; }

/** 深拷贝一份基础布局，保持 LAYOUT.view 的对象标识不变（view.js 复用了它）。 */
export const LAYOUT = (() => {
  const out = {};
  for (const k of Object.keys(BASE)) {
    const v = BASE[k];
    out[k] = typeof v === 'number' ? v : { ...v };
  }
  return out;
})();

/** 按像素密度重算布局。cols/rows 是计数，不参与缩放。 */
export function configureLayout(scale) {
  UI_SCALE = Math.max(1, scale | 0);
  for (const k of Object.keys(BASE)) {
    const v = BASE[k];
    if (typeof v === 'number') { LAYOUT[k] = v * UI_SCALE; continue; }
    for (const kk of Object.keys(v)) {
      if (kk === 'cols' || kk === 'rows') continue;
      LAYOUT[k][kk] = v[kk] * UI_SCALE;
    }
  }
  return LAYOUT;
}

/** 状态栏按钮定义（绘制与命中测试共用）。 */
export const STATUS_BUTTONS = [
  { id: 'new', label: '新图' },
  { id: 'demo', label: '示例' },
  { id: 'help', label: '帮助' },
];

export function statusButtonRect(i) {
  const b = LAYOUT.sbtn;
  return { x: b.x + i * (b.w + b.gap), y: b.y, w: b.w, h: b.h };
}

export const TOOLS = [
  { id: 'inspect', name: '查看', hint: '点击建筑看信息' },
  { id: 'raise', name: '抬高', hint: '拖拽抬升地形' },
  { id: 'lower', name: '降低', hint: '拖拽下沉地形' },
  { id: 'level', name: '整平', hint: '整到首次点的高度' },
  { id: 'terrain', name: '地表', hint: '涂抹地表类型' },
  { id: 'road', name: '道路', hint: '拖拽铺路或铁路' },
  { id: 'build', name: '建筑', hint: '下方选建筑后放置' },
  { id: 'prop', name: '小品', hint: '树木/车辆/街具' },
  { id: 'bulldoze', name: '拆除', hint: '清除建筑与道路' },
];

const CATEGORIES = ['住宅', '商业', '工业', '市政', '公园', '交通', '地形'];

export class UI {
  constructor(pal, font, assets) {
    this.pal = pal;
    this.font = font;
    this.assets = assets;
    this.mapCanvas = null;
    this.mapVersion = -1;
    this.mapNight = false;
    this.hoverItem = -1;
    this.hoverTool = -1;
    this.hoverTab = -1;

    const c = (r, p) => this.pal.rgb(this.pal.snap(r, p));
    this.C = {
      face: c('grey', 0.30),
      faceDark: c('grey', 0.10),
      light: c('grey', 0.90),
      mid: c('grey', 0.52),
      dark: c('grey', 0.00),
      ink: c('ink', 0.20),
      text: c('grey', 0.92),
      textDim: c('grey', 0.55),
      accent: c('teal', 0.55),
      accentDim: c('teal', 0.15),
      warn: c('roof', 0.60),
      gold: c('gold', 0.70),
      white: c('grey', 1.0),
      sel: c('teal', 0.85),
    };
  }

  // ---------------------------------------------------------------- 绘制原语

  /** DOS 风格双线镶边：外亮内暗（凸起）或外暗内亮（凹陷）。 */
  bevel(ctx, x, y, w, h, raised = true, face = null, noFill = false) {
    const p = UI_SCALE; // 线宽跟随像素密度，保证观感不随分辨率变细
    if (!noFill) {
      ctx.fillStyle = face || this.C.face;
      ctx.fillRect(x, y, w, h);
    }
    const tl = raised ? this.C.light : this.C.dark;
    const br = raised ? this.C.dark : this.C.light;
    ctx.fillStyle = tl;
    ctx.fillRect(x, y, w - p, p);
    ctx.fillRect(x, y, p, h - p);
    ctx.fillStyle = this.C.ink;
    ctx.fillRect(x + p, y + p, w - 3 * p, p);
    ctx.fillRect(x + p, y + p, p, h - 3 * p);
    ctx.fillStyle = br;
    ctx.fillRect(x, y + h - p, w, p);
    ctx.fillRect(x + w - p, y, p, h);
    ctx.fillStyle = this.C.mid;
    ctx.fillRect(x + w - 2 * p, y + p, p, h - 3 * p);
    ctx.fillRect(x + p, y + h - 2 * p, w - 3 * p, p);
  }

  /**
   * 2x2 有序网点填充：DOS 面板的经典质感。
   * coverage 决定 2x2 块里填几块（0.5 = 对角棋盘），块大小跟随像素密度。
   */
  dither(ctx, x, y, w, h, a, b, coverage = 0.5) {
    const p = UI_SCALE;
    ctx.fillStyle = a;
    ctx.fillRect(x, y, w, h);
    if (coverage <= 0) return;
    ctx.fillStyle = b;
    if (coverage >= 1) {
      ctx.fillRect(x, y, w, h);
      return;
    }
    const cell = 2 * p;
    const spots = [[0, 0], [1, 1], [1, 0], [0, 1]].slice(0, Math.max(1, Math.round(coverage * 4)));
    for (let yy = 0; yy + p <= h; yy += cell) {
      for (let xx = 0; xx + p <= w; xx += cell) {
        for (const [sx, sy] of spots) ctx.fillRect(x + xx + sx * p, y + yy + sy * p, p, p);
      }
    }
  }

  panel(ctx, r, title = null) {
    this.bevel(ctx, r.x, r.y, r.w, r.h, true);
    ctx.fillStyle = this.C.faceDark;
    ctx.fillRect(r.x + 2, r.y + 2, r.w - 4, r.h - 4);
    if (title) {
      ctx.fillStyle = this.C.accentDim;
      ctx.fillRect(r.x + 3, r.y + 3, r.w - 6, this.font.lineHeight - 1);
      this.font.draw(ctx, title, r.x + 5, r.y + 3, this.C.accent);
    }
  }

  text(ctx, s, x, y, color = null) {
    this.font.draw(ctx, s, x, y, color || this.C.text);
  }

  // ---------------------------------------------------------------- 状态栏

  drawStatus(ctx, st) {
    const r = LAYOUT.status;
    ctx.fillStyle = this.C.ink;
    ctx.fillRect(r.x, r.y, r.w, r.h);
    ctx.fillStyle = this.C.accent;
    ctx.fillRect(r.x, r.y, px(92), r.h);
    this.font.draw(ctx, 'ISO-CITY 98', r.x + px(4), r.y + px(2), this.C.ink);

    const y = r.y + px(2);
    let x = px(100);
    const stat = (label, value, color) => {
      this.font.draw(ctx, label, x, y, this.C.textDim);
      x += this.font.textWidth(label) + px(3);
      this.font.draw(ctx, value, x, y, color || this.C.text);
      x += this.font.textWidth(value) + px(10);
    };
    stat('人口', fmt(st.pop), this.C.text);
    stat('岗位', fmt(st.jobs), this.C.text);
    stat('建筑', fmt(st.buildings), this.C.textDim);
    stat('道路', fmt(st.roads), this.C.textDim);

    const sel = st.selectionText || st.toolName || '';
    this.font.draw(ctx, this.font.clip(sel, px(108)), px(330), y, this.C.accent);

    // 状态栏按钮
    for (let i = 0; i < STATUS_BUTTONS.length; i++) {
      const b = statusButtonRect(i);
      const on = this.hoverStatus === i;
      this.bevel(ctx, b.x, b.y, b.w, b.h, true, on ? this.C.mid : this.C.face);
      this.font.drawCenter(ctx, STATUS_BUTTONS[i].label, b.x + b.w / 2, b.y + px(1),
        on ? this.C.white : this.C.text);
    }
    // 昼夜
    const d = LAYOUT.dayBtn;
    this.bevel(ctx, d.x, d.y, d.w, d.h, true, st.night ? this.C.accentDim : this.C.face);
    this.font.drawCenter(ctx, st.night ? '夜间' : '白天', d.x + d.w / 2, d.y + px(1),
      st.night ? this.C.gold : this.C.text);
    // 时钟
    const c = LAYOUT.clockBox;
    this.bevel(ctx, c.x, c.y, c.w, c.h, false, this.C.faceDark);
    this.font.draw(ctx, `D${st.day || 1} ${st.clock || '--:--'}`, c.x + px(3), c.y + px(1), this.C.textDim);
  }

  // ---------------------------------------------------------------- 底部面板

  drawPanel(ctx, st) {
    const P = LAYOUT.panel;
    this.bevel(ctx, P.x, P.y, P.w, P.h, true);
    this.dither(ctx, P.x + 2, P.y + 2, P.w - 4, P.h - 4, this.C.faceDark, this.C.face, 0.5);

    this.drawTools(ctx, st);
    this.drawTabs(ctx, st);
    this.drawItems(ctx, st);
    this.drawInfo(ctx, st);
  }

  drawTools(ctx, st) {
    const T = LAYOUT.tools;
    for (let i = 0; i < TOOLS.length; i++) {
      const col = i % T.cols, row = (i / T.cols) | 0;
      const x = T.x + col * (T.bw + T.gap);
      const y = T.y + row * (T.bh + T.gap);
      const on = st.toolIndex === i;
      const hov = this.hoverTool === i;
      this.bevel(ctx, x, y, T.bw, T.bh, !on, on ? this.C.accentDim : hov ? this.C.mid : this.C.face);
      this.font.drawCenter(ctx, TOOLS[i].name, x + T.bw / 2, y + px(6), on || hov ? this.C.white : this.C.text);
      // 功能角标（小方块，区分同类按钮）
      ctx.fillStyle = on ? this.C.white : this.C.textDim;
      ctx.fillRect(x + px(3), y + px(3), px(2), px(2));
    }
  }

  drawTabs(ctx, st) {
    const T = LAYOUT.tabs;
    if (!st.tabsActive) return;
    for (let i = 0; i < CATEGORIES.length; i++) {
      const x = T.x + i * (T.bw + T.gap);
      const on = st.categoryIndex === i;
      const hov = this.hoverTab === i;
      this.bevel(ctx, x, T.y, T.bw, T.bh, !on, on ? this.C.accentDim : hov ? this.C.mid : this.C.face);
      this.font.drawCenter(ctx, CATEGORIES[i], x + T.bw / 2, T.y + px(3), on || hov ? this.C.white : this.C.text);
    }
  }

  drawItems(ctx, st) {
    const T = LAYOUT.items;
    const list = st.items || [];
    const perPage = T.cols * T.rows;
    const page = st.itemPage || 0;
    const start = page * perPage;
    for (let i = 0; i < perPage; i++) {
      const idx = start + i;
      const col = i % T.cols, row = (i / T.cols) | 0;
      const x = T.x + col * (T.bw + T.gap);
      const y = T.y + row * (T.bh + T.gap);
      const it = list[idx];
      const on = st.itemIndex === idx;
      const hov = this.hoverItem === i;
      this.bevel(ctx, x, y, T.bw, T.bh, !on, on ? this.C.accentDim : hov ? this.C.mid : this.C.faceDark);
      if (!it) continue;
      if (it.kind === 'sprite' && it.sprite) {
        this.drawIcon(ctx, it.sprite, x + T.bw / 2, y + T.bh / 2 - px(3), T.bw - px(8), T.bh - px(10), it.night);
      } else if (it.color) {
        ctx.fillStyle = it.color;
        ctx.fillRect(x + px(6), y + px(5), T.bw - px(12), T.bh - px(14));
        this.bevel(ctx, x + px(6), y + px(5), T.bw - px(12), T.bh - px(14), false, null);
      }
      if (it.short) {
        this.font.drawCenter(ctx, it.short, x + T.bw / 2, y + T.bh - px(12), on ? this.C.white : this.C.textDim);
      }
      if (on) {
        ctx.fillStyle = this.C.sel;
        ctx.fillRect(x + px(1), y + px(1), T.bw - px(2), px(1));
      }
    }
    // 翻页指示
    const pages = Math.max(1, Math.ceil(list.length / perPage));
    if (pages > 1) {
      this.font.draw(ctx, `${page + 1}/${pages}`, T.x + T.cols * (T.bw + T.gap) - px(24), T.y - px(12), this.C.textDim);
      this.font.draw(ctx, '滚轮翻页', T.x, T.y - px(12), this.C.textDim);
    }
  }

  /** 用真实精灵图当按钮图标（先用双线性缩小，CRT 量化会把颜色压回色板）。 */
  drawIcon(ctx, name, cx, cy, boxW, boxH, night = false) {
    if (!this.assets.has(name)) return;
    const sp = this.assets.get(name);
    const scale = Math.min(boxW / sp.w, boxH / sp.h);
    const w = Math.max(2, Math.round(sp.w * scale));
    const h = Math.max(2, Math.round(sp.h * scale));
    const r = this.assets.rect(sp);
    ctx.save();
    ctx.imageSmoothingEnabled = true;
    ctx.drawImage(this.assets.sheets[r.sheet], r.x, r.y, sp.w, sp.h,
      Math.round(cx - w / 2), Math.round(cy - h / 2 + px(2)), w, h);
    ctx.restore();
    void night;
  }

  drawInfo(ctx, st) {
    const r = LAYOUT.info;
    this.bevel(ctx, r.x, r.y, r.w, r.h, false, this.C.faceDark);
    const sel = st.selectedItem;
    let y = r.y + px(4);
    if (!sel) {
      this.font.draw(ctx, '提示', r.x + px(4), y, this.C.accent);
      y += this.font.lineHeight + px(2);
      const hint = st.toolHint || '';
      for (const line of wrap(this.font, hint, r.w - px(8))) {
        this.font.draw(ctx, line, r.x + px(4), y, this.C.textDim);
        y += this.font.lineHeight;
      }
      return;
    }
    this.font.draw(ctx, this.font.clip(sel.label || sel.name, r.w - px(8)), r.x + px(4), y, this.C.accent);
    y += this.font.lineHeight;
    this.font.draw(ctx, sel.category || '-', r.x + px(4), y, this.C.textDim);
    y += this.font.lineHeight + px(2);
    const rows = [];
    if (sel.cost) rows.push(['造价', `$${sel.cost}`]);
    if (sel.pop) rows.push(['人口', `+${sel.pop}`]);
    if (sel.jobs) rows.push(['岗位', `+${sel.jobs}`]);
    if (sel.fp) rows.push(['占地', `${sel.fp[0]}x${sel.fp[1]}`]);
    for (const [k, v] of rows) {
      this.font.draw(ctx, k, r.x + px(4), y, this.C.textDim);
      this.font.draw(ctx, String(v), r.x + px(34), y, this.C.text);
      y += this.font.lineHeight;
    }
  }

  // ---------------------------------------------------------------- 小地图

  /** 小地图离屏画布：1 像素 = 1 格。 */
  buildMap(city, night) {
    if (this.mapCanvas && this.mapVersion === city.version && this.mapNight === night) return this.mapCanvas;
    const cv = document.createElement('canvas');
    cv.width = city.w;
    cv.height = city.h;
    const c = cv.getContext('2d');
    const img = c.createImageData(city.w, city.h);
    const TER = {
      0: [88, 132, 65], 1: [71, 109, 54], 2: [101, 79, 52], 3: [199, 178, 131],
      4: [160, 141, 121], 5: [151, 128, 84], 6: [126, 100, 66], 7: [155, 161, 174],
      8: [155, 161, 174], 9: [36, 83, 99],
    };
    for (let y = 0; y < city.h; y++) {
      for (let x = 0; x < city.w; x++) {
        const i = city.idx(x, y);
        let col = TER[city.terrain[i]] || [80, 80, 80];
        if (city.road[i] > 0) col = [81, 86, 98];
        const o = city.objectAt(x, y);
        if (o) {
          if (o.category === '公园' || o.category === '植被') col = [82, 135, 79];
          else if (o.category === '住宅') col = [186, 99, 70];
          else if (o.category === '商业') col = [221, 176, 56];
          else if (o.category === '工业') col = [160, 141, 121];
          else col = [47, 191, 176];
        }
        // 简单明暗：高度越高越亮，制造立体感
        const hb = 1 + city.height[i] * 0.06;
        const p = (y * city.w + x) * 4;
        img.data[p] = Math.min(255, col[0] * hb);
        img.data[p + 1] = Math.min(255, col[1] * hb);
        img.data[p + 2] = Math.min(255, col[2] * hb);
        img.data[p + 3] = 255;
      }
    }
    c.putImageData(img, 0, 0);
    this.mapCanvas = cv;
    this.mapVersion = city.version;
    this.mapNight = night;
    return cv;
  }

  drawMinimap(ctx, city, camRect = null) {
    const r = LAYOUT.map;
    this.bevel(ctx, r.x, r.y, r.w, r.h, false, this.C.ink);
    const cv = this.buildMap(city, this.mapNight);
    const scale = Math.min((r.w - px(6)) / city.w, (r.h - px(6)) / city.h);
    const w = Math.floor(city.w * scale);
    const h = Math.floor(city.h * scale);
    const ox = r.x + Math.floor((r.w - w) / 2);
    const oy = r.y + Math.floor((r.h - h) / 2);
    ctx.imageSmoothingEnabled = false;
    ctx.drawImage(cv, 0, 0, city.w, city.h, ox, oy, w, h);
    this.bevel(ctx, r.x + px(1), r.y + px(1), r.w - px(2), r.h - px(2), false, null, true);
    if (camRect) {
      const sx = ox + camRect.x * scale;
      const sy = oy + camRect.y * scale;
      const sw = Math.max(px(3), camRect.w * scale);
      const sh = Math.max(px(3), camRect.h * scale);
      const t = px(1);
      ctx.fillStyle = this.C.white;
      ctx.fillRect(sx, sy, sw, t);
      ctx.fillRect(sx, sy + sh - t, sw, t);
      ctx.fillRect(sx, sy, t, sh);
      ctx.fillRect(sx + sw - t, sy, t, sh);
    }
  }

  /** 小地图命中 → 格坐标。 */
  mapHit(city, x, y) {
    const r = LAYOUT.map;
    if (x < r.x + 2 || y < r.y + 2 || x >= r.x + r.w - 2 || y >= r.y + r.h - 2) return null;
    const scale = Math.min((r.w - px(6)) / city.w, (r.h - px(6)) / city.h);
    const w = city.w * scale, h = city.h * scale;
    const ox = r.x + (r.w - w) / 2, oy = r.y + (r.h - h) / 2;
    const tx = Math.floor((x - ox) / scale);
    const ty = Math.floor((y - oy) / scale);
    if (!city.inside(tx, ty)) return null;
    return { x: tx, y: ty };
  }

  // ---------------------------------------------------------------- 悬浮提示

  tooltip(ctx, text, x, y) {
    const w = this.font.textWidth(text) + 8;
    const h = this.font.lineHeight + 4;
    let tx = Math.min(LAYOUT.W - w - px(2), Math.max(px(2), x + px(10)));
    let ty = Math.max(px(2), y - h - px(6));
    this.bevel(ctx, tx, ty, w, h, true, this.C.ink);
    this.font.draw(ctx, text, tx + px(4), ty + px(2), this.C.gold);
  }

  // ---------------------------------------------------------------- 对话框

  dialog(ctx, d) {
    const w = (d.w || 320) * UI_SCALE, h = (d.h || 160) * UI_SCALE;
    const x = ((LAYOUT.W - w) / 2) | 0, y = ((LAYOUT.H - h) / 2) | 0;
    ctx.fillStyle = 'rgba(6,8,14,0.55)';
    ctx.fillRect(0, 0, LAYOUT.W, LAYOUT.H);
    this.bevel(ctx, x, y, w, h, true);
    ctx.fillStyle = this.C.faceDark;
    ctx.fillRect(x + px(3), y + px(3), w - px(6), h - px(6));
    ctx.fillStyle = this.C.accentDim;
    ctx.fillRect(x + px(3), y + px(3), w - px(6), px(14));
    this.font.draw(ctx, d.title || '', x + px(7), y + px(4), this.C.accent);
    let ty = y + px(22);
    for (const line of d.lines || []) {
      this.font.draw(ctx, line, x + px(8), ty, this.C.text);
      ty += this.font.lineHeight + px(1);
    }
    if (d.buttons) {
      let bx = x + w - px(8);
      for (let i = d.buttons.length - 1; i >= 0; i--) {
        const b = d.buttons[i];
        const bw = this.font.textWidth(b.label) + px(14);
        const bh = px(18);
        bx -= bw;
        this.bevel(ctx, bx, y + h - px(24), bw, bh, true, this.C.face);
        this.font.drawCenter(ctx, b.label, bx + bw / 2, y + h - px(24) + px(2), this.C.text);
        b.rect = { x: bx, y: y + h - px(24), w: bw, h: bh };
        bx -= px(6);
      }
    }
    return { x, y, w, h };
  }
}

/** 命中测试：返回被点中的控件描述。 */
export function hitTest(st, x, y) {
  const T = LAYOUT.tools;
  for (let i = 0; i < TOOLS.length; i++) {
    const col = i % T.cols, row = (i / T.cols) | 0;
    const bx = T.x + col * (T.bw + T.gap);
    const by = T.y + row * (T.bh + T.gap);
    if (x >= bx && x < bx + T.bw && y >= by && y < by + T.bh) return { kind: 'tool', index: i };
  }
  if (st.tabsActive) {
    const G = LAYOUT.tabs;
    for (let i = 0; i < CATEGORIES.length; i++) {
      const bx = G.x + i * (G.bw + G.gap);
      if (x >= bx && x < bx + G.bw && y >= G.y && y < G.y + G.bh) return { kind: 'tab', index: i };
    }
  }
  const I = LAYOUT.items;
  const perPage = I.cols * I.rows;
  for (let i = 0; i < perPage; i++) {
    const col = i % I.cols, row = (i / I.cols) | 0;
    const bx = I.x + col * (I.bw + I.gap);
    const by = I.y + row * (I.bh + I.gap);
    if (x >= bx && x < bx + I.bw && y >= by && y < by + I.bh) {
      const idx = (st.itemPage || 0) * perPage + i;
      if (idx < (st.items || []).length) return { kind: 'item', index: idx };
      return { kind: 'empty', index: i };
    }
  }
  for (let i = 0; i < STATUS_BUTTONS.length; i++) {
    const b = statusButtonRect(i);
    if (x >= b.x && x < b.x + b.w && y >= b.y && y < b.y + b.h) return { kind: 'status', index: i };
  }
  const db = LAYOUT.dayBtn;
  if (x >= db.x && x < db.x + db.w && y >= db.y && y < db.y + db.h) return { kind: 'daynight' };
  if (x >= LAYOUT.status.x && y >= LAYOUT.status.y && y < LAYOUT.status.y + LAYOUT.status.h) {
    return { kind: 'status', index: -1 };
  }
  if (x >= LAYOUT.panel.x && y >= LAYOUT.panel.y) return { kind: 'panel' };
  return { kind: 'view' };
}

/** 悬浮高亮索引计算（与 hitTest 结果对应）。 */
export function hoverIndices(st, x, y) {
  const h = hitTest(st, x, y);
  return {
    tool: h.kind === 'tool' ? h.index : -1,
    tab: h.kind === 'tab' ? h.index : -1,
    item: h.kind === 'item' ? h.index - (st.itemPage || 0) * (LAYOUT.items.cols * LAYOUT.items.rows) : -1,
    status: h.kind === 'status' ? h.index : -1,
  };
}

export function itemsForTool(toolId, assets, categoryIndex, page) {
  if (toolId === 'terrain') {
    return TERRAIN.map((t) => ({
      kind: 'sprite', sprite: `${t.sprite}_0`, name: t.key, label: t.name, short: t.name.slice(0, 2),
      terrainId: t.id, category: '地形',
    }));
  }
  if (toolId === 'road') {
    return ROADS.map((rd) => ({
      kind: 'sprite', sprite: `${rd.sprite}.5`, name: rd.key, label: rd.name, short: rd.name.slice(0, 2),
      roadId: rd.id, cost: rd.cost, category: '交通',
    }));
  }
  if (toolId === 'build' || toolId === 'prop') {
    const cats = assets.catalog();
    const catName = CATEGORIES[categoryIndex];
    const list = cats.get(catName) || [];
    if (toolId === 'prop') {
      const props = assets.manifest.sprites.filter((s) => s.kind === 'prop' && s.variant === 0);
      return props.map(toItem);
    }
    return list.map(toItem);
  }
  void page;
  return [];
}

function toItem(s) {
  return {
    kind: 'sprite', sprite: s.name, def: s, name: s.name, label: s.label,
    short: s.label.slice(0, 2), cost: s.cost, pop: s.pop, jobs: s.jobs,
    fp: s.fp, category: s.category, level: s.level,
  };
}

export function categoryList() { return CATEGORIES; }

/** 按宽度折行（中文按字符断行，英文按空格优先）。 */
export function wrap(font, text, maxW) {
  const out = [];
  let line = '';
  for (const ch of text) {
    if (ch === '\n') { out.push(line); line = ''; continue; }
    if (font.textWidth(line + ch) > maxW) { out.push(line); line = ''; }
    line += ch;
  }
  if (line) out.push(line);
  return out;
}

function fmt(n) {
  if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
  if (n >= 10000) return (n / 1000).toFixed(1) + 'k';
  return String(n | 0);
}
