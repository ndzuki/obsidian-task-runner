// 城市数据模型：高度场 + 地表 + 道路 + 单体占用。
// 玩法只保留「地图编辑」与「建造」，所以模型刻意保持扁平：
// 没有财政、没有分区需求，只有格子、单体与一份轻量的统计数据。

/** 地表类型表。id 与存档顺序绑定，只能追加不能重排。 */
export const TERRAIN = [
  { id: 0, key: 'grass', name: '草地', sprite: 'terrain.grass' },
  { id: 1, key: 'meadow', name: '深草地', sprite: 'terrain.meadow' },
  { id: 2, key: 'dirt', name: '裸土', sprite: 'terrain.dirt' },
  { id: 3, key: 'sand', name: '沙地', sprite: 'terrain.sand' },
  { id: 4, key: 'rock', name: '岩石', sprite: 'terrain.rock' },
  { id: 5, key: 'gravel', name: '砾石', sprite: 'terrain.gravel' },
  { id: 6, key: 'farm', name: '农田', sprite: 'terrain.farm' },
  { id: 7, key: 'pave', name: '铺装', sprite: 'terrain.pave' },
  { id: 8, key: 'concrete', name: '水泥', sprite: 'terrain.concrete' },
  { id: 9, key: 'water', name: '水面', sprite: 'terrain.water_f0' },
];

/** 道路类型表。id 0 表示无路。 */
export const ROADS = [
  { id: 1, key: 'asphalt', name: '柏油路', sprite: 'road.asphalt', cost: 12 },
  { id: 2, key: 'dirt', name: '土路', sprite: 'road.dirt', cost: 4 },
  { id: 3, key: 'rail', name: '铁路', sprite: 'road.rail', cost: 30 },
];

export const MAX_HEIGHT = 6;
export const WATER_HEIGHT = 0;

export class City {
  constructor(w = 64, h = 64) {
    this.w = w;
    this.h = h;
    const n = w * h;
    this.height = new Int8Array(n);
    this.terrain = new Uint8Array(n);
    this.road = new Uint8Array(n); // 0 = 无
    this.objects = [];
    this.occ = new Int32Array(n); // 0 = 空，否则为 objects 下标 + 1
    this.version = 0; // 每次变更自增，供渲染缓存失效
    this.reset('grass', 0);
  }

  idx(x, y) { return y * this.w + x; }
  inside(x, y) { return x >= 0 && y >= 0 && x < this.w && y < this.h; }

  reset(terrainKey = 'grass', height = 1) {
    const t = TERRAIN.find((e) => e.key === terrainKey) || TERRAIN[0];
    this.height.fill(height);
    this.terrain.fill(t.id);
    this.road.fill(0);
    this.objects.length = 0;
    this.occ.fill(0);
    this.version++;
  }

  at(x, y) {
    if (!this.inside(x, y)) return null;
    return this.idx(x, y);
  }

  heightAt(x, y) {
    const i = this.at(x, y);
    return i === null ? WATER_HEIGHT : this.height[i];
  }

  terrainAt(x, y) {
    const i = this.at(x, y);
    return i === null ? 9 : this.terrain[i];
  }

  isWater(x, y) { return this.terrainAt(x, y) === 9; }

  /** 道路连通掩码：bit0=+X bit1=+Y bit2=-X bit3=-Y。 */
  roadMask(x, y) {
    let m = 0;
    const d = [[1, 0], [0, 1], [-1, 0], [0, -1]];
    for (let i = 0; i < 4; i++) {
      const nx = x + d[i][0], ny = y + d[i][1];
      if (this.inside(nx, ny) && this.road[this.idx(nx, ny)] > 0) m |= 1 << i;
    }
    return m;
  }

  // ---- 编辑操作（全部返回是否真的改变了数据，便于 UI 反馈与缓存失效）----

  setHeight(x, y, h) {
    const i = this.at(x, y);
    if (i === null) return false;
    h = Math.max(0, Math.min(MAX_HEIGHT, h | 0));
    if (this.height[i] === h) return false;
    this.height[i] = h;
    this.version++;
    return true;
  }

  setTerrain(x, y, id) {
    const i = this.at(x, y);
    if (i === null || this.terrain[i] === id) return false;
    this.terrain[i] = id;
    this.version++;
    return true;
  }

  setRoad(x, y, type) {
    const i = this.at(x, y);
    if (i === null || this.road[i] === type) return false;
    this.road[i] = type;
    // 道路所在格必须清掉地表上的单体
    if (type > 0) this.removeAt(x, y);
    this.version++;
    return true;
  }

  objectAt(x, y) {
    const i = this.at(x, y);
    if (i === null) return null;
    const ref = this.occ[i];
    return ref > 0 ? this.objects[ref - 1] : null;
  }

  /** 检查能否在此放置占地 fw x fh 的单体。 */
  canPlace(x, y, fw, fh, opts = {}) {
    const allowWater = opts.allowWater === true;
    const flat = opts.requireFlat !== false;
    let h0 = null;
    for (let dy = 0; dy < fh; dy++) {
      for (let dx = 0; dx < fw; dx++) {
        const cx = x + dx, cy = y + dy;
        if (!this.inside(cx, cy)) return false;
        const i = this.idx(cx, cy);
        if (this.occ[i] > 0) return false;
        if (this.road[i] > 0) return false;
        if (!allowWater && this.terrain[i] === 9) return false;
        if (flat) {
          if (h0 === null) h0 = this.height[i];
          else if (this.height[i] !== h0) return false;
        }
      }
    }
    return true;
  }

  place(obj, x, y) {
    const fw = obj.fp[0], fh = obj.fp[1];
    if (!this.canPlace(x, y, fw, fh, { allowWater: obj.allowWater })) return false;
    const rec = {
      name: obj.name, sprite: obj.sprite || obj.name, label: obj.label, category: obj.category,
      x, y, fw, fh, level: obj.level || 1,
      pop: obj.pop || 0, jobs: obj.jobs || 0, cost: obj.cost || 0,
      variant: obj.variant | 0,
    };
    this.objects.push(rec);
    const id = this.objects.length;
    for (let dy = 0; dy < fh; dy++) {
      for (let dx = 0; dx < fw; dx++) this.occ[this.idx(x + dx, y + dy)] = id;
    }
    this.version++;
    return true;
  }

  removeAt(x, y) {
    const i = this.at(x, y);
    if (i === null) return false;
    const ref = this.occ[i];
    if (ref <= 0) return false;
    return this.removeIndex(ref - 1);
  }

  removeIndex(oi) {
    const rec = this.objects[oi];
    if (!rec) return false;
    for (let dy = 0; dy < rec.fh; dy++) {
      for (let dx = 0; dx < rec.fw; dx++) {
        if (this.inside(rec.x + dx, rec.y + dy)) this.occ[this.idx(rec.x + dx, rec.y + dy)] = 0;
      }
    }
    this.objects.splice(oi, 1);
    // objects 下标变了，重建占用表（单体数量不大，直接重建最稳）
    this.occ.fill(0);
    for (let k = 0; k < this.objects.length; k++) {
      const r = this.objects[k];
      for (let dy = 0; dy < r.fh; dy++) {
        for (let dx = 0; dx < r.fw; dx++) {
          if (this.inside(r.x + dx, r.y + dy)) this.occ[this.idx(r.x + dx, r.y + dy)] = k + 1;
        }
      }
    }
    this.version++;
    return true;
  }

  /** 轻量统计：只做人口/岗位/建筑数与维护费，不做经营。 */
  stats() {
    let pop = 0, jobs = 0, buildings = 0, roads = 0;
    for (const o of this.objects) { pop += o.pop; jobs += o.jobs; buildings++; }
    for (let i = 0; i < this.road.length; i++) if (this.road[i] > 0) roads++;
    return { pop, jobs, buildings, roads };
  }

  /** 地表统计（小地图用）。 */
  landRatio() {
    let land = 0;
    for (let i = 0; i < this.terrain.length; i++) if (this.terrain[i] !== 9) land++;
    return land / this.terrain.length;
  }

  center() {
    // 找一块适合开局的陆地
    for (let r = 0; r < Math.max(this.w, this.h); r++) {
      for (let dy = -r; dy <= r; dy++) {
        for (let dx = -r; dx <= r; dx++) {
          const x = (this.w >> 1) + dx, y = (this.h >> 1) + dy;
          if (!this.inside(x, y)) continue;
          if (this.terrain[this.idx(x, y)] !== 9 && this.height[this.idx(x, y)] >= 1) return { x, y };
        }
      }
    }
    return { x: this.w >> 1, y: this.h >> 1 };
  }

  toJSON() {
    return {
      v: 1, w: this.w, h: this.h,
      height: Array.from(this.height),
      terrain: Array.from(this.terrain),
      road: Array.from(this.road),
      // 必须深拷贝：撤销栈会长期持有快照，若直接引用 this.objects，
      // 之后的 splice 会把快照一起改掉（撤销就永远恢复不出东西）。
      objects: this.objects.map((o) => ({ ...o })),
    };
  }

  static fromJSON(data) {
    const c = new City(data.w, data.h);
    c.height.set(data.height);
    c.terrain.set(data.terrain);
    c.road.set(data.road);
    c.objects = data.objects || [];
    c.occ.fill(0);
    for (let k = 0; k < c.objects.length; k++) {
      const r = c.objects[k];
      for (let dy = 0; dy < r.fh; dy++) {
        for (let dx = 0; dx < r.fw; dx++) {
          if (c.inside(r.x + dx, r.y + dy)) c.occ[c.idx(r.x + dx, r.y + dy)] = k + 1;
        }
      }
    }
    c.version++;
    return c;
  }
}

// ---------------------------------------------------------------- 地图生成

/** 确定性 value noise（双线性平滑插值）。 */
function makeNoise(seed) {
  const perm = new Uint8Array(512);
  let s = (seed >>> 0) || 1;
  const rnd = () => {
    s ^= s << 13; s >>>= 0;
    s ^= s >> 17;
    s ^= s << 5; s >>>= 0;
    return s / 4294967296;
  };
  const p = new Uint8Array(256);
  for (let i = 0; i < 256; i++) p[i] = i;
  for (let i = 255; i > 0; i--) {
    const j = Math.floor(rnd() * (i + 1));
    const t = p[i]; p[i] = p[j]; p[j] = t;
  }
  for (let i = 0; i < 512; i++) perm[i] = p[i & 255];
  const grad = (ix, iy) => perm[(perm[ix & 255] + (iy & 255)) & 255] / 255;
  return (x, y) => {
    const x0 = Math.floor(x), y0 = Math.floor(y);
    const fx = x - x0, fy = y - y0;
    const sx = fx * fx * (3 - 2 * fx), sy = fy * fy * (3 - 2 * fy);
    const a = grad(x0, y0), b = grad(x0 + 1, y0);
    const c = grad(x0, y0 + 1), d = grad(x0 + 1, y0 + 1);
    return (a + (b - a) * sx) * (1 - sy) + (c + (d - c) * sx) * sy;
  };
}

/**
 * 生成一座开局岛屿：中间高、四周入海，按海拔分层给出沙滩/草地/深草/岩石。
 * 关键是把「径向衰减」压得比噪声弱一些，否则整张图会变成一块高原。
 */
export function generateIsland(w = 64, h = 64, seed = 12345) {
  const city = new City(w, h);
  const n1 = makeNoise(seed);
  const n2 = makeNoise(seed * 7 + 13);
  const n3 = makeNoise(seed * 31 + 7);
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      const i = city.idx(x, y);
      const nx = x / w - 0.5, ny = y / h - 0.5;
      const r = Math.sqrt(nx * nx * 1.05 + ny * ny * 1.25) * 2;
      const falloff = Math.max(0, 1 - Math.pow(r * 1.12, 2));
      const nz = n1(x / 13, y / 13) * 0.66 + n2(x / 6.5, y / 6.5) * 0.34;
      const v = falloff * 0.78 + (nz - 0.5) * 0.92;
      let hgt = 0;
      if (v > 0.32) hgt = 1;
      if (v > 0.50) hgt = 2;
      if (v > 0.70) hgt = 3;
      if (v > 0.90) hgt = 4;
      city.height[i] = hgt;
      city.terrain[i] = hgt === 0 ? 9 : 0;
    }
  }
  smoothCoast(city);
  classifyTerrain(city, n2, n3);
  city.version++;
  return city;
}

/** 抹掉孤立的单格水/陆，让海岸线更连贯。 */
function smoothCoast(city) {
  const { w, h } = city;
  const copy = Int8Array.from(city.height);
  for (let y = 1; y < h - 1; y++) {
    for (let x = 1; x < w - 1; x++) {
      let land = 0;
      for (let dy = -1; dy <= 1; dy++) {
        for (let dx = -1; dx <= 1; dx++) {
          if (copy[(y + dy) * w + (x + dx)] > 0) land++;
        }
      }
      const i = city.idx(x, y);
      if (land <= 2) { city.height[i] = 0; city.terrain[i] = 9; continue; }
      if (land >= 7 && city.height[i] === 0) city.height[i] = 1;
    }
  }
}

/** 按海拔与「是否临水」决定地表：海岸一圈是沙滩，高处是岩石/砾石。 */
function classifyTerrain(city, n2, n3) {
  const { w, h } = city;
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      const i = city.idx(x, y);
      const hh = city.height[i];
      if (hh === 0) { city.terrain[i] = 9; continue; }
      let coast = false;
      for (let dy = -1; dy <= 1 && !coast; dy++) {
        for (let dx = -1; dx <= 1; dx++) {
          const nx2 = x + dx, ny2 = y + dy;
          if (!city.inside(nx2, ny2) || city.height[city.idx(nx2, ny2)] === 0) { coast = true; break; }
        }
      }
      if (coast && hh <= 1) { city.terrain[i] = 3; continue; }   // 沙滩
      if (hh === 1) { city.terrain[i] = 0; continue; }           // 草地
      if (hh === 2) { city.terrain[i] = n2(x / 3.1, y / 3.1) > 0.70 ? 1 : 0; continue; }
      if (hh === 3) { city.terrain[i] = n3(x / 2.9, y / 2.9) > 0.55 ? 4 : 5; continue; }
      city.terrain[i] = n3(x / 4.2, y / 4.2) > 0.45 ? 4 : 5;
    }
  }
}
