// Package render 是离线预渲染管线的核心：
// 固定 2:1 等距正交投影 + Z-buffer 平面着色光栅化 + 超采样抗锯齿，
// 再把「美术定向光照」「烘焙接触阴影」「自发光辉光」合成进一张透明背景精灵图。
//
// 这里刻意不做任何真实光照/PBR：明暗来自材质表里手调的色阶位置，
// 阴影是美术指定的固定方向扫掠体，观感优先于物理正确。
package render

import (
	"fmt"
	"image"
	"math"

	"isocity98/internal/art"
	"isocity98/internal/geom"
)

// 等距投影常量：1 格 = 32x16 像素的菱形，1 高度单位 = 8 像素。
// 32:16 即严格的 2:1，与 DOS/PC-98 时代等距游戏一致。
//
// 这三个量是**像素密度**，不是几何尺度：几何体一律用「格」为单位建模，
// 因此把 Scale 从 1 调到 2 就等于把整条管线的分辨率翻倍（1 格变 64x32 像素），
// 既不改任何建模代码，也不改色板与图案的相对比例。
const (
	BaseTileW = 32
	BaseTileH = 16
	BaseZUnit = 8
)

var (
	TileW = BaseTileW
	TileH = BaseTileH
	ZUnit = BaseZUnit
	// Scale 是当前的像素密度倍数。
	Scale = 1
)

// SetScale 设置整条管线的像素密度（1 = 32x16 格，2 = 64x32 格）。
// 同时把「程序化图案的纹素密度」一起调大，保证砖缝/木纹在视觉上的相对粗细不变。
func SetScale(s int) {
	if s < 1 {
		s = 1
	}
	if s > 4 {
		s = 4
	}
	Scale = s
	TileW, TileH, ZUnit = BaseTileW*s, BaseTileH*s, BaseZUnit*s
	art.SetTexelsPerTile(float64(BaseTileW * s))
}

// Project 把格坐标投影到屏幕坐标（屏幕 Y 向下），并给出深度（越大越靠近相机）。
func Project(p geom.Vec3) (sx, sy, depth float64) {
	return (p.X - p.Y) * (float64(TileW) / 2), (p.X+p.Y)*(float64(TileH)/2) - p.Z*float64(ZUnit), p.X + p.Y + 2*p.Z
}

// ProjectXY 是不带深度的快捷投影。
func ProjectXY(x, y, z float64) (float64, float64) {
	sx, sy, _ := Project(geom.V(x, y, z))
	return sx, sy
}

// Sprite 是一张已渲染好的精灵图，Anchor 是网格局部原点 (0,0,0) 在图像中的像素位置。
// 运行时只需 screen = tileScreenPos - Anchor 即可贴图。
type Sprite struct {
	Name    string
	Img     *image.NRGBA
	W, H    int
	AnchorX int
	AnchorY int
}

// ShadowSrc 描述一个投影体积：底面矩形 + 上下高度，用于生成扫掠阴影。
type ShadowSrc struct {
	X0, Y0, X1, Y1 float64
	Z0, Z1         float64
}

// ShadowOpts 控制烘焙阴影。方向由美术指定：屏幕右下（与左上光源一致）。
type ShadowOpts struct {
	On    bool
	DX    float64 // 每单位高度的地面 X 偏移（格）
	DY    float64 // 每单位高度的地面 Y 偏移（格）
	Alpha float32
	Steps int
	Srcs  []ShadowSrc
}

// Options 是一次精灵渲染的参数。
type Options struct {
	SS      int     // 超采样倍率
	Pad     int     // 四周留白（1x 像素）
	Expand  float64 // 屏幕空间外扩（1x 像素），用于消除瓦片接缝
	Bounds  *Rect   // 手动指定 1x 画布范围（含原点），地形瓦片用
	NoTrim  bool    // 不裁剪透明边（地形/道路必须保持统一尺寸）
	Sprite  art.SpriteOpts
	Shadow  ShadowOpts
	NoGlow  bool
	GlowAmt float64
}

// Rect 是 1x 像素的整数矩形。
type Rect struct{ MinX, MinY, MaxX, MaxY int }

func (r Rect) W() int { return r.MaxX - r.MinX }
func (r Rect) H() int { return r.MaxY - r.MinY }

// Renderer 持有色板与光照档。
type Renderer struct {
	Pal     *art.Palette
	Profile *art.Profile
}

func New(pal *art.Palette, prof *art.Profile) *Renderer {
	return &Renderer{Pal: pal, Profile: prof}
}

type vtx struct {
	x, y  float64 // 屏幕（相对画布原点，已乘超采样）
	d     float64 // 深度
	w     geom.Vec3
	bias  float32
	valid bool
}

type raster struct {
	w, h  int
	ss    int
	depth []float32
	col   *art.Buf
	lit   *art.Buf
	r     *Renderer
}

// Render 把网格渲染成精灵图。
func (r *Renderer) Render(name string, m *geom.Mesh, o Options) (*Sprite, error) {
	ss := o.SS
	if ss <= 0 {
		ss = 4
	}
	if len(m.Quads) == 0 {
		return nil, fmt.Errorf("render: 网格 %s 为空", name)
	}
	for _, q := range m.Quads {
		if _, ok := r.Profile.Get(q.Mat); !ok {
			return nil, fmt.Errorf("render: %s 引用了 %q 档不存在的材质 %q", name, r.Profile.Name, q.Mat)
		}
	}

	// 1) 在 1x 空间算出画面范围（含阴影扫掠体）
	bounds := o.Bounds
	if bounds == nil {
		b := r.autoBounds(m, o)
		bounds = &b
	}
	W, H := bounds.W(), bounds.H()
	if W <= 0 || H <= 0 {
		return nil, fmt.Errorf("render: %s 画面范围为空", name)
	}

	// 2) 超采样画布
	rs := &raster{
		w: W * ss, h: H * ss, ss: ss,
		depth: make([]float32, W*ss*H*ss),
		col:   art.NewBuf(W*ss, H*ss),
		lit:   art.NewBuf(W*ss, H*ss),
		r:     r,
	}
	for i := range rs.depth {
		rs.depth[i] = float32(math.Inf(-1))
	}

	// 3) 光栅化所有面
	patCache := map[string]art.Pattern{}
	for _, q := range m.Quads {
		mat, _ := r.Profile.Get(q.Mat)
		pat, ok := patCache[mat.Pattern]
		if !ok {
			pat = art.Pat(mat.Pattern)
			patCache[mat.Pattern] = pat
		}
		rs.drawQuad(q, mat, pat, bounds, o.Expand)
	}

	// 4) 阴影层（在物体之下）
	var shadow *art.Buf
	if o.Shadow.On && len(o.Shadow.Srcs) > 0 {
		shadow = rs.drawShadow(o.Shadow, bounds, ss)
	}

	// 5) 降采样
	col := rs.col.Downsample(ss)
	lit := rs.lit.Downsample(ss)
	if shadow != nil {
		shadow = shadow.Downsample(ss)
	}

	// 6) 辉光：对自发光层做模糊后加法叠加（含轮廓外的光晕）
	if !o.NoGlow && r.Profile.GlowGain > 0 && lit.Opacity() > 0 {
		amt := o.GlowAmt
		if amt == 0 {
			amt = float64(r.Profile.GlowGain)
		}
		glow := lit.Blur(1).Blur(2)
		addGlow(col, glow, amt)
	}

	// 7) 合成阴影到底层
	if shadow != nil {
		under := art.NewBuf(col.W, col.H)
		under.Composite(shadow, 0, 0)
		under.Composite(col, 0, 0)
		col = under
	}

	// 8) 裁剪 + 量化
	img := col.ToNRGBA(r.Pal, o.Sprite)
	ax, ay := -bounds.MinX, -bounds.MinY
	if !o.NoTrim {
		bb := alphaBounds(img)
		if bb.Dx() > 0 && bb.Dy() > 0 {
			img = cropNRGBA(img, bb)
			ax -= bb.Min.X
			ay -= bb.Min.Y
		}
	}
	return &Sprite{
		Name: name, Img: img, W: img.Rect.Dx(), H: img.Rect.Dy(),
		AnchorX: ax, AnchorY: ay,
	}, nil
}

// autoBounds 根据网格与阴影的投影范围推算画布，并保证锚点在画面内。
func (r *Renderer) autoBounds(m *geom.Mesh, o Options) Rect {
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	add := func(x, y, z float64) {
		sx, sy, _ := Project(geom.V(x, y, z))
		minX, maxX = math.Min(minX, sx), math.Max(maxX, sx)
		minY, maxY = math.Min(minY, sy), math.Max(maxY, sy)
	}
	for _, q := range m.Quads {
		for _, p := range q.P {
			add(p.X, p.Y, p.Z)
		}
	}
	if o.Shadow.On {
		for _, s := range o.Shadow.Srcs {
			dx := o.Shadow.DX * (s.Z1 - s.Z0)
			dy := o.Shadow.DY * (s.Z1 - s.Z0)
			for _, c := range [4][2]float64{{s.X0, s.Y0}, {s.X1, s.Y0}, {s.X1, s.Y1}, {s.X0, s.Y1}} {
				add(c[0], c[1], s.Z0)
				add(c[0]+dx, c[1]+dy, s.Z0)
			}
		}
	}
	// 保证局部原点 (0,0,0) 在画面内，锚点才有意义
	add(0, 0, 0)
	pad := float64(o.Pad)
	if pad == 0 {
		pad = 1
	}
	return Rect{
		MinX: int(math.Floor(minX - pad)),
		MinY: int(math.Floor(minY - pad)),
		MaxX: int(math.Ceil(maxX + pad)),
		MaxY: int(math.Ceil(maxY + pad)),
	}
}

func (rs *raster) project(p geom.Vec3, bounds *Rect) vtx {
	sx, sy, d := Project(p)
	return vtx{
		x: (sx - float64(bounds.MinX)) * float64(rs.ss),
		y: (sy - float64(bounds.MinY)) * float64(rs.ss),
		d: d, w: p, valid: true,
	}
}

func (rs *raster) drawQuad(q geom.Quad, mat art.Material, pat art.Pattern, bounds *Rect, expand float64) {
	v := [4]vtx{}
	for i, p := range q.P {
		v[i] = rs.project(p, bounds)
		v[i].bias = q.Bias
	}
	shade := q.Shade
	if shade == geom.ShadeAuto {
		shade = shadeFromFace(q.Face)
	}
	alpha := q.Alpha
	if alpha <= 0 {
		alpha = mat.Alpha
	}
	if alpha <= 0 {
		alpha = 1
	}
	// 拆成两个三角形；退化四边形只画一个三角形
	for _, t := range [2][3]int{{0, 1, 2}, {0, 2, 3}} {
		p0, p1, p2 := v[t[0]], v[t[1]], v[t[2]]
		if expand > 0 {
			p0, p1, p2 = expandTri(p0, p1, p2, expand*float64(rs.ss))
		}
		rs.triColor(p0, p1, p2, q, mat, pat, int(shade), alpha)
	}
}

// triColor 是平面着色的核心：逐像素求程序化图案 → 查色阶 → 写色。
func (rs *raster) triColor(p0, p1, p2 vtx, q geom.Quad, mat art.Material, pat art.Pattern, shade int, alpha float32) {
	eachPixel(p0, p1, p2, rs.w, rs.h, func(x, y int, b0, b1, b2 float64) {
		d := float32(b0*p0.d+b1*p1.d+b2*p2.d) + q.Bias
		i := y*rs.w + x
		if d <= rs.depth[i] {
			return
		}
		wx := b0*p0.w.X + b1*p1.w.X + b2*p2.w.X
		wy := b0*p0.w.Y + b1*p1.w.Y + b2*p2.w.Y
		wz := b0*p0.w.Z + b1*p1.w.Z + b2*p2.w.Z
		delta := float32(0)
		if pat != nil {
			u, vv := art.FaceUV(q.Face, wx, wy, wz)
			delta = pat(art.PatCtx{U: u + q.UOff, V: vv + q.VOff, X: wx, Y: wy, Z: wz, Face: q.Face})
		}
		col, glow := rs.r.resolve(mat, shade, delta, wx, wy, wz)
		rs.depth[i] = d
		cr, cg, cb := float32(col.R)/255, float32(col.G)/255, float32(col.B)/255
		rs.col.SetRGB(x, y, cr, cg, cb)
		if glow > 0 {
			rs.lit.Add(x, y, cr, cg, cb, 1, glow)
		}
	})
}

// resolve 把材质 + 朝向 + 图案偏移解析成最终颜色。
// 这是「美术定向光照」的落地点：明暗 = 色阶位置，而不是光照方程。
func (r *Renderer) resolve(m art.Material, shade int, delta float32, wx, wy, wz float64) (art.RGBA, float32) {
	prof := r.Profile
	pos := m.Shade[clampShade(shade)]
	if m.Emissive {
		if delta <= 0 {
			// 灭灯的窗/未点的霓虹：用暗玻璃色阶，随环境明暗一起压暗
			return r.tint(r.Pal.Snap(m.OffRamp, 0.30*prof.ShadeScale)), 0
		}
		p := (0.50 + delta*0.48) * (0.86 + 0.14*pos)
		return r.Pal.Snap(m.Ramp, p), m.Glow
	}
	p := pos*prof.ShadeScale + delta*art.PatternAmp
	c := r.Pal.Snap(m.Ramp, p)
	return r.tint(c), 0
}

func (r *Renderer) tint(c art.RGBA) art.RGBA {
	if r.Profile.TintAmt <= 0 {
		return c
	}
	return art.Lerp(c, r.Profile.Tint, r.Profile.TintAmt)
}

func clampShade(s int) int {
	if s < 0 || s > 3 {
		return 0
	}
	return s
}

func shadeFromFace(f art.Face) int8 {
	switch f {
	case art.FaceTop:
		return geom.ShadeTop
	case art.FacePosY, art.FaceNegX:
		return geom.ShadeLeft
	case art.FacePosX, art.FaceNegY:
		return geom.ShadeRight
	}
	return geom.ShadeBottom
}

// drawShadow 用「底面矩形按高度扫掠」的方式生成烘焙阴影。
// 每步画一个底面矩形并取 max 合成，得到连续的扫掠体。
func (rs *raster) drawShadow(o ShadowOpts, bounds *Rect, ss int) *art.Buf {
	buf := art.NewBuf(rs.w, rs.h)
	steps := o.Steps
	if steps <= 0 {
		steps = 8
	}
	col := art.Hex("0a0a0e")
	cr, cg, cb := float32(col.R)/255, float32(col.G)/255, float32(col.B)/255
	alpha := o.Alpha
	if alpha <= 0 {
		alpha = 0.32
	}
	for _, s := range o.Srcs {
		h := s.Z1 - s.Z0
		if h < 0 {
			h = 0
		}
		// 扫掠体：底面矩形沿光方向平移 h 的范围内取并集
		for k := 0; k <= steps; k++ {
			t := float64(k) / float64(steps)
			dx := o.DX * h * t
			dy := o.DY * h * t
			z := s.Z0
			a := rs.project(geom.V(s.X0+dx, s.Y0+dy, z), bounds)
			b := rs.project(geom.V(s.X1+dx, s.Y0+dy, z), bounds)
			c := rs.project(geom.V(s.X1+dx, s.Y1+dy, z), bounds)
			d := rs.project(geom.V(s.X0+dx, s.Y1+dy, z), bounds)
			for _, tri := range [2][3]vtx{{a, b, c}, {a, c, d}} {
				eachPixel(tri[0], tri[1], tri[2], rs.w, rs.h, func(x, y int, b0, b1, b2 float64) {
					buf.Blend(x, y, cr, cg, cb, alpha)
				})
			}
		}
	}
	return buf
}

// eachPixel 遍历三角形覆盖的像素，回调给出重心坐标（始终为正）。
func eachPixel(p0, p1, p2 vtx, w, h int, fn func(x, y int, b0, b1, b2 float64)) {
	area := edge(p0.x, p0.y, p1.x, p1.y, p2.x, p2.y)
	if math.Abs(area) < 1e-9 {
		return
	}
	if area < 0 {
		p0, p1 = p1, p0
		area = -area
	}
	minX := int(math.Floor(math.Min(p0.x, math.Min(p1.x, p2.x))))
	maxX := int(math.Ceil(math.Max(p0.x, math.Max(p1.x, p2.x))))
	minY := int(math.Floor(math.Min(p0.y, math.Min(p1.y, p2.y))))
	maxY := int(math.Ceil(math.Max(p0.y, math.Max(p1.y, p2.y))))
	if minX < 0 {
		minX = 0
	}
	if minY < 0 {
		minY = 0
	}
	if maxX > w-1 {
		maxX = w - 1
	}
	if maxY > h-1 {
		maxY = h - 1
	}
	for y := minY; y <= maxY; y++ {
		fy := float64(y) + 0.5
		for x := minX; x <= maxX; x++ {
			fx := float64(x) + 0.5
			b0 := edge(p1.x, p1.y, p2.x, p2.y, fx, fy) / area
			if b0 < -1e-9 {
				continue
			}
			b1 := edge(p2.x, p2.y, p0.x, p0.y, fx, fy) / area
			if b1 < -1e-9 {
				continue
			}
			b2 := 1 - b0 - b1
			if b2 < -1e-9 {
				continue
			}
			fn(x, y, b0, b1, b2)
		}
	}
}

func edge(ax, ay, bx, by, px, py float64) float64 {
	return (bx-ax)*(py-ay) - (by-ay)*(px-ax)
}

// expandTri 把三角形从重心向外扩张 k 像素（超采样空间），用于消除瓦片接缝。
func expandTri(p0, p1, p2 vtx, k float64) (vtx, vtx, vtx) {
	cx := (p0.x + p1.x + p2.x) / 3
	cy := (p0.y + p1.y + p2.y) / 3
	push := func(p vtx) vtx {
		dx, dy := p.x-cx, p.y-cy
		l := math.Hypot(dx, dy)
		if l < 1e-9 {
			return p
		}
		p.x += dx / l * k
		p.y += dy / l * k
		return p
	}
	return push(p0), push(p1), push(p2)
}

// addGlow 把发光层叠加进颜色层，并给轮廓外的光晕补上 alpha，让辉光真的「溢出」。
func addGlow(col, glow *art.Buf, amt float64) {
	for i := range col.A {
		ga := glow.A[i]
		if ga <= 0.002 {
			continue
		}
		k := float32(amt)
		col.R[i] += glow.R[i] * k
		col.G[i] += glow.G[i] * k
		col.B[i] += glow.B[i] * k
		if a := ga * float32(amt) * 0.85; a > col.A[i] {
			col.A[i] = a
		}
	}
}

func alphaBounds(img *image.NRGBA) image.Rectangle {
	minX, minY := img.Rect.Max.X, img.Rect.Max.Y
	maxX, maxY := img.Rect.Min.X-1, img.Rect.Min.Y-1
	for y := img.Rect.Min.Y; y < img.Rect.Max.Y; y++ {
		for x := img.Rect.Min.X; x < img.Rect.Max.X; x++ {
			if img.Pix[img.PixOffset(x, y)+3] > 6 {
				if x < minX {
					minX = x
				}
				if y < minY {
					minY = y
				}
				if x > maxX {
					maxX = x
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	if maxX < minX || maxY < minY {
		return image.Rectangle{}
	}
	return image.Rect(minX, minY, maxX+1, maxY+1)
}

func cropNRGBA(img *image.NRGBA, r image.Rectangle) *image.NRGBA {
	r = r.Intersect(img.Rect)
	out := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	for y := 0; y < r.Dy(); y++ {
		src := img.PixOffset(r.Min.X, r.Min.Y+y)
		dst := out.PixOffset(0, y)
		copy(out.Pix[dst:dst+r.Dx()*4], img.Pix[src:src+r.Dx()*4])
	}
	return out
}
