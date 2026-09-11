// CRT 后处理。
//
// 管线位置：所有精灵图与 UI 都已经画进内部分辨率帧缓冲之后，
// 再统一做「辉光 → 扫描线 → 荫罩 → 暗角 → 有限色板抖动」，最后交给浏览器
// 以整数倍最近邻放大。这样复古质感是全局一致的，而不是每个资源各自为政地"做旧"。
//
// 帧缓冲尺寸由像素密度决定（1x = 640x400，2x = 1280x800），这里按需重建缓冲。
// 逐像素循环用 32 位视图写回（1 次存储而不是 4 次），否则 2x 下会明显掉帧。

import { bayer } from './palette.js';

export const CRT_MODES = ['OFF', 'SCANLINE', 'FULL CRT'];

export class CRT {
  constructor(pal) {
    this.pal = pal;
    this.mode = 2; // 0=关 1=扫描线(廉价) 2=全效果
    this.scan = 0.82;
    this.glow = 0.55;
    this.vig = 0.15;
    this.w = 0;
    this.h = 0;
    this.stripes = null;
    this.lastMs = 0;
    this.buildPal32();
  }

  /** 预乘 32 位色板（字节序安全：先写字节再按 Uint32 读）。 */
  buildPal32() {
    const tmp = new Uint8ClampedArray(4);
    const view = new Uint32Array(tmp.buffer);
    this.pal32 = new Uint32Array(this.pal.colors.length);
    for (let i = 0; i < this.pal.colors.length; i++) {
      const c = this.pal.colors[i];
      tmp[0] = c[0]; tmp[1] = c[1]; tmp[2] = c[2]; tmp[3] = 255;
      this.pal32[i] = view[0];
    }
  }

  /** 按帧缓冲尺寸（重新）准备缓冲。 */
  ensure(w, h) {
    if (this.w === w && this.h === h) return;
    this.w = w;
    this.h = h;
    this.buf = document.createElement('canvas');
    this.buf.width = w;
    this.buf.height = h;
    this.bctx = this.buf.getContext('2d');
    this.img = this.bctx.createImageData(w, h);
    this.out32 = new Uint32Array(this.img.data.buffer);
    this.bw = Math.max(1, w >> 2);
    this.bh = Math.max(1, h >> 2);
    this.bloom = new Float32Array(this.bw * this.bh * 3);
    this.bloomTmp = new Float32Array(this.bw * this.bh * 3);
    this.buildVignette();
    this.buildRowTables();
    this.stripes = null;
  }

  /** 预计算「逐行抖动阈值」与「荫罩×暗角」，主循环里就只剩乘加与一次查表。 */
  buildRowTables() {
    const W = this.w;
    this.bayerRows = [];
    for (let ph = 0; ph < 8; ph++) {
      const row = new Float32Array(W);
      for (let x = 0; x < W; x++) row[x] = bayer(x, ph) * 0.94 * 11;
      this.bayerRows.push(row);
    }
    const shade = new Float32Array(W);
    for (let x = 0; x < W; x++) shade[x] = ((x % 3) === 0 ? 0.93 : 1) * this.vx[x];
    this.shadeX = shade;
  }

  buildVignette() {
    const w = this.w, h = this.h;
    this.vx = new Float32Array(w);
    this.vy = new Float32Array(h);
    for (let x = 0; x < w; x++) {
      const t = (x / (w - 1)) * 2 - 1;
      this.vx[x] = Math.max(0, 1 - this.vig * t * t);
    }
    for (let y = 0; y < h; y++) {
      const t = (y / (h - 1)) * 2 - 1;
      this.vy[y] = Math.max(0, 1 - this.vig * t * t);
    }
  }

  /** 廉价模式用的扫描线遮罩（一次生成，之后只做一次 drawImage）。 */
  stripePattern(ctx) {
    if (this.stripes) return this.stripes;
    const cv = document.createElement('canvas');
    cv.width = 4;
    cv.height = 2;
    const c = cv.getContext('2d');
    c.fillStyle = 'rgba(0,0,0,0)';
    c.fillRect(0, 0, 4, 2);
    c.fillStyle = `rgba(4,8,14,${(1 - this.scan).toFixed(3)})`;
    c.fillRect(0, 1, 4, 1);
    c.fillStyle = 'rgba(4,8,14,0.06)';
    c.fillRect(0, 0, 1, 2);
    c.fillRect(2, 0, 1, 2);
    this.stripes = ctx.createPattern(cv, 'repeat');
    return this.stripes;
  }

  /**
   * 把帧缓冲输出到显示画布（两者尺寸一致，只做后处理）。
   *
   * 三种档位的代价差一个数量级，高像素密度下很重要：
   *   0 OFF      —— 一次 drawImage
   *   1 SCANLINE —— drawImage + 扫描线图案 + 暗角渐变（全程由 canvas 合成，无逐像素循环）
   *   2 FULL CRT —— 逐像素：辉光 + 扫描线 + 荫罩 + 暗角 + 有限色板抖动
   */
  present(src, dstCtx, dw, dh) {
    const w = src.width, h = src.height;
    const t0 = performance.now();
    dstCtx.imageSmoothingEnabled = false;
    if (this.mode !== 2) {
      dstCtx.clearRect(0, 0, dw, dh);
      dstCtx.drawImage(src, 0, 0, w, h, 0, 0, dw, dh);
      if (this.mode === 1) {
        this.ensure(w, h);
        dstCtx.save();
        dstCtx.fillStyle = this.stripePattern(dstCtx);
        dstCtx.fillRect(0, 0, dw, dh);
        dstCtx.fillStyle = this.vignetteGradient(dstCtx, dw, dh);
        dstCtx.fillRect(0, 0, dw, dh);
        dstCtx.restore();
      }
      this.lastMs = performance.now() - t0;
      return;
    }
    this.ensure(w, h);
    const sctx = src.getContext('2d', { willReadFrequently: true });
    const img = sctx.getImageData(0, 0, w, h);
    this.process(img.data);
    // 尺寸一致时直接写回显示画布，省掉一次整屏 blit
    dstCtx.putImageData(this.img, 0, 0);
    this.lastMs = performance.now() - t0;
    this.trackPerf();
  }

  /** 暗角（廉价档用径向渐变，由 canvas 合成，不逐像素算）。 */
  vignetteGradient(ctx, w, h) {
    if (this.vg && this.vgW === w && this.vgH === h) return this.vg;
    const g = ctx.createRadialGradient(w / 2, h / 2, Math.min(w, h) * 0.34,
      w / 2, h / 2, Math.max(w, h) * 0.74);
    g.addColorStop(0, 'rgba(4,8,14,0)');
    g.addColorStop(1, `rgba(4,8,14,${this.vig})`);
    this.vg = g;
    this.vgW = w;
    this.vgH = h;
    return g;
  }

  /**
   * 全效果档如果持续跟不上，自动降到扫描线档。
   * 高像素密度下逐像素循环是主要开销（3x 约 21ms/帧），
   * 与其掉到 25fps，不如换成更轻的后处理并明确告诉用户怎么切回去。
   */
  trackPerf() {
    if (this.lastMs > 24) {
      this.slowFrames = (this.slowFrames || 0) + 1;
    } else {
      this.slowFrames = 0;
    }
    if (this.slowFrames > 45) {
      this.slowFrames = 0;
      this.mode = 1;
      if (this.onDegrade) this.onDegrade(this.lastMs);
    }
  }

  /** 逐像素主循环：辉光叠加 + 扫描线 + 荫罩 + 暗角 + 量化抖动。 */
  process(src) {
    const lut = this.pal.lut;
    const p32 = this.pal32;
    const W = this.w, H = this.h;
    const out = this.out32;
    const bloom = this.bloom;
    const full = this.mode === 2;

    if (full) this.buildBloom(src);

    const scan = this.scan;
    const vy = this.vy;
    const BW = this.bw;
    const glo = this.glow;
    const bayerRows = this.bayerRows;
    const shadeX = this.shadeX;

    for (let y = 0; y < H; y++) {
      const rowScan = (y & 1) === 1 ? scan : 1;
      const kRow = rowScan * vy[y];
      const by = (y >> 2) * BW;
      const bayerRow = bayerRows[y & 7];
      const row = y * W;
      let i = row * 4;
      for (let x = 0; x < W; x++, i += 4) {
        let r = src[i], g = src[i + 1], b = src[i + 2];
        if (full) {
          const bi = (by + (x >> 2)) * 3;
          r += bloom[bi] * glo;
          g += bloom[bi + 1] * glo;
          b += bloom[bi + 2] * glo;
        }
        // 荫罩（每 3 列压暗一列）+ 扫描线 + 暗角：三个系数已在建表时乘好
        const k = kRow * shadeX[x];
        r *= k; g *= k; b *= k;
        // 有限色板量化（8x8 有序抖动）；先加抖动再夹取，否则 255 附近会溢出索引
        const thr = bayerRow[x];
        const R = clamp255(r + thr) >> 2;
        const G = clamp255(g + thr) >> 2;
        const B = clamp255(b + thr) >> 2;
        out[row + x] = p32[lut[(R << 12) | (G << 6) | B]];
      }
    }
  }

  /** 亮度阈值 + 两次盒式模糊，得到低分辨率辉光。 */
  buildBloom(src) {
    const W = this.w;
    const BW = this.bw, BH = this.bh;
    const bloom = this.bloom;
    const tmp = this.bloomTmp;
    bloom.fill(0);
    for (let y = 0; y < BH; y++) {
      for (let x = 0; x < BW; x++) {
        const i = ((y << 2) * W + (x << 2)) * 4;
        const r = src[i], g = src[i + 1], b = src[i + 2];
        const luma = 0.299 * r + 0.587 * g + 0.114 * b;
        const t = luma > 168 ? (luma - 168) / 87 : 0;
        const o = (y * BW + x) * 3;
        bloom[o] = r * t * 0.5;
        bloom[o + 1] = g * t * 0.5;
        bloom[o + 2] = b * t * 0.5;
      }
    }
    boxBlur3(bloom, tmp, BW, BH, 1);
    boxBlur3(bloom, tmp, BW, BH, 2);
  }
}

function clamp255(v) { return v < 0 ? 0 : v > 255 ? 255 : v; }

/** 就地做一次可分离盒式模糊（半径 r，两趟）。 */
function boxBlur3(buf, tmp, w, h, r) {
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      let sr = 0, sg = 0, sb = 0, n = 0;
      for (let k = -r; k <= r; k++) {
        const xx = x + k;
        if (xx < 0 || xx >= w) continue;
        const o = (y * w + xx) * 3;
        sr += buf[o]; sg += buf[o + 1]; sb += buf[o + 2]; n++;
      }
      const o = (y * w + x) * 3;
      tmp[o] = sr / n; tmp[o + 1] = sg / n; tmp[o + 2] = sb / n;
    }
  }
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      let sr = 0, sg = 0, sb = 0, n = 0;
      for (let k = -r; k <= r; k++) {
        const yy = y + k;
        if (yy < 0 || yy >= h) continue;
        const o = (yy * w + x) * 3;
        sr += tmp[o]; sg += tmp[o + 1]; sb += tmp[o + 2]; n++;
      }
      const o = (y * w + x) * 3;
      buf[o] = sr / n; buf[o + 1] = sg / n; buf[o + 2] = sb / n;
    }
  }
}
