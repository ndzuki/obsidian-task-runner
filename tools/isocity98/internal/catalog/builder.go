// Package catalog 用程序化方式建模全部建筑单体、地形、道路与道具。
// 每个条目只是一个函数：用 geom 的少量构件拼出形体，材质名引用 art 的光照档。
// 同一原型用不同随机种子生成多个变体，得到「成组但不重复」的城市肌理。
package catalog

import (
	"fmt"
	"math"

	"isocity98/internal/art"
	"isocity98/internal/geom"
	"isocity98/internal/render"
)

// 种类：决定预渲染时的默认参数与运行时的摆放规则。
const (
	KindBuilding = "building"
	KindTerrain  = "terrain"
	KindRoad     = "road"
	KindProp     = "prop"
	KindVehicle  = "vehicle"
)

// Def 描述一个可预渲染的单体。
type Def struct {
	Name      string
	Label     string // UI 显示名（中文）
	Kind      string
	Category  string
	Footprint [2]int  // 占地格数
	Height    float64 // 世界高度（单位：格高度），用于烘焙阴影长度
	Variants  int
	Cost      int
	Pop       int
	Jobs      int
	Level     int  // 成长等级 1..3（1 最低）
	NoShadow  bool // 地形/道路不需要投影
	// ShadowFP 可选：自定义投影占地 [x0,y0,x1,y1]。
	// 小体量道具（车辆/长椅）若用整格投影会变成一个巨大的黑洞。
	ShadowFP []float64
	Cliff    bool // 侧壁瓦片：使用 CliffBounds
	Water    bool // 允许放在水面上（船、码头）
	// Seasonal 表示「形状随季节变化」，需要按季节各烘一套（树/灌木/花坛/水面）。
	// 只是颜色变化的东西不要标它 —— 那些交给运行时的调色板重映射。
	Seasonal bool
	Anim     bool // 变体是动画帧而不是随机变体
	Bounds   *render.Rect
	Build    func(b *B)
}

// B 是建模辅助器：持有网格与当前矩形范围（格坐标）。
// Inset/Sub 可以派生出「退台」子范围，用于塔楼收分、屋顶设备等。
type B struct {
	M              *geom.Mesh
	X0, Y0, X1, Y1 float64
	R              *art.Rand
	Frame          int // 变体/动画帧序号：用于给程序化图案换相位，避免同款瓦片完全一样
}

func (b *B) W() float64 { return b.X1 - b.X0 }
func (b *B) H() float64 { return b.Y1 - b.Y0 }
func (b *B) CX() float64 {
	return (b.X0 + b.X1) / 2
}
func (b *B) CY() float64 {
	return (b.Y0 + b.Y1) / 2
}

// Inset 返回四边内收 m 的子范围（共享网格与随机源）。
func (b *B) Inset(m float64) *B {
	return &B{M: b.M, X0: b.X0 + m, Y0: b.Y0 + m, X1: b.X1 - m, Y1: b.Y1 - m, R: b.R}
}

// Sub 返回指定矩形范围的子范围。
func (b *B) Sub(x0, y0, x1, y1 float64) *B {
	return &B{M: b.M, X0: x0, Y0: y0, X1: x1, Y1: y1, R: b.R}
}

func (b *B) Box(x0, y0, z0, x1, y1, z1 float64, mat string) *B {
	b.M.Box(x0, y0, z0, x1, y1, z1, mat)
	return b
}

func (b *B) Cyl(cx, cy, r, z0, z1 float64, sides int, mat string) *B {
	b.M.Cylinder(cx, cy, r, z0, z1, sides, mat)
	return b
}

func (b *B) Cone(cx, cy, r, z0, z1 float64, sides int, mat string) *B {
	b.M.Cone(cx, cy, r, z0, z1, sides, mat)
	return b
}

func (b *B) Blob(cx, cy, cz, rx, ry, rz float64, rings, segs int, jitter float64, mat string) *B {
	b.M.Ellipsoid(cx, cy, cz, rx, ry, rz, rings, segs, jitter, int(b.R.Next()%997), mat)
	return b
}

// Walls 建四面墙（不含顶面），高度 z0..z1。
func (b *B) Walls(z0, z1 float64, mat string) *B {
	b.M.Box(b.X0, b.Y0, z0, b.X1, b.Y1, z1, mat)
	return b
}

// Solid 建一个完整长方体（含顶面）。
func (b *B) Solid(z0, z1 float64, mat string) *B {
	b.M.Box(b.X0, b.Y0, z0, b.X1, b.Y1, z1, mat)
	return b
}

// FloorBand 在 z 处加一圈略微外挑的楼层线脚，是「1x 下能读出楼层」的关键。
func (b *B) FloorBand(z float64, out float64, mat string) *B {
	b.M.Box(b.X0-out, b.Y0-out, z, b.X1+out, b.Y1+out, z+0.055, mat)
	return b
}

// Windows 在四面墙上铺窗格阵列。rows 为楼层数，cols 为每面列数。
func (b *B) Windows(z0, z1 float64, rows, cols int, winMat, frameMat string) *B {
	return b.WindowsMargin(z0, z1, rows, cols, winMat, frameMat, 0.13)
}

// WindowsMargin 可指定窗格距墙边的留白。
func (b *B) WindowsMargin(z0, z1 float64, rows, cols int, winMat, frameMat string, margin float64) *B {
	if rows < 1 {
		rows = 1
	}
	if cols < 1 {
		cols = 1
	}
	zw0, zw1 := z0+0.14, z1-0.14
	if zw1-zw0 < 0.2 {
		zw0, zw1 = z0+0.05, z1-0.05
	}
	b.M.WindowGrid(art.FacePosX, b.X1, b.Y0+margin, b.Y1-margin, zw0, zw1, cols, rows, winMat, frameMat)
	b.M.WindowGrid(art.FaceNegX, b.X0, b.Y0+margin, b.Y1-margin, zw0, zw1, cols, rows, winMat, frameMat)
	b.M.WindowGrid(art.FacePosY, b.Y1, b.X0+margin, b.X1-margin, zw0, zw1, cols, rows, winMat, frameMat)
	b.M.WindowGrid(art.FaceNegY, b.Y0, b.X0+margin, b.X1-margin, zw0, zw1, cols, rows, winMat, frameMat)
	return b
}

// WindowsOn 只在指定朝向的墙上铺窗（用于「正面有窗、侧面是防火墙」的联排住宅）。
func (b *B) WindowsOn(face art.Face, z0, z1 float64, rows, cols int, winMat, frameMat string) *B {
	margin := 0.13
	zw0, zw1 := z0+0.14, z1-0.14
	if zw1-zw0 < 0.2 {
		zw0, zw1 = z0+0.05, z1-0.05
	}
	switch face {
	case art.FacePosX:
		b.M.WindowGrid(face, b.X1, b.Y0+margin, b.Y1-margin, zw0, zw1, cols, rows, winMat, frameMat)
	case art.FaceNegX:
		b.M.WindowGrid(face, b.X0, b.Y0+margin, b.Y1-margin, zw0, zw1, cols, rows, winMat, frameMat)
	case art.FacePosY:
		b.M.WindowGrid(face, b.Y1, b.X0+margin, b.X1-margin, zw0, zw1, cols, rows, winMat, frameMat)
	case art.FaceNegY:
		b.M.WindowGrid(face, b.Y0, b.X0+margin, b.X1-margin, zw0, zw1, cols, rows, winMat, frameMat)
	}
	return b
}

// FlatRoof 平屋顶 + 女儿墙。
func (b *B) FlatRoof(z float64, capMat string, parapet float64, parapetMat string) *B {
	b.M.SlabTop(z, b.X0, b.Y0, b.X1, b.Y1, capMat)
	if parapet > 0 {
		b.M.Parapet(b.X0, b.Y0, b.X1, b.Y1, z, parapet, 0.07, parapetMat)
	}
	return b
}

// GableRoof 双坡屋顶；ridgeAlongX 决定屋脊方向。
func (b *B) GableRoof(z0, z1 float64, ridgeAlongX bool, roofMat, capMat string) *B {
	inset := b.H() / 2
	if ridgeAlongX {
		inset = b.W() / 2
	}
	if ridgeAlongX {
		b.M.Frustum(b.X0, b.Y0, b.X1, b.Y1, z0, z1, 0, inset, roofMat, capMat)
	} else {
		b.M.Frustum(b.X0, b.Y0, b.X1, b.Y1, z0, z1, inset, 0, roofMat, capMat)
	}
	return b
}

// HipRoof 四坡屋顶（inset 相同即攒尖）。
func (b *B) HipRoof(z0, z1, inset float64, roofMat, capMat string) *B {
	b.M.Frustum(b.X0, b.Y0, b.X1, b.Y1, z0, z1, inset, inset, roofMat, capMat)
	return b
}

// Eave 出檐：屋顶下沿向外挑一圈薄板。
func (b *B) Eave(z, out float64, mat string) *B {
	b.M.Box(b.X0-out, b.Y0-out, z-0.05, b.X1+out, b.Y1+out, z, mat)
	return b
}

// Door 在指定朝向的墙上开门（face, at 为中心位置）。
func (b *B) Door(face art.Face, at, z0, w, h float64, mat string) *B {
	const bias = 0.03
	switch face {
	case art.FacePosX:
		b.M.DecalX(b.X1, at-w/2, at+w/2, z0, z0+h, mat, bias)
	case art.FaceNegX:
		b.M.DecalX(b.X0, at-w/2, at+w/2, z0, z0+h, mat, bias)
	case art.FacePosY:
		b.M.DecalY(b.Y1, at-w/2, at+w/2, z0, z0+h, mat, bias)
	case art.FaceNegY:
		b.M.DecalY(b.Y0, at-w/2, at+w/2, z0, z0+h, mat, bias)
	}
	return b
}

// Awning 在墙上装雨棚。
func (b *B) Awning(face art.Face, at, z, w, depth float64, mat string) *B {
	switch face {
	case art.FacePosX:
		b.M.Box(b.X1, at-w/2, z-0.06, b.X1+depth, at+w/2, z, mat)
		b.M.Quad(mat, art.FacePosX, geom.ShadeLeft,
			geom.V(b.X1+depth, at-w/2, z-0.06), geom.V(b.X1+depth, at+w/2, z-0.06),
			geom.V(b.X1+depth, at+w/2, z), geom.V(b.X1+depth, at-w/2, z))
	case art.FaceNegX:
		b.M.Box(b.X0-depth, at-w/2, z-0.06, b.X0, at+w/2, z, mat)
	case art.FacePosY:
		b.M.Box(at-w/2, b.Y1, z-0.06, at+w/2, b.Y1+depth, z, mat)
		b.M.Quad(mat, art.FacePosY, geom.ShadeLeft,
			geom.V(at-w/2, b.Y1+depth, z-0.06), geom.V(at+w/2, b.Y1+depth, z-0.06),
			geom.V(at+w/2, b.Y1+depth, z), geom.V(at-w/2, b.Y1+depth, z))
	case art.FaceNegY:
		b.M.Box(at-w/2, b.Y0-depth, z-0.06, at+w/2, b.Y0, z, mat)
	}
	return b
}

// Sign 在墙上挂招牌（自发光材质在夜间会点亮）。
func (b *B) Sign(face art.Face, at, z, w, h float64, mat string) *B {
	const bias = 0.05
	switch face {
	case art.FacePosX:
		b.M.Box(b.X1, at-w/2, z, b.X1+0.06, at+w/2, z+h, "trim.dark")
		b.M.DecalX(b.X1+0.061, at-w/2+0.03, at+w/2-0.03, z+0.03, z+h-0.03, mat, bias)
	case art.FaceNegX:
		b.M.Box(b.X0-0.06, at-w/2, z, b.X0, at+w/2, z+h, "trim.dark")
		b.M.DecalX(b.X0-0.061, at-w/2+0.03, at+w/2-0.03, z+0.03, z+h-0.03, mat, bias)
	case art.FacePosY:
		b.M.Box(at-w/2, b.Y1, z, at+w/2, b.Y1+0.06, z+h, "trim.dark")
		b.M.DecalY(b.Y1+0.061, at-w/2+0.03, at+w/2-0.03, z+0.03, z+h-0.03, mat, bias)
	case art.FaceNegY:
		b.M.Box(at-w/2, b.Y0-0.06, z, at+w/2, b.Y0, z+h, "trim.dark")
		b.M.DecalY(b.Y0-0.061, at-w/2+0.03, at+w/2-0.03, z+0.03, z+h-0.03, mat, bias)
	}
	return b
}

// Chimney 烟囱。
func (b *B) Chimney(x, y, z0, h, r float64, mat string) *B {
	b.M.Box(x-r, y-r, z0, x+r, y+r, z0+h, mat)
	b.M.Box(x-r*1.3, y-r*1.3, z0+h, x+r*1.3, y+r*1.3, z0+h+0.07, "trim.dark")
	return b
}

// Antenna 天线/桅杆。
func (b *B) Antenna(x, y, z0, h float64) *B {
	b.M.Box(x-0.035, y-0.035, z0, x+0.035, y+0.035, z0+h, "metal.pipe")
	b.M.Box(x-0.02, y-0.22, z0+h*0.72, x+0.02, y+0.22, z0+h*0.74, "metal.pipe")
	if h > 1.2 {
		b.M.Box(x-0.02, y-0.16, z0+h*0.5, x+0.02, y+0.16, z0+h*0.52, "metal.pipe")
	}
	return b
}

// WaterTank 屋顶水箱（工业/老城区标志物）。
func (b *B) WaterTank(x, y, z, r, h float64, mat string) *B {
	b.M.Box(x-r*0.7, y-r*0.7, z, x+r*0.7, y+r*0.7, z+0.5, "metal.dark")
	b.M.Cylinder(x, y, r, z+0.5, z+0.5+h, 8, mat)
	b.M.Cylinder(x, y, r*0.85, z+0.5+h, z+0.5+h+0.12, 8, "trim.dark")
	return b
}

// Stairs 台阶。
func (b *B) Stairs(x0, y0, x1, y1, z0, z1 float64, steps int, alongX bool, mat string) *B {
	b.M.Stairs(x0, y0, x1, y1, z0, z1, steps, alongX, mat)
	return b
}

// Stoop 入口台阶 + 门廊（住宅常见）。
func (b *B) Stoop(face art.Face, at, w, ztop float64) *B {
	const d = 0.34
	switch face {
	case art.FacePosY:
		b.Stairs(at-w/2, b.Y1, at+w/2, b.Y1+d, 0, ztop, 3, false, "concrete.pad")
	case art.FaceNegY:
		b.Stairs(at-w/2, b.Y0-d, at+w/2, b.Y0, 0, ztop, 3, false, "concrete.pad")
	case art.FacePosX:
		b.Stairs(b.X1, at-w/2, b.X1+d, at+w/2, 0, ztop, 3, true, "concrete.pad")
	case art.FaceNegX:
		b.Stairs(b.X0-d, at-w/2, b.X0, at+w/2, 0, ztop, 3, true, "concrete.pad")
	}
	return b
}

// ACUnits 屋顶空调机组。
func (b *B) ACUnits(z float64, n int, mat string) *B {
	for i := 0; i < n; i++ {
		x := b.X0 + 0.3 + b.R.Float()*(b.W()-0.9)
		y := b.Y0 + 0.3 + b.R.Float()*(b.H()-0.9)
		w := 0.22 + b.R.Float()*0.18
		b.M.Box(x, y, z, x+w, y+w, z+0.16+b.R.Float()*0.1, mat)
	}
	return b
}

// Planter 场地绿化：沿给定矩形边种树。
func (b *B) Trees(x, y, n int, mat string) *B {
	for i := 0; i < n; i++ {
		px := b.X0 + b.R.Float()*b.W()
		py := b.Y0 + b.R.Float()*b.H()
		TreeAt(b.M, px, py, 0, b.R, mat)
	}
	return b
}

// TreeAt 在指定位置生成一棵树（供多处复用）。
func TreeAt(m *geom.Mesh, x, y, z float64, r *art.Rand, leaf string) {
	h := 0.42 + r.Float()*0.22
	m.Box(x-0.05, y-0.05, z, x+0.05, y+0.05, z+h, "trunk")
	crown := h + 0.28 + r.Float()*0.2
	m.Ellipsoid(x, y, z+crown, 0.30+r.Float()*0.1, 0.30+r.Float()*0.1, 0.26+r.Float()*0.08,
		3, 8, 0.14, int(r.Next()%997), leaf)
	if r.Chance(0.5) {
		m.Ellipsoid(x+0.13, y-0.1, z+crown*0.72, 0.19, 0.19, 0.16, 3, 8, 0.16, int(r.Next()%997), leaf)
	}
}

// RoofDeck 屋顶花园/平台。
func (b *B) RoofDeck(z float64, mat string) *B {
	b.M.SlabTop(z, b.X0, b.Y0, b.X1, b.Y1, mat)
	return b
}

// Pipe 沿墙的落水管/管道。
func (b *B) Pipe(x, y, z0, z1 float64) *B {
	b.M.Box(x-0.045, y-0.045, z0, x+0.045, y+0.045, z1, "metal.pipe")
	return b
}

// Ledges 在多个高度加挑檐（现代办公楼的水平线条）。
func (b *B) Ledges(z0 float64, levels int, dz, out float64, mat string) *B {
	for i := 0; i < levels; i++ {
		b.FloorBand(z0+float64(i)*dz, out, mat)
	}
	return b
}

// Sawtooth 锯齿形采光屋顶（厂房）。
func (b *B) Sawtooth(z0, z1 float64, teeth int, roofMat, glassMat string) *B {
	w := b.W() / float64(teeth)
	for i := 0; i < teeth; i++ {
		x0 := b.X0 + float64(i)*w
		x1 := x0 + w
		// 斜屋面
		b.M.Quad(roofMat, art.FaceTop, geom.ShadeTop,
			geom.V(x0, b.Y0, z1), geom.V(x1, b.Y0, z0), geom.V(x1, b.Y1, z0), geom.V(x0, b.Y1, z1))
		// 竖向天窗
		b.M.Quad(glassMat, art.FacePosY, geom.ShadeLeft,
			geom.V(x1, b.Y1, z0), geom.V(x1, b.Y0, z0), geom.V(x1, b.Y0, z1), geom.V(x1, b.Y1, z1))
		b.M.Tri(glassMat, art.FacePosX, geom.ShadeRight,
			geom.V(x1, b.Y0, z0), geom.V(x1, b.Y1, z0), geom.V(x1, b.Y1, z0))
	}
	return b
}

// Vary 返回带抖动的数值，用于打破机械感。
func (b *B) Vary(base, amount float64) float64 {
	return base + (b.R.Float()-0.5)*2*amount
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// mustName 生成变体名。
func variantName(base string, i int) string {
	if i == 0 {
		return base
	}
	return fmt.Sprintf("%s_%d", base, i)
}

// shadowFor 由占地与高度推导烘焙阴影源。
func shadowFor(d Def) []render.ShadowSrc {
	if d.NoShadow || d.Height <= 0 {
		return nil
	}
	w, h := float64(d.Footprint[0]), float64(d.Footprint[1])
	return []render.ShadowSrc{{X0: 0, Y0: 0, X1: w, Y1: h, Z0: 0, Z1: d.Height}}
}

// BuildMesh 生成单体网格（带变体种子）。
func BuildMesh(d Def, variant int) *geom.Mesh {
	m := geom.NewMesh()
	b := &B{M: m, X0: 0, Y0: 0, X1: float64(d.Footprint[0]), Y1: float64(d.Footprint[1]),
		Frame: variant,
		R:     art.NewRand(uint32(0x51ed270b) ^ hashName(d.Name) ^ uint32(variant*7919+1))}
	d.Build(b)
	return m
}

func hashName(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}
