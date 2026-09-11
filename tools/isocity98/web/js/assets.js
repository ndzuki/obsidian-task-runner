// 资产加载：manifest + 图集。运行时只做精灵图合成，不做任何建模或光照。

export class Assets {
  constructor(manifest, palette, sheets) {
    this.manifest = manifest;
    this.pal = palette;
    this.sheets = sheets; // HTMLImageElement / ImageBitmap 数组
    this.byName = new Map();
    for (const s of manifest.sprites) this.byName.set(s.name, s);
    this.tile = manifest.tile;
    this.scale = manifest.scale || 1;
    this.night = false;
  }

  static async load(base, onProgress = () => {}) {
    onProgress(0.05, 'loading palette…');
    const palUrl = new URL('assets/palette.json', base).href;
    const manUrl = new URL('assets/manifest.json', base).href;
    const [palJson, manJson] = await Promise.all([
      fetch(palUrl).then((r) => {
        if (!r.ok) throw new Error(`palette.json ${r.status}`);
        return r.json();
      }),
      fetch(manUrl).then((r) => {
        if (!r.ok) throw new Error(`manifest.json ${r.status}`);
        return r.json();
      }),
    ]);
    const { Palette } = await import('./palette.js');
    const pal = new Palette(palJson);
    onProgress(0.2, 'building colour LUT…');
    pal.buildLUT();

    const sheets = [];
    for (let i = 0; i < manJson.sheets.length; i++) {
      onProgress(0.3 + 0.6 * (i / manJson.sheets.length), `loading atlas ${i + 1}/${manJson.sheets.length}…`);
      const img = await loadImage(new URL(`assets/${manJson.sheets[i].file}`, base).href);
      sheets.push(img);
    }
    onProgress(1, 'ready');
    return new Assets(manJson, pal, sheets);
  }

  /** 取精灵图条目；不存在时抛错（比静默画不出来更容易定位问题）。 */
  get(name) {
    const s = this.byName.get(name);
    if (!s) throw new Error(`未知精灵图 ${name}`);
    return s;
  }
  has(name) { return this.byName.has(name); }

  /** 取当前昼夜档对应的图集坐标。 */
  rect(sp) {
    if (this.night && sp.nightSheet !== undefined && sp.nightSheet >= 0) {
      return { sheet: sp.nightSheet, x: sp.nightX, y: sp.nightY };
    }
    return { sheet: sp.sheet, x: sp.x, y: sp.y };
  }

  draw(ctx, name, dx, dy) {
    const sp = typeof name === 'string' ? this.get(name) : name;
    const r = this.rect(sp);
    ctx.drawImage(this.sheets[r.sheet], r.x, r.y, sp.w, sp.h, dx, dy, sp.w, sp.h);
    return sp;
  }

  /** 按分类聚合建筑目录（UI 用）。 */
  catalog() {
    const cats = new Map();
    for (const s of this.manifest.sprites) {
      if (s.kind !== 'building' && s.kind !== 'prop') continue;
      if (s.anim) continue;
      if (s.variant !== 0) continue; // 目录只展示原型，落地时随机挑变体
      if (s.category === '植被') continue;
      if (!cats.has(s.category)) cats.set(s.category, []);
      cats.get(s.category).push(s);
    }
    return cats;
  }

  /** 去掉变体后缀，得到原型名（"shop.row_2" → "shop.row"）。 */
  baseOf(name) {
    if (this.byName.has(name)) {
      // 已经是完整名，但仍可能是某个变体
      const m = /^(.*)_(\d+)$/.exec(name);
      if (m && this.byName.has(m[1] + '_0')) return m[1];
      return name;
    }
    const m = /^(.*)_(\d+)$/.exec(name);
    return m ? m[1] : name;
  }

  /** 取某一原型的全部变体精灵名（无变体时返回 [原型名]）。 */
  variants(name) {
    const base = this.baseOf(name);
    const out = [];
    for (let i = 0; i < 12; i++) {
      if (this.byName.has(`${base}_${i}`)) out.push(`${base}_${i}`);
    }
    if (out.length) return out;
    return this.byName.has(base) ? [base] : [name];
  }

  /** 随机挑一个变体；用于落地时制造「成组但不重复」的肌理。 */
  pickVariant(base) {
    const vs = this.variants(base);
    return vs[Math.floor(Math.random() * vs.length)];
  }

  /** 动画帧（水面等）。 */
  frames(name) {
    const out = [];
    for (let i = 0; i < 16; i++) {
      const n = `${name}_f${i}`;
      if (this.byName.has(n)) out.push(n); else break;
    }
    return out;
  }
}

function loadImage(url) {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => resolve(img);
    img.onerror = () => reject(new Error(`无法加载图像 ${url}`));
    img.src = url;
  });
}
