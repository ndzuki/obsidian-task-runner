package catalog

import (
	"isocity98/internal/art"
	"isocity98/internal/geom"
)

func shopCorner() Def {
	return Def{
		Name: "shop.corner", Label: "街角小店", Kind: KindBuilding, Category: "商业",
		Footprint: [2]int{1, 1}, Height: 1.99, Variants: 4, Cost: 260, Jobs: 10, Level: 1,
		Build: func(b *B) {
			b.Lot("pad.concrete", "concrete.curb")
			w := b.Inset(0.08)
			w.M.Box(w.X0, w.Y0, 0, w.X1, w.Y1, 0.1, "trim.dark")
			w.M.Box(w.X0, w.Y0, 0.1, w.X1, w.Y1, 1.35, "wall.tile.white")
			w.M.Box(w.X0-0.02, w.Y0-0.02, 1.35, w.X1+0.02, w.Y1+0.02, 1.48, "wall.brick")
			// 一层大玻璃橱窗
			w.M.DecalY(w.Y1, w.X0+0.1, w.X1-0.1, 0.22, 1.15, "window", 0.03)
			w.M.DecalX(w.X1, w.Y0+0.1, w.Y1-0.1, 0.22, 1.15, "window", 0.03)
			w.M.DecalY(w.Y1, w.X0+0.28, w.X0+0.52, 0.12, 0.95, "door.glass", 0.05)
			// 雨棚 + 招牌
			aw := []string{"awning.red", "awning.green", "awning.teal"}[b.R.Pick(3)]
			w.Awning(art.FacePosY, w.CX(), 1.34, w.W()*0.86, 0.3, aw)
			sg := "sign.neon.pink"
			if b.R.Chance(0.5) {
				sg = "sign.neon.cyan"
			}
			w.Sign(art.FacePosY, w.CX(), 1.52, 0.62, 0.34, sg)
			w.M.SlabTop(1.48, w.X0, w.Y0, w.X1, w.Y1, "roof.felt")
			w.M.Parapet(w.X0, w.Y0, w.X1, w.Y1, 1.48, 0.26, 0.06, "wall.brick")
			w.ACUnits(1.74, 2, "metal.dark")
		},
	}
}

func shopRow() Def {
	return Def{
		Name: "shop.row", Label: "沿街商铺", Kind: KindBuilding, Category: "商业",
		Footprint: [2]int{2, 1}, Height: 3.40, Variants: 3, Cost: 620, Jobs: 34, Level: 2,
		Build: func(b *B) {
			b.Lot("pad.concrete", "concrete.curb")
			mats := []string{"wall.stucco", "wall.brick", "wall.tile.white", "wall.stucco.pink"}
			awn := []string{"awning.red", "awning.green", "awning.teal"}
			for i := 0; i < 2; i++ {
				u := b.Sub(b.X0+float64(i)+0.03, b.Y0+0.06, b.X0+float64(i+1)-0.03, b.Y1-0.06)
				wall := mats[(i+b.R.Int(0, 3))%len(mats)]
				u.M.Box(u.X0, u.Y0, 0, u.X1, u.Y1, 0.12, "trim.dark")
				u.M.Box(u.X0, u.Y0, 0.12, u.X1, u.Y1, 2.1, wall)
				u.M.DecalY(u.Y1, u.X0+0.08, u.X1-0.08, 0.2, 1.3, "window", 0.03)
				u.M.DecalX(u.X1, u.Y0+0.3, u.Y1-0.3, 0.2, 1.3, "window", 0.03)
				u.Door(art.FacePosY, u.CX(), 0.12, 0.32, 1.0, "door.glass")
				u.Awning(art.FacePosY, u.CX(), 1.9, u.W()*0.85, 0.28, awn[b.R.Pick(3)])
				u.Sign(art.FacePosY, u.CX(), 2.16, 0.6, 0.3, pickNeon(b.R))
				u.Windows(1.5, 2.0, 1, 2, "window.small", "")
				u.M.SlabTop(2.1, u.X0, u.Y0, u.X1, u.Y1, "roof.felt")
				u.M.Parapet(u.X0, u.Y0, u.X1, u.Y1, 2.1, 0.5, 0.06, wall)
			}
			b.ACUnits(2.6, 2, "metal.dark")
			b.Antenna(b.X0+0.4, b.Y0+0.35, 2.6, 0.8)
		},
	}
}

func pickNeon(r *art.Rand) string {
	if r.Chance(0.5) {
		return "sign.neon.pink"
	}
	return "sign.neon.cyan"
}

func officeBlock() Def {
	return Def{
		Name: "office.block", Label: "写字楼", Kind: KindBuilding, Category: "商业",
		Footprint: [2]int{2, 2}, Height: 6.55, Variants: 3, Cost: 2600, Jobs: 120, Level: 2,
		Build: func(b *B) {
			b.Lot("pad.concrete", "concrete.curb")
			w := b.Inset(0.12)
			w.M.Box(w.X0, w.Y0, 0, w.X1, w.Y1, 0.55, "wall.stone")
			w.Windows(0.1, 0.5, 1, 3, "window", "")
			w.M.Box(w.X0-0.08, w.Y0-0.08, 0.55, w.X1+0.08, w.Y1+0.08, 0.66, "concrete.curb")
			w.M.Box(w.X0, w.Y0, 0.66, w.X1, w.Y1, 4.3, "wall.concrete.warm")
			rows := 6
			for i := 0; i < rows; i++ {
				z := 0.66 + float64(i)*0.606
				w.Windows(z+0.08, z+0.52, 1, 3, "window", "")
				w.M.Box(w.X0-0.06, w.Y0-0.06, z+0.52, w.X1+0.06, w.Y1+0.06, z+0.6, "concrete.curb")
			}
			w.M.SlabTop(4.3, w.X0, w.Y0, w.X1, w.Y1, "roof.gravel")
			w.M.Parapet(w.X0, w.Y0, w.X1, w.Y1, 4.3, 0.42, 0.1, "wall.concrete")
			// 顶层设备层
			t := b.Inset(0.5)
			t.M.Box(t.X0, t.Y0, 4.72, t.X1, t.Y1, 5.15, "wall.metal")
			t.ACUnits(5.15, 3, "metal.dark")
			t.Antenna(t.CX(), t.CY(), 5.15, 1.4)
			// 入口雨棚
			w.M.Box(w.CX()-0.7, w.Y1, 0.55, w.CX()+0.7, w.Y1+0.5, 0.75, "trim.dark")
			w.M.Box(w.CX()-0.66, w.Y1, 0.75, w.CX()+0.66, w.Y1+0.46, 0.8, "wall.glass.blue")
		},
	}
}

func officeTower() Def {
	return Def{
		Name: "office.tower", Label: "高层办公塔", Kind: KindBuilding, Category: "商业",
		Footprint: [2]int{2, 2}, Height: 14.22, Variants: 3, Cost: 9800, Jobs: 460, Level: 3,
		Build: func(b *B) {
			b.Lot("pad.concrete", "concrete.curb")
			// 裙房
			p := b.Inset(0.06)
			p.M.Box(p.X0, p.Y0, 0, p.X1, p.Y1, 1.1, "wall.stone")
			p.Windows(0.14, 1.02, 1, 4, "window", "")
			p.M.Box(p.X0-0.1, p.Y0-0.1, 1.1, p.X1+0.1, p.Y1+0.1, 1.22, "concrete.curb")
			p.M.Box(p.CX()-0.85, p.Y1, 0, p.CX()+0.85, p.Y1+0.6, 0.9, "trim.dark")
			// 塔身：玻璃幕墙 + 竖向线条，五段
			glass := "wall.glass"
			segs := []struct {
				ix, iy, h float64
			}{{0.3, 0.3, 2.4}, {0.42, 0.42, 2.3}, {0.54, 0.54, 2.2}, {0.64, 0.64, 1.9}, {0.72, 0.72, 1.3}}
			z := 1.22
			for i, sg := range segs {
				t := b.Sub(b.X0+sg.ix, b.Y0+sg.iy, b.X1-sg.ix, b.Y1-sg.iy)
				wall := glass
				if i%2 == 1 {
					wall = "wall.glass.blue"
				}
				t.M.Box(t.X0, t.Y0, z, t.X1, t.Y1, z+sg.h, wall)
				rows := int(sg.h / 0.72)
				if rows < 1 {
					rows = 1
				}
				t.Windows(z+0.06, z+sg.h-0.06, rows, 3, "window", "")
				for k := 0; k <= rows; k++ {
					zz := z + float64(k)*sg.h/float64(rows)
					t.M.Box(t.X0-0.05, t.Y0-0.05, zz, t.X1+0.05, t.Y1+0.05, zz+0.07, "metal.dark")
				}
				z += sg.h
			}
			top := b.Sub(b.X0+0.72, b.Y0+0.72, b.X1-0.72, b.Y1-0.72)
			top.M.SlabTop(z, top.X0, top.Y0, top.X1, top.Y1, "roof.felt")
			top.M.Parapet(top.X0, top.Y0, top.X1, top.Y1, z, 0.3, 0.07, "metal.dark")
			top.Antenna(top.CX(), top.CY(), z+0.3, 2.6)
			top.ACUnits(z+0.3, 2, "metal.dark")
			// 停机坪标志
			top.M.DecalTop(z+0.011, top.CX()-0.28, top.CY()-0.28, top.CX()+0.28, top.CY()+0.28, "paint.white", 0.02)
		},
	}
}

// ---------------------------------------------------------------- 商业（街面细分）

// pediment 生成古典山花（正立面三角楣饰）：银行、博物馆等柱廊建筑的共用件。
// 用一对三角面 + 两片斜面拼出一个有厚度的三棱柱，避免只看正面时像贴纸。
func pediment(m *geom.Mesh, x0, x1, y, z0, h float64, mat string) {
	const d = 0.16
	cx := (x0 + x1) / 2
	m.Tri(mat, art.FacePosY, geom.ShadeLeft,
		geom.V(x0, y, z0), geom.V(x1, y, z0), geom.V(cx, y, z0+h))
	m.Tri(mat, art.FaceNegY, geom.ShadeRight,
		geom.V(x1, y-d, z0), geom.V(x0, y-d, z0), geom.V(cx, y-d, z0+h))
	m.Quad(mat, art.FaceTop, geom.ShadeTop,
		geom.V(x0, y, z0), geom.V(cx, y, z0+h), geom.V(cx, y-d, z0+h), geom.V(x0, y-d, z0))
	m.Quad(mat, art.FaceTop, geom.ShadeTop,
		geom.V(cx, y, z0+h), geom.V(x1, y, z0), geom.V(x1, y-d, z0), geom.V(cx, y-d, z0+h))
}

// parasol 露台遮阳伞（咖啡馆、广场共用）：细杆 + 单层伞面。
func parasol(m *geom.Mesh, x, y, z0, r, h float64, poleMat, topMat string) {
	m.Box(x-0.025, y-0.025, z0, x+0.025, y+0.025, z0+h, poleMat)
	m.Cone(x, y, r, z0+h, z0+h+r*0.55, 8, topMat)
}

func bank() Def {
	return Def{
		Name: "bank", Label: "银行", Kind: KindBuilding, Category: "商业",
		Footprint: [2]int{2, 2}, Height: 5.4, Variants: 3, Cost: 6400, Jobs: 130, Level: 3,
		Build: func(b *B) {
			stone, trim := "wall.stone", "trim.cream"
			if b.R.Chance(0.5) {
				stone = "wall.concrete.warm"
			}
			if b.R.Chance(0.4) {
				trim = "trim.white"
			}
			b.Lot("pad.tile", "concrete.curb")
			w := b.Inset(0.12)
			// 台基 + 两层大窗主体
			w.M.Box(w.X0-0.08, w.Y0-0.08, 0, w.X1+0.08, w.Y1+0.08, 0.34, "wall.stone")
			w.M.Box(w.X0, w.Y0, 0.34, w.X1, w.Y1, 3.1, stone)
			w.Windows(0.62, 1.6, 1, 3, "window", trim)
			w.FloorBand(1.72, 0.09, trim)
			w.Windows(1.98, 2.92, 1, 3, "window", trim)
			w.M.Box(w.X0-0.12, w.Y0-0.12, 3.1, w.X1+0.12, w.Y1+0.12, 3.36, trim)
			w.M.SlabTop(3.36, w.X0, w.Y0, w.X1, w.Y1, "roof.gravel")
			w.M.Parapet(w.X0, w.Y0, w.X1, w.Y1, 3.36, 0.26, 0.08, trim)
			// 正面柱廊：柱础 + 五根柱 + 檐部 + 山花
			px0, px1 := w.X0+0.08, w.X1-0.08
			w.M.Box(px0-0.06, w.Y1, 0.34, px1+0.06, w.Y1+0.62, 0.46, "wall.stone")
			for i := 0; i < 5; i++ {
				cx := px0 + float64(i)*(px1-px0)/4
				w.M.Box(cx-0.09, w.Y1+0.38, 0.46, cx+0.09, w.Y1+0.56, 0.52, trim)
				w.M.Cylinder(cx, w.Y1+0.47, 0.07, 0.52, 2.55, 8, trim)
			}
			w.M.Box(px0-0.14, w.Y1, 2.55, px1+0.14, w.Y1+0.76, 2.78, trim)
			pediment(w.M, px0-0.14, px1+0.14, w.Y1+0.76, 2.78, 0.58, trim)
			w.Door(art.FacePosY, w.CX(), 0.34, 0.42, 1.4, "door.wood")
			// 中央采光亭 + 铜绿穹顶（地标天际线）
			t := b.Sub(w.CX()-0.46, w.CY()-0.46, w.CX()+0.46, w.CY()+0.46)
			t.M.Box(t.X0, t.Y0, 3.36, t.X1, t.Y1, 4.5, stone)
			t.Windows(3.54, 4.4, 1, 2, "window", trim)
			t.M.Box(t.X0-0.07, t.Y0-0.07, 4.5, t.X1+0.07, t.Y1+0.07, 4.62, trim)
			t.M.Cylinder(t.CX(), t.CY(), 0.3, 4.62, 4.74, 10, trim)
			t.Blob(t.CX(), t.CY(), 4.74, 0.33, 0.33, 0.3, 3, 10, 0.03, "roof.copper")
			t.M.Cylinder(t.CX(), t.CY(), 0.03, 5.0, 5.4, 6, "metal.pipe")
			// 门前的石球与花池
			b.M.Cylinder(b.X1-0.24, b.Y1-0.06, 0.11, 0, 0.34, 8, "wall.stone")
			b.M.Box(b.X0+0.06, b.Y1-0.16, 0, b.X0+0.34, b.Y1-0.02, 0.26, "wall.stone")
			b.M.Box(b.X0+0.09, b.Y1-0.14, 0.26, b.X0+0.31, b.Y1-0.04, 0.36, "hedge")
		},
	}
}

func hotel() Def {
	return Def{
		Name: "hotel", Label: "旅馆", Kind: KindBuilding, Category: "商业",
		Footprint: [2]int{2, 2}, Height: 8.2, Variants: 3, Cost: 7200, Jobs: 110, Level: 3,
		Build: func(b *B) {
			wall := "wall.stucco"
			if b.R.Chance(0.5) {
				wall = "wall.brick"
			}
			body := "wall.concrete.warm"
			if b.R.Chance(0.5) {
				body = "wall.tile.white"
			}
			neon := pickNeon(b.R)
			b.Lot("pad.concrete", "concrete.curb")
			w := b.Inset(0.1)
			// 裙房：门厅与餐厅
			w.M.Box(w.X0, w.Y0, 0, w.X1, w.Y1, 0.5, wall)
			w.Windows(0.12, 0.42, 1, 4, "window", "")
			w.M.Box(w.X0-0.08, w.Y0-0.08, 0.5, w.X1+0.08, w.Y1+0.08, 0.62, "trim.cream")
			// 客房层：每层一条通长阳台 + 水平楼层线（1x 下读得出层数）
			z := 0.62
			for i := 0; i < 6; i++ {
				w.M.Box(w.X0, w.Y0, z, w.X1, w.Y1, z+0.92, body)
				w.Windows(z+0.16, z+0.8, 1, 4, "window", "trim.white")
				w.M.Box(w.X0+0.12, w.Y1, z+0.1, w.X1-0.12, w.Y1+0.2, z+0.16, "concrete.curb")
				w.M.Box(w.X0+0.12, w.Y1+0.17, z+0.16, w.X1-0.12, w.Y1+0.2, z+0.5, "trim.white")
				w.FloorBand(z+0.88, 0.05, "trim.cream")
				z += 0.92
			}
			// 檐口与屋面
			w.M.Box(w.X0-0.14, w.Y0-0.14, z, w.X1+0.14, w.Y1+0.14, z+0.24, "trim.cream")
			w.M.SlabTop(z+0.24, w.X0, w.Y0, w.X1, w.Y1, "roof.gravel")
			w.M.Parapet(w.X0, w.Y0, w.X1, w.Y1, z+0.24, 0.34, 0.07, "trim.cream")
			// 入口雨棚 + 玻璃门厅
			w.M.Box(w.CX()-0.62, w.Y1, 0.62, w.CX()+0.62, w.Y1+0.56, 0.8, "trim.dark")
			w.M.Box(w.CX()-0.58, w.Y1, 0.8, w.CX()+0.58, w.Y1+0.52, 0.84, "wall.glass.blue")
			w.Awning(art.FacePosY, w.CX(), 0.62, 1.3, 0.5, "awning.teal")
			w.Door(art.FacePosY, w.CX(), 0.5, 0.44, 1.0, "door.glass")
			w.Sign(art.FacePosY, w.CX(), 1.0, 0.9, 0.36, neon)
			// 角部竖招：两面都挂，等距视角下永远读得到
			w.Sign(art.FacePosX, w.Y0+0.6, 3.0, 0.36, 2.4, neon)
			w.Sign(art.FacePosY, w.CX()+0.58, 3.0, 0.36, 2.4, neon)
			// 屋顶广告牌
			top := b.Inset(0.42)
			top.M.Box(top.CX()-0.85, top.CY()-0.09, z+0.58, top.CX()+0.85, top.CY()+0.09, 7.5, "trim.dark")
			top.M.DecalY(top.CY()+0.091, top.CX()-0.78, top.CX()+0.78, z+0.7, 7.4, neon, 0.03)
			top.M.DecalX(top.CX()+0.851, top.CY()-0.06, top.CY()+0.06, z+0.7, 7.4, neon, 0.03)
			top.Antenna(top.X0+0.24, top.Y1-0.24, z+0.58, 1.5)
			top.ACUnits(z+0.58, 2, "metal.dark")
		},
	}
}

func fastFood() Def {
	return Def{
		Name: "fastfood", Label: "快餐店", Kind: KindBuilding, Category: "商业",
		Footprint: [2]int{1, 1}, Height: 3.4, Variants: 3, Cost: 900, Jobs: 24, Level: 1,
		Build: func(b *B) {
			neon := pickNeon(b.R)
			main := "wall.tile.white"
			if b.R.Chance(0.5) {
				main = "wall.stucco"
			}
			b.Lot("pad.asphalt", "concrete.curb")
			w := b.Inset(0.07)
			w.M.Box(w.X0, w.Y0, 0, w.X1, w.Y1, 0.12, "trim.dark")
			w.M.Box(w.X0, w.Y0, 0.12, w.X1, w.Y1, 1.5, main)
			// 三面落地玻璃 + 玻璃门
			w.M.DecalY(w.Y1, w.X0+0.06, w.X1-0.06, 0.24, 1.24, "window.hall", 0.03)
			w.M.DecalX(w.X1, w.Y0+0.06, w.Y1-0.06, 0.24, 1.24, "window.hall", 0.03)
			w.M.DecalY(w.Y1, w.CX()-0.12, w.CX()+0.12, 0.12, 1.2, "door.glass", 0.05)
			// 条带雨棚与霓虹招牌带
			w.Awning(art.FacePosY, w.CX(), 1.5, w.W()*0.92, 0.3, "awning.red")
			w.M.Box(w.X0-0.03, w.Y0-0.03, 1.5, w.X1+0.03, w.Y1+0.03, 1.56, "trim.white")
			w.Sign(art.FacePosY, w.CX(), 1.6, 0.7, 0.34, neon)
			w.M.SlabTop(1.56, w.X0, w.Y0, w.X1, w.Y1, "roof.felt")
			w.M.Parapet(w.X0, w.Y0, w.X1, w.Y1, 1.56, 0.28, 0.06, main)
			w.ACUnits(1.84, 1, "metal.dark")
			// 路边高杆霓虹灯箱（夜里是街口最亮的点）
			px0 := b.X1 - 0.3
			b.M.Box(px0+0.09, b.Y0+0.1, 0, px0+0.15, b.Y0+0.16, 2.4, "metal.pipe")
			b.M.Box(px0, b.Y0-0.06, 2.4, px0+0.26, b.Y0+0.34, 3.4, "trim.dark")
			b.M.DecalY(b.Y0+0.341, px0+0.03, px0+0.23, 2.5, 3.3, neon, 0.03)
			b.M.DecalX(px0+0.261, b.Y0-0.02, b.Y0+0.3, 2.5, 3.3, neon, 0.03)
			// 免下车菜单牌 + 外摆遮阳伞
			b.M.Box(b.X0+0.06, b.Y0+0.34, 0, b.X0+0.1, b.Y0+0.38, 1.1, "metal.pipe")
			b.M.Box(b.X0+0.02, b.Y0+0.28, 1.1, b.X0+0.14, b.Y0+0.44, 1.5, "trim.dark")
			parasol(b.M, b.X0+0.36, b.Y0+0.22, 0, 0.2, 0.9, "trim.white", "awning.red")
		},
	}
}

func gasStation() Def {
	return Def{
		Name: "gasstation", Label: "加油站", Kind: KindBuilding, Category: "商业",
		Footprint: [2]int{2, 2}, Height: 3.7, Variants: 3, Cost: 1600, Jobs: 16, Level: 1,
		Build: func(b *B) {
			brand := "awning.red"
			if b.R.Chance(0.5) {
				brand = "awning.green"
			}
			b.Lot("pad.asphalt", "concrete.curb")
			// 便利店
			k := b.Sub(b.X0+0.12, b.Y0+0.12, b.X0+1.02, b.Y0+0.82)
			k.M.Box(k.X0, k.Y0, 0, k.X1, k.Y1, 2.15, "wall.tile.white")
			k.Windows(0.36, 1.5, 1, 2, "window", "")
			k.Door(art.FacePosY, k.CX(), 0, 0.34, 1.0, "door.glass")
			k.M.SlabTop(2.15, k.X0, k.Y0, k.X1, k.Y1, "roof.felt")
			k.M.Parapet(k.X0, k.Y0, k.X1, k.Y1, 2.15, 0.24, 0.05, "trim.cream")
			k.ACUnits(2.39, 1, "metal.dark")
			// 加油雨棚：四柱 + 顶板 + 品牌色带
			cx0, cy0, cx1, cy1 := b.X0+0.2, b.Y0+1.15, b.X1-0.2, b.Y1-0.18
			for _, p := range [4][2]float64{{cx0, cy0}, {cx1, cy0}, {cx0, cy1}, {cx1, cy1}} {
				b.M.Cylinder(p[0], p[1], 0.06, 0, 2.6, 8, "metal.pipe")
			}
			b.M.Box(cx0-0.1, cy0-0.1, 2.6, cx1+0.1, cy1+0.1, 2.78, "wall.metal")
			b.M.Box(cx0-0.1, cy0-0.1, 2.78, cx1+0.1, cy1+0.1, 2.95, brand)
			b.M.Box(cx0-0.14, cy0-0.14, 2.95, cx1+0.14, cy1+0.14, 3.04, "trim.white")
			// 油泵岛：底座 + 泵体 + 顶灯箱 + 油枪
			my := (cy0 + cy1) / 2
			for i := 0; i < 2; i++ {
				px := cx0 + 0.36 + float64(i)*(cx1-cx0-0.72)
				b.M.Box(px-0.2, my-0.62, 0, px+0.2, my+0.62, 0.14, "concrete.curb")
				b.M.Box(px-0.11, my-0.17, 0.14, px+0.11, my+0.17, 0.98, "wall.metal")
				b.M.Box(px-0.14, my-0.2, 0.98, px+0.14, my+0.2, 1.1, "sign.panel")
				b.M.Box(px+0.02, my+0.17, 0.5, px+0.1, my+0.24, 0.58, "metal.dark")
			}
			// 路边价格牌
			b.M.Box(b.X1-0.3, b.Y1-0.26, 0, b.X1-0.22, b.Y1-0.18, 2.6, "metal.pipe")
			b.M.Box(b.X1-0.52, b.Y1-0.4, 2.6, b.X1-0.02, b.Y1-0.04, 3.7, "trim.dark")
			b.M.DecalY(b.Y1-0.041, b.X1-0.48, b.X1-0.06, 2.72, 3.58, "sign.panel", 0.03)
			b.M.DecalY(b.Y1-0.039, b.X1-0.42, b.X1-0.12, 2.9, 3.4, "paint.white", 0.05)
		},
	}
}

func supermarket() Def {
	return Def{
		Name: "supermarket", Label: "超市", Kind: KindBuilding, Category: "商业",
		Footprint: [2]int{3, 2}, Height: 4.2, Variants: 3, Cost: 3800, Jobs: 90, Level: 2,
		Build: func(b *B) {
			band := "trim.teal"
			if b.R.Chance(0.5) {
				band = "sign.panel"
			}
			b.Lot("pad.asphalt", "concrete.curb")
			// 大盒子卖场占三分之二，其余留作停车坪
			w := b.Sub(b.X0+0.12, b.Y0+0.12, b.X1-0.95, b.Y1-0.12)
			w.M.Box(w.X0, w.Y0, 0, w.X1, w.Y1, 0.2, "concrete.curb")
			w.M.Box(w.X0, w.Y0, 0.2, w.X1, w.Y1, 2.9, "wall.tile.white")
			w.M.Box(w.X0-0.04, w.Y0-0.04, 2.0, w.X1+0.04, w.Y1+0.04, 2.36, band)
			w.Windows(2.42, 2.84, 1, 7, "window.small", "")
			// 入口：玻璃幕墙 + 雨棚 + 招牌
			w.M.DecalY(w.Y1, w.X0+0.3, w.CX()-0.1, 0.2, 1.9, "window.hall", 0.03)
			w.Door(art.FacePosY, w.CX()+0.45, 0.2, 0.5, 1.7, "door.glass")
			w.M.Box(w.CX()+0.05, w.Y1, 2.0, w.CX()+0.9, w.Y1+0.6, 2.2, "trim.dark")
			w.Sign(art.FacePosY, w.CX()-0.75, 2.44, 1.0, 0.4, pickNeon(b.R))
			w.M.SlabTop(2.9, w.X0, w.Y0, w.X1, w.Y1, "roof.gravel")
			w.M.Parapet(w.X0, w.Y0, w.X1, w.Y1, 2.9, 0.34, 0.07, "wall.concrete")
			w.ACUnits(3.24, 3, "metal.dark")
			w.M.Box(w.CX()-0.8, w.CY()-0.09, 3.24, w.CX()+0.8, w.CY()+0.09, 4.2, "trim.dark")
			w.M.DecalY(w.CY()+0.091, w.CX()-0.72, w.CX()+0.72, 3.38, 4.1, band, 0.03)
			// 停车坪：车位标线、购物车、灯杆
			for i := 0; i < 4; i++ {
				y0 := b.Y0 + 0.28 + float64(i)*0.42
				b.M.DecalTop(0.02, b.X1-0.86, y0, b.X1-0.2, y0+0.03, "paint.white", 0.03)
			}
			b.M.Box(b.X1-0.5, b.Y0+0.12, 0, b.X1-0.34, b.Y0+0.3, 0.42, "metal.pipe")
			b.M.Box(b.X1-0.24, b.Y1-0.32, 0, b.X1-0.16, b.Y1-0.24, 2.4, "metal.pipe")
			b.M.Box(b.X1-0.34, b.Y1-0.42, 2.4, b.X1-0.06, b.Y1-0.14, 2.6, "streetlamp")
		},
	}
}

func departmentStore() Def {
	return Def{
		Name: "department", Label: "百货商店", Kind: KindBuilding, Category: "商业",
		Footprint: [2]int{2, 2}, Height: 7.2, Variants: 3, Cost: 8200, Jobs: 260, Level: 3,
		Build: func(b *B) {
			wall := "wall.stucco"
			if b.R.Chance(0.5) {
				wall = "wall.plaster.white"
			}
			neon := pickNeon(b.R)
			b.Lot("pad.tile", "concrete.curb")
			w := b.Inset(0.1)
			// 底层橱窗
			w.M.Box(w.X0, w.Y0, 0, w.X1, w.Y1, 0.46, "wall.stone")
			w.M.Box(w.X0, w.Y0, 0.46, w.X1, w.Y1, 1.42, "wall.tile.white")
			w.M.DecalY(w.Y1, w.X0+0.08, w.X1-0.08, 0.62, 1.32, "window.hall", 0.03)
			w.M.DecalX(w.X1, w.Y0+0.08, w.Y1-0.08, 0.62, 1.32, "window.hall", 0.03)
			w.Door(art.FacePosY, w.CX(), 0.46, 0.5, 0.9, "door.glass")
			w.Awning(art.FacePosY, w.CX(), 1.42, w.W()*0.94, 0.28, "awning.green")
			// 上部四层大窗
			z := 1.42
			for i := 0; i < 4; i++ {
				w.M.Box(w.X0, w.Y0, z, w.X1, w.Y1, z+1.02, wall)
				w.Windows(z+0.14, z+0.88, 1, 3, "window.hall", "trim.white")
				w.FloorBand(z+0.96, 0.07, "trim.cream")
				z += 1.02
			}
			// 檐口与屋面
			w.M.Box(w.X0-0.14, w.Y0-0.14, z, w.X1+0.14, w.Y1+0.14, z+0.26, "trim.cream")
			w.M.SlabTop(z+0.26, w.X0, w.Y0, w.X1, w.Y1, "roof.gravel")
			w.M.Parapet(w.X0, w.Y0, w.X1, w.Y1, z+0.26, 0.4, 0.08, "trim.cream")
			// 转角竖招：两面都挂
			w.Sign(art.FacePosX, w.Y1-0.4, 2.6, 0.36, 2.6, neon)
			w.Sign(art.FacePosY, w.X1-0.4, 2.6, 0.36, 2.6, neon)
			// 屋顶水箱、机组与广告牌
			top := b.Inset(0.5)
			top.WaterTank(top.X0+0.24, top.Y0+0.24, z+0.66, 0.18, 0.4, "wall.metal.rust")
			top.ACUnits(z+0.66, 2, "metal.dark")
			top.M.Box(top.CX()-0.7, top.CY()-0.08, z+0.66, top.CX()+0.7, top.CY()+0.08, 7.2, "trim.dark")
			top.M.DecalY(top.CY()+0.081, top.CX()-0.62, top.CX()+0.62, z+0.8, 7.1, neon, 0.03)
		},
	}
}

func cinema() Def {
	return Def{
		Name: "cinema", Label: "电影院", Kind: KindBuilding, Category: "商业",
		Footprint: [2]int{2, 2}, Height: 7.2, Variants: 3, Cost: 5600, Jobs: 60, Level: 2,
		Build: func(b *B) {
			neon := pickNeon(b.R)
			wall := "wall.plaster.white"
			if b.R.Chance(0.5) {
				wall = "wall.stucco.mint"
			}
			b.Lot("pad.tile", "concrete.curb")
			w := b.Inset(0.1)
			w.M.Box(w.X0, w.Y0, 0, w.X1, w.Y1, 4.6, wall)
			// 壁柱：把正立面切成海报栏与售票口的节奏
			px0, px1 := w.X0+0.12, w.X1-0.12
			for i := 0; i <= 4; i++ {
				px := px0 + float64(i)*(px1-px0)/4
				w.M.Box(px-0.045, w.Y1, 0.2, px+0.045, w.Y1+0.08, 4.4, "trim.cream")
			}
			// 海报灯箱 + 三扇玻璃门
			for i := 0; i < 2; i++ {
				cx := px0 + (float64(i)+0.5)*(px1-px0)/2
				w.M.DecalY(w.Y1, cx-0.24, cx+0.24, 0.7, 2.1, "window.hall", 0.03)
			}
			w.Door(art.FacePosY, w.CX(), 0, 0.62, 1.5, "door.glass")
			w.Door(art.FacePosY, w.CX()-0.42, 0, 0.32, 1.3, "door.glass")
			w.Door(art.FacePosY, w.CX()+0.42, 0, 0.32, 1.3, "door.glass")
			w.Windows(2.6, 4.3, 1, 4, "window.small", "")
			// 挑出式大帐幕 + 霓虹灯带
			w.M.Box(w.X0-0.04, w.Y1, 2.42, w.X1+0.04, w.Y1+0.92, 2.6, "trim.dark")
			w.M.Box(w.X0-0.02, w.Y1, 2.6, w.X1+0.02, w.Y1+0.9, 2.64, "trim.white")
			w.M.DecalY(w.Y1+0.921, w.X0+0.06, w.X1-0.06, 2.46, 2.6, neon, 0.03)
			w.M.DecalX(w.X1+0.041, w.Y1+0.06, w.Y1+0.88, 2.46, 2.6, neon, 0.03)
			w.M.Box(w.X0-0.1, w.Y0-0.1, 4.6, w.X1+0.1, w.Y1+0.1, 4.86, "trim.cream")
			w.M.SlabTop(4.86, w.X0, w.Y0, w.X1, w.Y1, "roof.felt")
			w.M.Parapet(w.X0, w.Y0, w.X1, w.Y1, 4.86, 0.42, 0.08, "trim.cream")
			// 竖向霓虹招牌：从帐幕上方一路挑到屋顶之上
			sx0 := w.X1 - 0.46
			w.M.Box(sx0, w.Y1, 2.8, sx0+0.34, w.Y1+0.44, 7.0, "trim.dark")
			w.M.DecalY(w.Y1+0.441, sx0+0.05, sx0+0.29, 2.9, 6.9, neon, 0.03)
			w.M.DecalX(sx0+0.341, w.Y1+0.06, w.Y1+0.38, 2.9, 6.9, neon, 0.03)
			w.M.Box(sx0-0.05, w.Y1-0.05, 7.0, sx0+0.39, w.Y1+0.49, 7.16, "trim.dark")
			// 门前花池与棕榈
			b.M.Box(b.X0+0.16, b.Y0+0.2, 0, b.X0+0.5, b.Y0+0.32, 0.5, "wall.stone")
			TreeAt(b.M, b.X0+0.33, b.Y0+0.26, 0.5, b.R, "leaf.palm")
		},
	}
}

func cafe() Def {
	return Def{
		Name: "cafe", Label: "咖啡馆", Kind: KindBuilding, Category: "商业",
		Footprint: [2]int{1, 1}, Height: 2.3, Variants: 4, Cost: 520, Jobs: 12, Level: 1,
		Build: func(b *B) {
			s := pickScheme(b.R)
			b.Lot("pad.tile", "concrete.curb")
			// 小体量店面压在一侧，另一侧做露天座
			h := b.Sub(b.X0+0.06, b.Y0+0.06, b.X0+0.6, b.Y1-0.06)
			h.M.Box(h.X0, h.Y0, 0, h.X1, h.Y1, 0.14, "trim.dark")
			h.M.Box(h.X0, h.Y0, 0.14, h.X1, h.Y1, 1.6, s.wall)
			h.M.DecalX(h.X1, h.Y0+0.1, h.Y1-0.1, 0.34, 1.28, "window.hall", 0.03)
			h.Door(art.FacePosX, h.CY(), 0.14, 0.34, 0.9, "door.glass")
			h.Awning(art.FacePosX, h.CY(), 1.6, h.H()*0.86, 0.36, "awning.green")
			h.M.SlabTop(1.6, h.X0, h.Y0, h.X1, h.Y1, "roof.shingle")
			h.M.Parapet(h.X0, h.Y0, h.X1, h.Y1, 1.6, 0.24, 0.05, "trim.cream")
			h.Sign(art.FacePosX, h.CY(), 1.9, 0.42, 0.3, "sign.panel")
			h.Chimney(h.X0+0.14, h.Y0+0.16, 1.84, 0.34, 0.05, "wall.brick")
			// 露天座：两张圆桌 + 遮阳伞 + 花箱
			parasol(b.M, b.X1-0.3, b.Y0+0.28, 0, 0.24, 1.4, "trim.white", "awning.green")
			b.M.Cylinder(b.X1-0.3, b.Y0+0.28, 0.13, 0, 0.42, 8, "trim.white")
			parasol(b.M, b.X1-0.26, b.Y1-0.26, 0, 0.2, 1.2, "trim.white", "awning.red")
			b.M.Cylinder(b.X1-0.26, b.Y1-0.26, 0.11, 0, 0.38, 8, "trim.white")
			b.M.Box(b.X1-0.52, b.Y1-0.46, 0, b.X1-0.3, b.Y1-0.24, 0.22, "crate.wood")
			b.M.Box(h.X0+0.02, b.Y1-0.16, 0, h.X1+0.18, b.Y1-0.02, 0.3, "wall.stone")
			b.M.Box(h.X0+0.06, b.Y1-0.14, 0.3, h.X1+0.14, b.Y1-0.04, 0.42, "leaf.spring")
		},
	}
}

// ---------------------------------------------------------------- 工业

// commercialDefs 汇总本文件内的单体（新增条目请同时登记到这里）。
func commercialDefs() []Def {
	return []Def{
		shopCorner(),
		shopRow(),
		officeBlock(),
		officeTower(),
		bank(),
		hotel(),
		fastFood(),
		gasStation(),
		supermarket(),
		departmentStore(),
		cinema(),
		cafe(),
	}
}
