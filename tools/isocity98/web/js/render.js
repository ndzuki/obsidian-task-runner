// 等距世界合成器。
//
// 运行时只做一件事：按画家算法把**离线预渲染好的精灵图**贴到画布上。
// 没有任何建模、光照或阴影计算 —— 那些全部烘焙在精灵图里了。
//
// 性能策略：整张地图一次性合成到一张大缓存画布，只有世界数据变化时才重建；
// 平移/缩放/每帧动画（水面）都只在视口层做 blit。

import { TERRAIN, ROADS, MAX_HEIGHT } from './city.js';

// 瓦片像素尺寸由 manifest 提供（离线管线可以按不同像素密度产出）。
// 用 let 导出：ES module 的实时绑定保证 view.js 等导入方拿到更新后的值。
export let TILE_W = 32;
export let TILE_H = 16;
export let Z_UNIT = 8;
export let PX_SCALE = 1;

/** 由 manifest.tile / manifest.scale 配置整条运行时的像素密度。 */
export function configureTile(tile, scale = 1) {
  if (tile && tile.w) {
    TILE_W = tile.w;
    TILE_H = tile.h;
    Z_UNIT = tile.z;
  }
  PX_SCALE = Math.max(1, scale | 0);
}

/** 缓存画布四周留白（随像素密度缩放）。 */
function margin() {
  return 200 * PX_SCALE;
}

/** 格坐标 → 相对世界原点的屏幕坐标（y 向下）。 */
export function project(x, y, z) {
  return { sx: (x - y) * (TILE_W / 2), sy: (x + y) * (TILE_H / 2) - z * Z_UNIT };
}

/** 地表 → 侧壁材质前缀。 */
const CLIFF_OF = {
  0: 'cliff.soil', 1: 'cliff.soil', 2: 'cliff.soil', 3: 'cliff.sand',
  4: 'cliff.rock', 5: 'cliff.rock', 6: 'cliff.farm', 7: 'cliff.pave',
  8: 'cliff.pave', 9: 'cliff.sand',
};
const SIDES = [
  { d: [1, 0], key: 'e' },
  { d: [0, 1], key: 's' },
  { d: [-1, 0], key: 'w' },
  { d: [0, -1], key: 'n' },
];

export class WorldRenderer {
  constructor(assets) {
    this.assets = assets;
    this.cache = null;
    this.version = -1;
    this.night = false;
    this.originX = 0;
    this.originY = 0;
    this.cacheW = 0;
    this.cacheH = 0;
    this.waterFrames = [];
    this.waterAnim = 0;
    // 整图缓存的内存预算（像素）。
    //
    // 缓存面积随像素密度按平方增长：1x 约 3.6M 像素（14MB）、2x 约 14M（54MB）、
    // 3x 约 32M（122MB）。而重建一次缓存 3x 下要 ~94ms —— 拖拽刷地形时每帧重建
    // 一次，手感会很差。所以超过预算就改成「逐帧只画可见范围」：
    // 实测 3x 下每帧 1654 次 drawImage ≈ 8ms，换来省下 122MB 且编辑无卡顿。
    this.budgetPx = 16_000_000;
    this.buildMs = 0;
    this.direct = false;
    // 最高的精灵向上溢出多少像素（= 锚点到图像顶部的最大距离）。
    // 直绘时用它把可见深度范围往「屏幕下方」多放几格，
    // 否则贴着视口下沿的高楼会被整栋裁掉。
    this.maxOverhang = 0;
    this.maxFootSpan = 0;
    for (const sp of assets.manifest.sprites) {
      if (sp.ay > this.maxOverhang) this.maxOverhang = sp.ay;
      // 单体按「占地东南角」入桶，因此大占地单体的桶位会比它的起点大很多
      const fp = sp.fp || [1, 1];
      const span = fp[0] + fp[1] - 2;
      if (span > this.maxFootSpan) this.maxFootSpan = span;
    }
    this._objKey = { version: -1, map: new Map() };
  }

  /** 计算地图逻辑画布范围（不论是否真的分配缓存）。 */
  measure(city) {
    const { w, h } = city;
    const MARGIN = margin();
    this.originX = MARGIN + (h - 1) * (TILE_W / 2);
    this.originY = MARGIN + MAX_HEIGHT * Z_UNIT;
    this.cacheW = (w + h - 1) * (TILE_W / 2) + MARGIN * 2;
    this.cacheH = (w + h - 1) * (TILE_H / 2) + MAX_HEIGHT * Z_UNIT + MARGIN * 2;
  }

  /**
   * 每帧准备：只做逻辑度量（画布范围），不再分配整图缓存。
   *
   * 为什么不缓存整图：
   *   1) 缓存面积随像素密度按平方增长（3x 下 7300x4400 ≈ 122MB）；
   *   2) 每次编辑/昼夜切换都要重建，3x 下实测 94ms，拖拽刷地形会卡成幻灯片；
   *   3) 水面动画只能靠「事后覆盖重画」，会把水画到本该遮挡它的崖壁之上（画面错误）。
   * 逐帧直绘可见范围（3x 下约 1650 次 drawImage ≈ 8ms）同时解决这三点。
   */
  ensure(city) {
    this.measure(city);
    this.direct = true;
    this.version = city.version;
    this.night = this.assets.night;
    return null;
  }

  /** 按深度分桶的单体（画家算法用），随城市版本号失效。 */
  objectsByKey(city) {
    if (this._objKey.version === city.version) return this._objKey.map;
    const map = new Map();
    for (const o of city.objects) {
      const key = o.x + o.fw - 1 + o.y + o.fh - 1;
      if (!map.has(key)) map.set(key, []);
      map.get(key).push(o);
    }
    this._objKey = { version: city.version, map };
    return map;
  }

  /**
   * 直接把可见范围画进视口（不分配整图缓存）。
   * 与缓存路径共用同一套 cell/object 与画家算法，因此两条路径的画面完全一致。
   */
  drawRange(ctx, city, view, opts = {}) {
    this.measure(city);
    const z = view.zoom;
    const T = {
      ctx,
      ox: (opts.vx || 0) + (this.originX - view.camX) * z,
      oy: (opts.vy || 0) + (this.originY - view.camY) * z,
      s: z,
      water: opts.water || null,
    };
    const hw = TILE_W / 2, hh = TILE_H / 2;
    const x0 = view.camX - this.originX, y0 = view.camY - this.originY;
    const x1 = x0 + (opts.vw || 0), y1 = y0 + (opts.vh || 0);
    // 可见的 x-y 范围：每格的屏幕水平半径是 hw
    const aMin = x0 / hw - 2, aMax = x1 / hw + 2;
    // 可见的 x+y 范围：上方要多留 MAX_HEIGHT（地形抬升），
    // 下方要多留 maxOverhang（高楼向上溢出，楼基在视口外也可能露出楼顶）
    const dMin = Math.max(0, Math.floor(y0 / hh) - MAX_HEIGHT - 2);
    const dMax = Math.min(city.w + city.h - 2, Math.ceil((y1 + this.maxOverhang) / hh) + 2);
    const byKey = this.objectsByKey(city);
    const dMaxObj = dMax + this.maxFootSpan; // 大占地单体的桶位比它的起点更大
    for (let d = dMin; d <= dMaxObj; d++) {
      if (d <= dMax) {
        let xa = Math.floor((d + aMin) / 2) - 1;
        let xb = Math.ceil((d + aMax) / 2) + 1;
        xa = Math.max(xa, Math.max(0, d - city.h + 1));
        xb = Math.min(xb, Math.min(city.w - 1, d));
        for (let x = xa; x <= xb; x++) {
          const y = d - x;
          if (!city.inside(x, y)) continue;
          this.cell(T, city, x, y);
        }
      }
      const objs = byKey.get(d);
      if (objs) for (const o of objs) this.object(T, city, o);
    }
  }

  /** 统一的精灵绘制：把「世界坐标 + 目标偏移 + 缩放」收在一处。 */
  put(T, sp, psx, psy) {
    const A = this.assets;
    const r = A.rect(sp);
    const s = T.s;
    T.ctx.drawImage(A.sheets[r.sheet], r.x, r.y, sp.w, sp.h,
      Math.round(T.ox + (psx - sp.ax) * s), Math.round(T.oy + (psy - sp.ay) * s),
      sp.w * s, sp.h * s);
  }

  cell(T, city, x, y) {
    const i = city.idx(x, y);
    const z = city.height[i];
    const isWater = city.terrain[i] === 9;
    const t = TERRAIN[city.terrain[i]];
    const name = isWater && T.water ? T.water : this.pickVariant(t.sprite, x, y, 3);
    const sp = this.assets.get(name);
    const p = project(x, y, z);
    this.put(T, sp, p.sx, p.sy);

    // 侧壁：只画比邻居高的那几层，逐层堆叠
    if (z > 0) {
      const prefix = CLIFF_OF[city.terrain[i]] || 'cliff.soil';
      for (let s = 0; s < 4; s++) {
        const nx = x + SIDES[s].d[0], ny = y + SIDES[s].d[1];
        const nh = city.inside(nx, ny) ? city.height[city.idx(nx, ny)] : 0;
        for (let k = 0; k < z - nh; k++) {
          // 侧壁瓦片同样带变体后缀（base_0/base_1），必须走同一套命名回退
          const base = `${prefix}.${SIDES[s].key}`;
          const cn = this.pickVariant(base, x + k * 3, y - k, 2);
          if (!this.assets.has(cn)) continue;
          const csp = this.assets.get(cn);
          const cp = project(x, y, z - k);
          this.put(T, csp, cp.sx, cp.sy);
        }
      }
    }

    // 道路
    const rt = city.road[i];
    if (rt > 0) {
      const spec = ROADS.find((r) => r.id === rt) || ROADS[0];
      const rn = `${spec.sprite}.${city.roadMask(x, y).toString(16)}`;
      if (this.assets.has(rn)) {
        const rsp = this.assets.get(rn);
        const p2 = project(x, y, z);
        this.put(T, rsp, p2.sx, p2.sy);
      }
    }
  }

  object(T, city, o) {
    const name = this.variantOf(o.sprite || o.name, o.variant);
    if (!name) return;
    const sp = this.assets.get(name);
    const z = city.heightAt(o.x, o.y);
    const p = project(o.x, o.y, z);
    this.put(T, sp, p.sx, p.sy);
  }

  /** 按「原型名 + 变体号」解析实际精灵名，兼容单形态（无后缀）与多形态。 */
  variantOf(base, variant) {
    const v = variant | 0;
    for (const cand of [`${base}_${v}`, base, `${base}_0`]) {
      if (this.assets.has(cand)) return cand;
    }
    return null;
  }

  pickVariant(base, x, y, n) {
    const k = (((x * 7 + y * 13) % n) + n) % n;
    // 多形态瓦片的命名规则是 base_0..base_{n-1}，单形态则是 base
    for (const cand of [`${base}_${k}`, base, `${base}_0`]) {
      if (this.assets.has(cand)) return cand;
    }
    return base;
  }

  /** 生成一张按系数压暗的瓦片画布（用作可平铺图案）。 */
  tileCanvas(sp, darken) {
    const cv = document.createElement('canvas');
    cv.width = sp.w;
    cv.height = sp.h;
    const c = cv.getContext('2d');
    const r = this.assets.rect(sp);
    c.drawImage(this.assets.sheets[r.sheet], r.x, r.y, sp.w, sp.h, 0, 0, sp.w, sp.h);
    if (darken > 0) {
      c.globalCompositeOperation = 'source-atop';
      c.fillStyle = `rgba(6,16,26,${darken})`;
      c.fillRect(0, 0, sp.w, sp.h);
    }
    return cv;
  }

  /** 水面逐帧动画：相位由帧循环推进，绘制时按相位取帧（无需事后覆盖）。 */
  animateWater(city, dtMs) {
    if (!this.waterFrames.length) this.waterFrames = this.assets.frames('terrain.water');
    this.waterAnim = (this.waterAnim + dtMs / 260) % 1;
  }

  waterFrameName() {
    if (!this.waterFrames.length) this.waterFrames = this.assets.frames('terrain.water');
    if (!this.waterFrames.length) return 'terrain.water_f0';
    return this.waterFrames[Math.floor(this.waterAnim * this.waterFrames.length) % this.waterFrames.length];
  }
}
