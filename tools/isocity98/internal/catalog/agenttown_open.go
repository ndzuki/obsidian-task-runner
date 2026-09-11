package catalog

import (
	"isocity98/internal/geom"
)

// ─────────────────────────────────────────────────────────────────────────────
// 剖面模式：等距视角下建筑会挡住它后面的居民，这是方案 A 唯一的结构性代价。
//
// 解决办法不是把楼做矮，而是让同一栋楼**多一个「去掉屋顶」的形态**：
// 一键切换就能像剖面图一样看清内部与身后的人 —— 本质上是把原面板的俯视视图
// 作为等距版的一个子模式保留下来。
//
// 关键在于它**不需要碰 21 个建筑的任何一行建模代码**：剖切是网格层面的后处理。
// ─────────────────────────────────────────────────────────────────────────────

// cutFraction 是剖面保留的体量比例：在建筑高度的这个位置做水平剖面。
//
// 为什么不按「找屋面板」来切：现代建筑的墙是一路砌到屋顶的，屋面板只是最顶上
// 那一层薄片 —— 把它删掉并不能看进去，你看到的仍然是墙顶（实测只削掉 5~20 个面、
// 高度几乎不变）。真正的剖面必须**把剖切面以上的所有几何都删掉**（墙、窗、屋顶
// 设备一并），这才是剖面图的做法。比例兼顾两点：切得够低才看得见内部，
// 切得太低会丢掉建筑用于识别的剪影。
const cutFraction = 0.62

// cutRoof 就地按水平剖面剖切网格：删掉剖面之上的几何，并把跨剖面的面夹断在剖面。
//
// 为什么必须「夹」而不是只「删」：像 at.priority 那种从地面贯通到塔顶的竖向翼片，
// 是**一个**跨了几层高的面。只按「整个面在剖面之上才删」的规则它会被整片保留，
// 剖完之后仍然顶到 76% 高度（实测被门禁抓到）。把跨剖面的顶点夹下来，
// 翼片才正确地断在剖切面上。
func cutRoof(m *geom.Mesh) {
	if len(m.Quads) == 0 {
		return
	}
	min, max := m.Bounds()
	h := max.Z - min.Z
	if h <= 0.6 {
		return // 太矮（广场、凉亭、旗杆）：剖切没有意义，保持原样
	}
	cutZ := min.Z + h*cutFraction
	out := m.Quads[:0]
	for _, q := range m.Quads {
		lowest := 1e9
		for _, p := range q.P {
			if p.Z < lowest {
				lowest = p.Z
			}
		}
		if lowest >= cutZ {
			continue // 整个面在剖切面之上：屋面、女儿墙、屋顶设备
		}
		for i := range q.P {
			if q.P[i].Z > cutZ {
				q.P[i].Z = cutZ // 跨剖面的面：夹断在剖切面
			}
		}
		out = append(out, q)
	}
	m.Quads = out
}

// AgentTownOpenDefs 返回「剖面模式」资产：21 个职业建筑各一个去屋顶的形态，
// 命名 `at.<stage>.open`。
//
// 只烘变体 0（剖切主要用于「看清谁被挡住了」，不需要每种配色都有剖面），
// 因此增量成本是 21 个精灵图 × 昼夜两档。
func AgentTownOpenDefs() []Def {
	var out []Def
	for _, d := range AgentTownDefs() {
		if d.Kind != KindBuilding {
			continue
		}
		orig := d.Build
		open := d
		open.Name = d.Name + ".open"
		open.Label = d.Label + "·剖面"
		open.Variants = 1
		open.NoShadow = true // 剖面是查看模式，不参与烘焙阴影
		open.Build = func(b *B) {
			orig(b)
			cutRoof(b.M)
		}
		// 声明高度必须等于剖切后的最高点（TestDeclaredHeightMatchesMesh 会强制），
		// 所以这里按剖切结果重算一次。
		probe := BuildMesh(open, 0)
		if len(probe.Quads) > 0 {
			_, mx := probe.Bounds()
			open.Height = mx.Z
		}
		out = append(out, open)
	}
	return out
}
