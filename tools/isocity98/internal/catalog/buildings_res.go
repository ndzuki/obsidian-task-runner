package catalog

import (
	"isocity98/internal/art"
)

func houseSmall() Def {
	return Def{
		Name: "house.small", Label: "独栋小屋", Kind: KindBuilding, Category: "住宅",
		Footprint: [2]int{1, 1}, Height: 2.5, Variants: 4, Cost: 120, Pop: 8, Level: 1,
		Build: func(b *B) {
			s := pickScheme(b.R)
			b.Lot("pad.grass", "concrete.curb")
			w := b.Inset(0.09)
			w.M.Box(w.X0, w.Y0, 0, w.X1, w.Y1, 0.16, "trim.cream")
			w.M.Box(w.X0, w.Y0, 0.16, w.X1, w.Y1, 1.55, s.wall)
			w.WindowsOn(art.FacePosY, 0.16, 1.5, 1, 2, "window.small", "")
			w.WindowsOn(art.FacePosX, 0.16, 1.5, 1, 1, "window.small", "")
			if b.R.Chance(0.5) {
				w.WindowsOn(art.FaceNegX, 0.16, 1.5, 1, 1, "window.small", "")
			}
			w.Door(art.FacePosY, w.CX(), 0.16, 0.28, 0.62, s.doorM)
			w.Stoop(art.FacePosY, w.CX(), 0.42, 0.16)
			w.GableRoof(1.55, 2.28, b.R.Chance(0.5), s.roof, s.roof)
			w.Eave(1.55, 0.13, "trim.cream")
			if b.R.Chance(0.6) {
				w.Chimney(w.X0+0.22, w.Y0+0.3, 2.0, 0.45, 0.075, "wall.brick")
			}
			// 院内小树与围篱
			if b.R.Chance(0.55) {
				TreeAt(b.M, b.X0+0.16, b.Y0+0.2+0.2*b.R.Float(), 0, b.R, s.leaf)
			}
			if b.R.Chance(0.4) {
				b.M.Box(b.X0, b.Y0+0.02, 0, b.X1, b.Y0+0.07, 0.16, "trim.white")
			}
		},
	}
}

func houseSuburban() Def {
	return Def{
		Name: "house.suburban", Label: "中产住宅", Kind: KindBuilding, Category: "住宅",
		Footprint: [2]int{1, 1}, Height: 2.34, Variants: 4, Cost: 220, Pop: 14, Level: 1,
		Build: func(b *B) {
			s := pickScheme(b.R)
			b.Lot("pad.grass", "concrete.curb")
			// 主屋占 2/3，车库占 1/3
			house := b.Sub(b.X0+0.06, b.Y0+0.06, b.X1-0.06, b.Y0+0.62)
			gar := b.Sub(b.X0+0.06, b.Y0+0.68, b.X1-0.06, b.Y1-0.06)
			house.M.Box(house.X0, house.Y0, 0, house.X1, house.Y1, 0.14, "trim.cream")
			house.M.Box(house.X0, house.Y0, 0.14, house.X1, house.Y1, 1.7, s.wall)
			house.WindowsOn(art.FacePosX, 0.2, 1.62, 1, 2, "window.small", "")
			house.WindowsOn(art.FacePosY, 0.2, 1.62, 1, 2, "window.small", "")
			house.Door(art.FaceNegY, house.CX()+0.12, 0.14, 0.26, 0.6, s.doorM)
			house.FloorBand(1.7, 0.03, "trim.band")
			house.GableRoof(1.7, 2.34, true, s.roof, s.roof)
			house.Eave(1.7, 0.12, "trim.white")
			// 车库：平顶 + 卷帘门
			gar.M.Box(gar.X0, gar.Y0, 0, gar.X1, gar.Y1, 1.05, "wall.brick")
			gar.M.DecalY(gar.Y1, gar.X0+0.06, gar.X1-0.06, 0.06, 0.72, "garage", 0.03)
			gar.M.SlabTop(1.05, gar.X0, gar.Y0, gar.X1, gar.Y1, "roof.felt")
			gar.M.Parapet(gar.X0, gar.Y0, gar.X1, gar.Y1, 1.05, 0.16, 0.05, "trim.cream")
			TreeAt(b.M, b.X0+0.14, b.Y0+0.16, 0, b.R, s.leaf)
			if b.R.Chance(0.5) {
				b.M.Box(b.X0+0.1, b.Y0+0.5, 0, b.X1-0.14, b.Y0+0.62, 0.02, "concrete.pad")
			}
		},
	}
}

func houseTownhouse() Def {
	return Def{
		Name: "house.townhouse", Label: "联排住宅", Kind: KindBuilding, Category: "住宅",
		Footprint: [2]int{2, 1}, Height: 3.79, Variants: 3, Cost: 460, Pop: 42, Level: 1,
		Build: func(b *B) {
			s := pickScheme(b.R)
			b.Lot("concrete.pad", "concrete.curb")
			unit := 1.0
			for i := 0; i < 2; i++ {
				u := b.Sub(b.X0+float64(i)*unit+0.04, b.Y0+0.06, b.X0+float64(i+1)*unit-0.04, b.Y1-0.06)
				wm := s.wall
				if i == 1 {
					wm = altWall(s.wall)
				}
				u.M.Box(u.X0, u.Y0, 0, u.X1, u.Y1, 0.2, "trim.cream")
				u.M.Box(u.X0, u.Y0, 0.2, u.X1, u.Y1, 2.55, wm)
				u.Windows(0.5, 2.45, 2, 2, "window", "trim.white")
				u.Door(art.FacePosY, u.CX(), 0.2, 0.3, 0.66, s.doorM)
				u.Stoop(art.FacePosY, u.CX(), 0.4, 0.2)
				u.M.SlabTop(2.55, u.X0, u.Y0, u.X1, u.Y1, "roof.felt")
				u.M.Parapet(u.X0, u.Y0, u.X1, u.Y1, 2.55, 0.3, 0.06, "trim.cream")
				if b.R.Chance(0.55) {
					u.M.Box(u.X0+0.2, u.Y0+0.3, 2.85, u.X0+0.42, u.Y0+0.5, 3.1, "metal.dark")
				}
			}
			// 屋顶水箱与天线
			if b.R.Chance(0.5) {
				b.WaterTank(b.X0+1.55, b.Y1-0.28, 2.85, 0.15, 0.32, "wall.metal.rust")
			}
			b.Antenna(b.X0+0.3, b.Y1-0.3, 2.85, 0.9)
		},
	}
}

func houseVilla() Def {
	return Def{
		Name: "house.villa", Label: "富裕别墅", Kind: KindBuilding, Category: "住宅",
		Footprint: [2]int{2, 2}, Height: 2.40, Variants: 3, Cost: 900, Pop: 20, Level: 2,
		Build: func(b *B) {
			s := pickScheme(b.R)
			b.Lot("pad.grass.dark", "concrete.curb")
			// 泳池 + 主体 + 侧翼（层次由低到高，保证水面压在池沿之上）
			b.M.Box(b.X0+0.14, b.Y0+0.14, -0.02, b.X0+0.86, b.Y0+0.74, -0.008, "pad.tile")
			b.M.Box(b.X0+0.2, b.Y0+0.2, -0.012, b.X0+0.8, b.Y0+0.68, -0.004, "water.shallow")
			m := b.Sub(b.X0+0.95, b.Y0+0.12, b.X1-0.1, b.Y1-0.12)
			m.M.Box(m.X0, m.Y0, 0, m.X1, m.Y1, 0.18, "trim.cream")
			m.M.Box(m.X0, m.Y0, 0.18, m.X1, m.Y1, 1.75, s.wall)
			m.Windows(0.4, 1.68, 2, 3, "window", "trim.white")
			m.Door(art.FacePosY, m.CX(), 0.18, 0.36, 0.78, "door.glass")
			m.FloorBand(1.75, 0.05, "trim.band")
			m.HipRoof(1.75, 2.4, 0.28, s.roof, s.roof)
			m.M.Box(m.X0-0.1, m.Y0-0.1, 1.68, m.X1+0.1, m.Y1+0.1, 1.75, "trim.cream")
			// 泳池边的遮阳伞
			px, py := b.X0+0.5, b.Y0+0.44
			b.M.Box(px-0.02, py-0.02, 0, px+0.02, py+0.02, 0.5, "trim.white")
			b.M.Cone(px, py, 0.26, 0.5, 0.66, 8, "awning.red")
			for i := 0; i < 3; i++ {
				TreeAt(b.M, b.X0+0.2+b.R.Float()*1.6, b.Y1-0.3+b.R.Float()*0.16, 0, b.R, s.leaf)
			}
		},
	}
}

func apartmentWalkup() Def {
	return Def{
		Name: "apartment.walkup", Label: "低层公寓", Kind: KindBuilding, Category: "住宅",
		Footprint: [2]int{2, 2}, Height: 4.86, Variants: 3, Cost: 1400, Pop: 96, Level: 2,
		Build: func(b *B) {
			wall := "wall.brick"
			if b.R.Chance(0.5) {
				wall = "wall.stucco"
			}
			b.Lot("concrete.pad", "concrete.curb")
			w := b.Inset(0.14)
			w.M.Box(w.X0, w.Y0, 0, w.X1, w.Y1, 0.24, "trim.dark")
			w.M.Box(w.X0, w.Y0, 0.24, w.X1, w.Y1, 3.5, wall)
			levels := 3
			for i := 0; i < levels; i++ {
				z := 0.24 + float64(i)*1.09
				w.Windows(z+0.22, z+0.98, 1, 3, "window", "")
				w.FloorBand(z+1.06, 0.06, "trim.cream")
			}
			w.Door(art.FacePosY, w.CX(), 0.24, 0.5, 0.9, "door.glass")
			w.M.Box(w.CX()-0.55, w.Y1, 0.24, w.CX()+0.55, w.Y1+0.36, 1.1, "concrete.pad")
			w.M.SlabTop(3.5, w.X0, w.Y0, w.X1, w.Y1, "roof.gravel")
			w.M.Parapet(w.X0, w.Y0, w.X1, w.Y1, 3.5, 0.34, 0.08, wall)
			w.WaterTank(w.X1-0.3, w.Y0+0.32, 3.84, 0.19, 0.4, "wall.metal.rust")
			w.ACUnits(3.84, 3, "metal.dark")
			// 外墙消防梯
			if b.R.Chance(0.6) {
				for i := 0; i < 3; i++ {
					z := 0.5 + float64(i)*1.09
					b.M.Box(w.X1+0.04, w.Y0+0.5, z, w.X1+0.3, w.Y1-0.3, z+0.06, "metal.pipe")
				}
			}
		},
	}
}

func apartmentTower() Def {
	return Def{
		Name: "apartment.tower", Label: "高层住宅楼", Kind: KindBuilding, Category: "住宅",
		Footprint: [2]int{2, 2}, Height: 10.96, Variants: 3, Cost: 4200, Pop: 340, Level: 3,
		Build: func(b *B) {
			b.Lot("concrete.pad", "concrete.curb")
			base := b.Inset(0.1)
			base.M.Box(base.X0, base.Y0, 0, base.X1, base.Y1, 0.6, "wall.concrete")
			base.Windows(0.16, 0.54, 1, 3, "window", "")
			base.M.Box(base.X0-0.06, base.Y0-0.06, 0.6, base.X1+0.06, base.Y1+0.06, 0.72, "concrete.curb")
			// 塔身：两段退台
			segs := []struct {
				inset float64
				h     float64
			}{{0.26, 3.4}, {0.46, 3.1}, {0.66, 1.9}}
			z := 0.72
			for i, sg := range segs {
				t := b.Inset(sg.inset)
				wall := "wall.concrete.warm"
				if i == 0 {
					wall = "wall.stucco.mint"
				}
				t.M.Box(t.X0, t.Y0, z, t.X1, t.Y1, z+sg.h, wall)
				rows := int(sg.h / 0.62)
				if rows < 1 {
					rows = 1
				}
				t.Windows(z+0.1, z+sg.h-0.1, rows, 2, "window", "")
				for k := 0; k <= rows; k++ {
					zz := z + float64(k)*sg.h/float64(rows)
					t.FloorBand(zz, 0.05, "trim.band")
				}
				// 每层阳台
				if i == 0 {
					for k := 0; k < 3; k++ {
						zz := z + 0.4 + float64(k)*1.1
						t.M.Box(t.X1, t.Y0+0.2, zz, t.X1+0.22, t.Y1-0.2, zz+0.06, "concrete.curb")
						t.M.Box(t.X1+0.19, t.Y0+0.2, zz, t.X1+0.22, t.Y1-0.2, zz+0.42, "trim.white")
					}
				}
				z += sg.h
			}
			top := b.Inset(0.66)
			top.M.SlabTop(z, top.X0, top.Y0, top.X1, top.Y1, "roof.gravel")
			top.M.Parapet(top.X0, top.Y0, top.X1, top.Y1, z, 0.34, 0.08, "wall.concrete")
			top.ACUnits(z+0.34, 2, "metal.dark")
			top.Antenna(top.CX(), top.CY(), z+0.34, 1.5)
			top.WaterTank(top.X0+0.28, top.Y1-0.3, z+0.34, 0.16, 0.36, "wall.metal.rust")
		},
	}
}

// ---------------------------------------------------------------- 商业

// residentialDefs 汇总本文件内的单体（新增条目请同时登记到这里）。
func residentialDefs() []Def {
	return []Def{
		houseSmall(),
		houseSuburban(),
		houseTownhouse(),
		houseVilla(),
		apartmentWalkup(),
		apartmentTower(),
	}
}
