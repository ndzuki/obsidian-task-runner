// 工具状态机：把「点/拖拽」翻译成对城市模型的编辑。
// 所有编辑都走 City 的方法，保证占用表与缓存版本号一致。

import { TERRAIN, ROADS, MAX_HEIGHT } from './city.js';

export class ToolRunner {
  constructor(city, assets) {
    this.city = city;
    this.assets = assets;
    this.tool = 'inspect';
    this.item = null;
    this.brush = 1; // 半径（0 = 单格）
    this.levelRef = null; // 整平工具参考高度
    this.last = null;
    this.strokeActive = false;
    this.onChange = () => {};
    this.onMessage = () => {};
    this.onBeforeEdit = () => {};
    this.undoArmed = false;
  }

  setCity(city) { this.city = city; }

  setTool(tool, item) {
    this.tool = tool;
    this.item = item || null;
    this.levelRef = null;
    this.last = null;
    this.strokeActive = false;
  }

  begin(tile, mods = {}) {
    if (!tile) return;
    this.strokeActive = true;
    // 一次「笔画」（按下到抬起）最多产生一个撤销点：
    // 到真正改动数据之前才压栈，避免空点击也塞进撤销栈。
    this.undoArmed = true;
    this.last = null;
    if (this.tool === 'level') this.levelRef = this.city.heightAt(tile.x, tile.y);
    if (this.tool === 'inspect') { this.inspect(tile); return; }
    if (mods.erase) { this.bulldozeAt(tile.x, tile.y); this.last = tile; return; }
    this.apply(tile.x, tile.y);
    this.last = tile;
  }

  drag(tile) {
    if (!this.strokeActive || !tile) return;
    if (this.tool === 'inspect') { this.inspect(tile); this.last = tile; return; }
    if (!this.last) { this.apply(tile.x, tile.y); this.last = tile; return; }
    // 沿拖拽路径补齐中间格，快速拖动也不会漏格
    const path = line(this.last.x, this.last.y, tile.x, tile.y);
    for (const p of path) this.apply(p.x, p.y);
    this.last = tile;
  }

  end() {
    this.strokeActive = false;
    this.last = null;
  }

  inspect(tile) {
    const o = this.city.objectAt(tile.x, tile.y);
    this.onMessage(o ? `${o.label} · 人口 ${o.pop} · 岗位 ${o.jobs}` :
      `${TERRAIN[this.city.terrainAt(tile.x, tile.y)].name} · 海拔 ${this.city.heightAt(tile.x, tile.y)}`);
  }

  markUndo() {
    if (!this.undoArmed) return;
    this.undoArmed = false;
    this.onBeforeEdit();
  }

  apply(x, y) {
    const c = this.city;
    if (!c.inside(x, y)) return;
    this.markUndo();
    const r = this.brush;
    switch (this.tool) {
      case 'raise': this.forEachBrush(x, y, r, (tx, ty) => {
        if (c.terrainAt(tx, ty) === 9 && c.heightAt(tx, ty) === 0) return;
        c.setHeight(tx, ty, c.heightAt(tx, ty) + 1);
      }); break;
      case 'lower': this.forEachBrush(x, y, r, (tx, ty) => {
        const h = c.heightAt(tx, ty);
        if (c.terrainAt(tx, ty) === 9) return;
        if (h - 1 <= 0) { c.removeAt(tx, ty); c.setRoad(tx, ty, 0); c.setTerrain(tx, ty, 9); c.setHeight(tx, ty, 0); }
        else c.setHeight(tx, ty, h - 1);
      }); break;
      case 'level': this.forEachBrush(x, y, r, (tx, ty) => {
        if (c.terrainAt(tx, ty) === 9) return;
        c.setHeight(tx, ty, this.levelRef ?? c.heightAt(tx, ty));
      }); break;
      case 'terrain': this.forEachBrush(x, y, r, (tx, ty) => this.paintTerrain(tx, ty)); break;
      case 'road': this.placeRoad(x, y); break;
      case 'build': this.placeObject(x, y, true); break;
      case 'prop': this.placeObject(x, y, false); break;
      case 'bulldoze': this.bulldozeAt(x, y); break;
      default: break;
    }
    this.onChange();
  }

  forEachBrush(x, y, r, fn) {
    for (let dy = -r; dy <= r; dy++) {
      for (let dx = -r; dx <= r; dx++) {
        if (dx * dx + dy * dy > r * r + r) continue;
        fn(x + dx, y + dy);
      }
    }
  }

  paintTerrain(x, y) {
    const c = this.city;
    const id = this.item?.terrainId ?? 0;
    if (!c.inside(x, y)) return;
    if (id === 9) {
      c.removeAt(x, y);
      c.setRoad(x, y, 0);
      c.setHeight(x, y, 0);
      c.setTerrain(x, y, 9);
      return;
    }
    // 从水面改成陆地：自动抬到海平面以上一格，做出岸线
    if (c.terrainAt(x, y) === 9) c.setHeight(x, y, 1);
    c.setTerrain(x, y, id);
  }

  placeRoad(x, y) {
    const c = this.city;
    if (!c.inside(x, y)) return;
    const type = this.item?.roadId ?? 1;
    if (c.terrainAt(x, y) === 9) return;
    if (c.objectAt(x, y)) c.removeAt(x, y);
    c.setRoad(x, y, type);
    if (c.heightAt(x, y) < 1) c.setHeight(x, y, 1);
  }

  placeObject(x, y, isBuilding) {
    const c = this.city;
    const def = this.item?.def;
    if (!def) return;
    const fp = def.fp || [1, 1];
    const allowWater = def.water === true;
    if (!c.canPlace(x, y, fp[0], fp[1], { allowWater, requireFlat: isBuilding })) {
      this.onMessage('此处无法放置');
      return;
    }
    const base = this.assets.baseOf(def.name);
    const ok = c.place({
      name: base, sprite: this.assets.pickVariant(base), label: def.label, category: def.category,
      fp, level: def.level || 1, pop: def.pop || 0, jobs: def.jobs || 0,
      cost: def.cost || 0, allowWater,
    }, x, y);
    if (ok) this.onMessage(`已建成 ${def.label}`);
  }

  bulldozeAt(x, y) {
    const c = this.city;
    if (!c.inside(x, y)) return;
    this.markUndo();
    if (c.objectAt(x, y)) { c.removeAt(x, y); return; }
    if (c.road[c.idx(x, y)] > 0) { c.setRoad(x, y, 0); return; }
  }
}

/** 两格之间的整数直线（Bresenham），用于拖拽补齐。 */
function line(x0, y0, x1, y1) {
  const out = [];
  let dx = Math.abs(x1 - x0), dy = Math.abs(y1 - y0);
  const sx = x0 < x1 ? 1 : -1, sy = y0 < y1 ? 1 : -1;
  let err = dx - dy;
  let x = x0, y = y0;
  let guard = 0;
  while (guard++ < 4096) {
    out.push({ x, y });
    if (x === x1 && y === y1) break;
    const e2 = 2 * err;
    if (e2 > -dy) { err -= dy; x += sx; }
    if (e2 < dx) { err += dx; y += sy; }
  }
  return out;
}

export { MAX_HEIGHT, TERRAIN, ROADS };
