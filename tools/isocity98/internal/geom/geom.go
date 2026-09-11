// Package geom 是程序化建模的最小几何词汇表：
// 只用长方体、棱台、柱体、锥体、椭球与贴花面拼装建筑单体，
// 全部在「格」坐标系里（1 格 = 1 单位），不做旋转（除车辆按 90° 步进）。
package geom

import (
	"math"

	"isocity98/internal/art"
)

// Vec3 是格坐标系下的三维点。Z 为高度，1 单位 = 8 像素。
type Vec3 struct{ X, Y, Z float64 }

func V(x, y, z float64) Vec3 { return Vec3{x, y, z} }

func (a Vec3) Add(b Vec3) Vec3 { return Vec3{a.X + b.X, a.Y + b.Y, a.Z + b.Z} }
func (a Vec3) Sub(b Vec3) Vec3 { return Vec3{a.X - b.X, a.Y - b.Y, a.Z - b.Z} }
func (a Vec3) Mul(k float64) Vec3 {
	return Vec3{a.X * k, a.Y * k, a.Z * k}
}
func (a Vec3) Lerp(b Vec3, t float64) Vec3 { return a.Add(b.Sub(a).Mul(t)) }

// 明暗槽位别名，便于构造器书写。
const (
	ShadeAuto   int8 = -1
	ShadeTop    int8 = int8(art.ShadeTop)
	ShadeLeft   int8 = int8(art.ShadeLeft)
	ShadeRight  int8 = int8(art.ShadeRight)
	ShadeBottom int8 = int8(art.ShadeBottom)
)

// Quad 是一个带材质的平面片。渲染器不做背面剔除，靠 Z-buffer 决定可见性，
// 因此高度场侧壁、建筑内壁都能正确遮挡。
type Quad struct {
	P     [4]Vec3
	Face  art.Face // 决定程序化图案的 UV 投影面
	Shade int8     // ShadeAuto 时由 Face 推导
	Mat   string
	Bias  float32 // 深度偏移，用于共面贴花（窗户、标线）
	Alpha float32 // 0 视为 1
	UOff  float64 // 图案相位偏移（水面动画）
	VOff  float64
}

// Mesh 是一组平面片。
type Mesh struct {
	Quads []Quad
}

func NewMesh() *Mesh { return &Mesh{} }

func (m *Mesh) Add(q Quad) *Mesh {
	m.Quads = append(m.Quads, q)
	return m
}

// Quad 追加一个任意四边形。
func (m *Mesh) Quad(mat string, face art.Face, shade int8, p0, p1, p2, p3 Vec3) *Mesh {
	return m.Add(Quad{P: [4]Vec3{p0, p1, p2, p3}, Face: face, Shade: shade, Mat: mat})
}

// Tri 以退化四边形表示三角形。
func (m *Mesh) Tri(mat string, face art.Face, shade int8, p0, p1, p2 Vec3) *Mesh {
	return m.Add(Quad{P: [4]Vec3{p0, p1, p2, p2}, Face: face, Shade: shade, Mat: mat})
}

func norm2(a, b float64) (float64, float64) {
	if a > b {
		return b, a
	}
	return a, b
}

// Box 追加一个长方体的五个面（不含底面）。坐标顺序无关。
func (m *Mesh) Box(x0, y0, z0, x1, y1, z1 float64, mat string) *Mesh {
	x0, x1 = norm2(x0, x1)
	y0, y1 = norm2(y0, y1)
	z0, z1 = norm2(z0, z1)
	// 顶面
	m.Quad(mat, art.FaceTop, ShadeTop,
		V(x0, y0, z1), V(x1, y0, z1), V(x1, y1, z1), V(x0, y1, z1))
	// +X（屏幕右下，背光面）
	m.Quad(mat, art.FacePosX, ShadeRight,
		V(x1, y0, z0), V(x1, y1, z0), V(x1, y1, z1), V(x1, y0, z1))
	// -X
	m.Quad(mat, art.FaceNegX, ShadeLeft,
		V(x0, y1, z0), V(x0, y0, z0), V(x0, y0, z1), V(x0, y1, z1))
	// +Y（屏幕左下，受光面）
	m.Quad(mat, art.FacePosY, ShadeLeft,
		V(x1, y1, z0), V(x0, y1, z0), V(x0, y1, z1), V(x1, y1, z1))
	// -Y
	m.Quad(mat, art.FaceNegY, ShadeRight,
		V(x0, y0, z0), V(x1, y0, z0), V(x1, y0, z1), V(x0, y0, z1))
	return m
}

// SlabTop 只画一个水平四边形（地形瓦片、场地、屋面）。
// 带一点深度偏移：屋面/场地常常与墙体的顶面共面，必须稳定压过它。
func (m *Mesh) SlabTop(z, x0, y0, x1, y1 float64, mat string) *Mesh {
	x0, x1 = norm2(x0, x1)
	y0, y1 = norm2(y0, y1)
	return m.Add(Quad{
		P:    [4]Vec3{V(x0, y0, z), V(x1, y0, z), V(x1, y1, z), V(x0, y1, z)},
		Face: art.FaceTop, Shade: ShadeTop, Mat: mat, Bias: 0.05,
	})
}

// Frustum 是一个「棱台」：底面矩形 (x0,y0)-(x1,y1) 在 z0，
// 顶面矩形按 inset 内收后在 z1。它是屋顶家族的通用件：
//
//	inset = 0        → 平屋顶（板）
//	insetX>0, Y=0    → 双坡屋顶（屋脊沿 X）
//	inset 都很小      → 四坡屋顶
//	inset 大到退化    → 攒尖顶
//
// roofMat 用于四个坡面，capMat 用于顶部平台（顶面退化时自动跳过）。
func (m *Mesh) Frustum(x0, y0, x1, y1, z0, z1, insetX, insetY float64, roofMat, capMat string) *Mesh {
	x0, x1 = norm2(x0, x1)
	y0, y1 = norm2(y0, y1)
	z0, z1 = norm2(z0, z1)
	tx0, tx1 := x0+insetX, x1-insetX
	ty0, ty1 := y0+insetY, y1-insetY
	degenerateX := tx1-tx0 < 1e-6
	degenerateY := ty1-ty0 < 1e-6
	// 四个坡面（朝 +Y 与 +X 的坡受光不同，形成屋顶转折）
	if !degenerateY {
		m.Quad(roofMat, art.FacePosY, ShadeLeft,
			V(x1, y1, z0), V(x0, y1, z0), V(tx0, ty1, z1), V(tx1, ty1, z1))
		m.Quad(roofMat, art.FaceNegY, ShadeRight,
			V(x0, y0, z0), V(x1, y0, z0), V(tx1, ty0, z1), V(tx0, ty0, z1))
	} else {
		// 退化成一条屋脊：两个坡面在脊线处收拢
		m.Quad(roofMat, art.FacePosY, ShadeLeft,
			V(x1, y1, z0), V(x0, y1, z0), V(tx0, ty1, z1), V(tx1, ty1, z1))
		m.Quad(roofMat, art.FaceNegY, ShadeRight,
			V(x0, y0, z0), V(x1, y0, z0), V(tx1, ty0, z1), V(tx0, ty0, z1))
	}
	if !degenerateX {
		m.Quad(roofMat, art.FacePosX, ShadeRight,
			V(x1, y0, z0), V(x1, y1, z0), V(tx1, ty1, z1), V(tx1, ty0, z1))
		m.Quad(roofMat, art.FaceNegX, ShadeLeft,
			V(x0, y1, z0), V(x0, y0, z0), V(tx0, ty0, z1), V(tx0, ty1, z1))
	} else {
		m.Tri(roofMat, art.FacePosX, ShadeRight,
			V(x1, y0, z0), V(x1, y1, z0), V(tx1, ty1, z1))
		m.Tri(roofMat, art.FaceNegX, ShadeLeft,
			V(x0, y1, z0), V(x0, y0, z0), V(tx0, ty0, z1))
	}
	if !degenerateX && !degenerateY {
		m.SlabTop(z1, tx0, ty0, tx1, ty1, capMat)
	}
	return m
}

// Parapet 在屋顶四周加女儿墙（矮墙圈），工业/商业建筑常见。
func (m *Mesh) Parapet(x0, y0, x1, y1, z, h, t float64, mat string) *Mesh {
	m.Box(x0, y0, z, x1, y0+t, z+h, mat)
	m.Box(x0, y1-t, z, x1, y1, z+h, mat)
	m.Box(x0, y0+t, z, x0+t, y1-t, z+h, mat)
	m.Box(x1-t, y0+t, z, x1, y1-t, z+h, mat)
	return m
}

// Cylinder 是 n 棱柱：顶盖 + 侧面。
func (m *Mesh) Cylinder(cx, cy, r, z0, z1 float64, sides int, mat string) *Mesh {
	z0, z1 = norm2(z0, z1)
	pts := make([]Vec3, sides)
	for i := 0; i < sides; i++ {
		a := 2 * math.Pi * float64(i) / float64(sides)
		pts[i] = V(cx+r*math.Cos(a), cy+r*math.Sin(a), 0)
	}
	// 顶盖
	for i := 0; i < sides; i++ {
		p0 := pts[i]
		p1 := pts[(i+1)%sides]
		m.Tri(mat, art.FaceTop, ShadeTop,
			V(cx, cy, z1), V(p0.X, p0.Y, z1), V(p1.X, p1.Y, z1))
	}
	// 侧面：按面法向决定明暗（朝 +Y 的受光，朝 +X 的背光）
	for i := 0; i < sides; i++ {
		p0 := pts[i]
		p1 := pts[(i+1)%sides]
		mx := (p0.X + p1.X) / 2
		my := (p0.Y + p1.Y) / 2
		sh := ShadeRight
		if my-cy > math.Abs(mx-cx) {
			sh = ShadeLeft
		}
		m.Quad(mat, art.FacePosY, sh,
			V(p0.X, p0.Y, z0), V(p1.X, p1.Y, z0), V(p1.X, p1.Y, z1), V(p0.X, p0.Y, z1))
	}
	return m
}

// Cone 是 n 棱锥（塔尖、树冠）。
func (m *Mesh) Cone(cx, cy, r, z0, z1 float64, sides int, mat string) *Mesh {
	z0, z1 = norm2(z0, z1)
	pts := make([]Vec3, sides)
	for i := 0; i < sides; i++ {
		a := 2 * math.Pi * float64(i) / float64(sides)
		pts[i] = V(cx+r*math.Cos(a), cy+r*math.Sin(a), 0)
	}
	for i := 0; i < sides; i++ {
		p0 := pts[i]
		p1 := pts[(i+1)%sides]
		mx := (p0.X + p1.X) / 2
		my := (p0.Y + p1.Y) / 2
		sh := ShadeRight
		if my-cy > math.Abs(mx-cx) {
			sh = ShadeLeft
		}
		m.Tri(mat, art.FacePosY, sh,
			V(p0.X, p0.Y, z0), V(p1.X, p1.Y, z0), V(cx, cy, z1))
	}
	return m
}

// Ellipsoid 是低多边形椭球（树冠、圆顶、油罐端头）。
// jitter 用于给顶点半径加确定性扰动，避免过于数学化的球。
func (m *Mesh) Ellipsoid(cx, cy, cz, rx, ry, rz float64, rings, segs int, jitter float64, seed int, mat string) *Mesh {
	pt := func(i, j int) Vec3 {
		phi := math.Pi * float64(i) / float64(rings)
		theta := 2 * math.Pi * float64(j) / float64(segs)
		k := 1.0
		if jitter > 0 {
			k = 1 + (float64(art.Hash(i, j, seed)%1000)/1000-0.5)*2*jitter
		}
		return V(
			cx+rx*k*math.Sin(phi)*math.Cos(theta),
			cy+ry*k*math.Sin(phi)*math.Sin(theta),
			cz+rz*k*math.Cos(phi),
		)
	}
	for i := 0; i < rings; i++ {
		for j := 0; j < segs; j++ {
			p0 := pt(i, j)
			p1 := pt(i, j+1)
			p2 := pt(i+1, j+1)
			p3 := pt(i+1, j)
			my := (p0.Y + p1.Y + p2.Y + p3.Y) / 4
			sh := ShadeRight
			switch {
			case (p0.Z+p1.Z+p2.Z+p3.Z)/4 > cz+rz*0.35:
				sh = ShadeTop
			case my-cy > 0:
				sh = ShadeLeft
			}
			m.Quad(mat, art.FaceTop, sh, p0, p1, p2, p3)
		}
	}
	return m
}

// DecalX 在 x=const 的竖直面上贴一片矩形（窗户、门、招牌）。
// y0..y1 与 z0..z1 为范围，bias 让贴花压过共面的墙。
func (m *Mesh) DecalX(x, y0, y1, z0, z1 float64, mat string, bias float32) *Mesh {
	y0, y1 = norm2(y0, y1)
	z0, z1 = norm2(z0, z1)
	return m.Add(Quad{
		P:    [4]Vec3{V(x, y0, z1), V(x, y1, z1), V(x, y1, z0), V(x, y0, z0)},
		Face: art.FacePosX, Shade: ShadeAuto, Mat: mat, Bias: bias,
	})
}

// DecalY 在 y=const 的竖直面上贴一片矩形。
func (m *Mesh) DecalY(y, x0, x1, z0, z1 float64, mat string, bias float32) *Mesh {
	x0, x1 = norm2(x0, x1)
	z0, z1 = norm2(z0, z1)
	return m.Add(Quad{
		P:    [4]Vec3{V(x1, y, z1), V(x0, y, z1), V(x0, y, z0), V(x1, y, z0)},
		Face: art.FacePosY, Shade: ShadeAuto, Mat: mat, Bias: bias,
	})
}

// DecalTop 在 z=const 的水平面上贴一片矩形（屋顶标线、场地花纹）。
func (m *Mesh) DecalTop(z, x0, y0, x1, y1 float64, mat string, bias float32) *Mesh {
	x0, x1 = norm2(x0, x1)
	y0, y1 = norm2(y0, y1)
	return m.Add(Quad{
		P:    [4]Vec3{V(x0, y0, z), V(x1, y0, z), V(x1, y1, z), V(x0, y1, z)},
		Face: art.FaceTop, Shade: ShadeAuto, Mat: mat, Bias: bias,
	})
}

// WindowGrid 在竖直面上铺一片窗格阵列。face 决定贴在哪一对面（PosX 或 PosY）。
// 用「窗 + 窗楣线脚」两步做，保证 1x 下也能看清楼层分隔。
func (m *Mesh) WindowGrid(face art.Face, fixed, a0, a1, z0, z1 float64, cols, rows int, winMat, frameMat string) *Mesh {
	da := (a1 - a0) / float64(cols)
	dz := (z1 - z0) / float64(rows)
	const gapH = 0.05  // 水平（窗与窗之间）留白
	const gapV = 0.085 // 垂直（窗与楼板之间）留白：窗高约占楼层 55%
	for i := 0; i < cols; i++ {
		for j := 0; j < rows; j++ {
			u0 := a0 + float64(i)*da + gapH
			u1 := a0 + float64(i+1)*da - gapH
			w0 := z0 + float64(j)*dz + gapV
			w1 := z0 + float64(j+1)*dz - gapV*0.8
			if w1 <= w0 {
				continue
			}
			if face == art.FacePosX || face == art.FaceNegX {
				m.DecalX(fixed, u0, u1, w0, w1, winMat, 0.02)
				if frameMat != "" {
					m.DecalX(fixed, a0, a1, z0+float64(j)*dz, z0+float64(j)*dz+gapV*0.7, frameMat, 0.03)
				}
			} else {
				m.DecalY(fixed, u0, u1, w0, w1, winMat, 0.02)
				if frameMat != "" {
					m.DecalY(fixed, a0, a1, z0+float64(j)*dz, z0+float64(j)*dz+gapV*0.7, frameMat, 0.03)
				}
			}
		}
	}
	return m
}

// Stairs 是一段台阶（用于高差处的入口）。
func (m *Mesh) Stairs(x0, y0, x1, y1, z0, z1 float64, steps int, alongX bool, mat string) *Mesh {
	if steps < 1 {
		steps = 1
	}
	for i := 0; i < steps; i++ {
		t0 := float64(i) / float64(steps)
		t1 := float64(i+1) / float64(steps)
		z := z0 + (z1-z0)*t1
		if alongX {
			a0 := x0 + (x1-x0)*t0
			a1 := x0 + (x1-x0)*t1
			m.Box(a0, y0, z0, a1, y1, z, mat)
		} else {
			a0 := y0 + (y1-y0)*t0
			a1 := y0 + (y1-y0)*t1
			m.Box(x0, a0, z0, x1, a1, z, mat)
		}
	}
	return m
}

// Translate 平移整个网格。
func (m *Mesh) Translate(d Vec3) *Mesh {
	for i := range m.Quads {
		for j := 0; j < 4; j++ {
			m.Quads[i].P[j] = m.Quads[i].P[j].Add(d)
		}
	}
	return m
}

// RotateZ90 绕**格子中心 (0.5,0.5)** 按 90° 步进旋转（k 为步数，可为负）。
// 车辆与角色四向复用同一模型。
//
// 为什么绕格子中心而不是绕原点：模型（车辆、角色、道具）都建在格子中心，
// 绕原点旋转会把它们整体甩出去**整整一格** —— 四个朝向的精灵会分别落在
// 相邻格子里，运行时按锚点绘制就变成「车停在旁边那格、人走到隔壁格」。
// 绕中心则四个朝向都稳稳落在本格。
func (m *Mesh) RotateZ90(k int) *Mesh {
	k = ((k % 4) + 4) % 4
	if k == 0 {
		return m
	}
	rot := func(p Vec3) Vec3 {
		x, y := p.X-0.5, p.Y-0.5
		for i := 0; i < k; i++ {
			x, y = -y, x
		}
		return V(x+0.5, y+0.5, p.Z)
	}
	for i := range m.Quads {
		for j := 0; j < 4; j++ {
			m.Quads[i].P[j] = rot(m.Quads[i].P[j])
		}
		// 面朝向同步旋转，保证图案不错切
		switch m.Quads[i].Face {
		case art.FacePosX:
			m.Quads[i].Face = art.FacePosX
		case art.FacePosY:
			m.Quads[i].Face = art.FacePosY
		}
		if m.Quads[i].Shade == ShadeLeft && k%2 == 1 {
			m.Quads[i].Shade = ShadeRight
		} else if m.Quads[i].Shade == ShadeRight && k%2 == 1 {
			m.Quads[i].Shade = ShadeLeft
		}
	}
	return m
}

// Clone 深拷贝网格。
func (m *Mesh) Clone() *Mesh {
	out := &Mesh{Quads: make([]Quad, len(m.Quads))}
	copy(out.Quads, m.Quads)
	return out
}

// Merge 把 other 平移到 off 后并入。
func (m *Mesh) Merge(other *Mesh, off Vec3) *Mesh {
	for _, q := range other.Quads {
		n := q
		for j := 0; j < 4; j++ {
			n.P[j] = n.P[j].Add(off)
		}
		m.Quads = append(m.Quads, n)
	}
	return m
}

// Bounds 返回网格的轴对齐包围盒。
func (m *Mesh) Bounds() (min, max Vec3) {
	first := true
	for _, q := range m.Quads {
		for _, p := range q.P {
			if first {
				min, max, first = p, p, false
				continue
			}
			min.X = math.Min(min.X, p.X)
			min.Y = math.Min(min.Y, p.Y)
			min.Z = math.Min(min.Z, p.Z)
			max.X = math.Max(max.X, p.X)
			max.Y = math.Max(max.Y, p.Y)
			max.Z = math.Max(max.Z, p.Z)
		}
	}
	return min, max
}
