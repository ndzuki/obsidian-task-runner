package catalog

import (
	"isocity98/internal/art"
	"isocity98/internal/geom"
)

func townHall() Def {
	return Def{
		Name: "townhall", Label: "市政厅", Kind: KindBuilding, Category: "市政",
		Footprint: [2]int{2, 2}, Height: 7.6, Variants: 2, Cost: 5200, Jobs: 90, Level: 3,
		Build: func(b *B) {
			b.Lot("pad.tile", "concrete.curb")
			w := b.Inset(0.1)
			w.M.Box(w.X0, w.Y0, 0, w.X1, w.Y1, 0.28, "wall.stone")
			w.M.Box(w.X0, w.Y0, 0.28, w.X1, w.Y1, 2.1, "wall.brick")
			w.Windows(0.55, 1.95, 2, 3, "window", "trim.cream")
			w.M.Box(w.X0-0.1, w.Y0-0.1, 2.1, w.X1+0.1, w.Y1+0.1, 2.28, "trim.cream")
			w.M.SlabTop(2.28, w.X0, w.Y0, w.X1, w.Y1, "roof.gravel")
			w.M.Parapet(w.X0, w.Y0, w.X1, w.Y1, 2.28, 0.3, 0.08, "trim.cream")
			// 门廊：柱子 + 山花
			pz := 1.5
			w.M.Box(w.CX()-0.75, w.Y1, 0.28, w.CX()+0.75, w.Y1+0.62, pz, "trim.cream")
			for i := 0; i < 4; i++ {
				px := w.CX() - 0.6 + float64(i)*0.4
				w.M.Cylinder(px, w.Y1+0.5, 0.055, 0.28, pz, 8, "trim.cream")
			}
			w.M.Box(w.CX()-0.8, w.Y1, pz, w.CX()+0.8, w.Y1+0.7, pz+0.16, "trim.cream")
			w.M.Quad("trim.cream", art.FacePosY, geom.ShadeLeft,
				geom.V(w.CX()+0.8, w.Y1+0.7, pz+0.16), geom.V(w.CX()-0.8, w.Y1+0.7, pz+0.16),
				geom.V(w.CX(), w.Y1+0.7, pz+0.62), geom.V(w.CX(), w.Y1+0.7, pz+0.62))
			w.M.Tri("trim.cream", art.FacePosY, geom.ShadeTop,
				geom.V(w.CX()-0.8, w.Y1+0.02, pz+0.16), geom.V(w.CX()+0.8, w.Y1+0.02, pz+0.16),
				geom.V(w.CX(), w.Y1+0.02, pz+0.62))
			w.M.DecalY(w.Y1, w.CX()-0.3, w.CX()+0.3, 0.28, 1.2, "door.wood", 0.03)
			// 钟楼
			t := b.Sub(w.CX()-0.55, w.CY()-0.55, w.CX()+0.55, w.CY()+0.55)
			t.M.Box(t.X0, t.Y0, 2.28, t.X1, t.Y1, 4.6, "wall.brick")
			t.Windows(4.3, 5.6, 1, 1, "window.hall", "")
			t.M.Box(t.X0-0.07, t.Y0-0.07, 4.6, t.X1+0.07, t.Y1+0.07, 4.74, "trim.cream")
			// 四面钟
			for _, f := range []art.Face{art.FacePosY, art.FacePosX, art.FaceNegY, art.FaceNegX} {
				t.Sign(f, t.CX(), 5.05, 0.42, 0.42, "trim.white")
				t.Sign(f, t.CX(), 5.02, 0.3, 0.3, "trim.dark")
			}
			t.M.Box(t.X0, t.Y0, 4.74, t.X1, t.Y1, 5.5, "wall.brick")
			t.M.Box(t.X0-0.1, t.Y0-0.1, 5.5, t.X1+0.1, t.Y1+0.1, 5.62, "trim.cream")
			t.HipRoof(5.62, 7.0, 0.55, "roof.copper", "roof.copper")
			t.M.Box(t.CX()-0.03, t.CY()-0.03, 7.0, t.CX()+0.03, t.CY()+0.03, 7.6, "metal.pipe")
			// 旗杆与花坛
			b.M.Box(b.X0+0.3, b.Y1-0.24, 0.28, b.X0+0.68, b.Y1-0.02, 0.5, "wall.stone")
			TreeAt(b.M, b.X0+0.49, b.Y1-0.13, 0.5, b.R, "hedge")
		},
	}
}

func school() Def {
	return Def{
		Name: "school", Label: "学校", Kind: KindBuilding, Category: "市政",
		Footprint: [2]int{3, 2}, Height: 2.90, Variants: 2, Cost: 3000, Jobs: 70, Level: 2,
		Build: func(b *B) {
			b.Lot("pad.concrete", "concrete.curb")
			w := b.Sub(b.X0+0.1, b.Y0+0.1, b.X1-0.1, b.Y0+1.3)
			w.M.Box(w.X0, w.Y0, 0, w.X1, w.Y1, 0.24, "wall.brick")
			w.M.Box(w.X0, w.Y0, 0.24, w.X1, w.Y1, 2.35, "wall.brick")
			w.Windows(0.5, 2.2, 2, 6, "window", "trim.cream")
			w.M.Box(w.X0-0.09, w.Y0-0.09, 2.35, w.X1+0.09, w.Y1+0.09, 2.5, "trim.cream")
			w.M.SlabTop(2.5, w.X0, w.Y0, w.X1, w.Y1, "roof.felt")
			w.M.Parapet(w.X0, w.Y0, w.X1, w.Y1, 2.5, 0.28, 0.07, "trim.cream")
			// 入口门厅 + 钟
			w.M.Box(w.CX()-0.5, w.Y1, 0, w.CX()+0.5, w.Y1+0.5, 2.0, "wall.brick")
			w.M.DecalY(w.Y1+0.5, w.CX()-0.32, w.CX()+0.32, 0.24, 1.4, "door.glass", 0.03)
			w.M.Box(w.CX()-0.56, w.Y1, 2.0, w.CX()+0.56, w.Y1+0.56, 2.16, "trim.cream")
			w.Sign(art.FacePosY, w.CX(), 2.5, 0.4, 0.4, "trim.white")
			// 操场
			b.M.Box(b.X0+0.3, b.Y0+1.55, -0.01, b.X1-0.3, b.Y1-0.3, 0, "pad.dirt")
			for i := 0; i < 2; i++ {
				b.M.Box(b.X0+0.5+float64(i)*2.0, b.Y0+1.7, 0, b.X0+0.5+float64(i)*2.0+0.06, b.Y1-0.45, 0.5, "metal.pipe")
				b.M.Box(b.X0+0.45+float64(i)*2.0, b.Y0+1.66, 0.4, b.X0+0.5+float64(i)*2.0+0.11, b.Y1-0.41, 0.46, "trim.white")
			}
			TreeAt(b.M, b.X0+0.3, b.Y1-0.3, 0, b.R, "leaf.spring")
			TreeAt(b.M, b.X0+3.0, b.Y1-0.35, 0, b.R, "leaf.spring")
		},
	}
}

// ---------------------------------------------------------------- 市政（追加）

// 说明：pediment / parasol 两个共用件定义在 buildings_com.go。

func church() Def {
	return Def{
		Name: "church", Label: "教堂", Kind: KindBuilding, Category: "市政",
		Footprint: [2]int{2, 2}, Height: 11.5, Variants: 3, Cost: 4800, Jobs: 20, Level: 2,
		Build: func(b *B) {
			wall, roof := "wall.stone", "roof.slate"
			if b.R.Chance(0.5) {
				wall = "wall.brick"
			}
			if b.R.Chance(0.4) {
				roof = "roof.tile.brown"
			}
			b.Lot("pad.tile", "concrete.curb")
			// 中殿：石基 + 高侧窗 + 双坡顶
			n := b.Sub(b.X0+0.12, b.Y0+0.12, b.X1-0.12, b.Y1-0.72)
			n.M.Box(n.X0, n.Y0, 0, n.X1, n.Y1, 0.34, "wall.stone")
			n.M.Box(n.X0, n.Y0, 0.34, n.X1, n.Y1, 3.2, wall)
			for i := 0; i < 3; i++ {
				x := n.X0 + 0.34 + float64(i)*0.5
				n.M.DecalY(n.Y1, x-0.13, x+0.13, 1.3, 2.5, "window", 0.03)
				n.M.DecalX(n.X1, n.Y0+0.34+float64(i)*0.22, n.Y0+0.56+float64(i)*0.22, 1.3, 2.5, "window", 0.03)
			}
			// 扶壁：让长墙在等距视角下有节奏
			for i := 0; i < 4; i++ {
				x := n.X0 + 0.14 + float64(i)*(n.W()-0.28)/3
				n.M.Box(x-0.06, n.Y1, 0.34, x+0.06, n.Y1+0.12, 2.6, "wall.stone")
			}
			n.M.Box(n.X0-0.08, n.Y0-0.08, 3.2, n.X1+0.08, n.Y1+0.08, 3.36, "trim.cream")
			// 屋脊沿 X（长轴）：insetY 取 H/2 才能收成一条干净的脊线
			n.M.Frustum(n.X0, n.Y0, n.X1, n.Y1, 3.36, 5.1, 0, n.H()/2, roof, roof)
			// 前塔：门洞 + 玫瑰窗 + 钟室 + 尖塔 + 十字
			t := b.Sub(b.CX()-0.36, b.Y1-0.66, b.CX()+0.36, b.Y1-0.02)
			t.M.Box(t.X0-0.06, t.Y0-0.06, 0, t.X1+0.06, t.Y1+0.06, 0.4, "wall.stone")
			t.M.Box(t.X0, t.Y0, 0.4, t.X1, t.Y1, 6.4, wall)
			t.Door(art.FacePosY, t.CX(), 0.4, 0.4, 1.5, "door.wood")
			t.M.Box(t.CX()-0.26, t.Y0+0.06, 0.4, t.CX()+0.26, t.Y1-0.06, 0.44, "wall.stone")
			// 玫瑰窗与钟室四面开口
			t.M.DecalY(t.Y1, t.CX()-0.2, t.CX()+0.2, 2.3, 3.2, "window.hall", 0.03)
			t.M.DecalY(t.Y1, t.CX()-0.24, t.CX()+0.24, 2.26, 2.36, "trim.cream", 0.05)
			t.M.DecalY(t.Y1, t.CX()-0.24, t.CX()+0.24, 3.14, 3.24, "trim.cream", 0.05)
			t.M.DecalY(t.Y1, t.CX()-0.16, t.CX()+0.16, 4.7, 5.7, "window.hall", 0.03)
			t.M.DecalY(t.Y0, t.CX()-0.16, t.CX()+0.16, 4.7, 5.7, "window.hall", 0.03)
			t.M.DecalX(t.X1, t.CY()-0.16, t.CY()+0.16, 4.7, 5.7, "window.hall", 0.03)
			t.M.DecalX(t.X0, t.CY()-0.16, t.CY()+0.16, 4.7, 5.7, "window.hall", 0.03)
			t.M.Box(t.X0-0.1, t.Y0-0.1, 6.4, t.X1+0.1, t.Y1+0.1, 6.6, "trim.cream")
			t.Cone(t.CX(), t.CY(), 0.48, 6.6, 10.6, 8, roof)
			t.M.Box(t.CX()-0.025, t.CY()-0.025, 10.4, t.CX()+0.025, t.CY()+0.025, 11.5, "metal.pipe")
			t.M.Box(t.CX()-0.15, t.CY()-0.025, 10.95, t.CX()+0.15, t.CY()+0.025, 11.05, "metal.pipe")
			// 前庭：柏树与石板路
			TreeAt(b.M, b.X0+0.2, b.Y1-0.22, 0, b.R, "leaf.dark")
			TreeAt(b.M, b.X1-0.2, b.Y1-0.22, 0, b.R, "leaf.dark")
			b.M.Box(b.CX()-0.3, b.Y1-0.3, 0, b.CX()+0.3, b.Y1, 0.03, "pad.tile")
		},
	}
}

func policeStation() Def {
	return Def{
		Name: "police", Label: "警察局", Kind: KindBuilding, Category: "市政",
		Footprint: [2]int{2, 1}, Height: 3.05, Variants: 3, Cost: 1800, Jobs: 40, Level: 2,
		Build: func(b *B) {
			wall := "wall.concrete.warm"
			if b.R.Chance(0.5) {
				wall = "wall.brick"
			}
			b.Lot("pad.concrete", "concrete.curb")
			w := b.Inset(0.1)
			// 两层小楼：底层值班室通透、上层办公
			w.M.Box(w.X0, w.Y0, 0, w.X1, w.Y1, 0.26, "wall.stone")
			w.M.Box(w.X0, w.Y0, 0.26, w.X1, w.Y1, 2.5, wall)
			w.Windows(0.6, 1.4, 1, 4, "window", "trim.white")
			w.FloorBand(1.52, 0.06, "trim.cream")
			w.Windows(1.78, 2.34, 1, 4, "window", "trim.white")
			w.Door(art.FacePosY, w.CX(), 0.26, 0.44, 1.05, "door.glass")
			w.M.Box(w.CX()-0.42, w.Y1, 1.4, w.CX()+0.42, w.Y1+0.44, 1.56, "trim.dark")
			w.Sign(art.FacePosY, w.CX(), 1.66, 0.9, 0.36, "sign.neon.cyan")
			w.M.SlabTop(2.5, w.X0, w.Y0, w.X1, w.Y1, "roof.gravel")
			w.M.Parapet(w.X0, w.Y0, w.X1, w.Y1, 2.5, 0.32, 0.07, "trim.cream")
			w.ACUnits(2.82, 2, "metal.dark")
			// 值班室侧窗与警徽灯箱
			w.M.DecalX(w.X1, w.Y0+0.2, w.Y1-0.2, 0.5, 1.4, "window.hall", 0.03)
			w.Sign(art.FacePosX, w.CY(), 1.7, 0.6, 0.34, "sign.panel")
			// 院内：旗杆、花池、临时停车标线
			b.M.Cylinder(b.X0+0.2, b.Y1-0.2, 0.025, 0, 2.6, 6, "metal.pipe")
			b.M.Box(b.X0+0.16, b.Y1-0.3, 0, b.X0+0.34, b.Y1-0.08, 0.34, "wall.stone")
			b.M.DecalTop(0.02, b.X1-0.72, b.Y0+0.16, b.X1-0.24, b.Y0+0.22, "paint.white", 0.03)
			b.M.DecalTop(0.02, b.X1-0.72, b.Y1-0.22, b.X1-0.24, b.Y1-0.16, "paint.white", 0.03)
			b.M.Box(b.X1-0.16, b.Y0+0.3, 0, b.X1-0.06, b.Y0+0.42, 1.6, "streetlamp")
		},
	}
}

func fireStation() Def {
	return Def{
		Name: "firestation", Label: "消防站", Kind: KindBuilding, Category: "市政",
		Footprint: [2]int{2, 2}, Height: 6.4, Variants: 3, Cost: 2600, Jobs: 55, Level: 2,
		Build: func(b *B) {
			wall := "wall.brick"
			if b.R.Chance(0.5) {
				wall = "wall.tile.white"
			}
			b.Lot("pad.concrete", "concrete.curb")
			// 车库主体：三扇卷帘门 + 上方宿舍窗
			h := b.Sub(b.X0+0.1, b.Y0+0.1, b.X1-0.1, b.Y1-0.55)
			h.M.Box(h.X0, h.Y0, 0, h.X1, h.Y1, 0.2, "concrete.curb")
			h.M.Box(h.X0, h.Y0, 0.2, h.X1, h.Y1, 2.6, wall)
			for i := 0; i < 3; i++ {
				dx := h.X0 + 0.22 + float64(i)*0.56
				h.M.DecalY(h.Y1, dx, dx+0.44, 0.2, 2.0, "garage", 0.03)
				h.M.DecalY(h.Y1, dx, dx+0.44, 2.0, 2.06, "trim.dark", 0.05)
			}
			h.Windows(2.1, 2.5, 1, 4, "window", "trim.white")
			h.M.Box(h.X0-0.09, h.Y0-0.09, 2.6, h.X1+0.09, h.Y1+0.09, 2.76, "trim.cream")
			h.M.SlabTop(2.76, h.X0, h.Y0, h.X1, h.Y1, "roof.felt")
			h.M.Parapet(h.X0, h.Y0, h.X1, h.Y1, 2.76, 0.34, 0.07, "trim.cream")
			h.Sign(art.FacePosY, h.CX(), 3.2, 1.0, 0.4, "sign.panel")
			// 瞭望塔：细高塔身 + 挑出观测台 + 四坡顶
			t := b.Sub(b.X1-0.66, b.Y1-0.62, b.X1-0.12, b.Y1-0.08)
			t.M.Box(t.X0, t.Y0, 0, t.X1, t.Y1, 5.2, wall)
			t.Windows(3.4, 4.4, 1, 1, "window.hall", "trim.white")
			t.M.Box(t.X0-0.12, t.Y0-0.12, 5.2, t.X1+0.12, t.Y1+0.12, 5.42, "trim.cream")
			t.M.Parapet(t.X0-0.12, t.Y0-0.12, t.X1+0.12, t.Y1+0.12, 5.42, 0.44, 0.05, "trim.white")
			t.HipRoof(5.42, 6.4, 0.42, "roof.teal", "roof.teal")
			// 训练场：晾带塔 + 消防栓 + 出车标线
			b.M.Box(b.X0+0.16, b.Y0+0.3, 0, b.X0+0.22, b.Y1-0.3, 1.1, "metal.pipe")
			b.M.Box(b.X0+0.28, b.Y0+0.3, 0, b.X0+0.34, b.Y1-0.3, 1.1, "metal.pipe")
			b.M.DecalTop(0.02, b.X1-0.5, b.Y0+0.02, b.X1-0.06, b.Y0+0.08, "paint.white", 0.03)
			b.M.Cylinder(b.X1-0.24, b.CY(), 0.07, 0, 0.6, 8, "car.red")
			TreeAt(b.M, b.X0+0.2, b.Y0+0.16, 0, b.R, "leaf.spring")
		},
	}
}

func hospital() Def {
	return Def{
		Name: "hospital", Label: "医院", Kind: KindBuilding, Category: "市政",
		Footprint: [2]int{3, 2}, Height: 5.48, Variants: 3, Cost: 6200, Jobs: 150, Level: 3,
		Build: func(b *B) {
			wall := "wall.plaster.white"
			if b.R.Chance(0.5) {
				wall = "wall.tile.white"
			}
			b.Lot("pad.concrete", "concrete.curb")
			// 主楼：三层病房窗阵 + 每层水平线
			m := b.Sub(b.X0+0.12, b.Y0+0.12, b.X1-1.05, b.Y1-0.12)
			m.M.Box(m.X0, m.Y0, 0, m.X1, m.Y1, 0.3, "wall.stone")
			z := 0.3
			for i := 0; i < 3; i++ {
				m.M.Box(m.X0, m.Y0, z, m.X1, m.Y1, z+1.2, wall)
				m.Windows(z+0.22, z+1.0, 1, 3, "window", "trim.white")
				m.FloorBand(z+1.13, 0.06, "trim.cream")
				z += 1.2
			}
			m.M.Box(m.X0-0.12, m.Y0-0.12, z, m.X1+0.12, m.Y1+0.12, z+0.24, "trim.cream")
			m.M.SlabTop(z+0.24, m.X0, m.Y0, m.X1, m.Y1, "roof.gravel")
			m.M.Parapet(m.X0, m.Y0, m.X1, m.Y1, z+0.24, 0.34, 0.07, "trim.cream")
			// 屋顶红十字灯箱：两面都做，等距视角下永远读得到
			cz := z + 0.58
			m.M.Box(m.CX()-0.52, m.CY()-0.1, cz, m.CX()+0.52, m.CY()+0.1, cz+1.0, "trim.white")
			m.M.DecalY(m.CY()+0.101, m.CX()-0.1, m.CX()+0.1, cz+0.12, cz+0.88, "car.red", 0.03)
			m.M.DecalY(m.CY()+0.101, m.CX()-0.3, m.CX()+0.3, cz+0.38, cz+0.62, "car.red", 0.03)
			m.M.DecalX(m.X0-0.001, m.CY()-0.1, m.CY()+0.1, cz+0.12, cz+0.88, "car.red", 0.03)
			m.M.DecalX(m.X0-0.001, m.CY()-0.3, m.CY()+0.3, cz+0.38, cz+0.62, "car.red", 0.03)
			m.Antenna(m.X0+0.3, m.Y0+0.3, z+0.58, 1.0)
			// 急诊翼：玻璃门厅 + 救护车雨棚
			e := b.Sub(b.X1-0.98, b.Y0+0.4, b.X1-0.12, b.Y1-0.4)
			e.M.Box(e.X0, e.Y0, 0, e.X1, e.Y1, 2.4, wall)
			e.M.DecalY(e.Y1, e.X0+0.1, e.X1-0.1, 0.24, 1.9, "window.hall", 0.03)
			e.Door(art.FacePosY, e.CX(), 0, 0.5, 1.7, "door.glass")
			e.M.SlabTop(2.4, e.X0, e.Y0, e.X1, e.Y1, "roof.felt")
			e.M.Parapet(e.X0, e.Y0, e.X1, e.Y1, 2.4, 0.26, 0.06, "trim.cream")
			e.M.Box(e.X0-0.08, e.Y1, 2.0, e.X1+0.08, e.Y1+0.5, 2.16, "trim.dark")
			e.Sign(art.FacePosY, e.CX(), 2.5, 0.6, 0.3, "sign.neon.cyan")
			// 救护车专用道标线
			b.M.DecalTop(0.02, b.X1-0.95, b.Y1-0.2, b.X0+2.6, b.Y1-0.14, "paint.white", 0.03)
			TreeAt(b.M, b.X0+0.24, b.Y1-0.26, 0, b.R, "leaf.spring")
			TreeAt(b.M, b.X0+0.5, b.Y1-0.26, 0, b.R, "leaf.spring")
		},
	}
}

func museum() Def {
	return Def{
		Name: "museum", Label: "博物馆", Kind: KindBuilding, Category: "市政",
		Footprint: [2]int{3, 2}, Height: 6.6, Variants: 3, Cost: 7400, Jobs: 70, Level: 3,
		Build: func(b *B) {
			trim := "trim.cream"
			if b.R.Chance(0.4) {
				trim = "trim.white"
			}
			b.Lot("pad.tile", "concrete.curb")
			// 大台阶 + 台基
			b.M.Stairs(b.X0+0.5, b.Y1-0.28, b.X1-0.5, b.Y1+0.16, 0, 0.5, 4, false, "wall.stone")
			w := b.Sub(b.X0+0.18, b.Y0+0.18, b.X1-0.18, b.Y1-0.72)
			w.M.Box(w.X0-0.1, w.Y0-0.1, 0, w.X1+0.1, w.Y1+0.1, 0.5, "wall.stone")
			w.M.Box(w.X0, w.Y0, 0.5, w.X1, w.Y1, 3.4, "wall.stone")
			w.Windows(0.8, 1.9, 1, 5, "window", trim)
			w.FloorBand(2.06, 0.07, trim)
			w.Windows(2.24, 3.2, 1, 5, "window", trim)
			w.M.Box(w.X0-0.12, w.Y0-0.12, 3.4, w.X1+0.12, w.Y1+0.12, 3.66, trim)
			w.M.SlabTop(3.66, w.X0, w.Y0, w.X1, w.Y1, "roof.gravel")
			w.M.Parapet(w.X0, w.Y0, w.X1, w.Y1, 3.66, 0.24, 0.08, trim)
			// 正面柱廊 + 山花
			px0, px1 := w.X0+0.3, w.X1-0.3
			w.M.Box(px0-0.07, w.Y1, 0.5, px1+0.07, w.Y1+0.6, 0.62, "wall.stone")
			for i := 0; i < 6; i++ {
				cx := px0 + float64(i)*(px1-px0)/5
				w.M.Cylinder(cx, w.Y1+0.46, 0.075, 0.62, 3.0, 10, trim)
			}
			w.M.Box(px0-0.16, w.Y1, 3.0, px1+0.16, w.Y1+0.74, 3.28, trim)
			pediment(w.M, px0-0.16, px1+0.16, w.Y1+0.74, 3.28, 0.66, trim)
			w.Door(art.FacePosY, w.CX(), 0.5, 0.5, 1.5, "door.wood")
			// 中央穹顶：鼓座 + 铜绿穹顶 + 采光亭
			t := b.Sub(w.CX()-0.62, w.CY()-0.42, w.CX()+0.62, w.CY()+0.42)
			t.M.Box(t.X0, t.Y0, 3.66, t.X1, t.Y1, 4.5, "wall.stone")
			t.Windows(3.78, 4.4, 1, 6, "window.hall", "")
			t.M.Box(t.X0-0.06, t.Y0-0.06, 4.5, t.X1+0.06, t.Y1+0.06, 4.62, trim)
			t.Blob(t.CX(), t.CY(), 4.62, 0.66, 0.44, 0.62, 4, 12, 0.03, "roof.copper")
			t.M.Cylinder(t.CX(), t.CY(), 0.16, 5.24, 5.7, 8, "wall.stone")
			t.M.Cone(t.CX(), t.CY(), 0.24, 5.7, 6.5, 8, "roof.copper")
			t.M.Cylinder(t.CX(), t.CY(), 0.02, 6.5, 6.6, 6, "metal.pipe")
			// 前广场：雕塑与树阵
			b.M.Cylinder(b.X0+0.3, b.Y1-0.14, 0.16, 0, 0.5, 8, "wall.stone")
			b.M.Box(b.X0+0.24, b.Y1-0.2, 0.5, b.X0+0.36, b.Y1-0.08, 0.86, "wall.stone")
			for i := 0; i < 3; i++ {
				TreeAt(b.M, b.X1-0.3-float64(i)*0.28, b.Y1-0.14, 0, b.R, "leaf.olive")
			}
		},
	}
}

func library() Def {
	return Def{
		Name: "library", Label: "图书馆", Kind: KindBuilding, Category: "市政",
		Footprint: [2]int{2, 2}, Height: 4.8, Variants: 3, Cost: 3200, Jobs: 46, Level: 2,
		Build: func(b *B) {
			wall := "wall.brick"
			if b.R.Chance(0.5) {
				wall = "wall.tile.teal"
			}
			b.Lot("pad.concrete", "concrete.curb")
			w := b.Inset(0.12)
			w.M.Box(w.X0, w.Y0, 0, w.X1, w.Y1, 0.3, "wall.stone")
			w.M.Box(w.X0, w.Y0, 0.3, w.X1, w.Y1, 2.9, wall)
			w.Windows(0.6, 1.8, 1, 3, "window", "trim.white")
			w.FloorBand(1.94, 0.06, "trim.cream")
			w.Windows(2.1, 2.76, 1, 3, "window", "trim.white")
			w.M.Box(w.X0-0.12, w.Y0-0.12, 2.9, w.X1+0.12, w.Y1+0.12, 3.14, "trim.cream")
			w.M.SlabTop(3.14, w.X0, w.Y0, w.X1, w.Y1, "roof.gravel")
			w.M.Parapet(w.X0, w.Y0, w.X1, w.Y1, 3.14, 0.28, 0.08, "trim.cream")
			// 入口门廊：双柱 + 山花
			px0, px1 := w.CX()-0.42, w.CX()+0.42
			w.M.Box(px0-0.08, w.Y1, 0.3, px1+0.08, w.Y1+0.52, 0.42, "wall.stone")
			for i := 0; i < 2; i++ {
				cx := px0 + float64(i)*(px1-px0)
				w.M.Cylinder(cx, w.Y1+0.4, 0.075, 0.42, 2.5, 10, "trim.white")
			}
			w.M.Box(px0-0.14, w.Y1, 2.5, px1+0.14, w.Y1+0.66, 2.74, "trim.white")
			pediment(w.M, px0-0.14, px1+0.14, w.Y1+0.66, 2.74, 0.5, "trim.white")
			w.Door(art.FacePosY, w.CX(), 0.3, 0.44, 1.3, "door.wood")
			// 屋面采光阁：图书馆的顶光
			t := b.Sub(w.CX()-0.5, w.CY()-0.4, w.CX()+0.5, w.CY()+0.4)
			t.M.Box(t.X0, t.Y0, 3.42, t.X1, t.Y1, 3.98, "wall.tile.white")
			t.Windows(3.5, 3.9, 1, 4, "window.hall", "")
			t.M.Box(t.X0-0.07, t.Y0-0.07, 3.98, t.X1+0.07, t.Y1+0.07, 4.1, "trim.cream")
			t.HipRoof(4.1, 4.8, 0.3, "roof.teal", "roof.teal")
			// 门前：还书箱 + 长椅
			b.M.Box(b.X0+0.14, b.Y1-0.26, 0, b.X0+0.34, b.Y1-0.08, 0.5, "sign.panel.dark")
			b.M.Box(b.X0+0.16, b.Y1-0.24, 0.5, b.X0+0.32, b.Y1-0.1, 0.56, "metal.dark")
			b.M.Box(b.X1-0.48, b.Y1-0.2, 0, b.X1-0.16, b.Y1-0.14, 0.12, "crate.wood")
			b.M.Box(b.X1-0.48, b.Y1-0.2, 0.12, b.X1-0.16, b.Y1-0.14, 0.34, "crate.wood")
			TreeAt(b.M, b.X1-0.22, b.Y0+0.2, 0, b.R, "leaf.spring")
		},
	}
}

func stadium() Def {
	return Def{
		Name: "stadium", Label: "体育场", Kind: KindBuilding, Category: "市政",
		Footprint: [2]int{3, 3}, Height: 5.2, Variants: 3, Cost: 8800, Jobs: 90, Level: 3,
		Build: func(b *B) {
			b.Lot("pad.concrete", "concrete.curb")
			// 看台：四面台阶由场边向外升起，外圈用矮墙收边
			step := "concrete.pad"
			if b.R.Chance(0.5) {
				step = "concrete.curb"
			}
			b.M.Stairs(0.3, 0.72, 2.7, 0.16, 0, 1.7, 6, false, step)
			b.M.Stairs(0.3, 2.28, 2.7, 2.84, 0, 1.7, 6, false, step)
			b.M.Stairs(0.72, 0.3, 0.16, 2.7, 0, 1.7, 6, true, step)
			b.M.Stairs(2.28, 0.3, 2.84, 2.7, 0, 1.7, 6, true, step)
			b.M.Box(0.1, 0.05, 0, 2.9, 0.17, 1.95, "wall.concrete")
			b.M.Box(0.1, 2.83, 0, 2.9, 2.95, 1.95, "wall.concrete")
			b.M.Box(0.05, 0.17, 0, 0.17, 2.83, 1.95, "wall.concrete")
			b.M.Box(2.83, 0.17, 0, 2.95, 2.83, 1.95, "wall.concrete")
			// 场地：草皮 + 边线 + 罚球区
			b.M.DecalTop(0.02, 0.78, 0.78, 2.22, 2.22, "pad.grass", 0.03)
			const lw = 0.035
			b.M.DecalTop(0.03, 0.78, 0.78, 2.22, 0.78+lw, "paint.white", 0.05)
			b.M.DecalTop(0.03, 0.78, 2.22-lw, 2.22, 2.22, "paint.white", 0.05)
			b.M.DecalTop(0.03, 0.78, 0.78, 0.78+lw, 2.22, "paint.white", 0.05)
			b.M.DecalTop(0.03, 2.22-lw, 0.78, 2.22, 2.22, "paint.white", 0.05)
			b.M.DecalTop(0.03, 1.5-lw/2, 0.78, 1.5+lw/2, 2.22, "paint.white", 0.05)
			b.M.DecalTop(0.03, 0.98, 1.5-0.24, 1.32, 1.5+0.24, "paint.white", 0.05)
			b.M.DecalTop(0.03, 1.68, 1.5-0.24, 2.02, 1.5+0.24, "paint.white", 0.05)
			// 四角灯塔 + 记分牌
			for _, p := range [4][2]float64{{0.18, 0.18}, {2.82, 0.18}, {0.18, 2.82}, {2.82, 2.82}} {
				b.M.Cylinder(p[0], p[1], 0.06, 0, 4.2, 8, "metal.pipe")
				b.M.Box(p[0]-0.26, p[1]-0.14, 4.2, p[0]+0.26, p[1]+0.14, 4.9, "metal.dark")
				b.M.Box(p[0]-0.24, p[1]-0.12, 4.9, p[0]+0.24, p[1]+0.12, 4.98, "streetlamp")
				b.M.Box(p[0]-0.06, p[1]-0.06, 4.2, p[0]+0.06, p[1]+0.06, 5.2, "metal.pipe")
			}
			b.M.Box(1.28, 2.42, 1.95, 1.72, 2.58, 3.1, "trim.dark")
			b.M.DecalY(2.581, 1.34, 1.66, 2.1, 3.0, "sign.panel", 0.03)
			b.M.DecalX(1.721, 2.46, 2.54, 2.1, 3.0, "sign.panel", 0.03)
			TreeAt(b.M, 2.62, 2.6, 0, b.R, "leaf.spring")
		},
	}
}

// civicDefs 汇总本文件内的单体（新增条目请同时登记到这里）。
func civicDefs() []Def {
	return []Def{
		townHall(),
		school(),
		church(),
		policeStation(),
		fireStation(),
		hospital(),
		museum(),
		library(),
		stadium(),
	}
}
