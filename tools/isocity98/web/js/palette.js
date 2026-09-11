// 母板与量化：与 Go 端 internal/art 完全同构的实现。
// 运行时不做任何实时渲染，但仍需要这份色板来做三件事：
//   1) UI 用色与精灵图同源（保证整屏观感统一）；
//   2) 用有序抖动画大面积渐变（天空/面板）；
//   3) CRT 后处理时把混色像素压回有限色板。

/** 4x4 有序抖动矩阵（0..15）。 */
export const BAYER4 = new Uint8Array([
  0, 8, 2, 10,
  12, 4, 14, 6,
  3, 11, 1, 9,
  15, 7, 13, 5,
]);

/** 8x8 有序抖动矩阵（0..63），与 Go 端 art.Bayer8 一致。 */
export const BAYER8 = new Uint8Array([
  0, 32, 8, 40, 2, 34, 10, 42,
  48, 16, 56, 24, 50, 18, 58, 26,
  12, 44, 4, 36, 14, 46, 6, 38,
  60, 28, 52, 20, 62, 30, 54, 22,
  3, 35, 11, 43, 1, 33, 9, 41,
  51, 19, 59, 27, 49, 17, 57, 25,
  15, 47, 7, 39, 13, 45, 5, 37,
  63, 31, 55, 23, 61, 29, 53, 21,
]);

/** 抖动阈值，严格落在 (-0.5, 0.5) 内（与 Go 端 art.Bayer 完全一致）。 */
export function bayer(x, y) {
  return (BAYER8[((y & 7) << 3) | (x & 7)] + 0.5) / 64 - 0.5;
}

/** 4x4 有序网点，用于 UI 的规则网点填充。 */
export function dot4(x, y) {
  return BAYER4[((y & 3) << 2) | (x & 3)];
}

export class Palette {
  constructor(json) {
    this.name = json.name;
    this.colors = json.colors.map((c) => [c.r, c.g, c.b]);
    this.names = json.colors.map((c) => c.name);
    this.ramps = json.ramps;
    this.rampId = json.rampId;
    this.hexes = json.colors.map((c) => c.hex);
    this.lut = null;
  }

  static async load(url) {
    const res = await fetch(url);
    if (!res.ok) throw new Error(`无法加载色板 ${url}: ${res.status}`);
    return new Palette(await res.json());
  }

  /**
   * 6-6-6 最近色查找表；与 Go 端 art.BuildLUT 完全同构。
   * 用 6 位而不是 5 位：母板里有若干近邻色（ink 与 asphalt 的暗阶等），
   * 5 位截断会把它们折进同一格。
   */
  buildLUT() {
    const BITS = 6;
    const N = 1 << BITS;
    const lut = new Uint8Array(N * N * N);
    const cols = this.colors;
    for (let r = 0; r < N; r++) {
      const R = (r * 255) / (N - 1);
      for (let g = 0; g < N; g++) {
        const G = (g * 255) / (N - 1);
        for (let b = 0; b < N; b++) {
          const B = (b * 255) / (N - 1);
          let best = 0;
          let bestD = Infinity;
          for (let i = 0; i < cols.length; i++) {
            const c = cols[i];
            const dr = R - c[0], dg = G - c[1], db = B - c[2];
            const d = 2 * dr * dr + 4 * dg * dg + 3 * db * db;
            if (d < bestD) { bestD = d; best = i; }
          }
          lut[(r << (2 * BITS)) | (g << BITS) | b] = best;
        }
      }
    }
    // 每个母板颜色钉住自己所在的格，保证量化往返稳定
    for (let i = 0; i < cols.length; i++) {
      const c = cols[i];
      lut[((c[0] >> 2) << (2 * BITS)) | ((c[1] >> 2) << BITS) | (c[2] >> 2)] = i;
    }
    this.lut = lut;
    this.lutBits = BITS;
    return lut;
  }

  /** 把颜色量化到色板，thr 为抖动阈值，amp 为抖动幅度（8 位色阶单位）。 */
  quantize(r, g, b, thr = 0, amp = 7) {
    const sh = 8 - (this.lutBits || 6);
    const R = clamp255(r + thr * amp) >> sh;
    const G = clamp255(g + thr * amp) >> sh;
    const B = clamp255(b + thr * amp) >> sh;
    const b6 = this.lutBits || 6;
    return this.lut[(R << (2 * b6)) | (G << b6) | B];
  }

  /** 色阶上按位置取色（0..1），硬边不插值，与 Go 端 Snap 一致。 */
  snap(ramp, pos) {
    const idx = this.ramps[this.rampId[ramp]];
    if (!idx || idx.length === 0) throw new Error(`未知色阶 ${ramp}`);
    const n = idx.length;
    const i = Math.min(n - 1, Math.max(0, Math.round(clamp01(pos) * (n - 1))));
    return this.colors[idx[i]];
  }

  /** 色阶上连续插值（再由抖动量化），用于渐变。 */
  lerpRamp(ramp, pos) {
    const idx = this.ramps[this.rampId[ramp]];
    const n = idx.length;
    if (n === 1) return this.colors[idx[0]];
    const f = clamp01(pos) * (n - 1);
    const i = Math.min(n - 2, Math.floor(f));
    const t = f - i;
    const a = this.colors[idx[i]];
    const b = this.colors[idx[i + 1]];
    return [a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t, a[2] + (b[2] - a[2]) * t];
  }

  /** 取色阶中间色。 */
  rampMid(ramp) {
    const idx = this.ramps[this.rampId[ramp]];
    return this.colors[idx[idx.length >> 1]];
  }

  rgb(c) { return `rgb(${c[0]},${c[1]},${c[2]})`; }
  hex(i) { return this.hexes[i]; }
}

function clamp01(v) { return v < 0 ? 0 : v > 1 ? 1 : v; }
function clamp255(v) { return v < 0 ? 0 : v > 255 ? 255 : v; }
