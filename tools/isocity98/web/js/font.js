// 位图字体：把系统字体在离屏画布上渲染后**二值化**成点阵缓存。
//
// 这样做的理由：DOS/PC-98 时代的界面文字是点阵字体，
// 直接 fillText 会带来抗锯齿灰边，破坏「整屏有限色板」的观感。
// 二值化后每个字只有「亮/灭」两态，再按色板着色，观感立刻回到那个年代。

const SIZE = 12; // CJK 点阵高度，PC-98 常见 12/16 点阵
const PAD = 1;

export class Font {
  constructor(pal, opts = {}) {
    this.pal = pal;
    this.size = opts.size || SIZE;
    this.lineHeight = this.size + 3;
    this.cache = new Map(); // `${ch}|${color}` → canvas
    this.metrics = new Map();
    this.measureCtx = document.createElement('canvas').getContext('2d');
    this.measureCtx.font = this.cssFont();
    this.threshold = opts.threshold ?? 0.42;
  }

  cssFont() {
    // ASCII 走等宽字体（点阵感强），CJK 回落到系统黑体
    return `${this.size}px ui-monospace, "SFMono-Regular", Menlo, "Noto Sans CJK SC", "Source Han Sans SC", monospace`;
  }

  width(ch) {
    let w = this.metrics.get(ch);
    if (w !== undefined) return w;
    const m = this.measureCtx.measureText(ch);
    w = Math.max(1, Math.round(m.width));
    this.metrics.set(ch, w);
    return w;
  }

  textWidth(s) {
    let w = 0;
    for (const ch of s) w += this.width(ch);
    return w;
  }

  /** 取得（并按颜色缓存）单字点阵画布。 */
  glyph(ch, color) {
    const key = `${ch}|${color}`;
    const hit = this.cache.get(key);
    if (hit) return hit;
    const src = this.bitmap(ch);
    const cv = document.createElement('canvas');
    cv.width = src.w;
    cv.height = src.h;
    const c = cv.getContext('2d');
    const im = c.createImageData(src.w, src.h);
    const rgb = parseColor(color);
    for (let i = 0; i < src.data.length; i++) {
      if (src.data[i]) {
        im.data[i * 4] = rgb[0];
        im.data[i * 4 + 1] = rgb[1];
        im.data[i * 4 + 2] = rgb[2];
        im.data[i * 4 + 3] = 255;
      }
    }
    c.putImageData(im, 0, 0);
    this.cache.set(key, cv);
    return cv;
  }

  /** 二值化点阵（只依赖字符，与颜色无关）。 */
  bitmap(ch) {
    const key = `#${ch}`;
    const hit = this.cache.get(key);
    if (hit) return hit;
    const w = this.width(ch) + PAD * 2;
    const h = this.size + PAD * 2;
    const cv = document.createElement('canvas');
    cv.width = w;
    cv.height = h;
    const c = cv.getContext('2d');
    c.font = this.cssFont();
    c.textBaseline = 'top';
    c.fillStyle = '#fff';
    c.fillText(ch, PAD, PAD);
    const img = c.getImageData(0, 0, w, h);
    const out = new Uint8Array(w * h);
    const t = this.threshold * 255;
    for (let i = 0; i < w * h; i++) out[i] = img.data[i * 4 + 3] >= t ? 1 : 0;
    const rec = { w, h, data: out };
    this.cache.set(key, rec);
    return rec;
  }

  /** 在 (x,y) 处绘制字符串；y 为文字顶部。 */
  draw(ctx, s, x, y, color) {
    let cx = Math.round(x);
    const cy = Math.round(y);
    for (const ch of s) {
      if (ch !== ' ') {
        const g = this.glyph(ch, color);
        const ox = cx - PAD;
        ctx.drawImage(g, ox, cy - PAD);
      }
      cx += this.width(ch);
    }
    return cx;
  }

  /** 带 1px 阴影的绘制，用于状态栏等需要从背景里跳出来的场合。 */
  drawShadow(ctx, s, x, y, color, shadow) {
    this.draw(ctx, s, x + 1, y + 1, shadow ?? 'rgb(10,10,14)');
    this.draw(ctx, s, x, y, color);
  }

  drawCenter(ctx, s, cx, y, color) {
    this.draw(ctx, s, cx - this.textWidth(s) / 2, y, color);
  }

  /** 截断到指定宽度（超出用 ~ 结尾），UI 里避免文字溢出。 */
  clip(s, maxW) {
    if (this.textWidth(s) <= maxW) return s;
    let out = '';
    let w = 0;
    for (const ch of s) {
      const cw = this.width(ch);
      if (w + cw + this.width('~') > maxW) break;
      out += ch;
      w += cw;
    }
    return out + '~';
  }
}

function parseColor(c) {
  if (Array.isArray(c)) return c;
  const m = /^rgb\((\d+),\s*(\d+),\s*(\d+)\)$/.exec(c);
  if (m) return [+m[1], +m[2], +m[3]];
  const h = c.replace('#', '');
  return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)];
}
