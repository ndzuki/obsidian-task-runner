package catalog

import (
	"fmt"
	"math"

	"isocity98/internal/art"
	"isocity98/internal/geom"
)

// All 返回全部可预渲染单体（不含变体展开）。
func All() []Def {
	var out []Def
	out = append(out, terrainDefs()...)
	out = append(out, cliffDefs()...)
	out = append(out, roadDefs()...)
	out = append(out, buildingDefs()...)
	out = append(out, propDefs()...)
	out = append(out, vehicleDefs()...)
	seen := map[string]bool{}
	for _, d := range out {
		if seen[d.Name] {
			panic("catalog: 重名单体 " + d.Name)
		}
		seen[d.Name] = true
		if d.Variants < 1 {
			panic("catalog: " + d.Name + " 变体数必须 >= 1")
		}
		if d.Build == nil {
			panic("catalog: " + d.Name + " 缺少 Build")
		}
	}
	return out
}

// GenericDefs 是与具体题材无关的通用资产：地形、侧壁、道路、道具。
// Agent Town 探针复用它，避免为了一个面板重复一整套地面/道路瓦片。
func GenericDefs() []Def {
	var out []Def
	out = append(out, terrainDefs()...)
	out = append(out, cliffDefs()...)
	out = append(out, roadDefs()...)
	out = append(out, propDefs()...)
	return out
}

// FillerDefs 返回「城市填充」建筑：住宅 / 商业 / 公共 / 工业。
//
// 与 GenericDefs 分开的原因：填充建筑是可选的市容织物，不是每张地图都要。
// Agent Town 放大到 60x60 后需要一座**真城镇**而不是 21 座孤楼，
// 于是直接复用城市沙盒这 42 个原型当街区填充 —— 零新增建模。
func FillerDefs() []Def { return buildingDefs() }

// AgentTownFillerDefs 只返回 Agent Town 布局里**真正用到**的那几种填充建筑。
//
// 全量 42 个原型会把图集翻倍（8MB → 16MB），而布局只用到 10 种。
// 资产集应当服务于布局，而不是「把仓库里所有东西都烘一遍」。
var agentTownFiller = map[string]bool{
	"house.small": true, "house.suburban": true, "apartment.walkup": true,
	"cafe": true, "shop.corner": true, "shop.row": true,
	"park.small": true, "parking": true, "playground": true, "statue": true,
}

func AgentTownFillerDefs() []Def {
	var out []Def
	for _, d := range buildingDefs() {
		if agentTownFiller[d.Name] {
			out = append(out, d)
		}
	}
	return out
}

func buildingDefs() []Def {
	var out []Def
	out = append(out, residentialDefs()...)
	out = append(out, commercialDefs()...)
	out = append(out, industrialDefs()...)
	out = append(out, civicDefs()...)
	out = append(out, parkDefs()...)
	return out
}

// ---------------------------------------------------------------- 道路

// 4 邻接方向（格坐标）：0=+X 1=+Y 2=-X 3=-Y
var dirVec = [4][2]float64{{1, 0}, {0, 1}, {-1, 0}, {0, -1}}

// 道路瓦片：整格铺装 + 按连通掩码绘制标线与路缘。
func roadDefs() []Def {
	var out []Def
	for _, spec := range []struct {
		Name   string
		Label  string
		Base   string
		Mark   string // 空表示不画标线
		Rail   bool
		Modern bool // 现代街道：两侧人行道
	}{
		{"road.asphalt", "柏油路", "pad.asphalt", "paint.white", false, false},
		{"road.dirt", "土路", "pad.dirt", "", false, false},
		{"road.rail", "铁路", "pad.gravel", "", true, false},
		// 现代街道：两侧人行道 + 路缘 + 中央虚线。现代城镇观感的关键一条 ——
		// 原来的柏油路只在「未连通」方向长出路缘，读起来仍是乡间小路。
		{"road.street", "城镇街道", "pad.asphalt", "paint.white", false, true},
	} {
		for mask := 0; mask < 16; mask++ {
			sp, m := spec, mask
			out = append(out, Def{
				Name: fmt.Sprintf("%s.%x", sp.Name, m), Label: sp.Label,
				Kind: KindRoad, Category: "交通", Footprint: [2]int{1, 1},
				Variants: 1, NoShadow: true, Bounds: &TileBounds,
				Build: func(b *B) {
					roadTile(b, sp.Base, sp.Mark, sp.Rail, m, sp.Modern)
				},
			})
		}
	}
	return out
}

// roadTile 绘制一格道路。mask 为 4 邻接连通掩码；modern 表示现代街道（两侧人行道）。
func roadTile(b *B, base, mark string, rail bool, mask int, modern ...bool) {
	b.M.SlabTop(0, 0, 0, 1, 1, base)
	if rail {
		railTile(b, mask)
		return
	}
	if mark != "" {
		for i := 0; i < 4; i++ {
			if mask&(1<<uint(i)) == 0 {
				continue
			}
			d := dirVec[i]
			// 中心到边中点的虚线
			seg(b, 0.5, 0.5, 0.5+d[0]*0.5, 0.5+d[1]*0.5, 0.02, 0.06, 0.16, 0.34, mark)
			seg(b, 0.5, 0.5, 0.5+d[0]*0.5, 0.5+d[1]*0.5, 0.02, 0.06, 0.62, 0.82, mark)
		}
	}
	isModern := len(modern) > 0 && modern[0]
	// 未连通方向：现代街道铺一条完整人行道（铺装面 + 路缘），普通道路只放路缘石
	sw := 0.07   // 路缘石宽度
	side := 0.24 // 人行道进深
	for i := 0; i < 4; i++ {
		if mask&(1<<uint(i)) != 0 {
			continue
		}
		switch i {
		case 0: // +X 边
			if isModern {
				b.M.Box(1-side, 0.06, 0.005, 1, 0.94, 0.05, "pad.tile")
			}
			b.M.Box(1-sw, 0.06, 0, 1, 0.94, 0.07, "concrete.curb")
		case 1: // +Y 边
			if isModern {
				b.M.Box(0.06, 1-side, 0.005, 0.94, 1, 0.05, "pad.tile")
			}
			b.M.Box(0.06, 1-sw, 0, 0.94, 1, 0.07, "concrete.curb")
		case 2: // -X 边
			if isModern {
				b.M.Box(0, 0.06, 0.005, side, 0.94, 0.05, "pad.tile")
			}
			b.M.Box(0, 0.06, 0, sw, 0.94, 0.07, "concrete.curb")
		case 3: // -Y 边
			if isModern {
				b.M.Box(0.06, 0, 0.005, 0.94, side, 0.05, "pad.tile")
			}
			b.M.Box(0.06, 0, 0, 0.94, sw, 0.07, "concrete.curb")
		}
	}
	// 交叉口中心补一块，避免虚线在中心断开
	if bitsOn(mask) >= 3 {
		b.M.DecalTop(0.025, 0.34, 0.34, 0.66, 0.66, "pad.asphalt", 0.02)
	}
}

// seg 在 a→b 的连线上取 [t0,t1] 段，铺一条宽 w 的带（用于车道虚线）。
func seg(b *B, ax, ay, bx, by, z, w, t0, t1 float64, mat string) {
	dx, dy := bx-ax, by-ay
	l := math.Hypot(dx, dy)
	if l < 1e-9 {
		return
	}
	nx, ny := -dy/l*w, dx/l*w
	x0, y0 := ax+dx*t0, ay+dy*t0
	x1, y1 := ax+dx*t1, ay+dy*t1
	b.M.Add(geom.Quad{
		P: [4]geom.Vec3{
			geom.V(x0+nx, y0+ny, z), geom.V(x1+nx, y1+ny, z),
			geom.V(x1-nx, y1-ny, z), geom.V(x0-nx, y0-ny, z),
		},
		Face: art.FaceTop, Shade: geom.ShadeTop, Mat: mat, Bias: 0.02,
	})
}

func bitsOn(m int) int {
	n := 0
	for i := 0; i < 4; i++ {
		if m&(1<<uint(i)) != 0 {
			n++
		}
	}
	return n
}

// railTile 画铁路：道砟 + 枕木 + 双轨。
func railTile(b *B, mask int) {
	// 直通方向：0/2 为 X 向，1/3 为 Y 向
	mainX := mask&(1<<0) != 0 || mask&(1<<2) != 0
	if !mainX && !(mask&(1<<1) != 0 || mask&(1<<3) != 0) {
		mainX = true
	}
	gauge := 0.17
	drawRail := func(x0, y0, x1, y1 float64) {
		b.M.Add(geom.Quad{
			P: [4]geom.Vec3{
				geom.V(x0, y0, 0.05), geom.V(x1, y1, 0.05),
				geom.V(x1, y1, 0.09), geom.V(x0, y0, 0.09),
			},
			Face: art.FaceTop, Shade: geom.ShadeTop, Mat: "chrome", Bias: 0.02,
		})
	}
	_ = drawRail
	if mainX {
		b.M.Box(0.06, 0.5-gauge-0.035, 0.02, 0.94, 0.5-gauge+0.035, 0.09, "chrome")
		b.M.Box(0.06, 0.5+gauge-0.035, 0.02, 0.94, 0.5+gauge+0.035, 0.09, "chrome")
		for i := 0; i < 5; i++ {
			x := 0.1 + float64(i)*0.2
			b.M.Box(x, 0.5-gauge-0.07, 0.01, x+0.07, 0.5+gauge+0.07, 0.055, "crate.wood")
		}
	} else {
		b.M.Box(0.5-gauge-0.035, 0.06, 0.02, 0.5-gauge+0.035, 0.94, 0.09, "chrome")
		b.M.Box(0.5+gauge-0.035, 0.06, 0.02, 0.5+gauge+0.035, 0.94, 0.09, "chrome")
		for i := 0; i < 5; i++ {
			y := 0.1 + float64(i)*0.2
			b.M.Box(0.5-gauge-0.07, y, 0.01, 0.5+gauge+0.07, y+0.07, 0.055, "crate.wood")
		}
	}
	// 弯道用一小段斜轨示意
	if bitsOn(mask) >= 2 && mask&(1<<0) != 0 && mask&(1<<1) != 0 {
		b.M.Box(0.5, 0.5, 0.03, 0.94, 0.58, 0.085, "chrome")
		b.M.Box(0.5, 0.5, 0.03, 0.58, 0.94, 0.085, "chrome")
	}
}
