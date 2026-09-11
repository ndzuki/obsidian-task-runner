package main

import (
	"fmt"
	"image"
	"os"
	"strings"

	"isocity98/internal/art"
	"isocity98/internal/catalog"
	"isocity98/internal/render"
	"isocity98/internal/sheet"
)

// Agent Town 探针：把监控面板的小镇布局用等距管线合成出来。
//
// 与原面板（俯视 960x540）的对应关系：
//
//	原 spec §5 用屏幕像素坐标描述「四象限 + 中央广场 + 横纵主街」；
//	等距视角下没有「象限」——菱形地图的四个**角**才是四个方向：
//	  上角 = 北（文职）、右角 = 东（工业/开发）、下角 = 南（自然）、左角 = 西（知识/审计）。
//	中央广场自然落在菱形正中，两条主街沿菱形的两个轴向穿过。
//
// 画布严格取 960x540：30x30 格在 1x 下正好 (30+30)*16 = 960 宽、(30+30)*8 = 480 高。
// agentTownMap 是**现代城镇**规划图。
//
// 规划原则（对应「不要再像 RPG 村庄」的要求）：
//   - 环形主干道 + 四条支路导入**中央广场**，广场内不通车（现代城镇中心的通行做法）；
//   - 所有街道都是 2 格宽 + 两侧人行道（road.street），不再是乡间土路；
//   - 按方正街区（block）成组布置建筑，留出统一退线，而不是散落的独栋；
//   - 四个方向分区：北=市政文化、东=科技园区、南=公园水岸、西=办公核心；
//   - 沿街等距行道树 + 水岸林荫步道，绿化成线成片而非随机点缀。
//
// agentTownMap 是**现代城镇**规划图（30x30，画布 960x540）。
//
// 规划原则（对应「不要挤在一起、要更多活动空间」）：
//   - **中央广场放大到 10x10**（占全图 11%），是小镇真正的主活动空间；
//   - 主大道 2 格宽，从广场四边通向地图边缘，广场内不通车；
//   - 21 栋职业建筑分四区布置，**彼此至少留 2 格空隙**，不再肩并肩挤成一片；
//   - 空隙里点缀小体量填充（住宅/小店/咖啡），保持城市感但不制造拥堵；
//   - 沿街等距行道树 + 水岸林荫步道，绿化成线成片。
func agentTownMap(defs []catalog.Def) *scenMap {
	const N = 30
	m := newScenMap(N, N, "terrain.grass")
	// 水岸：四周留 2 格水面，陆地抬高 1 格
	for y := 0; y < N; y++ {
		for x := 0; x < N; x++ {
			if x < 2 || y < 2 || x > 27 || y > 27 {
				m.SetH(x, y, -1)
				m.Ter[m.idx(x, y)] = "terrain.water"
			} else {
				m.SetH(x, y, 1)
			}
		}
	}
	// 主大道（2 格宽）：十字贯通，但在中央广场处断开，广场内不通车
	for i := 2; i <= 9; i++ {
		m.RoadV(14, i, i)
		m.RoadV(15, i, i)
		m.RoadH(14, i, i)
		m.RoadH(15, i, i)
	}
	for i := 20; i <= 27; i++ {
		m.RoadV(14, i, i)
		m.RoadV(15, i, i)
		m.RoadH(14, i, i)
		m.RoadH(15, i, i)
	}
	// 中央广场 10x10：整片铺装（主活动空间）
	for y := 10; y <= 19; y++ {
		for x := 10; x <= 19; x++ {
			m.Ter[m.idx(x, y)] = "terrain.pave"
		}
	}
	// 广场四角的小活动场地（铺装 + 树）
	for _, q := range []struct{ x, y int }{{6, 6}, {20, 6}, {6, 20}, {20, 20}} {
		m.Paint(q.x, q.y, q.x+3, q.y+3, "terrain.pave")
	}
	// 水岸步道
	for i := 2; i <= 27; i++ {
		m.Ter[m.idx(i, 2)] = "terrain.pave"
		m.Ter[m.idx(2, i)] = "terrain.pave"
		m.Ter[m.idx(i, 27)] = "terrain.pave"
		m.Ter[m.idx(27, i)] = "terrain.pave"
	}

	P := func(name string, x, y, fw, fh int) { m.Add(name, x, y, fw, fh) }

	// ── 21 栋职业建筑：四区分置，彼此留 ≥2 格空隙 ──────────────────
	// 北西 · 市政与文化
	P("at.pm", 3, 3, 3, 2)
	P("at.knowledge", 8, 3, 3, 2)
	P("at.plan-review", 3, 7, 2, 2)
	P("at.design", 6, 7, 2, 2)
	P("at.priority", 9, 7, 2, 2)
	P("at.conventions", 3, 10, 1, 2)
	// 北东 · 规划 / 研发 / 办公
	P("at.planning", 16, 3, 2, 2)
	P("at.refining", 19, 3, 2, 2)
	P("at.split", 22, 3, 2, 2)
	P("at.audit", 16, 7, 2, 2)
	P("at.working", 19, 7, 2, 2)
	P("at.closed", 22, 7, 2, 2)
	// 南西 · 服务与休闲（临水岸）
	P("at.merge", 3, 16, 2, 2)
	P("at.conflict", 6, 16, 2, 2)
	P("at.ready", 3, 19, 2, 2)
	P("at.needs-grilling", 6, 19, 2, 2)
	P("at.idle", 3, 22, 2, 2)
	P("at.blocked", 6, 22, 1, 1)
	// 南东 · 科技园区与农业
	P("at.implementing", 16, 16, 3, 2)
	P("at.review", 16, 19, 3, 2)
	P("at.done", 16, 22, 3, 2)
	P("at.farm", 21, 22, 3, 2)
	P("at.pond", 21, 16, 2, 2)
	// 中央广场
	P("at.fountain", 14, 14, 2, 2)
	P("at.flagpole", 11, 11, 1, 1)

	// ── 占位集与校验 ──────────────────────────────────────────────
	occupied := map[[2]int]bool{}
	for _, o := range m.Obj {
		for dy := 0; dy < o.FH; dy++ {
			for dx := 0; dx < o.FW; dx++ {
				occupied[[2]int{o.X + dx, o.Y + dy}] = true
			}
		}
	}
	sizeOf := map[string][2]int{}
	for _, d := range defs {
		sizeOf[d.Name] = d.Footprint
	}
	free := func(x, y, fw, fh int) bool {
		for dy := 0; dy < fh; dy++ {
			for dx := 0; dx < fw; dx++ {
				px, py := x+dx, y+dy
				if !m.inside(px, py) || m.At(px, py) <= 0 {
					return false
				}
				if m.Road[m.idx(px, py)] >= 0 || occupied[[2]int{px, py}] {
					return false
				}
			}
		}
		return true
	}
	put := func(name string, x, y int) bool {
		fw, fh := 1, 1
		if s, ok := sizeOf[name]; ok {
			fw, fh = s[0], s[1]
		}
		if !free(x, y, fw, fh) {
			return false
		}
		for dy := 0; dy < fh; dy++ {
			for dx := 0; dx < fw; dx++ {
				occupied[[2]int{x + dx, y + dy}] = true
			}
		}
		m.Add(name, x, y, fw, fh)
		return true
	}
	// 空隙填充：只在**离最近建筑 ≥1 格**的位置放小体量，避免再度挤成一团。
	// 这就是「空隙」与「空地」的区别：留白是设计的一部分，不是没排满。
	tooClose := func(x, y, fw, fh int) bool {
		for dy := -1; dy < fh+1; dy++ {
			for dx := -1; dx < fw+1; dx++ {
				if occupied[[2]int{x + dx, y + dy}] {
					return true
				}
			}
		}
		return false
	}
	small := []string{"house.small", "house.suburban", "cafe", "shop.corner",
		"park.small", "parking", "playground", "statue", "apartment.walkup", "shop.row"}
	filler := 0
	for y := 3; y <= 26; y += 3 {
		for x := 3; x <= 26; x += 3 {
			nm := small[(x*7+y*13)%len(small)]
			s, ok := sizeOf[nm]
			if !ok {
				continue
			}
			if tooClose(x, y, s[0], s[1]) {
				continue
			}
			if put(nm, x, y) {
				filler++
			}
		}
	}

	// ── 绿化 ────────────────────────────────────────────────────
	green := 0
	for i := 4; i <= 25; i += 4 {
		for _, t := range [][2]int{{12, i}, {17, i}, {i, 12}, {i, 17}} {
			if put("at.tree.oak", t[0], t[1]) {
				green++
			}
		}
	}
	for i := 3; i <= 26; i += 4 {
		for _, t := range [][2]int{{i, 3}, {i, 26}, {3, i}, {26, i}} {
			if put("at.tree.pine", t[0], t[1]) {
				green++
			}
		}
	}
	for _, p := range []struct{ x, y int }{
		{10, 10}, {19, 10}, {10, 19}, {19, 19}, {12, 10}, {17, 19},
	} {
		if put("at.tree.oak", p.x, p.y) {
			green++
		}
	}
	for _, p := range []struct{ x, y int }{{11, 10}, {18, 10}, {11, 19}, {18, 19}} {
		put("at.bush", p.x, p.y)
	}
	for _, p := range []struct{ x, y int }{{10, 12}, {19, 12}, {10, 17}, {19, 17}} {
		put("at.flower", p.x, p.y)
	}

	// ── 居民：每个职业居民站到**自己那栋楼**前 ──────────────────
	// 第三列 key 是面板 STAGE 表的键，运行时据此把 agent 派到对应建筑。
	station := []struct{ b, n, key string }{
		{"at.refining", "npc.refiner", "refining"}, {"at.planning", "npc.planner", "planning"},
		{"at.plan-review", "npc.reviewer-plan", "plan-review"}, {"at.implementing", "npc.implementer", "implementing"},
		{"at.review", "npc.reviewer", "review"}, {"at.merge", "npc.releaser", "merge"},
		{"at.audit", "npc.auditor", "audit"}, {"at.pm", "npc.pm", "pm"},
		{"at.design", "npc.designer", "design"}, {"at.split", "npc.splitter", "split"},
		{"at.blocked", "npc.blocked", "blocked"}, {"at.needs-grilling", "npc.griller", "needs-grilling"},
		{"at.done", "npc.celebrant", "done"}, {"at.closed", "npc.archivist", "closed"},
		{"at.ready", "npc.standby", "ready"}, {"at.idle", "npc.resting", "idle"},
		{"at.working", "npc.worker", "working"}, {"at.conventions", "npc.legislator", "conventions"},
		{"at.knowledge", "npc.librarian", "knowledge"}, {"at.priority", "npc.prioritizer", "priority"},
		{"at.conflict", "npc.arbiter", "conflict"},
	}
	byName := map[string]scenObj{}
	for _, o := range m.Obj {
		byName[o.Name] = o
	}
	findSpot := func(o scenObj) (int, int, bool) {
		var try [][2]int
		for dx := 0; dx < o.FW; dx++ {
			try = append(try, [2]int{o.X + dx, o.Y + o.FH})
		}
		for dy := 0; dy < o.FH; dy++ {
			try = append(try, [2]int{o.X + o.FW, o.Y + dy})
		}
		for dy := 0; dy < o.FH; dy++ {
			try = append(try, [2]int{o.X - 1, o.Y + dy})
		}
		for dx := 0; dx < o.FW; dx++ {
			try = append(try, [2]int{o.X + dx, o.Y - 1})
		}
		for r := 2; r <= 3; r++ {
			for dx := -r; dx <= o.FW+r-1; dx++ {
				try = append(try, [2]int{o.X + dx, o.Y + o.FH + r - 1})
				try = append(try, [2]int{o.X + dx, o.Y - r})
			}
		}
		for _, t := range try {
			if free(t[0], t[1], 1, 1) {
				return t[0], t[1], true
			}
		}
		return 0, 0, false
	}
	stationed := 0
	for i, st := range station {
		o, ok := byName[st.b]
		if !ok {
			continue
		}
		x, y, ok := findSpot(o)
		if !ok {
			continue
		}
		occupied[[2]int{x, y}] = true
		dir := 3
		if y < o.Y {
			dir = 1
		}
		m.Add(fmt.Sprintf("%s_f%d", st.n, dir*4+(i%4)), x, y, 1, 1)
		m.Stations = append(m.Stations, npcStation{Stage: st.key, Sprite: st.n, X: x, Y: y, Dir: dir})
		stationed++
	}

	// ── 小精灵与动物：散布在广场与绿地 ──────────────────────────
	spirits := []struct {
		n    string
		x, y int
	}{
		{"spirit.idea", 12, 13}, {"spirit.gear", 17, 16}, {"spirit.lens", 12, 16},
		{"spirit.scale", 17, 13}, {"spirit.spark", 14, 12}, {"spirit.leaf", 13, 17},
		{"spirit.quill", 5, 12}, {"spirit.cloud", 24, 12}, {"spirit.puzzle", 12, 5},
		{"spirit.key", 17, 24}, {"spirit.dice", 5, 24}, {"spirit.hourglass", 24, 5},
	}
	placedSpirits := 0
	for i, sp := range spirits {
		if put(fmt.Sprintf("%s_f%d", sp.n, (i%4)*4), sp.x, sp.y) {
			placedSpirits++
			continue
		}
		for _, alt := range [][2]int{{4, 8 + i*2}, {25, 8 + i*2}, {8 + i*2, 4}, {8 + i*2, 25}} {
			if put(fmt.Sprintf("%s_f%d", sp.n, (i%4)*4), alt[0], alt[1]) {
				placedSpirits++
				break
			}
		}
	}
	animals := []struct {
		n    string
		x, y int
	}{
		{"animal.dog", 13, 14}, {"animal.cat", 16, 14}, {"animal.bird", 14, 11},
		{"animal.duck", 22, 17}, {"animal.cat", 4, 14}, {"animal.bird", 20, 25},
	}
	placedAnimals := 0
	for i, a := range animals {
		if put(fmt.Sprintf("%s_f%d", a.n, (i%2)*4), a.x, a.y) {
			placedAnimals++
		}
	}
	fmt.Printf("  小镇：21 栋职业建筑 + %d 栋空隙填充、%d 棵树，%d 个居民到岗，%d 精灵 + %d 动物\n",
		filler, green, stationed, placedSpirits, placedAnimals)
	return m
}

// checkAgentTownLayout 自检：建筑不重叠、不压路、脚下是平的陆地。
// 布局是手写的，靠这条自检把「手滑写重了坐标」变成可见的失败而不是画面里的怪事。
func checkAgentTownLayout(m *scenMap) []string {
	occ := map[[2]int]string{}
	var problems []string
	// 居民/小精灵/动物本来就走在街道与人行道上，不受「不得压路」约束
	isChar := func(n string) bool {
		return strings.HasPrefix(n, "npc.") || strings.HasPrefix(n, "spirit.") ||
			strings.HasPrefix(n, "animal.")
	}
	for _, o := range m.Obj {
		for dy := 0; dy < o.FH; dy++ {
			for dx := 0; dx < o.FW; dx++ {
				x, y := o.X+dx, o.Y+dy
				if !m.inside(x, y) {
					problems = append(problems, fmt.Sprintf("%s 超出地图 (%d,%d)", o.Name, x, y))
					continue
				}
				k := [2]int{x, y}
				if prev, dup := occ[k]; dup {
					problems = append(problems, fmt.Sprintf("%s 与 %s 在 (%d,%d) 重叠", o.Name, prev, x, y))
				}
				occ[k] = o.Name
				i := m.idx(x, y)
				if m.Hgt[i] <= 0 {
					problems = append(problems, fmt.Sprintf("%s 压在水面上 (%d,%d)", o.Name, x, y))
				}
				if m.Road[i] >= 0 && !isChar(o.Name) && !isChar(occ[k]) {
					problems = append(problems, fmt.Sprintf("%s 压在道路上 (%d,%d)", o.Name, x, y))
				}
			}
		}
	}
	return problems
}

// ─────────────────────────────────────────────────────────────────────────────
// 运行时阴影：把「几何」与「光」解耦的落地点。
//
// 建筑只需「占地矩形 + 高度」两个数就能算出影子，因此太阳角度可以连续变化，
// 而**不需要重新烘焙任何素材** —— 这是等距 + 预渲染这套组合能胜任
// 「实时太阳角度」这个原 spec 要求的关键。
// ─────────────────────────────────────────────────────────────────────────────

// sunCfg 是「每单位高度在地面上产生的影子偏移（格）」。
type sunCfg struct {
	dx, dy float64
}

// sunFor 把时段名换成影子偏移：低太阳角 → 影子长，正午 → 影子短。
func sunFor(preset string) sunCfg {
	switch preset {
	case "morning": // 清晨：太阳在东（+x），影子甩向西（-x）
		return sunCfg{-1.30, -0.42}
	case "noon": // 正午：近乎当顶
		return sunCfg{-0.26, -0.09}
	case "dusk": // 黄昏：太阳在西（-x），影子甩向东（+x）
		return sunCfg{1.30, 0.42}
	default: // 与烘焙素材一致的固定光向
		return sunCfg{0.36, 0.155}
	}
}

// drawShadowSweep 把占地矩形沿光向扫掠出的影子画成有序抖动的暗色形状。
// 不建多边形、不算凸包：沿方向铺 N 个渐隐的菱形即可，便宜且贴合抖动风格。
func drawShadowSweep(set *sheet.Set, dst *image.NRGBA, cam sheet.Cam, pal *art.Palette,
	x0, y0, fw, fh, h float64, sun sunCfg) {
	const steps = 10
	c := art.Hex("1d1e2a")
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		fillShadowRect(dst, cam, c, x0+sun.dx*h*t, y0+sun.dy*h*t, fw, fh, 1.0-0.55*t)
	}
}

// fillShadowRect 在格坐标画一个半透明暗色菱形，按 level 决定网点密度。
func fillShadowRect(dst *image.NRGBA, cam sheet.Cam, c art.RGBA, x0, y0, fw, fh, level float64) {
	for gy := y0; gy < y0+fh; gy += 0.25 {
		for gx := x0; gx < x0+fw; gx += 0.25 {
			sx, sy := projectG(gx, gy, cam)
			if !insideShadow(dst, sx, sy) {
				continue
			}
			px, py := sx/cam.Scale, sy/cam.Scale
			on := level >= 1.0 || (px+py)%2 == 0
			if !on && level < 0.45 {
				on = (px*3+py*5)%4 == 0
			}
			if !on {
				continue
			}
			i := dst.PixOffset(sx, sy)
			if dst.Pix[i+3] == 0 {
				continue
			}
			a := 0.34 * level
			dst.Pix[i] = uint8(float64(dst.Pix[i])*(1-a) + float64(c.R)*a)
			dst.Pix[i+1] = uint8(float64(dst.Pix[i+1])*(1-a) + float64(c.G)*a)
			dst.Pix[i+2] = uint8(float64(dst.Pix[i+2])*(1-a) + float64(c.B)*a)
		}
	}
}

func insideShadow(dst *image.NRGBA, x, y int) bool {
	return x >= 0 && y >= 0 && x < dst.Rect.Dx() && y < dst.Rect.Dy()
}

// projectG 把格坐标投影到画布像素（与 Cam.project 同一套公式，不需要锚点）。
func projectG(gx, gy float64, cam sheet.Cam) (int, int) {
	sx := (cam.OX + int((gx-gy)*float64(render.TileW/2))) * cam.Scale
	sy := (cam.OY + int((gx+gy)*float64(render.TileH/2))) * cam.Scale
	return sx, sy
}

// heightLookup 从目录里取「原型名 → 世界高度」，用于运行时算影子。
// 影子只需要「占地 + 高度」两个数，与精灵图无关 —— 这正是把几何与光解耦的意义。
func heightLookup(defs []catalog.Def) map[string]float64 {
	m := map[string]float64{}
	for _, d := range defs {
		m[d.Name] = d.Height
	}
	return m
}

func baseName(n string) string {
	i := len(n)
	for i > 0 && n[i-1] >= '0' && n[i-1] <= '9' {
		i--
	}
	if i > 0 && i < len(n) && n[i-1] == '_' {
		return n[:i-1]
	}
	return n
}

// atSceneOpts 是小镇合成的选项。
type atSceneOpts struct {
	Defs    []catalog.Def // 用于查高度算影子；nil 表示不画运行时阴影
	Sun     string        // morning / noon / dusk / ""（=烘焙时的固定光向）
	Season  string        // spring / summer / autumn / winter / ""（=夏）
	Time    string        // dawn / day / dusk / night / ""（=白天）
	Weather string        // clear / rain / snow / fog / petals / ""（=晴）
}

// seasonalName 把布局里的**基础名**解析成本季的精灵名。
//
// 树 / 灌木 / 花坛 / 水面的**形状**随季节变，因此名字里带季节与摇摆帧
// （`at.tree.oak_winter_1`），而建筑与道路与季节无关、名字原样返回。
// 让季节成为名字的一部分，manifest 就不必为季节增加任何维度。
func seasonalName(base, season string, x, y int) string {
	if season == "" {
		season = "summer"
	}
	sway := (x*7 + y*13) % 3
	switch base {
	case "at.tree.oak", "at.tree.pine", "at.bush", "at.flower", "at.water":
		return fmt.Sprintf("%s_%s_%d", base, season, sway)
	}
	return base
}

// AgentTownScene 把小镇合成为严格的 960x540（再按 scale 放大）。
func AgentTownScene(set *sheet.Set, pal *art.Palette, scale int) *image.NRGBA {
	return AgentTownSceneSun(set, pal, scale, atSceneOpts{})
}

// AgentTownSceneSun 合成小镇，按 opts 决定季节与运行时阴影。
func AgentTownSceneSun(set *sheet.Set, pal *art.Palette, scale int, opts atSceneOpts) *image.NRGBA {
	// 逻辑画布回到 spec 锁定的 960x540（1x 下 30x30 格正好铺满）。
	// 若要更大的画布，改这里与 agentTownMap 的 N 即可，其余代码自适应。
	const CW, CH = 960, 540
	m := agentTownMap(opts.Defs)
	if probs := checkAgentTownLayout(m); len(probs) > 0 {
		for _, p := range probs[:min(len(probs), 12)] {
			fmt.Println("  布局自检:", p)
		}
		panic(fmt.Sprintf("Agent Town 布局自检失败（%d 处）", len(probs)))
	}
	base := image.NewNRGBA(image.Rect(0, 0, CW, CH))
	sheet.Sky(base, pal, "sky", "sky")
	dst := image.NewNRGBA(image.Rect(0, 0, CW*scale, CH*scale))
	scaleBlit(dst, base, scale)

	// 30x30 格的菱形：宽 (30+30)*16 = 960，高 (30+30)*8 = 480。
	// 水平居中；垂直让南角正好落在画布底部（北角上方留出高楼的空间）。
	cam := sheet.Cam{OX: CW/2 + render.TileW/2, OY: CH - 30*render.TileH, Scale: scale}
	sun := sunFor(opts.Sun)
	heights := heightLookup(opts.Defs)
	winter := opts.Season == "winter"
	drawV := func(base string, variant int, cx, cy, z float64) {
		set.DrawV(dst, cam, base, variant, cx, cy, z)
	}
	draw := func(name string, cx, cy, z float64) {
		set.Draw(dst, cam, name, cx, cy, z)
	}

	// 与运行时相同的画家算法：按 d = x+y 升序铺地形/侧壁/道路，再插入单体
	var chars []scenObj
	maxD := m.W + m.H
	for d := 0; d <= maxD; d++ {
		for x := 0; x < m.W; x++ {
			y := d - x
			if !m.inside(x, y) {
				continue
			}
			h := m.At(x, y)
			ter := m.Ter[m.idx(x, y)]
			if ter == "terrain.water" {
				// 水面按季节换形：夏季液态、冬季结冰（形状不同 → 必须各烘一套）
				drawV(seasonalName("at.water", opts.Season, x, y), 0, float64(x), float64(y), float64(h))
			} else {
				drawV(ter, (x*7+y*13)%3, float64(x), float64(y), float64(h))
				if winter && h > 0 {
					// 冬季地面积雪：叠一层覆盖瓦片（其余季节该资产不存在）
					draw(seasonalName("at.snow", opts.Season, x, y), float64(x), float64(y), float64(h))
				}
			}
			dirs := [4][2]int{{1, 0}, {0, 1}, {-1, 0}, {0, -1}}
			for i, dd := range dirs {
				nh := m.At(x+dd[0], y+dd[1])
				for k := 0; k < h-nh; k++ {
					// 侧壁精灵带变体后缀（cliff.soil.e_0/_1），而 cliffName 是基础名。
					// 用精确查找会整片取不到 —— 参考图会「没有侧壁」，画面上只是少了立体感，
					// 不报错、不空洞，极难发现（运行时侧壁正常，反倒暴露了离线图的缺失）。
					drawV(cliffName[i], (x+y+k)%2, float64(x), float64(y), float64(h-k))
				}
			}
			if mask := m.Road[m.idx(x, y)]; mask >= 0 {
				// 现代城镇街道：两侧人行道 + 中央虚线
				draw(fmt.Sprintf("road.street.%x", m.roadMask(x, y)), float64(x), float64(y), float64(h))
			}
		}
		for _, o := range m.Obj {
			depth := o.X + o.FW - 1 + o.Y + o.FH - 1
			if depth != d {
				continue
			}
			// 角色推迟到最后一个 pass：先判断有没有被建筑挡住，被挡的要以
			// 高亮剪影「透视」画到最上层，否则居民会被楼整栋吃掉（监控面板的大忌）。
			if isCharName(o.Name) {
				chars = append(chars, o)
				continue
			}
			// 运行时阴影：只查「占地 + 高度」，沿光向扫掠出抖动的暗色形状。
			// 太阳角度一变，影子长度与方向跟着变，无需重烘任何素材。
			if h := heights[baseName(o.Name)]; h > 0 {
				z := float64(m.At(o.X, o.Y))
				drawShadowSweep(set, dst, cam, pal,
					float64(o.X), float64(o.Y), float64(o.FW), float64(o.FH), h+z, sun)
			}
			// 单体名是「原型名」，实际精灵带变体后缀（at.refining_0/_1），必须走安全回退
			drawV(seasonalName(o.Name, opts.Season, o.X, o.Y), 0,
				float64(o.X), float64(o.Y), float64(m.At(o.X, o.Y)))
		}
	}

	// ── 角色 pass：被遮挡的移到最上层并画成高亮剪影 ──────────────────────
	xrayed := 0
	for _, o := range chars {
		name := seasonalName(o.Name, opts.Season, o.X, o.Y)
		sp, ok := lookupSprite(set, o.Name, o.X, o.Y, opts.Season)
		if !ok {
			continue
		}
		z := float64(m.At(o.X, o.Y))
		left, top := spriteTopLeft(cam, o.X, o.Y, z, sp)
		// 用「胸口」这一点代表角色是否被挡住：头与脚都容易被相邻格切到
		px := left + sp.W*cam.Scale/2
		py := top + int(float64(sp.H*cam.Scale)*0.45)
		if occludedAt(set, cam, m, o, px, py, opts.Season) {
			drawSilhouette(dst, sp, left, top, cam.Scale, xrayColor)
			xrayed++
		} else {
			drawV(name, 0, float64(o.X), float64(o.Y), z)
		}
	}
	fmt.Printf("  遮挡透视：%d/%d 个角色被建筑挡住，已用高亮剪影透出\n", xrayed, len(chars))

	// ── 季节 / 时段 / 天气：色阶重映射 + 天气粒子 ──────────────────
	// 顺序有讲究：先重映射（改变全图色温），再画粒子（粒子不该被重映射改色）。
	wx := opts.Weather
	if wx == "" {
		wx = seasonWeather(opts.Season) // 未指定时跟随季节的典型天气
	}
	rules := append(seasonRules(opts.Season), timeRules(opts.Time)...)
	rules = append(rules, weatherRules(wx)...)
	dst = applyRemap(dst, pal, buildRemap(pal, rules))
	drawWeather(dst, wx, cam.Scale)
	return dst
}

// ─────────────────────────────────────────────────────────────────────────────
// 遮挡透视（x-ray）
//
// 等距视角下建筑必然挡住后面的东西，这是方案 A 唯一的结构性代价。
// 剖面模式（at.*.open）解决「想看清内部」；这里解决「人不见了」：
// 逐角色判断胸口那一点是否被**深度更大**的建筑精灵覆盖，是则把它以高亮
// 剪影画到最上层 —— 不是画个标记，而是保持人形轮廓，这样一眼能看出
// 「谁在楼后面」而不是「那里有个点」。
// ─────────────────────────────────────────────────────────────────────────────

// xrayColor 是透视剪影的颜色：青绿高亮，在冷灰的现代建筑上最跳。
var xrayColor = art.Hex("7ff2e0")

// debugOccl 打开后会打印遮挡判断的几何细节。
var debugOccl = os.Getenv("ISO_DEBUG_XRAY") != ""

// lookupSprite 按「原型名」查精灵，回退规则与 sheet.DrawV 一致。
//
// 必须回退：绝大多数单体精灵带变体后缀（at.pm_0 / at.pm_1），
// 直接查 set.Sprites["at.pm"] 一定 miss —— 遮挡判断会因此永远为假。
func lookupSprite(set *sheet.Set, base string, x, y int, season string) (*render.Sprite, bool) {
	name := seasonalName(base, season, x, y)
	for _, n := range []string{name, name + "_0"} {
		if sp, ok := set.Sprites[n]; ok {
			return sp, true
		}
	}
	return nil, false
}

func isCharName(n string) bool {
	return strings.HasPrefix(n, "npc.") || strings.HasPrefix(n, "spirit.") ||
		strings.HasPrefix(n, "animal.")
}

// spriteTopLeft 返回某个单体精灵在画布上的左上角像素。
func spriteTopLeft(cam sheet.Cam, x, y int, z float64, sp *render.Sprite) (int, int) {
	sx := (cam.OX + int((float64(x)-float64(y))*float64(render.TileW/2)) - sp.AnchorX) * cam.Scale
	sy := (cam.OY + int((float64(x)+float64(y))*float64(render.TileH/2)) -
		int(z*float64(render.ZUnit)) - sp.AnchorY) * cam.Scale
	return sx, sy
}

// occludedAt 判断角色胸口那一点是否被更深的对象盖住。
//
// 只比深度更大（更靠南）的对象，且逐像素采样对方的 alpha ——
// 用矩形相交会误判：建筑精灵的包围盒里有大量透明区。
func occludedAt(set *sheet.Set, cam sheet.Cam, m *scenMap, self scenObj,
	px, py int, season string) bool {
	selfDepth := self.X + self.FW - 1 + self.Y + self.FH - 1
	if debugOccl {
		fmt.Printf("  [xray] %s @(%d,%d) 胸口=(%d,%d) 深度=%d\n", self.Name, self.X, self.Y, px, py, selfDepth)
	}
	for _, o := range m.Obj {
		if isCharName(o.Name) {
			continue // 角色之间不算「挡住」
		}
		if o.X+o.FW-1+o.Y+o.FH-1 <= selfDepth {
			continue // 只检查画在角色之后（更靠南）的东西
		}
		sp, ok := lookupSprite(set, o.Name, o.X, o.Y, season)
		if !ok {
			continue
		}
		left, top := spriteTopLeft(cam, o.X, o.Y, float64(m.At(o.X, o.Y)), sp)
		sx, sy := (px-left)/cam.Scale, (py-top)/cam.Scale
		if debugOccl && self.Name == "npc.reviewer_f4" {
			fmt.Printf("     候选 %s 深度=%d 精灵 %dx%d 左上=(%d,%d) 采样=(%d,%d)\n",
				o.Name, o.X+o.FW-1+o.Y+o.FH-1, sp.W, sp.H, left, top, sx, sy)
		}
		if sx < 0 || sy < 0 || sx >= sp.Img.Rect.Dx() || sy >= sp.Img.Rect.Dy() {
			continue
		}
		i := sp.Img.PixOffset(sx, sy)
		if sp.Img.Pix[i+3] >= 128 {
			return true
		}
	}
	return false
}

// drawSilhouette 以纯色画出精灵的剪影（保持人形轮廓，不是方块）。
func drawSilhouette(dst *image.NRGBA, sp *render.Sprite, left, top, scale int, c art.RGBA) {
	for y := 0; y < sp.H; y++ {
		for x := 0; x < sp.W; x++ {
			i := sp.Img.PixOffset(x, y)
			if sp.Img.Pix[i+3] < 128 {
				continue
			}
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					px, py := left+x*scale+dx, top+y*scale+dy
					if px < 0 || py < 0 || px >= dst.Rect.Dx() || py >= dst.Rect.Dy() {
						continue
					}
					j := dst.PixOffset(px, py)
					dst.Pix[j], dst.Pix[j+1], dst.Pix[j+2], dst.Pix[j+3] = c.R, c.G, c.B, 255
				}
			}
		}
	}
}
