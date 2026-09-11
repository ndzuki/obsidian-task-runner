package catalog

import (
	"isocity98/internal/geom"
)

func parkSmall() Def {
	return Def{
		Name: "park.small", Label: "口袋公园", Kind: KindBuilding, Category: "公园",
		Footprint: [2]int{1, 1}, Height: 1.34, Variants: 3, Cost: 90, Level: 1,
		Build: func(b *B) {
			b.M.SlabTop(0.002, b.X0+0.03, b.Y0+0.03, b.X1-0.03, b.Y1-0.03, "pad.grass.dark")
			b.M.Box(b.X0+0.1, b.Y0+0.1, 0, b.X1-0.1, b.Y1-0.34, 0.005, "pad.dirt")
			TreeAt(b.M, b.X0+0.5, b.Y0+0.5, 0, b.R, "leaf.spring")
			if b.R.Chance(0.6) {
				TreeAt(b.M, b.X0+0.76, b.Y0+0.72, 0, b.R, "leaf.olive")
			}
			// 长椅
			bx, by := b.X0+0.3, b.Y1-0.2
			b.M.Box(bx, by, 0, bx+0.34, by+0.06, 0.13, "crate.wood")
			b.M.Box(bx, by+0.05, 0.13, bx+0.34, by+0.1, 0.28, "crate.wood")
		},
	}
}

// ---------------------------------------------------------------- 公园与景观（追加）

// benchAt 公园长椅：三条木条座面 + 靠背 + 两条铸铁腿。
// 靠背朝 -Y/-X（即屏幕上方），座面朝观察者；这样高处的构件落在格子的远端，
// 精灵图锚点才有合理的投影余量。rot=0 长边沿 X，rot=1 长边沿 Y。
func benchAt(m *geom.Mesh, cx, cy float64, rot int, wood string) {
	for i := 0; i < 3; i++ {
		o := (float64(i) - 1) * 0.06
		if rot == 0 {
			m.Box(cx-0.44, cy+o-0.025, 0.22, cx+0.44, cy+o+0.025, 0.27, wood)
		} else {
			m.Box(cx+o-0.025, cy-0.44, 0.22, cx+o+0.025, cy+0.44, 0.27, wood)
		}
	}
	if rot == 0 {
		m.Box(cx-0.44, cy-0.16, 0.27, cx+0.44, cy-0.12, 0.52, wood)
		m.Box(cx-0.42, cy-0.14, 0, cx-0.37, cy+0.12, 0.22, "metal.dark")
		m.Box(cx+0.37, cy-0.14, 0, cx+0.42, cy+0.12, 0.22, "metal.dark")
	} else {
		m.Box(cx-0.16, cy-0.44, 0.27, cx-0.12, cy+0.44, 0.52, wood)
		m.Box(cx-0.14, cy-0.42, 0, cx+0.12, cy-0.37, 0.22, "metal.dark")
		m.Box(cx-0.14, cy+0.37, 0, cx+0.12, cy+0.42, 0.22, "metal.dark")
	}
}

// parkedCar 简易小汽车：车身 + 车窗 + 车顶 + 四轮。rot=0 车头朝 Y，rot=1 朝 X。
func parkedCar(m *geom.Mesh, cx, cy, z float64, rot int, body string) {
	const l, w, h = 0.4, 0.22, 0.15
	ex, ey := w, l
	if rot == 1 {
		ex, ey = l, w
	}
	m.Box(cx-ex, cy-ey, z+0.06, cx+ex, cy+ey, z+0.06+h, body)
	m.Box(cx-ex*0.66, cy-ey*0.5, z+0.06+h, cx+ex*0.66, cy+ey*0.5, z+0.06+h+0.13, "car.glass")
	m.Box(cx-ex*0.6, cy-ey*0.44, z+0.06+h+0.13, cx+ex*0.6, cy+ey*0.44, z+0.06+h+0.18, body)
	for _, sx := range [2]float64{-1, 1} {
		for _, sy := range [2]float64{-1, 1} {
			m.Box(cx+sx*ex*0.72-0.03, cy+sy*ey*0.74-0.05, z, cx+sx*ex*0.72+0.03, cy+sy*ey*0.74+0.05, z+0.09, "tire")
		}
	}
}

// lampPost 灯柱：细杆 + 悬臂 + 灯头（灯头用自发光材质，夜间点亮）。
func lampPost(m *geom.Mesh, x, y, h, arm float64) {
	m.Cylinder(x, y, 0.045, 0, h, 6, "metal.pipe")
	m.Box(x-0.02, y-0.02, h-0.12, x+0.02, y+arm, h, "metal.pipe")
	m.Box(x-0.09, y+arm-0.06, h-0.04, x+0.09, y+arm+0.06, h+0.12, "streetlamp")
	m.Box(x-0.11, y+arm-0.08, h+0.12, x+0.11, y+arm+0.08, h+0.18, "metal.dark")
}

func plaza() Def {
	return Def{
		Name: "plaza", Label: "广场", Kind: KindBuilding, Category: "公园",
		Footprint: [2]int{2, 2}, Height: 2.4, Variants: 3, Cost: 260, Level: 1,
		Build: func(b *B) {
			pad := "pad.tile"
			if b.R.Chance(0.5) {
				pad = "pad.concrete"
			}
			b.Lot(pad, "concrete.curb")
			// 铺装：外圈石带 + 中央方场，1x 下也能读出「广场」而不是空地
			const z = 0.02
			b.M.DecalTop(z, b.X0+0.1, b.Y0+0.1, b.X1-0.1, b.Y0+0.22, "pad.tile", 0.03)
			b.M.DecalTop(z, b.X0+0.1, b.Y1-0.22, b.X1-0.1, b.Y1-0.1, "pad.tile", 0.03)
			b.M.DecalTop(z, b.X0+0.1, b.Y0+0.22, b.X0+0.22, b.Y1-0.22, "pad.tile", 0.03)
			b.M.DecalTop(z, b.X1-0.22, b.Y0+0.22, b.X1-0.1, b.Y1-0.22, "pad.tile", 0.03)
			b.M.DecalTop(z, b.X0+0.62, b.Y0+0.62, b.X1-0.62, b.Y1-0.62, "pad.concrete", 0.03)
			// 喷泉：八角池 + 水面 + 二层水盘 + 水柱
			fx, fy := b.CX(), b.CY()
			b.M.Cylinder(fx, fy, 0.54, 0, 0.36, 8, "wall.stone")
			b.M.Cylinder(fx, fy, 0.47, 0.36, 0.4, 8, "water.shallow")
			b.M.Cylinder(fx, fy, 0.15, 0.4, 0.72, 8, "wall.stone")
			b.M.Cylinder(fx, fy, 0.32, 0.72, 0.82, 8, "wall.stone")
			b.M.Cylinder(fx, fy, 0.26, 0.82, 0.86, 8, "water.shallow")
			b.M.Cylinder(fx, fy, 0.05, 0.86, 1.34, 6, "water.foam")
			b.Blob(fx, fy, 1.4, 0.1, 0.1, 0.08, 3, 8, 0.2, "water.foam")
			// 四角：长椅 + 灯柱 + 行道树
			benchAt(b.M, b.X0+0.5, b.Y0+0.3, 0, "crate.wood")
			benchAt(b.M, b.X1-0.5, b.Y0+0.5, 1, "crate.wood")
			benchAt(b.M, b.X0+0.5, b.Y1-0.5, 1, "crate.wood")
			benchAt(b.M, b.X1-0.5, b.Y1-0.3, 0, "crate.wood")
			lampPost(b.M, b.X0+0.16, b.Y0+0.16, 2.2, 0.24)
			lampPost(b.M, b.X1-0.16, b.Y1-0.16, 2.2, -0.24)
			TreeAt(b.M, b.X0+0.24, b.Y1-0.24, 0, b.R, "leaf.olive")
			if b.R.Chance(0.6) {
				TreeAt(b.M, b.X1-0.24, b.Y0+0.24, 0, b.R, "leaf.spring")
			}
			// 花钵
			b.M.Cylinder(b.X0+0.5, b.Y1-0.16, 0.14, 0, 0.34, 8, "wall.stone")
			b.M.Cylinder(b.X0+0.5, b.Y1-0.16, 0.12, 0.34, 0.44, 8, "leaf.spring")
			b.M.Cylinder(b.X1-0.5, b.Y0+0.16, 0.14, 0, 0.34, 8, "wall.stone")
			b.M.Cylinder(b.X1-0.5, b.Y0+0.16, 0.12, 0.34, 0.44, 8, "leaf.spring")
		},
	}
}

func playground() Def {
	return Def{
		Name: "playground", Label: "儿童游乐场", Kind: KindBuilding, Category: "公园",
		Footprint: [2]int{2, 2}, Height: 2.50, Variants: 3, Cost: 180, Level: 1,
		Build: func(b *B) {
			b.Lot("pad.sand", "concrete.curb")
			// 沙坑
			b.M.Box(b.X0+0.1, b.Y1-0.78, 0, b.X1-0.7, b.Y1-0.1, 0.14, "crate.wood")
			b.M.DecalTop(0.15, b.X0+0.16, b.Y1-0.72, b.X1-0.76, b.Y1-0.16, "pad.sand", 0.03)
			// 攀爬塔：四柱 + 平台 + 锥顶 + 滑梯
			tx, ty := b.X0+0.46, b.Y0+0.5
			for _, p := range [4][2]float64{{tx - 0.18, ty - 0.18}, {tx + 0.18, ty - 0.18}, {tx - 0.18, ty + 0.18}, {tx + 0.18, ty + 0.18}} {
				b.M.Box(p[0]-0.035, p[1]-0.035, 0, p[0]+0.035, p[1]+0.035, 1.3, "crate.wood")
			}
			b.M.Box(tx-0.24, ty-0.24, 1.3, tx+0.24, ty+0.24, 1.42, "wall.wood")
			b.M.Box(tx-0.24, ty-0.24, 1.42, tx+0.24, ty-0.02, 1.5, "awning.red")
			b.M.Cone(tx, ty, 0.28, 2.0, 2.5, 4, "roof.metal")
			for _, p := range [4][2]float64{{tx - 0.18, ty - 0.18}, {tx + 0.18, ty - 0.18}, {tx - 0.18, ty + 0.18}, {tx + 0.18, ty + 0.18}} {
				b.M.Box(p[0]-0.03, p[1]-0.03, 1.42, p[0]+0.03, p[1]+0.03, 2.0, "crate.wood")
			}
			// 滑梯：五级递减的板，做出下坡
			for i := 0; i < 5; i++ {
				z0 := 1.28 - float64(i)*0.22
				b.M.Box(tx+0.24+float64(i)*0.16, ty-0.14, z0-0.06, tx+0.4+float64(i)*0.16, ty+0.14, z0, "sign.panel")
			}
			// 秋千：两组 A 形支架 + 横杆 + 座板
			sx0, sx1, sy := b.X1-0.62, b.X1-0.16, b.Y1-0.5
			for _, p := range [4][2]float64{{sx0, sy - 0.3}, {sx1, sy - 0.3}, {sx0, sy + 0.3}, {sx1, sy + 0.3}} {
				b.M.Box(p[0]-0.04, p[1]-0.04, 0, p[0]+0.04, p[1]+0.04, 1.9, "metal.pipe")
			}
			b.M.Box(sx0-0.06, sy-0.34, 1.9, sx1+0.06, sy-0.28, 1.98, "metal.pipe")
			b.M.Box(sx0-0.06, sy+0.28, 1.9, sx1+0.06, sy+0.34, 1.98, "metal.pipe")
			for i := 0; i < 2; i++ {
				cx := sx0 + 0.16 + float64(i)*0.28
				b.M.Box(cx-0.008, sy-0.004, 0.7, cx+0.008, sy+0.004, 1.92, "metal.pipe")
				b.M.Box(cx-0.1, sy-0.06, 0.68, cx+0.1, sy+0.06, 0.72, "tire")
			}
			// 转盘与跷跷板
			b.M.Cylinder(b.X1-0.34, b.Y0+0.34, 0.3, 0, 0.16, 12, "sign.panel")
			b.M.Box(b.X1-0.62, b.Y0+0.3, 0.16, b.X1-0.06, b.Y0+0.38, 0.22, "chrome")
			b.M.Box(b.X1-0.34, b.Y0+0.06, 0.16, b.X1-0.26, b.Y0+0.62, 0.22, "chrome")
			b.M.Cylinder(b.X0+0.3, b.Y1-0.28, 0.06, 0, 0.34, 8, "wall.metal.rust")
			b.M.Box(b.X0+0.08, b.Y1-0.32, 0.34, b.X0+0.52, b.Y1-0.24, 0.4, "crate.wood")
			TreeAt(b.M, b.X1-0.2, b.Y1-0.2, 0, b.R, "leaf.spring")
		},
	}
}

func statue() Def {
	return Def{
		Name: "statue", Label: "雕像基座", Kind: KindBuilding, Category: "公园",
		Footprint: [2]int{1, 1}, Height: 2.62, Variants: 3, Cost: 340, Level: 1,
		Build: func(b *B) {
			b.Lot("pad.tile", "concrete.curb")
			bronze := "roof.copper"
			if b.R.Chance(0.5) {
				bronze = "wall.stone"
			}
			// 三段式基座：底座 + 收分碑身 + 檐口
			b.M.Box(b.X0+0.16, b.Y0+0.16, 0, b.X1-0.16, b.Y1-0.16, 0.3, "wall.stone")
			b.M.Box(b.X0+0.24, b.Y0+0.24, 0.3, b.X1-0.24, b.Y1-0.24, 1.02, "wall.stone")
			b.M.Box(b.X0+0.2, b.Y0+0.2, 1.02, b.X1-0.2, b.Y1-0.2, 1.16, "trim.cream")
			b.M.DecalX(b.X1-0.24, b.Y0+0.3, b.Y1-0.3, 0.44, 0.9, "sign.panel.dark", 0.03)
			b.M.DecalY(b.Y1-0.24, b.X0+0.3, b.X1-0.3, 0.44, 0.9, "sign.panel.dark", 0.03)
			// 立像：双腿 + 长袍 + 躯干 + 双臂 + 头
			cx, cy := b.CX(), b.CY()
			b.M.Box(cx-0.12, cy-0.1, 1.16, cx-0.02, cy+0.1, 1.5, bronze)
			b.M.Box(cx+0.02, cy-0.1, 1.16, cx+0.12, cy+0.1, 1.5, bronze)
			b.M.Box(cx-0.16, cy-0.12, 1.5, cx+0.16, cy+0.13, 2.08, bronze)
			b.M.Box(cx-0.2, cy-0.09, 1.66, cx+0.2, cy+0.1, 1.78, bronze)
			b.M.Box(cx-0.26, cy-0.08, 2.0, cx+0.06, cy+0.09, 2.12, bronze)
			b.M.Box(cx+0.14, cy-0.07, 2.12, cx+0.2, cy+0.08, 2.62, bronze)
			b.M.Ellipsoid(cx, cy, 2.2, 0.1, 0.1, 0.12, 3, 8, 0.02, 17, bronze)
			b.M.Box(cx-0.05, cy-0.06, 2.3, cx+0.05, cy+0.06, 2.38, "trim.cream")
			// 基座四周：护栏与花钵
			b.M.Parapet(b.X0+0.1, b.Y0+0.1, b.X1-0.1, b.Y1-0.1, 0, 0.14, 0.03, "trim.dark")
			TreeAt(b.M, b.X0+0.12, b.Y1-0.12, 0, b.R, "leaf.dark")
		},
	}
}

func parkingLot() Def {
	return Def{
		Name: "parking", Label: "停车场", Kind: KindBuilding, Category: "公园",
		Footprint: [2]int{2, 2}, Height: 2.4, Variants: 3, Cost: 150, Level: 1,
		Build: func(b *B) {
			b.Lot("pad.asphalt", "concrete.curb")
			// 两排车位：白色标线 + 中间行车通道
			for i := 0; i <= 4; i++ {
				x := b.X0 + 0.18 + float64(i)*0.4
				b.M.DecalTop(0.02, x, b.Y0+0.14, x+0.035, b.Y0+0.78, "paint.white", 0.03)
				b.M.DecalTop(0.02, x, b.Y1-0.78, x+0.035, b.Y1-0.14, "paint.white", 0.03)
			}
			b.M.DecalTop(0.02, b.X0+0.18, b.Y0+0.14, b.X1-0.18, b.Y0+0.18, "paint.white", 0.03)
			b.M.DecalTop(0.02, b.X0+0.18, b.Y1-0.18, b.X1-0.18, b.Y1-0.14, "paint.white", 0.03)
			// 停三辆车（颜色随变体）+ 留一个空位
			colors := []string{"car.red", "car.blue", "car.yellow", "car.white", "car.teal", "car.dark"}
			base := b.R.Pick(len(colors))
			for i := 0; i < 3; i++ {
				parkedCar(b.M, b.X0+0.38+float64(i)*0.4, b.Y0+0.46, 0, 0, colors[(base+i)%len(colors)])
			}
			parkedCar(b.M, b.X1-0.38, b.Y1-0.46, 0, 0, colors[(base+3)%len(colors)])
			// 灯柱、指示牌、护栏绿篱
			lampPost(b.M, b.X1-0.12, b.Y0+0.12, 2.2, -0.22)
			b.M.Box(b.X0+0.06, b.Y1-0.2, 0, b.X0+0.1, b.Y1-0.16, 1.6, "metal.pipe")
			b.M.Box(b.X0+0.02, b.Y1-0.34, 1.6, b.X0+0.14, b.Y1-0.02, 1.94, "sign.panel")
			b.M.Box(b.X0+0.02, b.Y0+0.06, 0, b.X1-0.02, b.Y0+0.12, 0.46, "hedge")
			b.M.Box(b.X0+0.06, b.Y0+0.06, 0.46, b.X0+0.28, b.Y0+0.14, 0.5, "trim.dark")
		},
	}
}

func cemetery() Def {
	return Def{
		Name: "cemetery", Label: "墓园", Kind: KindBuilding, Category: "公园",
		Footprint: [2]int{2, 2}, Height: 2.10, Variants: 3, Cost: 220, Level: 1,
		Build: func(b *B) {
			b.Lot("pad.grass.dark", "concrete.curb")
			// 墓碑阵列：三排，逐块做确定性的高矮与倾斜变化
			stone := []string{"wall.stone", "trim.white", "wall.concrete"}
			for row := 0; row < 3; row++ {
				y := b.Y0 + 0.42 + float64(row)*0.5
				for i := 0; i < 4; i++ {
					if row == 2 && i == 3 {
						continue
					}
					x := b.X0 + 0.26 + float64(i)*0.42
					h := 0.4 + b.R.Float()*0.22
					mt := stone[b.R.Pick(len(stone))]
					b.M.Box(x-0.09, y-0.05, 0.06, x+0.09, y+0.05, h, mt)
					b.M.Cylinder(x, y, 0.09, h, h+0.05, 8, mt)
					b.M.DecalY(y+0.051, x-0.06, x+0.06, h*0.45, h*0.8, "trim.dark", 0.03)
					b.M.Box(x-0.14, y-0.09, 0, x+0.14, y+0.11, 0.06, "pad.gravel")
				}
			}
			// 家族墓：方尖碑 + 十字碑
			b.M.Box(b.X0+1.5, b.Y0+0.16, 0, b.X0+1.84, b.Y0+0.5, 0.28, "wall.stone")
			b.M.Frustum(b.X0+1.56, b.Y0+0.22, b.X0+1.78, b.Y0+0.44, 0.28, 1.9, 0.07, 0.07, "wall.stone", "wall.stone")
			b.M.Box(b.X0+1.62, b.Y0+0.28, 1.9, b.X0+1.72, b.Y0+0.38, 2.1, "trim.cream")
			b.M.Box(b.X0+1.5, b.Y1-0.62, 0, b.X0+1.86, b.Y1-0.26, 0.3, "wall.stone")
			b.M.Box(b.X0+1.63, b.Y1-0.54, 0.3, b.X0+1.73, b.Y1-0.34, 1.5, "trim.white")
			b.M.Box(b.X0+1.5, b.Y1-0.49, 0.82, b.X0+1.86, b.Y1-0.39, 0.98, "trim.white")
			// 铁栅栏（四面）+ 门柱与柏树
			for _, x := range []float64{b.X0 + 0.02, b.X0 + 0.5, b.X0 + 0.98, b.X0 + 1.46, b.X1 - 0.04} {
				b.M.Box(x-0.02, b.Y0+0.01, 0, x+0.02, b.Y0+0.06, 0.8, "metal.dark")
				b.M.Box(x-0.02, b.Y1-0.06, 0, x+0.02, b.Y1-0.01, 0.8, "metal.dark")
			}
			b.M.Box(b.X0+0.02, b.Y0+0.02, 0.72, b.X1-0.02, b.Y0+0.05, 0.78, "metal.dark")
			b.M.Box(b.X0+0.02, b.Y1-0.05, 0.72, b.X1-0.02, b.Y1-0.02, 0.78, "metal.dark")
			b.M.Box(b.X0+0.02, b.Y0+0.05, 0.38, b.X1-0.02, b.Y0+0.08, 0.44, "metal.dark")
			b.M.Box(b.X0+0.02, b.Y1-0.08, 0.38, b.X1-0.02, b.Y1-0.05, 0.44, "metal.dark")
			b.M.Box(b.X0+0.02, b.Y0+0.05, 0, b.X0+0.06, b.Y1-0.05, 0.8, "metal.dark")
			b.M.Box(b.X1-0.06, b.Y0+0.05, 0, b.X1-0.02, b.Y1-0.05, 0.8, "metal.dark")
			TreeAt(b.M, b.X1-0.2, b.Y1-0.2, 0, b.R, "leaf.dark")
			TreeAt(b.M, b.X0+0.18, b.Y1-0.2, 0, b.R, "leaf.dark")
			// 墓前小径
			b.M.DecalTop(0.02, b.X0+0.14, b.Y0+0.24, b.X0+0.2, b.Y1-0.14, "pad.gravel", 0.03)
			b.M.DecalTop(0.02, b.X0+0.2, b.Y0+1.06, b.X1-0.14, b.Y0+1.14, "pad.gravel", 0.03)
		},
	}
}

func avenue() Def {
	return Def{
		Name: "avenue", Label: "林荫道", Kind: KindBuilding, Category: "公园",
		Footprint: [2]int{2, 1}, Height: 2.38, Variants: 3, Cost: 200, Level: 1,
		Build: func(b *B) {
			leaf := "leaf.spring"
			switch b.R.Pick(3) {
			case 0:
				leaf = "leaf.olive"
			case 1:
				leaf = "leaf.autumn"
			}
			b.Lot("pad.grass", "concrete.curb")
			// 中央步道 + 两侧绿篱
			b.M.DecalTop(0.02, b.X0+0.34, b.Y0+0.06, b.X1-0.34, b.Y1-0.06, "pad.tile", 0.03)
			b.M.DecalTop(0.03, b.X0+0.5, b.Y0+0.06, b.X0+0.53, b.Y1-0.06, "pad.concrete", 0.05)
			b.M.DecalTop(0.03, b.X0+0.97, b.Y0+0.06, b.X1-0.53, b.Y1-0.06, "pad.concrete", 0.05)
			b.M.Box(b.X0+0.12, b.Y0+0.06, 0, b.X0+0.3, b.Y1-0.06, 0.5, "hedge")
			b.M.Box(b.X1-0.3, b.Y0+0.06, 0, b.X1-0.12, b.Y1-0.06, 0.5, "hedge")
			// 四棵行道树 + 两盏灯 + 一条长椅
			for i := 0; i < 2; i++ {
				TreeAt(b.M, b.X0+0.22, b.Y0+0.24+float64(i)*0.52, 0, b.R, leaf)
				TreeAt(b.M, b.X1-0.22, b.Y0+0.24+float64(i)*0.52, 0, b.R, leaf)
			}
			lampPost(b.M, b.X0+0.4, b.Y0+0.16, 2.2, 0.22)
			lampPost(b.M, b.X1-0.4, b.Y1-0.16, 2.2, -0.22)
			benchAt(b.M, b.CX(), b.Y0+0.3, 0, "crate.wood")
			b.M.Cylinder(b.X0+0.42, b.Y1-0.2, 0.1, 0, 0.34, 8, "wall.stone")
			b.M.Cylinder(b.X0+0.42, b.Y1-0.2, 0.08, 0.34, 0.44, 8, "leaf.spring")
		},
	}
}

// parkDefs 汇总本文件内的单体（新增条目请同时登记到这里）。
func parkDefs() []Def {
	return []Def{
		parkSmall(),
		plaza(),
		playground(),
		statue(),
		parkingLot(),
		cemetery(),
		avenue(),
	}
}
