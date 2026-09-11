package catalog

import (
	"math"

	"isocity98/internal/art"
	"isocity98/internal/geom"
)

func warehouse() Def {
	return Def{
		Name: "warehouse", Label: "仓库", Kind: KindBuilding, Category: "工业",
		Footprint: [2]int{3, 2}, Height: 3.2, Variants: 3, Cost: 1500, Jobs: 60, Level: 1,
		Build: func(b *B) {
			b.Lot("pad.asphalt", "concrete.curb")
			w := b.Inset(0.1)
			w.M.Box(w.X0, w.Y0, 0, w.X1, w.Y1, 0.16, "concrete.curb")
			w.M.Box(w.X0, w.Y0, 0.16, w.X1, w.Y1, 1.85, "wall.metal.rust")
			// 墙面板缝
			for i := 1; i < 8; i++ {
				x := w.X0 + float64(i)*w.W()/8
				w.M.DecalX(w.X1, x, x+0.02, 0.16, 1.85, "metal.dark", 0.02)
			}
			w.M.Box(w.X0-0.12, w.Y0-0.12, 1.85, w.X1+0.12, w.Y1+0.12, 2.0, "metal.dark")
			w.GableRoof(2.0, 2.95, true, "roof.metal", "roof.metal")
			// 装卸月台与卷帘门
			for i := 0; i < 2; i++ {
				dx := w.X0 + 0.5 + float64(i)*1.5
				w.M.DecalY(w.Y1, dx, dx+0.9, 0.3, 1.5, "garage", 0.03)
				w.M.Box(dx-0.05, w.Y1, 0.16, dx+0.95, w.Y1+0.34, 0.3, "concrete.pad")
			}
			w.M.DecalY(w.Y1, w.X0+2.6, w.X0+2.85, 0.16, 1.7, "door.metal", 0.03)
			// 屋顶通风器
			for i := 0; i < 3; i++ {
				vx := w.X0 + 0.6 + float64(i)*1.0
				w.M.Cylinder(vx, w.CY(), 0.12, 2.7, 3.25, 6, "metal.dark")
			}
			// 场地上的货箱与卡车位
			b.M.Box(b.X1-0.5, b.Y1-0.42, 0, b.X1-0.28, b.Y1-0.2, 0.2, "crate.wood")
			b.M.Box(b.X1-0.26, b.Y1-0.4, 0, b.X1-0.06, b.Y1-0.2, 0.17, "crate.wood")
			b.Pipe(w.X1-0.02, w.Y0+0.3, 0.16, 1.9)
		},
	}
}

func factory() Def {
	return Def{
		Name: "factory", Label: "工厂", Kind: KindBuilding, Category: "工业",
		Footprint: [2]int{3, 3}, Height: 5.07, Variants: 3, Cost: 3400, Jobs: 180, Level: 2,
		Build: func(b *B) {
			b.Lot("pad.asphalt", "concrete.curb")
			// 主厂房（锯齿采光顶）
			shed := b.Sub(b.X0+0.12, b.Y0+0.12, b.X1-0.12, b.Y0+1.75)
			shed.M.Box(shed.X0, shed.Y0, 0, shed.X1, shed.Y1, 1.6, "wall.metal")
			shed.M.Box(shed.X0-0.08, shed.Y0-0.08, 1.6, shed.X1+0.08, shed.Y1+0.08, 1.72, "metal.dark")
			shed.Sawtooth(1.72, 2.42, 3, "roof.metal", "wall.glass")
			// 辅楼
			an := b.Sub(b.X0+0.12, b.Y0+1.9, b.X0+1.5, b.Y1-0.12)
			an.M.Box(an.X0, an.Y0, 0, an.X1, an.Y1, 2.1, "wall.concrete")
			an.Windows(0.3, 2.0, 2, 2, "window", "")
			an.M.SlabTop(2.1, an.X0, an.Y0, an.X1, an.Y1, "roof.felt")
			an.M.Parapet(an.X0, an.Y0, an.X1, an.Y1, 2.1, 0.24, 0.06, "concrete.curb")
			// 烟囱与储罐
			b.Chimney(b.X0+2.3, b.Y0+0.5, 1.6, 3.4, 0.14, "wall.brick")
			b.Chimney(b.X0+2.65, b.Y0+0.5, 1.6, 2.6, 0.11, "wall.metal.rust")
			b.M.Cylinder(b.X1-0.42, b.Y1-0.5, 0.32, 0, 1.15, 10, "wall.metal")
			b.M.Cylinder(b.X1-0.42, b.Y1-0.5, 0.34, 1.15, 1.22, 10, "metal.dark")
			b.M.Cylinder(b.X1-1.05, b.Y1-0.34, 0.22, 0, 0.8, 10, "wall.metal.rust")
			// 管道与堆料
			for i := 0; i < 3; i++ {
				z := 0.4 + float64(i)*0.22
				b.M.Box(b.X0+1.6, b.Y1-0.3, z, b.X1-1.4, b.Y1-0.22, z+0.08, "metal.pipe")
			}
			b.M.Box(b.X0+0.2, b.Y1-0.5, 0, b.X0+0.75, b.Y1-0.16, 0.28, "crate.wood")
		},
	}
}

// ---------------------------------------------------------------- 工业（追加）

// geom 只提供竖直柱体。卧式圆柱是工业区的标志物（原木、球罐支腿、架空管道、
// 卧式储罐、圆锯片），这里补一个沿 X 轴 / Y 轴的柱体，明暗沿用柱面法向。
func cylShade(my, mz, cy, cz, r float64) int8 {
	switch {
	case mz-cz > 0.45*r:
		return geom.ShadeTop
	case my-cy > 0:
		return geom.ShadeLeft
	}
	return geom.ShadeRight
}

// cylX 画一个轴沿 X 的圆柱（x0..x1 为两端，cy/cz 为轴心）。
func cylX(m *geom.Mesh, x0, x1, cy, cz, r float64, sides int, mat string) {
	for i := 0; i < sides; i++ {
		a0 := 2 * math.Pi * float64(i) / float64(sides)
		a1 := 2 * math.Pi * float64(i+1) / float64(sides)
		y0, z0 := cy+r*math.Cos(a0), cz+r*math.Sin(a0)
		y1, z1 := cy+r*math.Cos(a1), cz+r*math.Sin(a1)
		m.Quad(mat, art.FacePosY, cylShade((y0+y1)/2, (z0+z1)/2, cy, cz, r),
			geom.V(x0, y0, z0), geom.V(x1, y0, z0), geom.V(x1, y1, z1), geom.V(x0, y1, z1))
		m.Tri(mat, art.FacePosX, geom.ShadeRight, geom.V(x1, cy, cz), geom.V(x1, y0, z0), geom.V(x1, y1, z1))
		m.Tri(mat, art.FaceNegX, geom.ShadeLeft, geom.V(x0, cy, cz), geom.V(x0, y1, z1), geom.V(x0, y0, z0))
	}
}

// cylY 画一个轴沿 Y 的圆柱（圆锯片、横向管道）。
func cylY(m *geom.Mesh, y0, y1, cx, cz, r float64, sides int, mat string) {
	for i := 0; i < sides; i++ {
		a0 := 2 * math.Pi * float64(i) / float64(sides)
		a1 := 2 * math.Pi * float64(i+1) / float64(sides)
		x0, z0 := cx+r*math.Cos(a0), cz+r*math.Sin(a0)
		x1, z1 := cx+r*math.Cos(a1), cz+r*math.Sin(a1)
		m.Quad(mat, art.FaceNegY, cylShade(0, (z0+z1)/2, 0, cz, r),
			geom.V(x0, y0, z0), geom.V(x1, y0, z0), geom.V(x1, y1, z1), geom.V(x0, y1, z1))
		m.Tri(mat, art.FacePosY, geom.ShadeLeft, geom.V(cx, y1, cz), geom.V(x0, y1, z0), geom.V(x1, y1, z1))
		m.Tri(mat, art.FaceNegY, geom.ShadeRight, geom.V(cx, y0, cz), geom.V(x1, y0, z1), geom.V(x0, y0, z0))
	}
}

func powerPlant() Def {
	return Def{
		Name: "powerplant", Label: "发电厂", Kind: KindBuilding, Category: "工业",
		Footprint: [2]int{3, 3}, Height: 11.70, Variants: 3, Cost: 12000, Jobs: 260, Level: 3,
		Build: func(b *B) {
			b.Lot("pad.asphalt", "concrete.curb")
			// 锅炉房：大跨度厂房 + 高侧窗
			w := b.Sub(b.X0+0.12, b.Y0+0.12, b.X1-1.0, b.Y0+2.1)
			w.M.Box(w.X0, w.Y0, 0, w.X1, w.Y1, 0.3, "concrete.curb")
			w.M.Box(w.X0, w.Y0, 0.3, w.X1, w.Y1, 4.4, "wall.concrete")
			w.Windows(1.0, 2.6, 2, 4, "window.hall", "wall.concrete.warm")
			w.M.Box(w.X0-0.1, w.Y0-0.1, 4.4, w.X1+0.1, w.Y1+0.1, 4.6, "metal.dark")
			w.M.SlabTop(4.6, w.X0, w.Y0, w.X1, w.Y1, "roof.metal")
			w.M.Parapet(w.X0, w.Y0, w.X1, w.Y1, 4.6, 0.3, 0.08, "metal.dark")
			for i := 0; i < 3; i++ {
				w.M.Cylinder(w.X0+0.4+float64(i)*0.6, w.CY(), 0.14, 4.9, 5.5, 8, "metal.pipe")
			}
			// 双曲线冷却塔：分层圆柱逼近收腰轮廓
			for k := 0; k < 2; k++ {
				tx, ty := b.X0+0.72+float64(k)*0.92, b.Y1-0.72
				segs := []struct{ r, h float64 }{{0.5, 1.5}, {0.4, 1.5}, {0.35, 1.5}, {0.34, 1.2}, {0.4, 0.8}}
				z := 0.0
				for i, sg := range segs {
					mt := "wall.concrete"
					if i == 0 {
						mt = "wall.concrete.warm"
					}
					if i == len(segs)-1 {
						mt = "metal.dark"
					}
					b.M.Cylinder(tx, ty, sg.r, z, z+sg.h, 16, mt)
					if i > 0 {
						b.M.Cylinder(tx, ty, sg.r+0.02, z, z+0.08, 16, "metal.dark")
					}
					z += sg.h
				}
			}
			// 烟囱：红白环带在 1x 下也一眼认得
			chx, chy := b.X1-0.6, b.Y0+0.5
			for i := 0; i < 6; i++ {
				mt := "wall.brick"
				if i%2 == 1 {
					mt = "trim.white"
				}
				b.M.Cylinder(chx, chy, 0.26-float64(i)*0.014, 0.3+float64(i)*1.85, 0.3+float64(i+1)*1.85, 12, mt)
			}
			b.M.Cylinder(chx, chy, 0.3, 11.4, 11.7, 12, "metal.dark")
			// 升压站：构架 + 绝缘子
			for i := 0; i < 3; i++ {
				px := b.X1 - 0.75 + float64(i)*0.28
				b.M.Box(px-0.03, b.Y1-1.5, 0, px+0.03, b.Y1-1.44, 2.4, "metal.pipe")
				b.M.Box(px-0.14, b.Y1-1.52, 2.4, px+0.14, b.Y1-1.42, 2.52, "chrome")
				b.M.Box(px-0.03, b.Y1-1.44, 1.2, px+0.03, b.Y1-1.0, 1.26, "metal.pipe")
			}
			b.M.Box(b.X1-0.9, b.Y1-1.7, 0, b.X1-0.4, b.Y1-1.2, 1.2, "wall.metal.rust")
			b.M.Box(b.X1-0.28, b.Y1-2.9, 0, b.X1-0.06, b.Y1-2.68, 0.9, "sign.panel.dark")
		},
	}
}

func tankFarm() Def {
	return Def{
		Name: "tankfarm", Label: "储油罐区", Kind: KindBuilding, Category: "工业",
		Footprint: [2]int{3, 2}, Height: 4.6, Variants: 3, Cost: 4200, Jobs: 40, Level: 2,
		Build: func(b *B) {
			b.Lot("pad.asphalt", "concrete.curb")
			// 防火堤
			b.M.Parapet(0.14, 0.14, 2.86, 1.86, 0, 0.3, 0.07, "concrete.curb")
			tanks := []struct{ x, y, r, h float64 }{
				{b.X0 + 0.55, b.Y0 + 0.62, 0.4, 1.9},
				{b.X0 + 1.5, b.Y0 + 0.62, 0.4, 1.9},
				{b.X0 + 0.55, b.Y1 - 0.6, 0.34, 1.6},
				{b.X0 + 1.5, b.Y1 - 0.6, 0.34, 1.6},
				{b.X0 + 2.42, b.Y0 + 0.72, 0.28, 1.35},
			}
			for i, t := range tanks {
				body := "wall.metal"
				if i%2 == 1 {
					body = "wall.metal.rust"
				}
				b.M.Cylinder(t.x, t.y, t.r+0.05, 0, 0.18, 14, "concrete.curb")
				b.M.Cylinder(t.x, t.y, t.r, 0.18, t.h, 14, body)
				b.M.Cylinder(t.x, t.y, t.r+0.02, t.h-0.12, t.h, 14, "metal.dark")
				b.M.Cylinder(t.x, t.y, t.r*0.96, t.h, t.h+0.06, 14, "metal.dark")
				b.M.Cylinder(t.x, t.y, 0.05, t.h+0.06, t.h+0.3, 6, "metal.pipe")
				// 罐顶栏杆与爬梯
				b.M.Box(t.x-0.02, t.y-t.r, t.h+0.06, t.x+0.02, t.y+t.r, t.h+0.5, "metal.pipe")
				b.M.Box(t.x-t.r*0.5, t.y+t.r, 0.18, t.x-t.r*0.4, t.y+t.r+0.06, 0.9, "metal.pipe")
			}
			// 连通管廊
			cylX(b.M, b.X0+0.55, b.X1-0.42, b.Y0+1.0, 0.5, 0.06, 6, "metal.pipe")
			cylX(b.M, b.X0+0.55, b.X1-0.42, b.Y0+1.0, 0.68, 0.06, 6, "metal.pipe")
			for i := 0; i < 4; i++ {
				px := b.X0 + 0.7 + float64(i)*0.6
				b.M.Box(px-0.04, b.Y0+0.96, 0, px+0.04, b.Y0+1.04, 0.74, "metal.pipe")
			}
			// 泵房 + 火炬塔
			b.M.Box(b.X1-0.78, b.Y1-0.86, 0, b.X1-0.2, b.Y1-0.34, 1.0, "wall.metal.rust")
			b.M.SlabTop(1.0, b.X1-0.78, b.Y1-0.86, b.X1-0.2, b.Y1-0.34, "roof.felt")
			b.M.Cylinder(b.X1-0.3, b.Y0+0.34, 0.08, 0, 4.2, 8, "wall.metal.rust")
			b.M.Box(b.X1-0.42, b.Y0+0.22, 4.2, b.X1-0.18, b.Y0+0.46, 4.6, "streetlamp")
			b.M.Box(b.X1-0.42, b.Y0+0.22, 4.2, b.X1-0.18, b.Y0+0.34, 4.34, "metal.dark")
			// 堤内危险区标线
			b.M.DecalTop(0.03, b.X0+0.2, b.Y0+0.2, b.X0+2.5, b.Y0+0.26, "paint.white", 0.03)
		},
	}
}

func refinery() Def {
	return Def{
		Name: "refinery", Label: "精炼厂", Kind: KindBuilding, Category: "工业",
		Footprint: [2]int{3, 3}, Height: 7.6, Variants: 3, Cost: 7600, Jobs: 150, Level: 3,
		Build: func(b *B) {
			b.Lot("pad.concrete", "concrete.curb")
			// 精馏塔：细高柱 + 环向平台 + 顶盖
			cols := []struct{ x, y, r, h float64 }{
				{b.X0 + 0.5, b.Y0 + 0.55, 0.24, 6.2},
				{b.X0 + 1.12, b.Y0 + 0.55, 0.18, 5.0},
				{b.X0 + 0.5, b.Y0 + 1.25, 0.18, 4.4},
			}
			for _, c := range cols {
				b.M.Cylinder(c.x, c.y, c.r+0.05, 0, 0.25, 12, "concrete.curb")
				b.M.Cylinder(c.x, c.y, c.r, 0.25, c.h, 12, "wall.metal")
				for z := 1.4; z < c.h-0.3; z += 1.2 {
					b.M.Cylinder(c.x, c.y, c.r+0.06, z, z+0.1, 12, "metal.dark")
				}
				b.M.Ellipsoid(c.x, c.y, c.h, c.r*1.02, c.r*1.02, c.r*0.9, 3, 10, 0.02, int(c.x*113), "wall.metal")
				b.M.Cylinder(c.x, c.y, 0.05, c.h+c.r*0.8, c.h+c.r*0.8+0.5, 6, "metal.pipe")
			}
			// 球罐：四条支腿 + 椭球罐体
			for i := 0; i < 2; i++ {
				sx, sy := b.X0+2.0, b.Y0+0.7+float64(i)*1.15
				for _, p := range [4][2]float64{{sx - 0.32, sy - 0.32}, {sx + 0.32, sy - 0.32}, {sx - 0.32, sy + 0.32}, {sx + 0.32, sy + 0.32}} {
					b.M.Box(p[0]-0.04, p[1]-0.04, 0, p[0]+0.04, p[1]+0.04, 0.85, "metal.pipe")
				}
				b.M.Box(sx-0.4, sy-0.4, 0.8, sx+0.4, sy+0.4, 0.9, "metal.dark")
				b.M.Ellipsoid(sx, sy, 1.5, 0.5, 0.5, 0.55, 4, 12, 0.02, int(sx*97), "wall.metal")
				b.M.Cylinder(sx, sy, 0.06, 2.05, 2.6, 6, "metal.pipe")
			}
			// 水处理澄清池：浅圆池 + 刮泥臂
			for i := 0; i < 2; i++ {
				cx := b.X0 + 0.75 + float64(i)*0.95
				cy := b.Y1 - 0.62
				b.M.Cylinder(cx, cy, 0.46, 0, 0.55, 14, "wall.concrete")
				b.M.Cylinder(cx, cy, 0.42, 0.55, 0.6, 14, "water.shallow")
				b.M.Box(cx-0.44, cy-0.03, 0.62, cx+0.44, cy+0.03, 0.68, "metal.dark")
				b.M.Box(cx-0.04, cy-0.04, 0.68, cx+0.04, cy+0.04, 1.1, "metal.pipe")
			}
			// 架空管廊：立柱 + 三层管道
			for i := 0; i < 5; i++ {
				px := b.X0 + 0.3 + float64(i)*0.6
				b.M.Box(px-0.05, b.Y0+1.55, 0, px+0.05, b.Y0+1.65, 1.5, "metal.pipe")
			}
			for k := 0; k < 3; k++ {
				cylX(b.M, b.X0+0.3, b.X1-0.3, b.Y0+1.6, 0.5+float64(k)*0.42, 0.07, 6, "metal.pipe")
			}
			// 火炬塔：塔顶用自发光材质，夜间是工业区的亮点
			b.M.Cylinder(b.X1-0.32, b.Y1-0.32, 0.09, 0, 6.6, 8, "wall.metal.rust")
			b.M.Box(b.X1-0.46, b.Y1-0.46, 6.6, b.X1-0.18, b.Y1-0.18, 6.9, "metal.dark")
			b.M.Box(b.X1-0.4, b.Y1-0.4, 6.9, b.X1-0.24, b.Y1-0.24, 7.6, "streetlamp")
			b.M.Box(b.X1-0.62, b.Y1-0.62, 0, b.X1-0.56, b.Y1-0.56, 2.4, "metal.pipe")
			b.Pipe(b.X0+0.22, b.Y0+2.4, 0, 2.0)
		},
	}
}

func sawmill() Def {
	return Def{
		Name: "sawmill", Label: "锯木厂", Kind: KindBuilding, Category: "工业",
		Footprint: [2]int{3, 2}, Height: 3.50, Variants: 3, Cost: 1800, Jobs: 45, Level: 1,
		Build: func(b *B) {
			b.Lot("pad.dirt", "concrete.curb")
			// 敞棚：木柱 + 双坡顶（屋脊沿 X，insetY 取 H/2）
			sx0, sy0, sx1, sy1 := b.X0+0.14, b.Y0+0.14, b.X0+1.7, b.Y0+1.05
			for _, p := range [4][2]float64{{sx0, sy0}, {sx1, sy0}, {sx0, sy1}, {sx1, sy1}} {
				b.M.Box(p[0]-0.05, p[1]-0.05, 0, p[0]+0.05, p[1]+0.05, 2.5, "wall.wood")
			}
			b.M.Box(sx0-0.06, sy0-0.06, 2.5, sx1+0.06, sy1+0.06, 2.62, "trim.dark")
			b.M.Frustum(sx0-0.12, sy0-0.12, sx1+0.12, sy1+0.12, 2.62, 3.5, 0, (sy1-sy0)/2+0.12, "roof.metal", "roof.metal")
			// 棚下：原木跑车与圆锯片
			b.M.Box(sx0+0.14, sy0+0.2, 0, sx1-0.2, sy0+0.5, 0.28, "metal.dark")
			b.M.Box(sx0+0.2, sy0+0.24, 0.28, sx1-0.26, sy0+0.46, 0.38, "crate.wood")
			cylY(b.M, sy0+0.62, sy0+0.7, sx0+0.5, 0.85, 0.34, 12, "chrome")
			b.M.Box(sx0+0.42, sy0+0.56, 0, sx0+0.58, sy0+0.76, 0.55, "wall.metal.rust")
			// 原木堆：卧式圆柱分层码放
			for row := 0; row < 4; row++ {
				z := 0.14 + float64(row)*0.25
				for i := 0; i < 4; i++ {
					y := b.Y0 + 1.3 + float64(i)*0.16
					mt := "trunk"
					if (i+row)%3 == 0 {
						mt = "crate.wood"
					}
					cylX(b.M, b.X0+1.85, b.X1-0.22, y, z, 0.12, 8, mt)
				}
			}
			// 成品板材堆与锯屑
			b.M.Box(b.X0+1.9, b.Y0+0.24, 0, b.X1-0.5, b.Y0+0.78, 0.5, "crate.wood")
			b.M.DecalTop(0.51, b.X0+1.9, b.Y0+0.24, b.X1-0.5, b.Y0+0.78, "wall.wood", 0.03)
			b.M.Box(b.X0+1.95, b.Y0+0.3, 0.5, b.X1-0.55, b.Y0+0.72, 0.86, "crate.wood")
			b.M.Ellipsoid(b.X0+0.5, b.Y1-0.4, 0.12, 0.42, 0.32, 0.22, 3, 8, 0.2, 31, "pad.sand")
			TreeAt(b.M, b.X1-0.2, b.Y1-0.24, 0, b.R, "leaf.pine")
		},
	}
}

func freightDepot() Def {
	return Def{
		Name: "freight", Label: "货运站", Kind: KindBuilding, Category: "工业",
		Footprint: [2]int{3, 2}, Height: 6.2, Variants: 3, Cost: 3600, Jobs: 95, Level: 2,
		Build: func(b *B) {
			b.Lot("pad.gravel", "concrete.curb")
			// 站房
			d := b.Sub(b.X0+0.12, b.Y0+0.12, b.X0+1.75, b.Y0+0.95)
			d.M.Box(d.X0, d.Y0, 0, d.X1, d.Y1, 0.24, "concrete.curb")
			d.M.Box(d.X0, d.Y0, 0.24, d.X1, d.Y1, 2.9, "wall.metal.rust")
			for i := 0; i < 4; i++ {
				x := d.X0 + 0.2 + float64(i)*0.36
				d.M.DecalY(d.Y1, x, x+0.26, 0.3, 2.3, "garage", 0.03)
			}
			d.Windows(2.4, 2.78, 1, 3, "window.small", "")
			d.M.Box(d.X0-0.09, d.Y0-0.09, 2.9, d.X1+0.09, d.Y1+0.09, 3.1, "metal.dark")
			d.M.SlabTop(3.1, d.X0, d.Y0, d.X1, d.Y1, "roof.metal")
			d.M.Parapet(d.X0, d.Y0, d.X1, d.Y1, 3.1, 0.26, 0.07, "metal.dark")
			// 装卸月台
			b.M.Box(b.X0+0.12, b.Y0+1.0, 0, b.X1-0.12, b.Y0+1.5, 0.62, "concrete.pad")
			for i := 0; i < 4; i++ {
				px := b.X0 + 0.3 + float64(i)*0.62
				b.M.Box(px-0.05, b.Y0+1.5, 0.4, px+0.05, b.Y0+1.58, 0.62, "tire")
				b.M.Box(px-0.04, b.Y0+0.98, 0.62, px+0.04, b.Y0+1.04, 1.0, "metal.pipe")
			}
			// 龙门吊：双腿 + 横梁 + 小车 + 吊钩
			for _, lx := range [2]float64{b.X0 + 0.5, b.X1 - 0.5} {
				b.M.Box(lx-0.07, b.Y0+1.62, 0, lx+0.07, b.Y0+1.78, 5.6, "wall.metal.rust")
				b.M.Box(lx-0.28, b.Y0+1.58, 0, lx+0.28, b.Y0+1.82, 0.18, "metal.dark")
				b.M.Box(lx-0.1, b.Y0+1.6, 3.0, lx+0.1, b.Y0+1.8, 3.12, "metal.dark")
			}
			b.M.Box(b.X0+0.4, b.Y0+1.62, 5.6, b.X1-0.4, b.Y0+1.78, 5.9, "metal.dark")
			b.M.Box(b.X0+0.4, b.Y0+1.6, 5.5, b.X1-0.4, b.Y0+1.8, 5.6, "metal.pipe")
			b.M.Box(b.X1-1.15, b.Y0+1.58, 5.9, b.X0+1.6, b.Y0+1.82, 6.2, "wall.metal")
			b.M.Box(b.X0+1.56, b.Y0+1.66, 4.7, b.X0+1.62, b.Y0+1.74, 5.9, "metal.pipe")
			b.M.Box(b.X0+1.44, b.Y0+1.6, 4.5, b.X0+1.74, b.Y0+1.8, 4.72, "metal.dark")
			// 铁路支线与道砟
			for k := 0; k < 2; k++ {
				b.M.Box(b.X0+0.1, b.Y1-0.42+float64(k)*0.2, 0.02, b.X1-0.1, b.Y1-0.36+float64(k)*0.2, 0.1, "chrome")
			}
			for i := 0; i < 7; i++ {
				x := b.X0 + 0.18 + float64(i)*0.4
				b.M.Box(x, b.Y1-0.5, 0.01, x+0.08, b.Y1-0.18, 0.06, "crate.wood")
			}
			// 集装箱：两只叠放
			b.M.Box(b.X1-0.72, b.Y0+0.2, 0, b.X1-0.16, b.Y0+0.62, 0.66, "wall.panel.teal")
			b.M.Box(b.X1-0.72, b.Y0+0.2, 0.66, b.X1-0.16, b.Y0+0.62, 1.16, "wall.metal.rust")
			b.M.Box(b.X1-0.7, b.Y0+0.7, 0, b.X1-0.2, b.Y0+1.1, 0.62, "crate.wood")
		},
	}
}

func waterTower() Def {
	return Def{
		Name: "watertower", Label: "水塔", Kind: KindBuilding, Category: "工业",
		Footprint: [2]int{1, 1}, Height: 6.8, Variants: 3, Cost: 900, Jobs: 6, Level: 1,
		Build: func(b *B) {
			b.Lot("pad.concrete", "concrete.curb")
			tank := "wall.metal.rust"
			if b.R.Chance(0.5) {
				tank = "wall.metal"
			}
			// 四条支腿 + 交叉拉杆 + 检修平台
			legs := [4][2]float64{{b.X0 + 0.2, b.Y0 + 0.2}, {b.X1 - 0.2, b.Y0 + 0.2}, {b.X0 + 0.2, b.Y1 - 0.2}, {b.X1 - 0.2, b.Y1 - 0.2}}
			for _, p := range legs {
				b.M.Cylinder(p[0], p[1], 0.05, 0, 3.7, 6, "metal.pipe")
			}
			for _, z := range [2]float64{1.2, 2.4} {
				b.M.Box(b.X0+0.16, b.Y0+0.35, z, b.X1-0.16, b.Y0+0.41, z+0.05, "metal.pipe")
				b.M.Box(b.X0+0.16, b.Y1-0.41, z, b.X1-0.16, b.Y1-0.35, z+0.05, "metal.pipe")
				b.M.Box(b.X0+0.35, b.Y0+0.16, z, b.X0+0.41, b.Y1-0.16, z+0.05, "metal.pipe")
				b.M.Box(b.X1-0.41, b.Y0+0.16, z, b.X1-0.35, b.Y1-0.16, z+0.05, "metal.pipe")
			}
			b.M.SlabTop(3.7, b.X0+0.08, b.Y0+0.08, b.X1-0.08, b.Y1-0.08, "metal.dark")
			b.M.Parapet(b.X0+0.08, b.Y0+0.08, b.X1-0.08, b.Y1-0.08, 3.7, 0.3, 0.04, "metal.pipe")
			// 罐体 + 箍 + 锥顶
			b.M.Cylinder(b.CX(), b.CY(), 0.36, 4.0, 5.9, 14, tank)
			b.M.Cylinder(b.CX(), b.CY(), 0.38, 4.5, 4.58, 14, "metal.dark")
			b.M.Cylinder(b.CX(), b.CY(), 0.38, 5.4, 5.48, 14, "metal.dark")
			b.M.Cone(b.CX(), b.CY(), 0.4, 5.9, 6.6, 14, "roof.metal")
			b.M.Cylinder(b.CX(), b.CY(), 0.04, 6.6, 6.8, 6, "metal.pipe")
			// 上水管与爬梯
			b.M.Box(b.X0+0.06, b.CY()-0.04, 0, b.X0+0.14, b.CY()+0.04, 4.4, "metal.pipe")
			for i := 0; i < 8; i++ {
				z := 0.4 + float64(i)*0.45
				b.M.Box(b.X1-0.16, b.CY()-0.12, z, b.X1-0.06, b.CY()+0.12, z+0.03, "metal.pipe")
			}
			b.M.Box(b.X1-0.24, b.CY()-0.02, 0, b.X1-0.02, b.CY()+0.02, 3.7, "metal.pipe")
		},
	}
}

// industrialDefs 汇总本文件内的单体（新增条目请同时登记到这里）。
func industrialDefs() []Def {
	return []Def{
		warehouse(),
		factory(),
		powerPlant(),
		tankFarm(),
		refinery(),
		sawmill(),
		freightDepot(),
		waterTower(),
	}
}
