package catalog

import (
	"math"
	"testing"

	"isocity98/internal/art"
	"isocity98/internal/render"
)

// defSets 返回需要全量校验的资产集。
//
// 两套美术（城市建造沙盒 / Agent Town 面板）共用同一套建造器与渲染管线，
// 因此必须共用同一套校验。早先这里只遍历 All()，于是 Agent Town 那 26 个原型的
// 材质拼写错误、空精灵、锚点跑飞、Height 写错**一个都拦不住** —— 而 Height
// 又直接决定运行时阴影的长度，错了只能靠肉眼在成品图里发现。
func defSets() map[string][]Def {
	return map[string][]Def{
		"game":      All(),
		"agenttown": AgentTownDefs(),
	}
}

func eachSet(t *testing.T, fn func(t *testing.T, defs []Def)) {
	t.Helper()
	for name, defs := range defSets() {
		t.Run(name, func(t *testing.T) { fn(t, defs) })
	}
}

func TestDefsAreWellFormed(t *testing.T) {
	eachSet(t, func(t *testing.T, defs []Def) {
		if len(defs) < 5 {
			t.Fatalf("资产集只有 %d 个原型，太少了", len(defs))
		}
		seen := map[string]bool{}
		for _, d := range defs {
			if seen[d.Name] {
				t.Fatalf("重名原型 %s", d.Name)
			}
			seen[d.Name] = true
			if d.Label == "" {
				t.Fatalf("%s 缺少中文 Label", d.Name)
			}
			if d.Footprint[0] < 1 || d.Footprint[1] < 1 || d.Footprint[0] > 6 || d.Footprint[1] > 6 {
				t.Fatalf("%s 占地非法 %v", d.Name, d.Footprint)
			}
			if d.Variants < 1 {
				t.Fatalf("%s 变体数非法 %d", d.Name, d.Variants)
			}
			if d.Height < 0 {
				t.Fatalf("%s 高度为负 %v", d.Name, d.Height)
			}
			if len(d.ShadowFP) != 0 && len(d.ShadowFP) != 4 {
				t.Fatalf("%s 的 ShadowFP 必须是 4 个数，得到 %d 个", d.Name, len(d.ShadowFP))
			}
			switch d.Kind {
			case KindBuilding, KindProp, KindTerrain, KindRoad, KindVehicle:
			default:
				t.Fatalf("%s 的 Kind %q 未知", d.Name, d.Kind)
			}
		}
	})
}

// 每个原型、每个变体都必须能建模出来，且不引用光照档里不存在的材质。
func TestEveryVariantBuildsWithValidMaterials(t *testing.T) {
	eachSet(t, func(t *testing.T, defs []Def) {
		for _, d := range defs {
			for _, sn := range seasonsOf(d) {
				SetSeason(sn)
				for v := 0; v < d.Variants; v++ {
					m := BuildMesh(d, v)
					if len(m.Quads) == 0 {
						// 季节资产允许在某些季节缺席（如积雪覆盖层在夏天）
						if d.Seasonal {
							continue
						}
						t.Fatalf("%s 变体 %d 没有生成任何面", d.Name, v)
					}
					for _, q := range m.Quads {
						if q.Mat == "" {
							t.Fatalf("%s 变体 %d 有面未指定材质", d.Name, v)
						}
						if _, ok := art.Day.Get(q.Mat); !ok {
							t.Fatalf("%s(%s) 变体 %d 引用了白天档不存在的材质 %q", d.Name, sn, v, q.Mat)
						}
						if _, ok := art.Night.Get(q.Mat); !ok {
							t.Fatalf("%s(%s) 变体 %d 引用了夜间档不存在的材质 %q", d.Name, sn, v, q.Mat)
						}
					}
				}
			}
		}
	})
	SetSeason("summer")
}

// seasonsOf 返回该原型需要校验的季节列表（非季节资产只需要一季）。
func seasonsOf(d Def) []string {
	if d.Seasonal {
		return Seasons
	}
	return []string{"summer"}
}

// 声明的 Height 必须等于「跨全部变体的最高剪影」。
//
// Height 是 Def 级常量，而变体之间高度会不同（例如 house.small 有一半变体
// 随机带烟囱）。运行时阴影的长度 = Height × 光向偏移，所以正确的语义是
// 「最高那版的高度」—— 逐个变体去比会误报，比最大值小则影子偏短。
//
// 这条测试写完立刻抓到 house.small 声明 2.50 而带烟囱那版才 2.52、
// 不带烟囱只有 2.28 的历史遗留问题。
func TestDeclaredHeightMatchesMesh(t *testing.T) {
	const tol = 0.06 // 全部对齐后收紧：留一点浮点余量，但能拦住明显的漂移
	eachSet(t, func(t *testing.T, defs []Def) {
		for _, d := range defs {
			if d.Height == 0 {
				continue // 平铺瓦片/纯地面不需要高度
			}
			maxZ := 0.0
			for _, sn := range seasonsOf(d) {
				SetSeason(sn)
				for v := 0; v < d.Variants; v++ {
					m := BuildMesh(d, v)
					if len(m.Quads) == 0 {
						continue
					}
					if _, max := m.Bounds(); max.Z > maxZ {
						maxZ = max.Z
					}
				}
			}
			SetSeason("summer")
			if math.Abs(maxZ-d.Height) > tol {
				// 用 Errorf 一次报出全部不符项，便于成批修正
				t.Errorf("%s 声明高度 %.2f，但跨变体最高点是 %.2f（差 %+.2f）",
					d.Name, d.Height, maxZ, maxZ-d.Height)
			}
		}
	})
}

// 每个原型都必须渲染出「有内容、锚点合法」的精灵图。
// 空精灵图（几何写飞了、材质错、范围算错）是最难在运行时发现的失败模式。
func TestEveryDefRendersNonEmptySprite(t *testing.T) {
	p := art.Pal()
	r := NewTestRenderer(p)
	eachSet(t, func(t *testing.T, defs []Def) {
		for _, d := range defs {
			SetSeason("summer")
			m := BuildMesh(d, 0)
			if len(m.Quads) == 0 {
				continue // 夏季不存在的季节资产（积雪覆盖层）
			}
			sp, err := r.Render(d.Name, m, testOptions(d))
			if err != nil {
				t.Fatalf("%s 渲染失败：%v", d.Name, err)
			}
			if sp.W <= 0 || sp.H <= 0 {
				t.Fatalf("%s 精灵尺寸非法 %dx%d", d.Name, sp.W, sp.H)
			}
			// 锚点允许略微落在裁剪后的图像之外（局部原点那一角可能是透明的），
			// 但不能离谱——那说明几何建在了远离原点的位置。
			lim := anchorMargin(sp)
			if sp.AnchorX < -lim || sp.AnchorX > sp.W+lim || sp.AnchorY < -lim || sp.AnchorY > sp.H+lim {
				t.Fatalf("%s 锚点 (%d,%d) 距离 %dx%d 图像过远",
					d.Name, sp.AnchorX, sp.AnchorY, sp.W, sp.H)
			}
			opaque := 0
			for i := 3; i < len(sp.Img.Pix); i += 4 {
				if sp.Img.Pix[i] > 8 {
					opaque++
				}
			}
			if opaque < 25 {
				t.Fatalf("%s 渲染结果几乎是空的（%d 个可见像素）", d.Name, opaque)
			}
		}
	})
}

// 地形/道路/侧壁瓦片必须**尺寸与锚点完全一致**，否则运行时拼接会出现缝隙。
func TestTilesAreSizeUniform(t *testing.T) {
	p := art.Pal()
	r := NewTestRenderer(p)
	groups := map[string][2]int{}
	for _, d := range All() {
		if d.Kind != KindTerrain && d.Kind != KindRoad {
			continue
		}
		want := TileBounds
		if d.Cliff {
			want = CliffBounds
		}
		m := BuildMesh(d, 0)
		sp, err := r.Render(d.Name, m, testOptions(d))
		if err != nil {
			t.Fatalf("%s 渲染失败：%v", d.Name, err)
		}
		if sp.W != want.W() || sp.H != want.H() {
			t.Fatalf("%s 尺寸 %dx%d，应为 %dx%d（统一画布是拼接的前提）",
				d.Name, sp.W, sp.H, want.W(), want.H())
		}
		key := "tile"
		if d.Cliff {
			key = "cliff"
		}
		got := [2]int{sp.AnchorX, sp.AnchorY}
		if prev, ok := groups[key]; ok && prev != got {
			t.Fatalf("%s 锚点 %v 与同类其他瓦片 %v 不一致", d.Name, got, prev)
		}
		groups[key] = got
	}
	if _, ok := groups["tile"]; !ok {
		t.Fatal("没有找到任何地面瓦片")
	}
	if _, ok := groups["cliff"]; !ok {
		t.Fatal("没有找到任何侧壁瓦片")
	}
}

// 道路每种连通掩码都必须存在，否则运行时会出现「画不出来的路口」。
func TestRoadMasksComplete(t *testing.T) {
	have := map[string]bool{}
	for _, d := range All() {
		have[d.Name] = true
	}
	for _, base := range []string{"road.asphalt", "road.dirt", "road.rail"} {
		for m := 0; m < 16; m++ {
			name := base + "." + hexDigit(m)
			if !have[name] {
				t.Fatalf("缺少道路瓦片 %s", name)
			}
		}
	}
}

// anchorMargin 允许的锚点越界余量。
func anchorMargin(sp *render.Sprite) int {
	m := sp.W / 2
	if sp.H/2 > m {
		m = sp.H / 2
	}
	if m < 16 {
		m = 16
	}
	return m
}

func hexDigit(m int) string {
	const digits = "0123456789abcdef"
	if m < 0 || m > 15 {
		return "?"
	}
	return string(digits[m])
}
