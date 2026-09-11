// 视口：相机、整数倍缩放、拾取（屏幕 → 格子）与光标预览。
// 相机在「整图缓存」的像素坐标系里移动，因此平移只是 drawImage 的一个偏移。

import { project, TILE_W, TILE_H, Z_UNIT, PX_SCALE } from './render.js';
import { MAX_HEIGHT } from './city.js';
import { LAYOUT } from './ui.js';

// 视口矩形直接复用 UI 布局里的那个对象（configureLayout 会就地改写它），
// 避免两处常量各写一遍后失同步。
export const VIEW = LAYOUT.view;

export class View {
  constructor() {
    this.camX = 0;
    this.camY = 0;
    this.zoom = 1;
    this.maxZoom = 4;
    this.hover = null; // {x,y}
  }

  centerOn(world, tx, ty) {
    const p = project(tx, ty, 0);
    this.camX = world.originX + p.sx - VIEW.w / (2 * this.zoom);
    this.camY = world.originY + p.sy - VIEW.h / (2 * this.zoom);
    this.clamp(world);
  }

  clamp(world) {
    const cw = world.cacheW || 0;
    const ch = world.cacheH || 0;
    const vw = VIEW.w / this.zoom;
    const vh = VIEW.h / this.zoom;
    const maxX = Math.max(0, cw - vw);
    const maxY = Math.max(0, ch - vh);
    // 允许略微越界（露出画布留白），但不让地图完全跑出视野
    const slack = 96 * PX_SCALE;
    this.camX = Math.min(maxX + slack, Math.max(-slack, this.camX));
    this.camY = Math.min(maxY + slack, Math.max(-slack, this.camY));
  }

  pan(dx, dy) {
    this.camX += dx;
    this.camY += dy;
  }

  setZoom(z, world, anchorVX, anchorVY) {
    z = Math.max(1, Math.min(this.maxZoom, z));
    if (z === this.zoom) return;
    // 以光标位置为锚点缩放，手感更接近老游戏的「就地放大」
    const ax = anchorVX !== undefined ? anchorVX : VIEW.w / 2;
    const ay = anchorVY !== undefined ? anchorVY : VIEW.h / 2;
    const wx = this.camX + ax / this.zoom;
    const wy = this.camY + ay / this.zoom;
    this.zoom = z;
    this.camX = wx - ax / this.zoom;
    this.camY = wy - ay / this.zoom;
    this.clamp(world);
  }

  /** 屏幕（画布）坐标 → 视口局部坐标。 */
  toLocal(vx, vy) {
    return { x: vx - VIEW.x, y: vy - VIEW.y };
  }

  inView(vx, vy) {
    return vx >= VIEW.x && vx < VIEW.x + VIEW.w && vy >= VIEW.y && vy < VIEW.y + VIEW.h;
  }

  /** 视口局部坐标 → 世界格（沿 z 从高到低求最近的表面交点）。 */
  pick(city, vx, vy) {
    const lx = (vx - VIEW.x) / this.zoom + this.camX;
    const ly = (vy - VIEW.y) / this.zoom + this.camY;
    return pickCache(city, this.originX, this.originY, lx, ly);
  }

  /** 格 → 画布屏幕坐标（锚点为该格 z 高度的菱形顶点）。 */
  toScreen(world, x, y, z) {
    const p = project(x, y, z);
    return {
      x: VIEW.x + (world.originX + p.sx - this.camX) * this.zoom,
      y: VIEW.y + (world.originY + p.sy - this.camY) * this.zoom,
      scale: this.zoom,
    };
  }

  /** 绘制视口：blit 缓存 + 水面逐帧覆盖 + 光标预览。 */
  draw(ctx, city, world, opts = {}) {
    world.ensure(city); // 只做逻辑度量；世界每帧直接绘制
    this.originX = world.originX;
    this.originY = world.originY;
    ctx.save();
    ctx.beginPath();
    ctx.rect(VIEW.x, VIEW.y, VIEW.w, VIEW.h);
    ctx.clip();

    ctx.imageSmoothingEnabled = false;
    const sw = VIEW.w / this.zoom;
    const sh = VIEW.h / this.zoom;
    ctx.fillStyle = '#0a0f16';
    ctx.fillRect(VIEW.x, VIEW.y, VIEW.w, VIEW.h);
    // 直接绘制可见范围：正确的画家序（水面会被更近的崖壁/建筑正常遮挡），
    // 也省掉了几十 MB 的整图缓存与重建卡顿。
    world.drawRange(ctx, city, this, {
      vx: VIEW.x, vy: VIEW.y, vw: sw, vh: sh,
      water: world.waterFrameName(),
    });

    if (this.hover) this.drawCursor(ctx, city, world, opts);
    ctx.restore();
  }

  /** 光标：1 格高亮；放置类工具额外画出占地轮廓。 */
  drawCursor(ctx, city, world, opts) {
    const { x, y } = this.hover;
    const z = city.heightAt(x, y);
    const fp = opts.footprint || [1, 1];
    const ok = opts.valid !== false;
    const z2 = this.zoom;
    const mk = (tx, ty, tz, color, fill) => {
      const s = this.toScreen(world, tx, ty, tz);
      const top = { x: s.x, y: s.y };
      const pts = [
        [top.x, top.y],
        [top.x + TILE_W / 2 * z2, top.y + TILE_H / 2 * z2],
        [top.x, top.y + TILE_H * z2],
        [top.x - TILE_W / 2 * z2, top.y + TILE_H / 2 * z2],
      ];
      if (fill) {
        ctx.fillStyle = fill;
        ctx.beginPath();
        ctx.moveTo(pts[0][0], pts[0][1]);
        for (let i = 1; i < 4; i++) ctx.lineTo(pts[i][0], pts[i][1]);
        ctx.closePath();
        ctx.fill();
      }
      crispDiamond(ctx, pts, color, z2);
    };
    // 占地范围（只看占地格，不看高度）
    for (let dy = 0; dy < fp[1]; dy++) {
      for (let dx = 0; dx < fp[0]; dx++) {
        const tx = x + dx, ty = y + dy;
        if (!city.inside(tx, ty)) continue;
        const tz = city.heightAt(tx, ty);
        const isFirst = dx === 0 && dy === 0;
        mk(tx, ty, tz, ok ? (isFirst ? '#ffffff' : '#9fe8e0') : '#d07f60',
          isFirst ? (ok ? 'rgba(159,232,224,0.22)' : 'rgba(208,127,96,0.28)') : null);
      }
    }
    void z;
  }
}

/** 用整数像素画菱形描边，避免 2D 路径抗锯齿把像素糊掉。 */
function crispDiamond(ctx, pts, color, scale) {
  ctx.fillStyle = color;
  const t = pts[0];
  const steps = Math.round(TILE_H * scale);
  const hw = (TILE_W / 2) * scale;
  // 描边粗细 = 缩放倍率 × 像素密度，保证在任何分辨率下都是同样的视觉重量
  const th = Math.max(1, Math.round(scale * PX_SCALE));
  const tx = Math.round(t[0]);
  const ty = Math.round(t[1]);
  for (let i = 0; i < steps; i++) {
    const k = i < steps / 2 ? i / (steps / 2) : (steps - i) / (steps / 2);
    const off = Math.round(k * hw);
    ctx.fillRect(tx - off, ty + i, th, 1);
    ctx.fillRect(tx + off - th + 1, ty + i, th, 1);
  }
}

/**
 * 沿 z 从高到低求最近的表面交点。
 * 固定屏幕点对应的是一条斜线：z 每加 1，(x,y) 同时 +0.5。
 *
 * 判定用的是「实心柱」而不是「恰好等于该层的顶面」：
 * 只要该格的高度 >= 该层，射线就已经进入这一格的实心体，
 * 于是点到侧壁（崖面）时也能选中它所属的格子。
 * 早期只比较 height === z 的写法会让所有崖面「点不中」。
 */
export function pickCache(city, originX, originY, cacheX, cacheY) {
  const A = (cacheX - originX) / (TILE_W / 2); // x - y
  const B = (cacheY - originY) / (TILE_H / 2); // x + y - z
  for (let z = MAX_HEIGHT; z >= 0; z--) {
    const s = B + z;
    const tx = Math.floor((s + A) / 2);
    const ty = Math.floor((s - A) / 2);
    if (!city.inside(tx, ty)) continue;
    const h = city.height[city.idx(tx, ty)];
    if (h >= z) return { x: tx, y: ty, z: h };
  }
  return null;
}

export { Z_UNIT };
