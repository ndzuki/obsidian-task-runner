package catalog

import (
	"isocity98/internal/art"
	"isocity98/internal/geom"
)

// ─────────────────────────────────────────────────────────────────────────────
// 四季：让「季节」成为与「像素密度」同级的管线参数。
//
// 关键判断是分清哪些东西**必须重烘**、哪些**运行时算就行**：
//
//	形状随季节变（树秃了、水面结冰）→ 必须离线按季节各烘一套
//	只是颜色变（草地、天空、墙面）  → 运行时调色板重映射即可（零资产成本）
//	纯动态（落叶、雪花、花瓣）      → 运行时粒子，与资产无关
//
// 只让「形状会变」的少数原型（树/灌木/花坛/水面）进入季节烘焙，
// 因此图集增量很小；建筑与道路仍是一套。
// ─────────────────────────────────────────────────────────────────────────────

var season = "summer"

// Seasons 是支持的季节，顺序固定（manifest 与运行时都按这个顺序）。
var Seasons = []string{"spring", "summer", "autumn", "winter"}

// SetSeason 设置当前烘焙季节。
func SetSeason(s string) {
	for _, v := range Seasons {
		if v == s {
			season = s
			return
		}
	}
	season = "summer"
}

// Season 返回当前烘焙季节。
func Season() string { return season }

func isWinter() bool { return season == "winter" }
func isSpring() bool { return season == "spring" }
func isAutumn() bool { return season == "autumn" }

// seasonalVariants 是季节资产的摇摆帧数：树冠左右轻微偏移，
// 3 帧循环即可读出「风在吹」，且每帧只有 1~2 像素位移。
const seasonalVariants = 3

// AgentTownSeasonal 是**形状随季节变化**的资产集。
// 命名规则：`<base>_<season>_<sway>`，例如 tree.oak_winter_1。
func AgentTownSeasonal() []Def {
	var out []Def
	// 阔叶树：春樱 / 夏茂 / 秋黄 / 冬秃
	out = append(out, seasonalTree("at.tree.oak", "阔叶树", "oak"))
	// 针叶树：常绿，但冬季枝上积雪
	out = append(out, seasonalTree("at.tree.pine", "针叶树", "pine"))
	// 灌木、花坛
	out = append(out, seasonalShrub("at.bush", "灌木"))
	out = append(out, seasonalFlowerbed("at.flower", "花坛"))
	// 水面：液态 / 结冰
	out = append(out, seasonalWater("at.water", "水面"))
	// 冬季地面积雪覆盖层（运行时叠在地表瓦片上）
	out = append(out, Def{
		Name: "at.snow", Label: "积雪", Kind: KindTerrain, Category: "地形",
		Footprint: [2]int{1, 1}, Variants: 1, Seasonal: true, NoShadow: true,
		Build: func(b *B) {
			// 只有冬季需要它；其余季节渲染成完全透明的空图（运行时按名字取不到就不画）
			if !isWinter() {
				return
			}
			b.M.SlabTop(0.02, 0, 0, 1, 1, "pad.snow")
		},
	})
	return out
}

// seasonalTree 生成一棵随季节换形体的树。
func seasonalTree(name, label, kind string) Def {
	return Def{
		Name: name, Label: label, Kind: KindProp, Category: "植被",
		Footprint: [2]int{1, 1}, Height: 1.9, Variants: seasonalVariants,
		Seasonal: true, NoShadow: true,
		Build: func(b *B) {
			sway := float64(b.Frame) * 0.02 // 摇摆：树冠水平偏移
			if kind == "pine" {
				pineAt(b, 0.5, 0.5, sway)
			} else {
				oakAt(b, 0.5, 0.5, sway)
			}
		},
	}
}

// oakAt 造一棵阔叶树：**四季形状真的不同**。
func oakAt(b *B, x, y, sway float64) {
	r := b.R
	trunkH := 0.50 + r.Float()*0.18
	b.M.Box(x-0.055, y-0.055, 0, x+0.055, y+0.055, trunkH, "trunk")
	if isWinter() {
		// 冬：没有树冠，只剩主干 + 几根秃枝（并落一点雪）
		branches := []struct{ dx, dy, dz, h float64 }{
			{0.16, 0.02, 0.30, 0.30}, {-0.15, 0.06, 0.26, 0.26},
			{0.04, 0.17, 0.34, 0.24}, {-0.05, -0.16, 0.24, 0.22},
		}
		for _, br := range branches {
			b.M.Box(x-0.03, y-0.03, trunkH+br.dz, x+0.03, y+0.03, trunkH+br.dz+br.h, "trunk")
		}
		b.M.Box(x-0.07, y-0.07, trunkH+0.52, x+0.07, y+0.07, trunkH+0.60, "pad.snow")
		return
	}
	// 春/夏/秋：树冠存在，但材质与疏密不同
	leaf := "leaf.spring"
	rad := 0.32
	switch {
	case isSpring():
		leaf, rad = "leaf.spring", 0.30
	case isAutumn():
		leaf, rad = "leaf.autumn", 0.29
	}
	crown := trunkH + 0.26
	b.M.Ellipsoid(x+sway, y, crown, rad+r.Float()*0.08, rad+r.Float()*0.08, 0.25, 3, 8, 0.14, int(r.Next()%997), leaf)
	if r.Chance(0.55) {
		b.M.Ellipsoid(x+sway+0.13, y-0.1, crown*0.72, 0.18, 0.18, 0.15, 3, 8, 0.16, int(r.Next()%997), leaf)
	}
	if isSpring() {
		// 春：冠上点缀花瓣
		for i := 0; i < 3; i++ {
			px := x + sway + (r.Float()-0.5)*0.5
			py := y + (r.Float()-0.5)*0.5
			b.M.Box(px-0.04, py-0.04, crown+0.22, px+0.04, py+0.04, crown+0.28, "roof.tile.red")
		}
	}
	if isAutumn() {
		// 秋：稀疏，露出几处空档（用更小的冠 + 一处缺口近似）
		b.M.Box(x+sway-0.30, y-0.30, crown-0.10, x+sway-0.14, y-0.14, crown+0.06, "trunk")
	}
}

// pineAt 造一棵针叶树：常绿，冬季积雪。
func pineAt(b *B, x, y, sway float64) {
	r := b.R
	b.M.Box(x-0.05, y-0.05, 0, x+0.05, y+0.05, 0.42, "trunk")
	h := 0.46
	for i := 0; i < 3; i++ {
		rad := 0.33 - float64(i)*0.085
		b.M.Cone(x+sway*float64(i+1), y, rad, h, h+0.52, 8, "leaf.pine")
		if isWinter() {
			// 冬：每层锥顶压一层雪
			b.M.Cone(x+sway*float64(i+1), y, rad*0.72, h+0.34, h+0.46, 8, "pad.snow")
		}
		h += 0.40
	}
	_ = r
}

// seasonalShrub 造一丛灌木。
func seasonalShrub(name, label string) Def {
	return Def{
		Name: name, Label: label, Kind: KindProp, Category: "植被",
		Footprint: [2]int{1, 1}, Height: 0.72, Variants: seasonalVariants,
		Seasonal: true, NoShadow: true,
		Build: func(b *B) {
			sway := float64(b.Frame) * 0.015
			if isWinter() {
				// 冬：只剩枯枝
				b.M.Box(0.44, 0.44, 0, 0.56, 0.56, 0.30, "trunk")
				b.M.Box(0.36, 0.46, 0.22, 0.64, 0.54, 0.28, "trunk")
				b.M.Box(0.42, 0.38, 0.30, 0.58, 0.46, 0.36, "pad.snow")
				return
			}
			leaf := "hedge"
			if isSpring() {
				leaf = "leaf.spring"
			} else if isAutumn() {
				leaf = "leaf.autumn"
			}
			b.M.Ellipsoid(0.5+sway, 0.5, 0.40, 0.30, 0.28, 0.26, 3, 8, 0.2, int(b.R.Next()%997), leaf)
			if b.R.Chance(0.5) {
				b.M.Ellipsoid(0.34+sway, 0.36, 0.26, 0.18, 0.17, 0.15, 3, 7, 0.22, int(b.R.Next()%997), leaf)
			}
		},
	}
}

// seasonalFlowerbed 造一个花坛：春/夏/秋有花，冬覆雪。
func seasonalFlowerbed(name, label string) Def {
	return Def{
		Name: name, Label: label, Kind: KindProp, Category: "植被",
		Footprint: [2]int{1, 1}, Height: 0.55, Variants: seasonalVariants,
		Seasonal: true, NoShadow: true,
		Build: func(b *B) {
			b.M.Box(0.10, 0.10, 0, 0.90, 0.90, 0.18, "concrete.curb")
			b.M.Box(0.16, 0.16, 0.14, 0.84, 0.84, 0.22, "pad.dirt")
			if isWinter() {
				b.M.Box(0.16, 0.16, 0.20, 0.84, 0.84, 0.30, "pad.snow")
				return
			}
			flower := "leaf.spring"
			bloom := "roof.tile.red"
			switch {
			case isSpring():
				bloom = "roof.tile.red"
			case isAutumn():
				flower, bloom = "leaf.autumn", "car.yellow"
			}
			for i := 0; i < 6; i++ {
				x := 0.22 + b.R.Float()*0.56
				y := 0.22 + b.R.Float()*0.56
				b.M.Box(x-0.03, y-0.03, 0.20, x+0.03, y+0.03, 0.36+b.R.Float()*0.1, flower)
				b.M.Box(x-0.05, y-0.05, 0.34, x+0.05, y+0.05, 0.42, bloom)
			}
		},
	}
}

// seasonalWater 造水面：夏季液态，冬季结冰。
func seasonalWater(name, label string) Def {
	return Def{
		Name: name, Label: label, Kind: KindTerrain, Category: "地形",
		Footprint: [2]int{1, 1}, Height: 0.2, Variants: seasonalVariants,
		Seasonal: true, NoShadow: true,
		Build: func(b *B) {
			mat := "water"
			if isWinter() {
				mat = "pad.ice"
			}
			b.M.Add(geom.Quad{
				P: [4]geom.Vec3{
					geom.V(0, 0, 0), geom.V(1, 0, 0), geom.V(1, 1, 0), geom.V(0, 1, 0),
				},
				Face: art.FaceTop, Shade: geom.ShadeTop, Mat: mat,
				UOff: float64(b.Frame) * 5,
			})
		},
	}
}
