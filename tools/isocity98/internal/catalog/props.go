package catalog

import (
	"isocity98/internal/art"
	"isocity98/internal/geom"
)

func treeOak() Def {
	return Def{
		Name: "tree.oak", Label: "阔叶树", Kind: KindProp, Category: "植被",
		Footprint: [2]int{1, 1}, Height: 1.36, Variants: 5,
		ShadowFP: []float64{0.28, 0.28, 0.72, 0.72},
		Build: func(b *B) {
			leaf := []string{"leaf.spring", "leaf.dark", "leaf.olive", "leaf.autumn"}[b.R.Pick(4)]
			TreeAt(b.M, 0.5, 0.5, 0, b.R, leaf)
		},
	}
}

func treePine() Def {
	return Def{
		Name: "tree.pine", Label: "针叶树", Kind: KindProp, Category: "植被",
		Footprint: [2]int{1, 1}, Height: 1.89, Variants: 3,
		ShadowFP: []float64{0.22, 0.22, 0.78, 0.78},
		Build: func(b *B) {
			b.M.Box(0.46, 0.46, 0, 0.54, 0.54, 0.45, "trunk")
			h := 0.5
			for i := 0; i < 3; i++ {
				r := 0.34 - float64(i)*0.09
				b.M.Cone(0.5, 0.5, r, h, h+0.55, 8, leafMat(b.R))
				h += 0.42
			}
		},
	}
}

// ---------------------------------------------------------------- 街道与景观道具（追加）
//
// 道具都是 1x1、体量小（0.5~2.5），用来把街道从「一排盒子」变成有生活感的场景。
// 共用件说明：lampPost / benchAt 定义在 buildings_park.go。

func bush() Def {
	return Def{
		Name: "prop.bush", Label: "灌木", Kind: KindProp, Category: "植被",
		Footprint: [2]int{1, 1}, Height: 0.77, Variants: 4,
		ShadowFP: []float64{0.16, 0.16, 0.84, 0.84},
		Build: func(b *B) {
			leaf := []string{"hedge", "leaf.dark", "leaf.olive", "leaf.spring"}[b.R.Pick(4)]
			// 冠幅要够大：低矮道具的轮廓必须盖过格子的远端角，否则投影余量不足
			b.M.Box(0.46, 0.46, 0, 0.54, 0.54, 0.2, "trunk")
			b.M.Ellipsoid(0.5, 0.5, 0.44, 0.4+b.R.Float()*0.04, 0.38+b.R.Float()*0.04, 0.24, 3, 8, 0.1, int(b.R.Next()%997), leaf)
			b.M.Ellipsoid(0.3, 0.34, 0.26, 0.2, 0.19, 0.15, 3, 8, 0.14, int(b.R.Next()%997), leaf)
			b.M.Ellipsoid(0.7, 0.64, 0.28, 0.19, 0.2, 0.16, 3, 8, 0.14, int(b.R.Next()%997), leaf)
			if b.R.Chance(0.5) {
				b.M.Ellipsoid(0.5, 0.5, 0.66, 0.13, 0.13, 0.1, 2, 6, 0.16, int(b.R.Next()%997), "leaf.spring")
			}
		},
	}
}

func rock() Def {
	return Def{
		Name: "prop.rock", Label: "岩石", Kind: KindProp, Category: "植被",
		Footprint: [2]int{1, 1}, Height: 1.22, Variants: 4,
		ShadowFP: []float64{0.14, 0.16, 0.86, 0.84},
		Build: func(b *B) {
			b.M.Ellipsoid(0.5, 0.5, 0.56, 0.38, 0.34, 0.28, 3, 8, 0.12, int(b.R.Next()%997), "wall.rock")
			b.M.Ellipsoid(0.3, 0.34, 0.26, 0.22, 0.2, 0.15, 3, 7, 0.22, int(b.R.Next()%997), "wall.rock")
			if b.R.Chance(0.7) {
				b.M.Ellipsoid(0.68, 0.64, 0.22, 0.19, 0.17, 0.13, 3, 7, 0.22, int(b.R.Next()%997), "wall.stone")
			}
			b.M.DecalTop(0.01, 0.2, 0.24, 0.8, 0.76, "pad.gravel", 0.03)
			if b.R.Chance(0.5) {
				TreeAt(b.M, 0.78, 0.2, 0, b.R, "leaf.dark")
			}
		},
	}
}

func streetLamp() Def {
	return Def{
		Name: "prop.streetlamp", Label: "街灯", Kind: KindProp, Category: "公园",
		Footprint: [2]int{1, 1}, Height: 2.5, Variants: 3,
		ShadowFP: []float64{0.42, 0.42, 0.58, 0.58},
		Build: func(b *B) {
			// 三条变体：单臂、反向臂、高杆
			arm := 0.24
			h := 2.2 + b.R.Float()*0.2
			if b.R.Chance(0.3) {
				arm = -0.24
			}
			b.M.Cylinder(0.5, 0.5, 0.09, 0, 0.16, 8, "concrete.curb")
			b.M.Cylinder(0.5, 0.5, 0.07, 0.16, 0.28, 8, "metal.dark")
			lampPost(b.M, 0.5, 0.5, h, arm)
			if b.R.Chance(0.4) {
				b.M.Cylinder(0.5, 0.5, 0.05, 0, 0.7, 6, "metal.pipe")
				b.M.Box(0.44, 0.28, 0.7, 0.56, 0.72, 0.76, "sign.panel.dark")
			}
		},
	}
}

func streetSign() Def {
	return Def{
		Name: "prop.sign", Label: "路牌", Kind: KindProp, Category: "公园",
		Footprint: [2]int{1, 1}, Height: 2.2, Variants: 3,
		ShadowFP: []float64{0.44, 0.44, 0.56, 0.56},
		Build: func(b *B) {
			b.M.Cylinder(0.5, 0.5, 0.07, 0, 0.14, 8, "concrete.curb")
			b.M.Cylinder(0.5, 0.5, 0.035, 0.14, 2.05, 6, "metal.pipe")
			if b.R.Chance(0.5) {
				// 路名牌：两块交叉的板
				b.M.Box(0.16, 0.47, 1.66, 0.84, 0.53, 1.96, "sign.panel")
				b.M.Box(0.47, 0.16, 1.94, 0.53, 0.84, 2.2, "sign.panel")
				b.M.DecalY(0.531, 0.2, 0.8, 1.72, 1.9, "paint.white", 0.04)
				b.M.DecalX(0.531, 0.2, 0.8, 2.0, 2.16, "paint.white", 0.04)
			} else {
				// 指示牌：竖排两片
				b.M.Box(0.42, 0.45, 1.2, 0.58, 0.55, 1.96, "sign.panel")
				b.M.DecalY(0.551, 0.44, 0.56, 1.3, 1.86, "paint.white", 0.04)
				b.M.Box(0.3, 0.45, 0.78, 0.7, 0.55, 1.14, "sign.panel.dark")
				b.M.DecalY(0.551, 0.34, 0.66, 0.86, 1.06, "paint.white", 0.04)
			}
			b.M.Box(0.34, 0.34, 0.14, 0.66, 0.66, 0.22, "metal.dark")
		},
	}
}

func propBench() Def {
	return Def{
		Name: "prop.bench", Label: "长椅", Kind: KindProp, Category: "公园",
		Footprint: [2]int{1, 1}, Height: 0.52, Variants: 3,
		ShadowFP: []float64{0.04, 0.3, 0.96, 0.7},
		Build: func(b *B) {
			benchAt(b.M, 0.5, 0.5, 0, "crate.wood")
			if b.R.Chance(0.5) {
				b.M.DecalTop(0.01, 0.2, 0.3, 0.8, 0.72, "pad.concrete", 0.03)
			}
		},
	}
}

func phoneBooth() Def {
	return Def{
		Name: "prop.phonebooth", Label: "电话亭", Kind: KindProp, Category: "公园",
		Footprint: [2]int{1, 1}, Height: 2.4, Variants: 3,
		ShadowFP: []float64{0.3, 0.3, 0.7, 0.7},
		Build: func(b *B) {
			b.M.Box(0.3, 0.3, 0, 0.7, 0.7, 0.14, "wall.brick")
			// 四角柱 + 玻璃四面
			for _, p := range [4][2]float64{{0.32, 0.32}, {0.68, 0.32}, {0.32, 0.68}, {0.68, 0.68}} {
				b.M.Box(p[0]-0.035, p[1]-0.035, 0.14, p[0]+0.035, p[1]+0.035, 2.2, "trim.dark")
			}
			b.M.Box(0.35, 0.35, 0.16, 0.65, 0.65, 2.14, "wall.glass.blue")
			b.M.Box(0.35, 0.35, 0.14, 0.65, 0.65, 0.36, "wall.tile.teal")
			b.M.DecalY(0.351, 0.4, 0.6, 0.5, 2.0, "window.hall", 0.04)
			b.M.DecalX(0.651, 0.4, 0.6, 0.5, 2.0, "window.hall", 0.04)
			b.M.Box(0.28, 0.28, 2.2, 0.72, 0.72, 2.34, "wall.tile.teal")
			b.M.Box(0.34, 0.34, 2.34, 0.66, 0.66, 2.4, "trim.dark")
			b.M.DecalY(0.72, 0.38, 0.62, 2.22, 2.34, "paint.white", 0.05)
			b.M.DecalTop(2.401, 0.36, 0.36, 0.64, 0.64, "sign.panel", 0.03)
		},
	}
}

func mailBox() Def {
	return Def{
		Name: "prop.mailbox", Label: "邮筒", Kind: KindProp, Category: "公园",
		Footprint: [2]int{1, 1}, Height: 1.2, Variants: 3,
		ShadowFP: []float64{0.35, 0.35, 0.65, 0.65},
		Build: func(b *B) {
			body := "car.red"
			if b.R.Chance(0.35) {
				body = "car.teal"
			}
			b.M.Cylinder(0.5, 0.5, 0.16, 0, 0.1, 10, "metal.dark")
			b.M.Cylinder(0.5, 0.5, 0.13, 0.1, 0.98, 10, body)
			b.M.Cylinder(0.5, 0.5, 0.145, 0.62, 0.68, 10, "trim.dark")
			b.M.Ellipsoid(0.5, 0.5, 0.98, 0.14, 0.14, 0.11, 3, 10, 0.02, 5, body)
			b.M.Box(0.62, 0.46, 0.72, 0.64, 0.54, 0.78, "trim.dark")
			b.M.DecalX(0.631, 0.42, 0.58, 0.58, 0.72, "paint.white", 0.04)
			b.M.DecalY(0.631, 0.42, 0.58, 0.58, 0.72, "paint.white", 0.04)
			b.M.Cylinder(0.5, 0.5, 0.02, 1.06, 1.2, 6, "metal.pipe")
		},
	}
}

func hedgeSegment() Def {
	return Def{
		Name: "prop.hedge", Label: "绿篱段", Kind: KindProp, Category: "植被",
		Footprint: [2]int{1, 1}, Height: 0.74, Variants: 3,
		ShadowFP: []float64{0.06, 0.3, 0.94, 0.7},
		Build: func(b *B) {
			leaf := []string{"hedge", "leaf.dark", "leaf.olive"}[b.R.Pick(3)]
			b.M.Box(0.06, 0.3, 0, 0.94, 0.7, 0.62, leaf)
			for i := 0; i < 5; i++ {
				x := 0.14 + float64(i)*0.18
				b.M.Ellipsoid(x, 0.5, 0.6, 0.14, 0.2, 0.12, 3, 8, 0.2, int(b.R.Next()%997), leaf)
			}
			if b.R.Chance(0.5) {
				b.M.Box(0.06, 0.3, 0, 0.94, 0.7, 0.06, "concrete.curb")
			}
		},
	}
}

func fenceSegment() Def {
	return Def{
		Name: "prop.fence", Label: "栅栏段", Kind: KindProp, Category: "公园",
		Footprint: [2]int{1, 1}, Height: 1.1, Variants: 3,
		ShadowFP: []float64{0.03, 0.4, 0.97, 0.6},
		Build: func(b *B) {
			wood := []string{"crate.wood", "wall.wood", "trim.white"}[b.R.Pick(3)]
			for i := 0; i < 4; i++ {
				x := 0.07 + float64(i)*0.287
				b.M.Box(x-0.035, 0.42, 0, x+0.035, 0.58, 0.96, wood)
				b.M.Cone(x, 0.5, 0.05, 0.96, 1.1, 4, wood)
			}
			for _, z := range [2]float64{0.28, 0.66} {
				b.M.Box(0.04, 0.44, z, 0.96, 0.56, z+0.12, wood)
			}
			b.M.Box(0.02, 0.4, 0, 0.98, 0.6, 0.06, "concrete.curb")
		},
	}
}

func pier() Def {
	return Def{
		Name: "prop.pier", Label: "码头", Kind: KindProp, Category: "公园",
		Footprint: [2]int{1, 1}, Height: 1.1, Variants: 3, Water: true,
		Build: func(b *B) {
			// 木桩伸到水面以下，栈桥面高于地面
			for _, p := range [4][2]float64{{0.14, 0.2}, {0.86, 0.2}, {0.14, 0.8}, {0.86, 0.8}} {
				b.M.Box(p[0]-0.05, p[1]-0.05, -0.55, p[0]+0.05, p[1]+0.05, 0.42, "trunk")
			}
			b.M.Box(0.24, 0.24, 0.14, 0.76, 0.76, 0.36, "trunk")
			for i := 0; i < 5; i++ {
				y := 0.08 + float64(i)*0.19
				b.M.Box(0.02, y, 0.42, 0.98, y+0.15, 0.5, "crate.wood")
			}
			b.M.Box(0.06, 0.06, 0.36, 0.94, 0.12, 0.42, "wall.wood")
			b.M.Box(0.06, 0.88, 0.36, 0.94, 0.94, 0.42, "wall.wood")
			// 系缆桩与缆绳
			b.M.Cylinder(0.2, 0.86, 0.055, 0.5, 0.86, 8, "metal.pipe")
			b.M.Cylinder(0.8, 0.14, 0.055, 0.5, 0.86, 8, "metal.pipe")
			b.M.Box(0.24, 0.12, 0.5, 0.76, 0.16, 0.55, "trim.dark")
			// 栈桥上的木箱与渔具
			b.M.Box(0.66, 0.6, 0.5, 0.86, 0.8, 0.72, "crate.wood")
			b.M.Box(0.7, 0.64, 0.72, 0.82, 0.76, 0.86, "crate.wood")
			b.M.Box(0.16, 0.3, 0.5, 0.2, 0.34, 1.02, "metal.pipe")
			b.M.Box(0.12, 0.26, 1.02, 0.24, 0.38, 1.08, "sign.panel.dark")
			if b.R.Chance(0.6) {
				b.M.Cylinder(0.34, 0.44, 0.1, 0.5, 0.62, 8, "sign.panel")
			}
		},
	}
}

func boat() Def {
	return Def{
		Name: "prop.boat", Label: "渔船", Kind: KindProp, Category: "公园",
		Footprint: [2]int{1, 1}, Height: 1.44, Variants: 4, Water: true,
		ShadowFP: []float64{0.1, 0.14, 0.9, 0.86},
		Build: func(b *B) {
			hull := []string{"wall.wood", "wall.tile.white", "car.blue", "car.red"}[b.R.Pick(4)]
			// 船壳：底窄上宽的棱台（inset 取负值即外扩），再补一个船首三角
			b.M.Frustum(0.3, 0.3, 0.7, 0.7, 0.05, 0.46, -0.2, -0.16, hull, "crate.wood")
			b.M.Tri(hull, art.FaceTop, geom.ShadeTop,
				geom.V(0.1, 0.14, 0.46), geom.V(0.06, 0.5, 0.46), geom.V(0.1, 0.86, 0.46))
			b.M.Box(0.12, 0.14, 0.4, 0.5, 0.2, 0.5, hull)
			b.M.Box(0.12, 0.8, 0.4, 0.5, 0.86, 0.5, hull)
			// 座板、桨与货箱
			b.M.Box(0.3, 0.26, 0.46, 0.44, 0.74, 0.52, "crate.wood")
			b.M.Box(0.56, 0.24, 0.46, 0.7, 0.76, 0.52, "crate.wood")
			b.M.Box(0.62, 0.78, 0.52, 0.68, 0.84, 0.78, "crate.wood")
			b.M.Box(0.36, 0.2, 0.52, 0.42, 0.8, 0.58, "trim.dark")
			// 桅杆与旗（或挂机）
			if b.R.Chance(0.65) {
				b.M.Box(0.48, 0.47, 0.52, 0.52, 0.53, 1.44, "metal.pipe")
				b.M.Box(0.52, 0.47, 1.16, 0.74, 0.53, 1.38, "awning.red")
				b.M.Box(0.2, 0.42, 0.94, 0.48, 0.58, 1.0, "wall.wood")
			} else {
				b.M.Box(0.4, 0.36, 0.52, 0.6, 0.64, 0.78, "sign.panel")
				b.M.Box(0.36, 0.32, 0.78, 0.64, 0.68, 0.9, hull)
				b.M.Cylinder(0.66, 0.66, 0.05, 0.52, 1.1, 6, "metal.pipe")
				b.M.Cylinder(0.66, 0.66, 0.09, 1.1, 1.24, 8, "wall.glass.blue")
			}
			if b.R.Chance(0.6) {
				b.M.Cylinder(0.76, 0.28, 0.11, 0, 0.34, 8, "tire")
			}
		},
	}
}

func flowerBed() Def {
	return Def{
		Name: "prop.flowerbed", Label: "花坛", Kind: KindProp, Category: "公园",
		Footprint: [2]int{1, 1}, Height: 0.7, Variants: 4,
		ShadowFP: []float64{0.14, 0.14, 0.86, 0.86},
		Build: func(b *B) {
			rim := "wall.stone"
			if b.R.Chance(0.4) {
				rim = "wall.brick"
			}
			b.M.Parapet(0.14, 0.14, 0.86, 0.86, 0, 0.36, 0.08, rim)
			b.M.DecalTop(0.02, 0.2, 0.2, 0.8, 0.8, "pad.dirt", 0.03)
			b.M.Box(0.24, 0.24, 0.34, 0.76, 0.76, 0.4, "hedge")
			bloom := []string{"car.red", "car.yellow", "car.white", "sign.neon.pink", "car.teal"}
			for i := 0; i < 6; i++ {
				x := 0.26 + b.R.Float()*0.48
				y := 0.26 + b.R.Float()*0.48
				b.M.Box(x-0.04, y-0.04, 0.4, x+0.04, y+0.04, 0.46+b.R.Float()*0.14, bloom[b.R.Pick(len(bloom))])
			}
			if b.R.Chance(0.5) {
				b.M.Cylinder(0.5, 0.5, 0.06, 0.4, 0.7, 6, "trunk")
			}
		},
	}
}

func trashCan() Def {
	return Def{
		Name: "prop.trashcan", Label: "垃圾箱", Kind: KindProp, Category: "公园",
		Footprint: [2]int{1, 1}, Height: 0.9, Variants: 3,
		ShadowFP: []float64{0.32, 0.32, 0.68, 0.68},
		Build: func(b *B) {
			body := "wall.metal.rust"
			if b.R.Chance(0.5) {
				body = "wall.tile.teal"
			}
			b.M.Cylinder(0.5, 0.5, 0.13, 0, 0.06, 10, "concrete.curb")
			b.M.Cylinder(0.5, 0.5, 0.15, 0.06, 0.7, 10, body)
			b.M.Cylinder(0.5, 0.5, 0.17, 0.7, 0.78, 10, "metal.dark")
			b.M.Box(0.42, 0.42, 0.78, 0.58, 0.58, 0.82, "trim.dark")
			b.M.DecalX(0.65, 0.42, 0.58, 0.3, 0.56, "trim.dark", 0.04)
			if b.R.Chance(0.6) {
				b.M.Box(0.4, 0.4, 0.82, 0.6, 0.6, 0.94, "metal.dark")
			}
			if b.R.Chance(0.5) {
				b.M.Box(0.56, 0.3, 0, 0.72, 0.44, 0.34, "crate.wood")
			}
		},
	}
}

func hydrant() Def {
	return Def{
		Name: "prop.hydrant", Label: "消防栓", Kind: KindProp, Category: "公园",
		Footprint: [2]int{1, 1}, Height: 0.9, Variants: 3,
		ShadowFP: []float64{0.36, 0.36, 0.64, 0.64},
		Build: func(b *B) {
			body := "car.red"
			if b.R.Chance(0.3) {
				body = "car.yellow"
			}
			b.M.Cylinder(0.5, 0.5, 0.14, 0, 0.1, 8, "wall.metal.rust")
			b.M.Cylinder(0.5, 0.5, 0.085, 0.1, 0.68, 8, body)
			b.M.Cylinder(0.5, 0.5, 0.105, 0.14, 0.2, 8, body)
			b.M.Box(0.34, 0.44, 0.4, 0.66, 0.56, 0.5, body)
			b.M.Cylinder(0.5, 0.5, 0.1, 0.68, 0.78, 8, body)
			b.M.Ellipsoid(0.5, 0.5, 0.78, 0.1, 0.1, 0.08, 3, 8, 0.02, 11, "metal.dark")
			b.M.Cylinder(0.5, 0.5, 0.025, 0.78, 0.9, 6, "chrome")
		},
	}
}

// propDefs 汇总本文件内的单体（新增条目请同时登记到这里）。
func propDefs() []Def {
	return []Def{
		treeOak(),
		treePine(),
		bush(),
		rock(),
		streetLamp(),
		streetSign(),
		propBench(),
		phoneBooth(),
		mailBox(),
		hedgeSegment(),
		fenceSegment(),
		pier(),
		boat(),
		flowerBed(),
		trashCan(),
		hydrant(),
	}
}

// leafMat 给针叶树在几档深绿之间做确定性变化（铜绿留给铜屋顶，别用在树和苔藓上）。
func leafMat(r *art.Rand) string {
	// 只取深绿系，避免出现「铜绿松树」这种违和配色
	opts := []string{"leaf.dark", "leaf.olive", "leaf.pine"}
	return opts[r.Int(0, 0)]
}
