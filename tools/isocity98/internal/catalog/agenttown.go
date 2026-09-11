package catalog

import (
	"math"

	"isocity98/internal/art"
	"isocity98/internal/geom"
)

// AgentTownDefs 是「Agent Town」监控面板专用的建筑集。
//
// 它与城市建造沙盒的 catalog.All() **互不影响**：沙盒继续用自己那套建筑原型，
// 这里只放监控面板需要的 21 个 stage 建筑 + 城镇专属道具。两者共用同一套建模
// 词汇（B 建造器）与渲染管线，因此风格天然一致。
//
// 本集的主题是**当代职场建筑 / 现代城镇**：干净、理性、克制，像一座规划良好的
// 科技园区或北欧现代主义街区。形体词汇固定为：
//
//   - 平屋顶 + 女儿墙，屋顶设备（空调机组 / 水箱 / 楼梯间 / 天窗 / 桅杆）；
//   - 玻璃幕墙 + 水平楼层线（FloorBand）与横向带窗（Windows 的 rows/cols），
//     这两样是 1x 下能读出「现代办公楼」的关键，因此不允许出现大面积平墙；
//   - 悬挑雨棚、底层架空柱廊、内院围合、屋顶花园 / 露台、露台薄板亭；
//   - 锯齿采光顶只给厂房（at.implementing / at.review），且做成干净的缓坡。
//
// 明确**不使用**乡村/童话语汇：双坡与四坡屋顶、灰泥小屋、美式壁板、
// 拟物趣味（茶壶/蘑菇/风车）、烟囱、老虎窗。全城只保留 1 处圆曲面地标
// （at.pm 议事厅的中庭采光塔用 Cylinder），其余维持方正的现代体量。
//
// 建模纪律（与渲染管线的约定）：
//   - 占地 1x1 / 1x2 / 2x2 / 3x2，高度 3.5~10，越核心的 stage 越挺拔；
//   - Def.Height 必须等于「跨全部变体的最高网格点」（CI 由
//     TestDeclaredHeightMatchesMesh 强制），因此顶层构件一律显式定高，
//     不使用 Vary 之类的随机抖动去改屋顶标高；
//   - 每个原型先铺场地（Lot/Pad），让小镇连片而不是散落的盒子；
//   - 底层必须有入口（Door + 台阶/雨棚/柱廊），1x 下能看出「门在哪」。
func AgentTownDefs() []Def {
	return []Def{
		// 北区文职
		atRefining(),
		atPlanning(),
		atPlanReview(),
		atDesign(),
		atConventions(),
		// 西区知识 / 审计
		atAudit(),
		atKnowledge(),
		atClosed(),
		atSplit(),
		// 东区工业 / 开发
		atImplementing(),
		atReview(),
		atMerge(),
		atConflict(),
		// 中央广场
		atPM(),
		atBlocked(),
		atNeedsGrilling(),
		atDone(),
		atReady(),
		atWorking(),
		atIdle(),
		// 北区地标
		atPriority(),
		// 城镇道具
		atFountain(),
		atFlagpole(),
		atPond(),
		atFarm(),
		atFlowerbed(),
	}
}

// ---------------------------------------------------------------- 共用件（仅 Agent Town 使用）

// atTrim 在现代配色里只保留两种中性线脚：亮白（干净）与深灰（克制）。
// 不做第三色——现代主义街区的秩序感来自「少而一致」。
func atTrim(r *art.Rand) string {
	if r.Chance(0.42) {
		return "trim.band"
	}
	return "trim.white"
}

// atFlag 挂一面旗。旗面用一片无厚度的四边形表达：等距视角下靠轮廓与颜色
// 就已经够读，做成有厚度的板反而会在 1x 下糊成一块色斑。
func atFlag(m *geom.Mesh, x, y, z, l, h float64, mat string) {
	m.Quad(mat, art.FacePosY, geom.ShadeLeft,
		geom.V(x, y, z),
		geom.V(x+l, y, z-h*0.12),
		geom.V(x+l*0.82, y, z-h*0.78),
		geom.V(x, y, z-h))
}

// atGlassBands 在 z0..z1 之间按 floors 层铺水平层间板（玻璃幕墙的横向分格）。
func atGlassBands(m *geom.Mesh, x0, y0, x1, y1, z0, z1 float64, floors int, mat string) {
	if floors < 1 {
		floors = 1
	}
	step := (z1 - z0) / float64(floors)
	for i := 1; i < floors; i++ {
		z := z0 + float64(i)*step
		m.Box(x0-0.02, y0-0.02, z, x1+0.02, y1+0.02, z+0.055, mat)
	}
}

// atFins 竖向遮阳翼（审计塔 / 规范楼的外墙韵律）。
// 每面墙的翼片略微挑出墙面，1x 下就是一条条竖向亮线。
func atFins(m *geom.Mesh, x0, y0, x1, y1, z0, z1, step float64, mat string) {
	for x := x0 + step; x < x1-0.01; x += step {
		m.Box(x-0.028, y1-0.01, z0, x+0.028, y1+0.05, z1, mat)
		m.Box(x-0.028, y0-0.05, z0, x+0.028, y0+0.01, z1, mat)
	}
	for y := y0 + step; y < y1-0.01; y += step {
		m.Box(x1-0.01, y-0.028, z0, x1+0.05, y+0.028, z1, mat)
		m.Box(x0-0.05, y-0.028, z0, x0+0.01, y+0.028, z1, mat)
	}
}

// atRoofTerrace 屋顶露台：楼板 + 女儿墙 + 细金属栏杆。
// 平屋顶上「有人能站上去」的观感全靠这三件；也是现代建筑最典型的剪影。
func atRoofTerrace(m *geom.Mesh, x0, y0, x1, y1, z, parapet, rail float64, capMat, railMat string) {
	m.SlabTop(z, x0, y0, x1, y1, "roof.gravel")
	if parapet > 0 {
		m.Parapet(x0, y0, x1, y1, z, parapet, 0.06, capMat)
	}
	if rail <= 0 {
		return
	}
	zr := z + parapet + rail
	// 栏杆立柱：四角 + 每边中点
	for _, p := range [8][2]float64{
		{x0 + 0.03, y0 + 0.03}, {x1 - 0.03, y0 + 0.03}, {x0 + 0.03, y1 - 0.03}, {x1 - 0.03, y1 - 0.03},
		{(x0 + x1) / 2, y0 + 0.03}, {(x0 + x1) / 2, y1 - 0.03},
		{x0 + 0.03, (y0 + y1) / 2}, {x1 - 0.03, (y0 + y1) / 2},
	} {
		m.Box(p[0]-0.018, p[1]-0.018, z+parapet, p[0]+0.018, p[1]+0.018, zr, railMat)
	}
	// 扶手压顶
	m.Box(x0, y0, zr, x1, y0+0.05, zr+0.045, railMat)
	m.Box(x0, y1-0.05, zr, x1, y1, zr+0.045, railMat)
	m.Box(x0, y0+0.05, zr, x0+0.05, y1-0.05, zr+0.045, railMat)
	m.Box(x1-0.05, y0+0.05, zr, x1, y1-0.05, zr+0.045, railMat)
}

// atSlab 悬挑雨棚 / 挑檐板。悬挑是现代建筑「入口在哪」的第一信号。
func atSlab(m *geom.Mesh, x0, y0, x1, y1, z, th float64, mat, edge string) {
	m.Box(x0, y0, z, x1, y1, z+th, mat)
	if edge != "" {
		m.Box(x0-0.03, y0-0.03, z+th, x1+0.03, y1+0.03, z+th+0.045, edge)
	}
}

// atCanopy 悬挑雨棚 + 细柱：底层柱廊/架空层的定番组合。
// 柱子只到棚底，棚板挑出柱列之外，才会读成「架空」而不是「贴着墙的盒子」。
func atCanopy(m *geom.Mesh, x0, y0, x1, y1, z, th float64, colMat, slabMat, edge string) {
	atSlab(m, x0, y0, x1, y1, z, th, slabMat, edge)
	dy0, dy1 := y0+0.16, y1-0.16
	for x := x0 + 0.16; x <= x1-0.1; x += (x1 - x0 - 0.32) / 2 {
		m.Cylinder(x, dy0, 0.045, 0, z, 8, colMat)
		m.Cylinder(x, dy1, 0.045, 0, z, 8, colMat)
	}
}

// atRoofDeck 屋顶设备平台：混凝土底台 + 机组，避免设备直接坐在防水层上。
func atRoofDeck(m *geom.Mesh, x0, y0, x1, y1, z, h float64, mat string) {
	m.Box(x0, y0, z, x1, y1, z+h, mat)
}

// atDish 抛物面天线（数据中心/卫星站）：仰角朝 +Y 的圆盘 + 支杆。
func atDish(m *geom.Mesh, x, y, z, r float64, mat string) {
	for i := 0; i < 10; i++ {
		a0 := 2 * math.Pi * float64(i) / 10
		a1 := 2 * math.Pi * float64(i+1) / 10
		m.Tri(mat, art.FacePosY, geom.ShadeLeft,
			geom.V(x, y, z),
			geom.V(x+r*math.Cos(a0), y+0.06, z+r*math.Sin(a0)),
			geom.V(x+r*math.Cos(a1), y+0.06, z+r*math.Sin(a1)))
	}
	m.Box(x-0.02, y-0.06, z-0.02, x+0.02, y, z+0.02, "metal.pipe")
}

// atMastAt 定高桅杆：杆体 + 顶部色块 + 两道横担。zTop 就是全楼最高点。
func atMastAt(m *geom.Mesh, x, y, z0, zTop float64, cap string) {
	m.Box(x-0.032, y-0.032, z0, x+0.032, y+0.032, zTop-0.10, "metal.pipe")
	m.Box(x-0.055, y-0.055, zTop-0.10, x+0.055, y+0.055, zTop, cap)
	for _, t := range [2]float64{0.40, 0.68} {
		z := z0 + (zTop-0.10-z0)*t
		m.Box(x-0.017, y-0.19, z, x+0.017, y+0.19, z+0.028, "metal.pipe")
	}
}

// atShedEnd 锯齿采光顶的端墙山墙：一条缓斜的天际线 + 端墙实体，
// 把锯齿的两端「包」进建筑里。薄檐板会在 1x 下读成悬空的板，所以不用。
func atShedEnd(m *geom.Mesh, x, y0, y1, zEave, zRidge float64, mat string) {
	m.Box(x-0.05, y0-0.04, zEave, x+0.05, y1+0.04, zRidge, mat)
	m.Box(x-0.07, y0-0.06, zRidge, x+0.07, y1+0.06, zRidge+0.06, "metal.pipe")
}

// atLaneMarks 场地标线：短划线阵列（测试场 / 交通枢纽的地面语言）。
func atLaneMarks(m *geom.Mesh, x0, y0, x1 float64, n int, z float64, mat string) {
	for i := 0; i < n; i++ {
		x := x0 + float64(i)*(x1-x0)/float64(n)
		m.DecalTop(z, x, y0, x+0.16, y0+0.05, mat, 0.06)
	}
}

// ---------------------------------------------------------------- 北区文职

// atRefining 需求精炼坊（研发中心）：2x2 / H=5.0。
// 砖 + 玻璃的两层小体量，中间挖一个内院，临院一侧整片玻璃；屋顶楼梯间最高。
// 剪影签名：方盒子 + 深色内院缺口 + 屋顶小楼梯间。
func atRefining() Def {
	return Def{
		Name: "at.refining", Label: "需求精炼坊", Kind: KindBuilding, Category: "市政",
		Footprint: [2]int{2, 2}, Height: 5.0, Variants: 3, Cost: 620, Level: 1,
		Build: func(b *B) {
			b.Lot("pad.concrete", "concrete.curb")
			// 内院与南侧入口铺装（比场地略高一级，读得出「这是院子」）
			b.M.DecalTop(0.02, 0.80, 0.62, 1.20, 1.38, "pad.tile", 0.06)
			b.M.Box(0.62, 0.62, -0.012, 1.38, 1.38, 0.06, "pad.gravel")
			wall := "wall.brick"
			if b.R.Chance(0.45) {
				wall = "wall.concrete.warm"
			}
			band := "trim.band"
			if b.R.Chance(0.4) {
				band = "trim.white"
			}
			glassMat := "wall.glass"
			if b.R.Chance(0.5) {
				glassMat = "wall.glass.blue"
			}
			// 主立面：L 形围合，让内院（x 0.62..1.38 / y 0.62..1.38）朝向观察者敞开
			b.M.Box(0.10, 0.10, 0, 0.62, 1.90, 4.60, wall)
			b.M.Box(1.38, 0.10, 0, 1.90, 1.90, 4.60, wall)
			b.M.Box(0.62, 1.38, 0, 1.38, 1.90, 4.60, wall)
			// 内院三面：临院整片玻璃 + 层间板（研发中心的中庭感）
			for _, f := range []struct{ x0, y0, x1, y1 float64 }{
				{0.62, 0.72, 0.66, 1.30}, {1.34, 0.72, 1.38, 1.30}, {0.68, 1.34, 1.32, 1.38},
			} {
				b.M.Box(f.x0, f.y0, 0.42, f.x1, f.y1, 4.18, glassMat)
			}
			b.M.Box(0.68, 1.30, 2.26, 1.32, 1.42, 2.34, band)
			b.M.Box(0.58, 1.30, 2.26, 0.66, 1.42, 2.34, band)
			b.M.Box(1.34, 1.30, 2.26, 1.42, 1.42, 2.34, band)
			// 外墙横向带窗：两层，每面 3 列（现代办公楼的节奏）
			b.Windows(0.52, 2.02, 2, 3, "window", band)
			b.Windows(2.34, 3.94, 2, 3, "window", band)
			// 入口：门 + 悬挑雨棚
			b.M.DecalY(1.90, 0.82, 1.18, 0.04, 1.20, "door.glass", 0.05)
			b.M.DecalY(1.90, 0.76, 1.24, 1.20, 1.28, band, 0.06)
			atSlab(b.M, 0.70, 1.62, 1.30, 2.02, 2.30, 0.14, "concrete.curb", band)
			for _, cx := range [2]float64{0.78, 1.22} {
				b.M.Cylinder(cx, 1.94, 0.045, 0, 2.30, 8, "metal.pipe")
			}
			// 平屋顶 + 女儿墙 + 屋顶设备
			b.M.Box(0.06, 0.06, 4.60, 1.94, 1.94, 4.74, "concrete.curb")
			b.M.SlabTop(4.74, 0.10, 0.10, 1.90, 1.90, "roof.gravel")
			b.M.Parapet(0.10, 0.10, 1.90, 1.90, 4.74, 0.14, 0.06, band)
			b.M.Box(0.26, 0.26, 4.74, 0.66, 0.82, 4.98, wall)
			b.M.Box(0.22, 0.22, 4.98, 0.70, 0.86, 5.00, band)
			b.M.Box(1.42, 0.30, 4.74, 1.74, 0.66, 4.92, "metal.dark")
			b.M.Box(1.42, 1.10, 4.74, 1.70, 1.40, 4.88, "metal.dark")
			// 内院里的树（1x 下是缺口里的一点绿）
			TreeAt(b.M, 1.0, 1.0, 0.06, b.R, "leaf.spring")
			TreeAt(b.M, 1.62, 1.62, 0, b.R, "leaf.olive")
		},
	}
}

// atPlanning 规划院（规划办公楼）：2x2 / H=6.0。
// 两层裙房 + 两级退台，屋顶露台带栏杆，角上一根细桅杆（全楼最高点，正好 6.0）。
// 剪影签名：三个退台叠成的塔 + 露台栏杆 + 细桅杆。
func atPlanning() Def {
	return Def{
		Name: "at.planning", Label: "规划院", Kind: KindBuilding, Category: "市政",
		Footprint: [2]int{2, 2}, Height: 6.0, Variants: 3, Cost: 820, Level: 2,
		Build: func(b *B) {
			b.Lot("pad.tile", "concrete.curb")
			wall, band := "wall.concrete", "trim.band"
			if b.R.Chance(0.5) {
				wall, band = "wall.tile.white", "trim.white"
			}
			// 裙房（两层）：横向带窗 + 入口柱廊
			b.M.Box(0.10, 0.10, 0, 1.90, 1.90, 2.40, wall)
			b.Windows(0.50, 2.06, 2, 3, "window", band)
			b.M.DecalY(1.90, 0.80, 1.20, 0.03, 1.16, "door.glass", 0.05)
			atCanopy(b.M, 0.68, 1.90, 1.32, 2.24, 2.20, 0.13, "metal.pipe", "concrete.curb", band)
			// 二级体量
			b.M.Box(0.24, 0.24, 2.40, 1.76, 1.76, 3.90, wall)
			b.Windows(2.70, 3.50, 1, 3, "window", band)
			b.M.Box(0.18, 0.18, 3.90, 1.82, 1.82, 4.04, "concrete.curb")
			b.M.Parapet(0.24, 0.24, 1.76, 1.76, 4.04, 0.16, 0.06, band)
			// 三级体量（最小一层）
			b.M.Box(0.38, 0.38, 4.04, 1.62, 1.62, 5.05, wall)
			b.Windows(4.34, 4.86, 1, 2, "window", band)
			// 屋顶露台 + 栏杆（顶面 5.11）
			atRoofTerrace(b.M, 0.38, 0.38, 1.62, 1.62, 5.05, 0.14, 0.30, band, "metal.pipe")
			// 露台沙盘桌：矮腿 + 半透明台面 + 网格线
			b.M.Box(0.58, 0.62, 5.11, 0.62, 0.66, 5.34, "metal.dark")
			b.M.Box(1.38, 0.62, 5.11, 1.42, 0.66, 5.34, "metal.dark")
			b.M.Box(0.58, 1.34, 5.11, 0.62, 1.38, 5.34, "metal.dark")
			b.M.Box(1.38, 1.34, 5.11, 1.42, 1.38, 5.34, "metal.dark")
			b.M.Box(0.54, 0.58, 5.34, 1.46, 1.42, 5.40, "wall.glass.blue")
			for i := 1; i < 4; i++ {
				x := 0.54 + float64(i)*0.23
				b.M.DecalTop(5.41, x, 0.58, x+0.014, 1.42, "trim.band", 0.05)
			}
			// 露台绿化：种植池 + 绿篱
			b.M.Box(0.46, 1.46, 5.11, 0.68, 1.56, 5.36, "concrete.curb")
			b.M.Box(0.48, 1.47, 5.36, 0.66, 1.55, 5.50, "hedge")
			// 桅杆：全楼最高点 6.00
			atMastAt(b.M, 1.55, 0.45, 5.11, 6.00, "car.red")
			TreeAt(b.M, 0.20, 1.78, 0, b.R, "leaf.olive")
			TreeAt(b.M, 1.80, 0.22, 0, b.R, "leaf.spring")
		},
	}
}

// atPlanReview 评审厅（评审中心）：2x2 / H=5.5。
// 底层架空柱廊 + 大挑檐雨棚，上部三层横向带窗，正面一角是整片玻璃的角窗会议室。
// 剪影签名：底层通透的柱廊阴影 + 一块挑出的雨棚 + 屋顶一角更高的玻璃体。
func atPlanReview() Def {
	return Def{
		Name: "at.plan-review", Label: "评审厅", Kind: KindBuilding, Category: "市政",
		Footprint: [2]int{2, 2}, Height: 5.5, Variants: 3, Cost: 760, Level: 2,
		Build: func(b *B) {
			b.Lot("pad.tile", "concrete.curb")
			wall, band := "wall.concrete.warm", "trim.band"
			if b.R.Chance(0.45) {
				wall, band = "wall.tile.white", "trim.white"
			}
			// 底层退进 0.16 形成架空柱廊；玻璃带窗 + 门
			b.M.Box(0.16, 0.16, 0, 1.84, 1.84, 1.00, wall)
			b.M.DecalY(1.84, 0.28, 1.72, 0.16, 0.94, "window.hall", 0.04)
			b.M.DecalY(1.84, 0.82, 1.18, 0.02, 0.96, "door.glass", 0.06)
			// 主体三层
			b.M.Box(0.10, 0.10, 1.00, 1.90, 1.90, 4.70, wall)
			b.Windows(1.30, 3.10, 3, 3, "window", band)
			// 二/三层之间加一道明确的层间板（评审中心的水平分段）
			b.M.Box(0.06, 0.06, 2.22, 1.94, 1.94, 2.30, "concrete.curb")
			// 悬挑雨棚 + 柱列（入口的仪式感但不浮夸）
			atCanopy(b.M, 0.00, 1.90, 2.00, 2.18, 3.30, 0.16, "metal.pipe", "concrete.curb", band)
			b.M.Box(0.04, 2.10, 2.88, 1.96, 2.18, 3.30, "wall.glass.blue")
			// 正面右角的玻璃角窗会议室：比主体高一层
			b.M.Box(1.20, 0.10, 4.70, 1.90, 0.86, 5.20, "wall.glass")
			atGlassBands(b.M, 1.20, 0.10, 1.90, 0.86, 4.70, 5.20, 2, band)
			b.M.Box(1.14, 0.04, 5.20, 1.96, 0.92, 5.32, "concrete.curb")
			b.M.Box(1.16, 0.06, 5.32, 1.94, 0.90, 5.38, band)
			// 主体平屋顶 + 女儿墙 + 屋顶设备
			b.M.Box(0.06, 0.06, 4.70, 1.94, 1.94, 4.84, "concrete.curb")
			b.M.SlabTop(4.84, 0.10, 0.10, 1.90, 1.90, "roof.gravel")
			b.M.Parapet(0.10, 0.10, 1.90, 1.90, 4.84, 0.18, 0.06, band)
			b.M.Box(0.26, 1.24, 4.84, 0.78, 1.70, 5.10, "metal.dark")
			b.M.Box(0.30, 1.28, 5.10, 0.74, 1.66, 5.16, "metal.pipe")
			// 楼梯间出屋面：全楼最高点 5.50
			b.M.Box(0.34, 0.24, 4.84, 0.94, 0.84, 5.36, wall)
			b.M.Box(0.30, 0.20, 5.36, 0.98, 0.88, 5.50, band)
			TreeAt(b.M, 0.20, 1.78, 0, b.R, "leaf.spring")
		},
	}
}

// atDesign 设计工坊（设计工作室）：2x2 / H=4.0。
// 低层整片玻璃 + 一排采光天窗；顶部挑檐板压住体量，屋面满铺采光带。
// 剪影签名：整栋「亮带」+ 屋顶四条并列天窗。
func atDesign() Def {
	return Def{
		Name: "at.design", Label: "设计工坊", Kind: KindBuilding, Category: "工业",
		Footprint: [2]int{2, 2}, Height: 4.0, Variants: 3, Cost: 560, Level: 1,
		Build: func(b *B) {
			b.Lot("pad.concrete", "concrete.curb")
			band := "trim.band"
			if b.R.Chance(0.4) {
				band = "trim.white"
			}
			// 主玻璃体量：2.2 层高，四面整片幕墙
			b.M.Box(0.42, 0.42, 0, 1.58, 1.58, 1.70, "wall.glass")
			atGlassBands(b.M, 0.42, 0.42, 1.58, 1.58, 0.0, 1.70, 2, band)
			b.M.Box(0.48, 0.48, 1.70, 1.52, 1.52, 2.60, "wall.glass.blue")
			atGlassBands(b.M, 0.48, 0.48, 1.52, 1.52, 1.70, 2.60, 2, band)
			// 侧翼实体体量：给玻璃一个对比面（白 vs 玻璃）
			wall := "wall.tile.white"
			if b.R.Chance(0.5) {
				wall = "wall.concrete"
			}
			b.M.Box(0.06, 0.06, 0, 0.42, 1.94, 2.30, wall)
			b.M.Box(1.58, 0.06, 0, 1.94, 1.94, 2.30, wall)
			b.M.Box(0.42, 0.06, 0, 1.58, 0.42, 2.30, wall)
			b.M.Box(0.42, 1.58, 0, 1.58, 1.94, 2.30, wall)
			// 顶部挑檐板 + 采光天窗四道（屋面本身就是「光」）
			b.M.Box(0.00, 0.00, 2.60, 2.00, 2.00, 2.84, "concrete.curb")
			b.M.Box(0.02, 0.02, 2.84, 1.98, 1.98, 2.90, band)
			for i := 0; i < 4; i++ {
				y := 0.34 + float64(i)*0.44
				b.M.Box(0.16, y-0.10, 3.62, 1.84, y-0.03, 3.88, "metal.pipe")
				b.M.Box(0.16, y-0.05, 3.66, 1.84, y+0.05, 3.92, "wall.glass")
				b.M.Box(0.16, y+0.10, 3.62, 1.84, y+0.17, 3.88, "metal.pipe")
			}
			// 四角天窗围板：把屋面收成干净的「锯齿采光带」
			b.M.Box(0.10, 0.10, 3.62, 0.20, 1.90, 4.00, wall)
			b.M.Box(1.80, 0.10, 3.62, 1.90, 1.90, 4.00, wall)
			// 入口 + 雨棚
			b.M.DecalY(1.58, 0.86, 1.14, 0.03, 1.06, "door.glass", 0.06)
			atSlab(b.M, 0.72, 1.58, 1.28, 2.02, 2.00, 0.12, "concrete.curb", band)
			b.M.Cylinder(0.80, 1.96, 0.04, 0, 2.00, 8, "metal.pipe")
			b.M.Cylinder(1.20, 1.96, 0.04, 0, 2.00, 8, "metal.pipe")
			// 屋顶设备：空调机组 + 通风管（变体决定多少）
			b.M.Box(0.24, 1.60, 2.90, 0.62, 1.86, 3.10, "metal.dark")
			if b.R.Chance(0.6) {
				b.M.Box(1.40, 1.58, 2.90, 1.80, 1.86, 3.14, "metal.dark")
			}
			// 前场：模型台与图纸筒
			b.M.Box(0.20, 1.66, 0, 0.60, 1.90, 0.40, "crate.wood")
			b.M.DecalTop(0.41, 0.22, 1.68, 0.58, 1.88, "paint.white", 0.05)
			cylX(b.M, 1.44, 1.90, 0.30, 0.10, 0.065, 8, "crate.wood")
			cylX(b.M, 1.46, 1.92, 0.42, 0.075, 0.05, 8, "trim.white")
			TreeAt(b.M, 1.82, 1.82, 0, b.R, "leaf.spring")
		},
	}
}

// atConventions 规范文书房（标准办公楼）：1x2 / H=5.0。
// 窄板楼 + 竖向线条 + 屋顶设备：竖直体量与密排楼层线是它的识别特征。
// 剪影签名：全城最窄的高板楼，像一枚竖立的书脊。
func atConventions() Def {
	return Def{
		Name: "at.conventions", Label: "规范文书房", Kind: KindBuilding, Category: "市政",
		Footprint: [2]int{1, 2}, Height: 5.0, Variants: 3, Cost: 560, Level: 1,
		Build: func(b *B) {
			b.Lot("pad.tile", "concrete.curb")
			wall, band := "wall.tile.white", "trim.band"
			if b.R.Chance(0.5) {
				wall, band = "wall.concrete", "trim.white"
			}
			b.M.Box(0.10, 0.10, 0, 0.90, 1.90, 4.30, wall)
			// 四层横向带窗（条文一样的节奏）
			b.Windows(0.36, 4.06, 4, 2, "window", band)
			// 竖向遮阳翼：把窄面切成 4 条竖线
			atFins(b.M, 0.10, 0.10, 0.90, 1.90, 0.24, 4.16, 0.34, band)
			// 入口：门 + 雨棚 + 门前台阶
			b.M.DecalY(1.90, 0.30, 0.70, 0.03, 1.10, "door.glass", 0.05)
			atSlab(b.M, 0.20, 1.62, 0.80, 2.02, 2.10, 0.12, "concrete.curb", band)
			b.Stairs(0.28, 2.02, 0.72, 2.26, 0, 0.10, 2, false, "concrete.pad")
			// 平屋顶 + 女儿墙
			b.M.Box(0.06, 0.06, 4.30, 0.94, 1.94, 4.44, "concrete.curb")
			b.M.SlabTop(4.44, 0.10, 0.10, 0.90, 1.90, "roof.felt")
			b.M.Parapet(0.10, 0.10, 0.90, 1.90, 4.44, 0.18, 0.06, band)
			// 屋顶：冷却机组 + 风管 + 楼梯间（最高点 5.00）
			b.M.Box(0.20, 0.30, 4.44, 0.74, 0.78, 4.74, "metal.dark")
			b.M.Box(0.24, 0.34, 4.74, 0.70, 0.74, 4.80, "metal.pipe")
			b.M.Box(0.20, 1.06, 4.44, 0.56, 1.62, 4.86, "wall.panel.teal")
			if b.R.Chance(0.5) {
				b.M.Box(0.62, 1.20, 4.44, 0.86, 1.70, 4.68, "metal.dark")
			}
			b.M.Box(0.26, 0.24, 4.86, 0.74, 0.86, 5.00, band)
			// 建筑铭牌
			b.M.Box(0.90, 1.30, 1.50, 0.96, 1.70, 1.86, "sign.panel")
			TreeAt(b.M, 0.20, 0.20, 0, b.R, "leaf.dark")
		},
	}
}

// ---------------------------------------------------------------- 西区知识 / 审计

// atAudit 审计塔（企业总部塔）：2x2 / H=9.0。
// 三段退台 + 竖向线条，顶部收成冠部，桅杆上挂一枚红色航空障碍灯色块。
// 剪影签名：全城第二高、周身竖线、顶部收分的方尖塔。
func atAudit() Def {
	return Def{
		Name: "at.audit", Label: "审计塔", Kind: KindBuilding, Category: "市政",
		Footprint: [2]int{2, 2}, Height: 9.0, Variants: 3, Cost: 1600, Level: 3,
		Build: func(b *B) {
			b.Lot("pad.concrete", "concrete.curb")
			body, band := "wall.concrete", "trim.band"
			if b.R.Chance(0.45) {
				body, band = "wall.tile.white", "trim.white"
			}
			// 塔基
			b.M.Box(0.06, 0.06, 0, 1.94, 1.94, 0.45, "concrete.curb")
			b.M.Box(0.14, 0.14, 0.45, 1.86, 1.86, 0.62, band)
			// 第 1 段
			b.M.Box(0.18, 0.18, 0.62, 1.82, 1.82, 3.00, body)
			b.M.Box(0.24, 0.24, 0.78, 1.76, 1.76, 2.86, "wall.glass.blue")
			atGlassBands(b.M, 0.24, 0.24, 1.76, 1.76, 0.78, 2.86, 3, band)
			atFins(b.M, 0.18, 0.18, 1.82, 1.82, 0.62, 2.98, 0.34, band)
			b.M.Box(0.14, 0.14, 3.00, 1.86, 1.86, 3.14, band)
			// 第 2 段
			b.M.Box(0.34, 0.34, 3.14, 1.66, 1.66, 5.40, body)
			b.M.Box(0.40, 0.40, 3.30, 1.60, 1.60, 5.28, "wall.glass.blue")
			atGlassBands(b.M, 0.40, 0.40, 1.60, 1.60, 3.30, 5.28, 3, band)
			atFins(b.M, 0.34, 0.34, 1.66, 1.66, 3.14, 5.38, 0.33, band)
			b.M.Box(0.30, 0.30, 5.40, 1.70, 1.70, 5.54, band)
			// 第 3 段
			b.M.Box(0.50, 0.50, 5.54, 1.50, 1.50, 7.30, body)
			b.M.Box(0.56, 0.56, 5.70, 1.44, 1.44, 7.18, "wall.glass.blue")
			atGlassBands(b.M, 0.56, 0.56, 1.44, 1.44, 5.70, 7.18, 3, band)
			atFins(b.M, 0.50, 0.50, 1.50, 1.50, 5.54, 7.28, 0.30, band)
			// 机电层 + 冠部
			b.M.Box(0.56, 0.56, 7.30, 1.44, 1.44, 7.90, "metal.dark")
			b.M.Box(0.50, 0.50, 7.90, 1.50, 1.50, 8.30, body)
			atFins(b.M, 0.50, 0.50, 1.50, 1.50, 7.90, 8.28, 0.25, band)
			b.M.Box(0.44, 0.44, 8.30, 1.56, 1.56, 8.48, band)
			// 桅杆 + 红色航空障碍灯色块（最高点 9.00）
			b.M.Box(0.94, 0.94, 8.48, 1.06, 1.06, 8.66, "metal.dark")
			atMastAt(b.M, 1.0, 1.0, 8.48, 9.00, "car.red")
			// 主入口：台阶 + 悬挑雨棚 + 柱列
			b.Stairs(0.74, 1.94, 1.26, 2.24, 0, 0.45, 4, false, "concrete.pad")
			atCanopy(b.M, 0.56, 1.86, 1.44, 2.20, 2.60, 0.14, "metal.pipe", "concrete.curb", band)
			b.M.DecalY(1.86, 0.86, 1.14, 0.62, 1.70, "door.glass", 0.06)
			TreeAt(b.M, 0.18, 1.82, 0, b.R, "leaf.olive")
			TreeAt(b.M, 1.82, 0.18, 0, b.R, "leaf.dark")
		},
	}
}

// atKnowledge 知识树馆（图书馆 / 知识中心）：3x2 / H=5.0。
// 两层大屋面 + 屋面绿化 + 中庭：中庭是玻璃盒子，里面一棵树从玻璃顶透出来
// —— 用现代手法保留「树」的意象，而不是把房子架在树干上。
// 剪影签名：横向大屋面 + 屋面绿块 + 西南角的玻璃中庭。
func atKnowledge() Def {
	return Def{
		Name: "at.knowledge", Label: "知识树馆", Kind: KindBuilding, Category: "市政",
		Footprint: [2]int{3, 2}, Height: 5.0, Variants: 3, Cost: 1200, Level: 2,
		Build: func(b *B) {
			b.Lot("pad.grass", "concrete.curb")
			wall, band := "wall.tile.white", "trim.band"
			if b.R.Chance(0.5) {
				wall, band = "wall.concrete", "trim.white"
			}
			leaf := "leaf.spring"
			switch b.R.Pick(3) {
			case 0:
				leaf = "leaf.olive"
			case 1:
				leaf = "leaf.dark"
			}
			// 南翼（两层，横向带窗）
			b.M.Box(0.10, 0.10, 0, 2.90, 1.20, 4.60, wall)
			b.Windows(0.50, 4.20, 4, 4, "window", band)
			// 北翼（一层 + 大片屋面）
			b.M.Box(0.10, 1.60, 0, 2.90, 1.90, 2.60, wall)
			b.Windows(0.50, 2.20, 2, 4, "window", band)
			// 西侧连接体（围合出内院）
			b.M.Box(0.10, 1.20, 0, 0.90, 1.60, 2.60, wall)
			// 内院：硬质铺装 + 汀步
			b.M.DecalTop(0.02, 0.96, 1.26, 2.84, 1.54, "pad.tile", 0.06)
			b.M.DecalTop(0.02, 1.30, 1.28, 1.70, 1.52, "pad.grass.dark", 0.05)
			// 女儿墙与女儿墙压顶
			for _, m := range []struct{ x0, y0, x1, y1, z float64 }{
				{0.10, 0.10, 2.90, 1.20, 4.60}, {0.10, 1.60, 2.90, 1.90, 2.60}, {0.10, 1.20, 0.90, 1.60, 2.60},
			} {
				b.M.SlabTop(m.z, m.x0, m.y0, m.x1, m.y1, "roof.gravel")
				b.M.Parapet(m.x0, m.y0, m.x1, m.y1, m.z, 0.16, 0.06, band)
			}
			// 屋面绿化（屋顶花园）：南翼三段绿块 + 北翼整条
			b.M.Box(0.24, 0.24, 4.76, 1.26, 1.06, 4.96, "hedge")
			b.M.Box(1.44, 0.24, 4.76, 2.76, 1.06, 4.92, "hedge")
			b.M.Box(0.24, 1.70, 2.76, 2.76, 1.86, 2.80, "hedge")
			// 北翼屋面的采光带（矩形天窗，一排两个）
			for _, x := range [2]float64{0.70, 1.90} {
				b.M.Box(x, 1.66, 2.76, x+0.60, 1.84, 2.98, "metal.pipe")
				b.M.Box(x+0.04, 1.68, 2.78, x+0.56, 1.82, 2.96, "wall.glass")
			}
			// 中庭：玻璃盒子 + 层间分格 + 玻璃顶（最高点 5.00）
			b.M.Box(0.60, 0.24, 0, 1.40, 1.06, 4.40, "wall.glass")
			atGlassBands(b.M, 0.60, 0.24, 1.40, 1.06, 0.0, 4.40, 4, band)
			b.M.Box(0.60, 0.24, 0.0, 1.40, 1.06, 0.18, band)
			b.M.Box(0.54, 0.18, 4.40, 1.46, 1.12, 4.50, "metal.pipe")
			b.M.Box(0.58, 0.22, 4.44, 1.42, 1.08, 4.50, "wall.glass")
			b.M.Box(0.54, 0.18, 4.50, 1.46, 1.12, 5.00, band)
			// 中庭的玻璃门（入口）
			b.M.DecalY(1.06, 0.80, 1.20, 0.02, 1.20, "door.glass", 0.06)
			// 中庭里的树：树干 + 三团树冠（都收在玻璃盒子内）
			b.M.Cylinder(1.0, 0.66, 0.075, 0, 2.90, 8, "trunk")
			b.M.Ellipsoid(1.0, 0.66, 3.25, 0.30, 0.30, 0.26, 3, 8, 0.14, 4242, leaf)
			b.M.Ellipsoid(0.86, 0.58, 3.05, 0.22, 0.22, 0.20, 3, 8, 0.16, 5151, leaf)
			b.M.Ellipsoid(1.12, 0.76, 3.12, 0.20, 0.20, 0.18, 3, 8, 0.16, 6161, leaf)
			// 主入口雨棚
			atCanopy(b.M, 1.60, 1.20, 2.50, 1.60, 2.90, 0.14, "metal.pipe", "concrete.curb", band)
			b.M.DecalY(1.20, 1.92, 2.18, 0.03, 1.10, "door.glass", 0.06)
			// 南翼屋面的设备
			b.M.Box(2.20, 0.24, 4.76, 2.72, 0.66, 4.98, "metal.dark")
			// 场地：长椅与树
			benchAt(b.M, 2.40, 1.70, 0, "crate.wood")
			TreeAt(b.M, 2.80, 1.82, 0, b.R, "leaf.spring")
			TreeAt(b.M, 0.20, 1.80, 0, b.R, "leaf.olive")
		},
	}
}

// atClosed 归档小馆（档案库）：2x2 / H=3.5。
// 无窗实体块 + 少量条形高窗 + 卸货平台。实体与留白是它的全部语言。
// 剪影签名：全城最钝的实心方块，侧面挂一条长卸货平台。
func atClosed() Def {
	return Def{
		Name: "at.closed", Label: "归档小馆", Kind: KindBuilding, Category: "市政",
		Footprint: [2]int{2, 2}, Height: 3.5, Variants: 3, Cost: 520, Level: 1,
		Build: func(b *B) {
			b.Lot("pad.concrete", "concrete.curb")
			wall, band := "wall.concrete", "trim.band"
			switch b.R.Pick(3) {
			case 0:
				wall, band = "wall.brick", "trim.dark"
			case 1:
				wall, band = "wall.tile.white", "trim.white"
			}
			b.M.Box(0.10, 0.10, 0, 1.60, 1.90, 3.34, wall)
			// 条形高窗：两排窄窗，竖向留白多（档案库的「少开窗」）
			b.WindowsOn(art.FacePosY, 0.60, 3.10, 2, 2, "window.small", band)
			b.WindowsOn(art.FacePosX, 0.60, 3.10, 2, 2, "window.small", band)
			b.WindowsOn(art.FaceNegX, 0.60, 3.10, 2, 2, "window.small", band)
			// 平屋顶 + 女儿墙 + 少量设备
			b.M.Box(0.06, 0.06, 3.34, 1.64, 1.94, 3.44, "concrete.curb")
			b.M.SlabTop(3.44, 0.10, 0.10, 1.60, 1.90, "roof.felt")
			b.M.Parapet(0.10, 0.10, 1.60, 1.90, 3.44, 0.06, 0.05, band)
			b.M.Box(1.16, 0.30, 3.44, 1.52, 0.74, 3.50, "metal.dark")
			// 侧向卸货平台：抬高 + 卷帘门 + 平台雨棚
			b.M.Box(1.60, 0.36, 0, 1.96, 1.64, 0.86, "concrete.curb")
			b.M.DecalX(1.64, 0.48, 0.90, 0.86, 1.72, "garage", 0.04)
			b.M.DecalX(1.64, 1.08, 1.50, 0.86, 1.72, "garage", 0.04)
			b.M.Box(1.96, 0.30, 2.22, 2.30, 1.70, 2.34, "metal.pipe")
			b.M.Box(1.94, 0.32, 2.34, 2.32, 1.68, 2.42, "concrete.curb")
			for _, y := range [2]float64{0.44, 1.52} {
				b.M.Cylinder(2.10, y, 0.035, 0, 2.22, 8, "metal.pipe")
			}
			// 人行门 + 铭牌
			b.M.DecalY(1.90, 0.42, 0.74, 0.02, 1.20, "door.metal", 0.05)
			b.M.Box(0.10, 0.24, 1.60, 0.16, 0.64, 1.86, "sign.panel")
			TreeAt(b.M, 0.20, 0.20, 0, b.R, "leaf.dark")
		},
	}
}

// atSplit 拆分工作台（联合办公）：2x2 / H=3.5。
// 两个模块化体量 + 中间一道玻璃连廊 + 屋顶露台；体量之间留出小院。
// 剪影签名：两段分开的方块被一条玻璃廊桥连起（负空间是两段之间的缺口）。
func atSplit() Def {
	return Def{
		Name: "at.split", Label: "拆分工作台", Kind: KindBuilding, Category: "工业",
		Footprint: [2]int{2, 2}, Height: 3.5, Variants: 3, Cost: 540, Level: 1,
		Build: func(b *B) {
			b.Lot("pad.concrete", "concrete.curb")
			wallA, wallB := "wall.concrete", "wall.tile.white"
			if b.R.Chance(0.45) {
				wallA, wallB = "wall.tile.white", "wall.brick"
			}
			band := "trim.band"
			if b.R.Chance(0.4) {
				band = "trim.white"
			}
			// 两个模块化体量
			mods := []struct {
				x0, x1 float64
				wall   string
			}{{0.06, 0.88, wallA}, {1.12, 1.94, wallB}}
			for _, m := range mods {
				b.M.Box(m.x0, 0.10, 0, m.x1, 1.90, 3.20, m.wall)
				b.Windows(0.50, 2.94, 3, 1, "window", band)
				b.M.Box(m.x0-0.04, 0.06, 3.20, m.x1+0.04, 1.94, 3.34, "concrete.curb")
				b.M.Parapet(m.x0, 0.10, m.x1, 1.90, 3.34, 0.14, 0.06, band)
			}
			// 屋顶露台（二选一栋有）：露台顶面与连廊顶齐平，全楼最高点 3.50
			if b.R.Chance(0.6) {
				atRoofTerrace(b.M, 0.30, 0.60, 0.88, 1.60, 3.34, 0.10, 0.015, "roof.gravel", "metal.pipe")
			} else {
				b.M.Box(0.30, 0.24, 3.34, 0.80, 0.70, 3.50, "metal.dark")
			}
			b.M.Box(1.24, 1.20, 3.34, 1.80, 1.70, 3.48, "wall.panel.teal")
			// 连廊：玻璃盒子横跨两栋之间（全楼最高点 3.50）
			b.M.Box(0.82, 1.30, 1.00, 1.18, 1.78, 2.86, "wall.glass")
			atGlassBands(b.M, 0.82, 1.30, 1.18, 1.78, 1.00, 2.86, 2, band)
			b.M.Box(0.84, 1.32, 2.86, 1.16, 1.76, 2.98, "wall.glass.blue")
			b.M.Box(0.78, 1.26, 2.98, 1.22, 1.82, 3.12, "concrete.curb")
			b.M.Box(0.80, 1.28, 3.12, 1.20, 1.80, 3.16, band)
			atMastAt(b.M, 1.0, 1.54, 3.16, 3.50, "car.yellow")
			// 小院：铺装 + 长椅 + 树
			b.M.DecalTop(0.02, 0.94, 0.20, 1.06, 1.86, "pad.tile", 0.06)
			benchAt(b.M, 1.0, 0.44, 0, "crate.wood")
			TreeAt(b.M, 0.20, 0.20, 0, b.R, "leaf.spring")
			TreeAt(b.M, 1.80, 1.80, 0, b.R, "leaf.olive")
			// 各自的入口门
			b.M.DecalY(1.90, 0.30, 0.62, 0.02, 1.06, "door.glass", 0.05)
			b.M.DecalY(1.90, 1.36, 1.68, 0.02, 1.06, "door.glass", 0.05)
		},
	}
}

// ---------------------------------------------------------------- 东区工业 / 开发

// atImplementing 开发工厂（科技园区厂房）：3x2 / H=5.0。
// 干净的缓坡锯齿采光顶 + 装卸口 + 屋脊设备管廊。锯齿只做 3 齿、坡度很缓，
// 因此不会读成老式厂房的尖锐锯齿。
// 剪影签名：一整排并列的斜面天窗 + 屋顶管架。
func atImplementing() Def {
	return Def{
		Name: "at.implementing", Label: "开发工厂", Kind: KindBuilding, Category: "工业",
		Footprint: [2]int{3, 2}, Height: 5.0, Variants: 3, Cost: 1000, Level: 2,
		Build: func(b *B) {
			b.Lot("pad.concrete", "concrete.curb")
			wall, panel := "wall.tile.white", "wall.metal"
			if b.R.Chance(0.45) {
				wall, panel = "wall.metal", "wall.panel.teal"
			}
			band := "trim.band"
			// 厂房主体
			b.M.Box(0.12, 0.12, 0, 2.88, 1.88, 3.00, wall)
			// 高侧窗带（连续玻璃带，不是一个个窗洞）
			b.M.DecalY(1.88, 0.24, 2.76, 2.10, 2.76, "window.hall", 0.04)
			b.M.DecalY(1.88, 0.24, 2.76, 2.76, 2.84, band, 0.06)
			b.M.DecalX(2.88, 0.24, 1.76, 2.10, 2.76, "window.hall", 0.04)
			b.M.DecalX(2.88, 0.24, 1.76, 2.76, 2.84, band, 0.06)
			// 锯齿采光顶：三齿、缓坡（3.20→4.20）+ 通长檐口与屋脊压条
			b.Sawtooth(3.20, 4.20, 3, "roof.metal", "wall.glass")
			b.M.Box(0.12, 0.12, 3.20, 2.88, 1.88, 3.30, band)
			b.M.Box(2.82, 0.12, 3.20, 2.92, 1.88, 4.20, "metal.pipe")
			// 端墙：把锯齿的两端收进山墙里（薄檐板在 1x 下会读成悬空的板）
			atShedEnd(b.M, 0.12, 0.16, 1.84, 3.20, 4.20, wall)
			atShedEnd(b.M, 2.88, 0.16, 1.84, 3.20, 4.20, wall)
			// 装卸口：抬高平台 + 卷帘门 + 雨棚
			b.M.Box(0.30, 1.88, 0, 1.10, 1.96, 0.44, "concrete.curb")
			b.M.Box(1.50, 1.88, 0, 2.40, 1.96, 0.44, "concrete.curb")
			b.M.DecalY(1.88, 0.38, 0.96, 0.44, 1.94, "garage", 0.06)
			b.M.DecalY(1.88, 1.58, 2.28, 0.44, 1.94, "garage", 0.06)
			b.M.Box(0.30, 1.88, 2.28, 2.40, 2.26, 2.42, "metal.pipe")
			b.M.Box(0.34, 1.90, 2.42, 2.36, 2.22, 2.50, band)
			for _, x := range [2]float64{0.40, 2.30} {
				b.M.Cylinder(x, 2.14, 0.035, 0, 2.28, 8, "metal.pipe")
			}
			// 人行门 + 铭牌
			b.M.DecalY(1.88, 2.58, 2.80, 0.02, 1.10, "door.glass", 0.05)
			b.M.Box(0.12, 0.30, 1.80, 0.18, 0.86, 2.10, "sign.panel")
			// 屋顶设备管廊：架在锯齿脊线之上的通长管架（最高点 5.00）
			for _, x := range [3]float64{0.34, 1.40, 2.46} {
				b.M.Box(x-0.03, 0.34, 3.30, x+0.03, 0.40, 4.58, "metal.pipe")
				b.M.Box(x-0.03, 1.60, 3.30, x+0.03, 1.66, 4.58, "metal.pipe")
				b.M.Box(x-0.06, 0.30, 4.58, x+0.06, 1.70, 4.66, "metal.pipe")
			}
			b.M.Box(0.28, 0.24, 4.66, 2.60, 0.42, 4.84, panel)
			b.M.Box(0.30, 0.26, 4.84, 2.58, 0.40, 5.00, "metal.dark")
			TreeAt(b.M, 2.80, 0.22, 0, b.R, "leaf.olive")
			TreeAt(b.M, 0.22, 1.80, 0, b.R, "leaf.dark")
		},
	}
}

// atReview 验收场（质检中心 + 露天测试场）：3x2 / H=4.4。
// 西侧质检大厅（横向带窗 + 采光顶带），东侧硬化测试场（标线 + 龙门架）。
// 剪影签名：一栋低矮的玻璃大厅 + 旁边一榀门式钢架。
func atReview() Def {
	return Def{
		Name: "at.review", Label: "验收场", Kind: KindBuilding, Category: "工业",
		Footprint: [2]int{3, 2}, Height: 4.4, Variants: 3, Cost: 940, Level: 2,
		Build: func(b *B) {
			b.Lot("pad.concrete", "concrete.curb")
			wall, band := "wall.tile.white", "trim.band"
			if b.R.Chance(0.45) {
				wall, band = "wall.concrete.warm", "trim.white"
			}
			// 测试场地坪（比场地高一级）+ 标线
			b.M.DecalTop(0.02, 1.36, 0.10, 2.92, 1.90, "pad.asphalt", 0.06)
			b.M.DecalTop(0.04, 1.40, 0.14, 2.88, 0.20, "paint.white", 0.06)
			b.M.DecalTop(0.04, 1.40, 1.80, 2.88, 1.86, "paint.white", 0.06)
			atLaneMarks(b.M, 1.46, 0.96, 2.80, 5, 0.045, "paint.white")
			b.M.DecalTop(0.05, 2.00, 0.60, 2.04, 1.40, "car.yellow", 0.06)
			// 质检大厅
			b.M.Box(0.12, 0.12, 0, 1.24, 1.88, 3.00, wall)
			b.Windows(0.50, 2.40, 2, 2, "window", band)
			b.M.DecalY(1.88, 0.24, 1.12, 2.52, 2.90, "wall.glass", 0.04)
			b.M.Box(0.08, 0.08, 3.00, 1.28, 1.92, 3.14, "concrete.curb")
			b.M.SlabTop(3.14, 0.12, 0.12, 1.24, 1.88, "roof.gravel")
			b.M.Parapet(0.12, 0.12, 1.24, 1.88, 3.14, 0.18, 0.06, band)
			b.M.Box(0.24, 1.30, 3.14, 0.62, 1.72, 3.36, "metal.dark")
			// 楼梯间出屋面（最高点 4.00）
			b.M.Box(0.66, 0.24, 3.14, 1.16, 0.82, 3.86, wall)
			b.M.Box(0.62, 0.20, 3.86, 1.20, 0.86, 4.00, band)
			// 入口：门 + 雨棚 + 台阶
			b.M.DecalY(1.88, 0.58, 0.82, 0.03, 1.14, "door.glass", 0.06)
			atSlab(b.M, 0.46, 1.88, 0.94, 2.22, 2.00, 0.12, "concrete.curb", band)
			// 龙门架（门式钢架）：双腿 + 横梁 + 小车吊钩，最高点 4.40
			gy := 1.00
			for _, lx := range [2]float64{1.58, 2.72} {
				b.M.Box(lx-0.07, gy-0.08, 0.06, lx+0.07, gy+0.08, 4.20, "metal.pipe")
				b.M.Box(lx-0.22, gy-0.16, 0.06, lx+0.22, gy+0.16, 0.16, "metal.dark")
				b.M.Box(lx-0.28, gy-0.22, 0.06, lx-0.20, gy+0.22, 0.22, "concrete.curb")
				b.M.Box(lx+0.20, gy-0.22, 0.06, lx+0.28, gy+0.22, 0.22, "concrete.curb")
			}
			b.M.Box(1.50, gy-0.10, 4.20, 2.80, gy+0.10, 4.40, "metal.pipe")
			b.M.Box(1.50, gy-0.13, 4.06, 2.80, gy+0.13, 4.20, "metal.dark")
			b.M.Box(2.02, gy-0.09, 3.86, 2.32, gy+0.09, 4.06, "sign.panel")
			b.M.Box(2.16, gy-0.018, 3.30, 2.19, gy+0.018, 3.86, "metal.pipe")
			b.M.Box(2.04, gy-0.10, 3.40, 2.30, gy+0.10, 3.52, "crate.wood")
			// 检验料箱与标定座
			b.M.Box(1.46, 1.46, 0.06, 1.72, 1.74, 0.40, "sign.panel.dark")
			b.M.Box(1.50, 1.50, 0.40, 1.68, 1.70, 0.48, "chrome")
			if b.R.Chance(0.55) {
				b.M.Box(2.56, 0.22, 0.06, 2.86, 0.54, 0.34, "crate.wood")
			}
			TreeAt(b.M, 0.24, 1.78, 0, b.R, "leaf.spring")
		},
	}
}

// atMerge 合并发布站（数据中心）：2x2 / H=6.5。
// 无窗机房体量 + 屋面冷却机组 + 天线桅杆与抛物面天线 + 备用发电机组。
// 剪影签名：一根高桅杆 + 一口锅，加上地面成排的机组。
func atMerge() Def {
	return Def{
		Name: "at.merge", Label: "合并发布站", Kind: KindBuilding, Category: "工业",
		Footprint: [2]int{2, 2}, Height: 6.5, Variants: 3, Cost: 1400, Level: 3,
		Build: func(b *B) {
			b.Lot("pad.concrete", "concrete.curb")
			wall, band := "wall.metal", "trim.band"
			if b.R.Chance(0.4) {
				wall, band = "wall.concrete", "trim.white"
			}
			// 主机房：实体块，只开两条水平窄窗
			b.M.Box(0.12, 0.12, 0, 1.88, 1.30, 4.30, wall)
			b.M.Box(0.10, 0.10, 4.30, 1.90, 1.32, 4.44, "concrete.curb")
			b.M.Box(0.06, 0.06, 4.44, 1.94, 1.36, 4.52, band)
			b.WindowsOn(art.FacePosY, 0.80, 1.50, 1, 3, "window.small", band)
			b.WindowsOn(art.FacePosX, 0.80, 1.50, 1, 3, "window.small", band)
			b.M.DecalY(1.30, 0.28, 1.72, 3.30, 3.90, "window.hall", 0.04)
			b.M.DecalX(1.88, 0.28, 1.14, 3.30, 3.90, "window.hall", 0.04)
			// 安防入口（小体量门斗）
			b.M.Box(0.60, 1.30, 0, 1.40, 1.86, 2.90, wall)
			b.M.DecalY(1.86, 0.78, 1.22, 0.03, 1.20, "door.glass", 0.06)
			b.M.Box(0.54, 1.26, 2.90, 1.46, 1.90, 3.02, "concrete.curb")
			b.M.Box(0.50, 1.22, 3.02, 1.50, 1.94, 3.10, band)
			// 屋面冷却机组（两台成排）
			for _, x := range [2]float64{0.24, 1.02} {
				b.M.Box(x, 0.24, 4.52, x+0.62, 0.72, 4.82, "metal.dark")
				b.M.Cylinder(x+0.18, 0.48, 0.11, 4.82, 4.92, 8, "metal.pipe")
				b.M.Cylinder(x+0.46, 0.48, 0.11, 4.82, 4.92, 8, "metal.pipe")
			}
			// 桅杆与抛物面天线（最高点 6.50）
			atMastAt(b.M, 1.00, 1.00, 4.52, 6.50, "car.red")
			atDish(b.M, 1.56, 1.04, 5.10, 0.22, "wall.glass.blue")
			b.M.Box(1.62, 1.00, 4.52, 1.74, 1.28, 5.10, "metal.pipe")
			// 备用发电机组 + 油罐（场地南侧）
			b.M.Box(0.20, 1.48, 0, 1.10, 1.82, 0.66, "metal.dark")
			b.M.Box(0.24, 1.52, 0.66, 1.06, 1.78, 0.74, "metal.pipe")
			b.M.Cylinder(1.50, 1.66, 0.16, 0, 0.70, 10, "wall.metal")
			b.M.Cylinder(1.50, 1.66, 0.05, 0.70, 0.80, 8, "metal.pipe")
			TreeAt(b.M, 1.82, 1.80, 0, b.R, "leaf.dark")
			TreeAt(b.M, 0.20, 0.20, 0, b.R, "leaf.olive")
		},
	}
}

// atConflict 仲裁庭（仲裁中心）：2x2 / H=5.0。
// 石材基座 + 柱列 + 玻璃上部：庄重但不浮夸。前场一道浅水镜面。
// 剪影签名：底实上虚的三段式，柱列投下密集的竖向阴影。
func atConflict() Def {
	return Def{
		Name: "at.conflict", Label: "仲裁庭", Kind: KindBuilding, Category: "市政",
		Footprint: [2]int{2, 2}, Height: 5.0, Variants: 3, Cost: 1100, Level: 2,
		Build: func(b *B) {
			b.Lot("pad.tile", "concrete.curb")
			stone, band := "wall.concrete.warm", "trim.band"
			if b.R.Chance(0.45) {
				stone, band = "wall.stone", "trim.white"
			}
			// 石材基座
			b.M.Box(0.10, 0.10, 0, 1.90, 1.90, 1.60, stone)
			// 玻璃中段 + 竖向金属分格（全楼唯一的通透层）
			b.M.Box(0.16, 0.16, 1.60, 1.84, 1.84, 3.60, "wall.glass")
			atGlassBands(b.M, 0.16, 0.16, 1.84, 1.84, 1.60, 3.60, 3, band)
			atFins(b.M, 0.16, 0.16, 1.84, 1.84, 1.66, 3.54, 0.42, band)
			b.M.Box(0.10, 0.10, 3.60, 1.90, 1.90, 3.74, "concrete.curb")
			// 石材上部 + 女儿墙
			b.M.Box(0.14, 0.14, 3.74, 1.86, 1.86, 4.80, stone)
			b.M.Box(0.08, 0.08, 4.80, 1.92, 1.92, 4.96, band)
			// 中间入口：门 + 大挑檐 + 六柱柱廊
			b.M.DecalY(1.90, 0.82, 1.18, 0.03, 1.30, "door.glass", 0.06)
			atCanopy(b.M, 0.34, 1.90, 1.66, 2.30, 3.70, 0.16, "metal.pipe", "concrete.curb", band)
			b.M.Box(0.34, 2.16, 3.86, 1.66, 2.30, 4.10, stone)
			// 前场浅水镜面
			b.M.DecalTop(0.02, 0.44, 2.36, 1.56, 2.86, "water.shallow", 0.06)
			b.M.Parapet(0.40, 2.32, 1.60, 2.90, -0.012, 0.10, 0.06, "concrete.curb")
			// 屋顶设备 + 旗杆
			b.M.Box(1.24, 0.30, 4.96, 1.72, 0.74, 5.00, "metal.dark")
			b.M.DecalX(1.92, 0.86, 1.14, 1.10, 2.30, "sign.panel", 0.05)
			TreeAt(b.M, 0.22, 0.22, 0, b.R, "leaf.olive")
			TreeAt(b.M, 1.78, 0.22, 0, b.R, "leaf.dark")
		},
	}
}

// ---------------------------------------------------------------- 中央广场

// atPM 统筹议事厅（市政厅）：3x2 / H=5.0。
// 宽体量 + 中央玻璃中庭（唯一允许的圆曲面：中庭采光塔用 Cylinder 收口）
// + 前广场台阶与列柱雨棚 + 三根旗杆。
// 剪影签名：横幅的板楼中央鼓起一段玻璃采光塔 —— 全城最横的体量。
func atPM() Def {
	return Def{
		Name: "at.pm", Label: "统筹议事厅", Kind: KindBuilding, Category: "市政",
		Footprint: [2]int{3, 2}, Height: 5.0, Variants: 3, Cost: 1800, Level: 3,
		Build: func(b *B) {
			b.Lot("pad.tile", "concrete.curb")
			wall, band := "wall.tile.white", "trim.band"
			if b.R.Chance(0.45) {
				wall, band = "wall.concrete", "trim.white"
			}
			glass := "wall.glass.blue"
			if b.R.Chance(0.5) {
				glass = "wall.glass"
			}
			// 前广场：三段浅台阶
			b.M.Box(0.16, 1.90, 0, 2.84, 2.90, 0.10, "concrete.pad")
			b.M.Box(0.16, 2.90, 0, 2.84, 3.24, 0.05, "concrete.pad")
			b.M.DecalTop(0.11, 0.30, 2.00, 2.70, 2.80, "pad.tile", 0.06)
			// 主体：3x2 宽的板式办公体量
			b.M.Box(0.10, 0.10, 0, 2.90, 1.90, 4.60, wall)
			b.M.DecalY(1.90, 0.24, 2.76, 0.10, 0.92, "window.hall", 0.04)
			b.Windows(1.10, 4.30, 4, 5, "window", band)
			// 中央中庭：整片玻璃幕墙（前后贯通，正立面完整露出 0.90 宽）
			b.M.Box(1.05, 0.80, 0, 1.95, 1.90, 4.86, glass)
			atGlassBands(b.M, 1.05, 0.80, 1.95, 1.90, 0.0, 4.86, 4, band)
			b.M.DecalY(1.90, 1.28, 1.72, 0.10, 1.40, "door.glass", 0.07)
			b.M.Box(1.01, 0.76, 4.86, 1.99, 1.94, 4.94, "concrete.curb")
			// 中庭采光塔（全城唯一的圆曲面构件）：圆柱 + 玻璃环 + 锥顶
			b.M.Cylinder(1.50, 1.35, 0.30, 4.86, 4.96, 12, glass)
			b.M.Cylinder(1.50, 1.35, 0.34, 4.90, 4.98, 12, band)
			// 屋顶露台 + 女儿墙 + 设备
			b.M.Box(0.06, 0.06, 4.60, 2.94, 1.94, 4.72, "concrete.curb")
			b.M.SlabTop(4.72, 0.10, 0.10, 2.90, 1.90, "roof.gravel")
			b.M.Parapet(0.10, 0.10, 1.02, 1.90, 4.72, 0.26, 0.06, band)
			b.M.Parapet(1.98, 0.10, 2.90, 1.90, 4.72, 0.26, 0.06, band)
			b.M.Box(0.24, 0.30, 4.72, 0.78, 0.76, 4.98, wall)
			b.M.Box(0.20, 0.26, 4.98, 0.86, 0.80, 5.00, band)
			b.M.Box(2.20, 1.24, 4.72, 2.76, 1.72, 4.90, "metal.dark")
			// 主入口：柱廊 + 悬挑雨棚
			atCanopy(b.M, 0.94, 1.90, 2.06, 2.40, 3.10, 0.16, "metal.pipe", "concrete.curb", band)
			b.M.Box(1.92, 2.20, 3.26, 2.20, 2.44, 3.44, wall)
			// 广场旗杆阵（三根）
			for i, fx := range [3]float64{0.40, 0.62, 0.84} {
				b.M.Cylinder(fx, 2.54, 0.03, 0, 4.30, 6, "metal.pipe")
				flagMat := []string{"awning.red", "car.yellow", "car.blue"}[i]
				atFlag(b.M, fx+0.02, 2.54, 4.26, 0.34, 0.24, flagMat)
			}
			TreeAt(b.M, 2.74, 2.74, 0, b.R, "leaf.spring")
			TreeAt(b.M, 0.22, 0.22, 0, b.R, "leaf.olive")
		},
	}
}

// atBlocked 阻塞等待亭（服务亭）：1x1 / H=2.0。
// 玻璃岗亭 + 道闸横杆 + 顶部指示灯。红白横杆与红灯就是「停止」的语义。
// 剪影签名：全城最小的体量，顶上一颗红灯，横杆挑出亭外。
func atBlocked() Def {
	return Def{
		Name: "at.blocked", Label: "阻塞等待亭", Kind: KindBuilding, Category: "市政",
		Footprint: [2]int{1, 1}, Height: 2.0, Variants: 3, Cost: 300, Level: 1,
		Build: func(b *B) {
			b.Lot("pad.concrete", "concrete.curb")
			wall, band := "wall.tile.white", "trim.band"
			if b.R.Chance(0.45) {
				wall, band = "wall.metal", "trim.dark"
			}
			// 亭身：实体角柱 + 两面玻璃
			b.M.Box(0.16, 0.16, 0, 0.80, 0.80, 0.20, "concrete.curb")
			b.M.Box(0.16, 0.16, 0.20, 0.30, 0.80, 1.50, wall)
			b.M.Box(0.66, 0.16, 0.20, 0.80, 0.80, 1.50, wall)
			b.M.Box(0.30, 0.16, 0.20, 0.66, 0.80, 0.34, wall)
			b.M.Box(0.30, 0.16, 0.34, 0.66, 0.80, 1.50, "wall.glass")
			b.M.Box(0.30, 0.16, 1.14, 0.66, 0.80, 1.22, band)
			// 平屋面板 + 挑檐
			b.M.Box(0.10, 0.10, 1.50, 0.86, 0.86, 1.64, "concrete.curb")
			b.M.Box(0.06, 0.06, 1.64, 0.90, 0.90, 1.72, band)
			// 顶部指示灯（最高点 2.00）：短柱 + 红块
			b.M.Cylinder(0.48, 0.48, 0.045, 1.72, 1.86, 8, "metal.pipe")
			b.M.Box(0.40, 0.40, 1.86, 0.56, 0.56, 1.96, "car.red")
			b.M.Box(0.38, 0.38, 1.96, 0.58, 0.58, 2.00, "car.red")
			// 道闸：立柱 + 红白横杆
			b.M.Box(0.06, 0.60, 0.20, 0.14, 0.68, 1.10, "metal.pipe")
			b.M.Box(0.86, 0.60, 0.20, 0.94, 0.68, 1.10, "metal.pipe")
			b.M.Box(0.10, 0.61, 0.98, 0.90, 0.67, 1.08, band)
			for i := 0; i < 4; i++ {
				x0 := 0.12 + float64(i)*0.20
				mt := "car.red"
				if i%2 == 1 {
					mt = "trim.white"
				}
				b.M.Box(x0, 0.60, 0.80, x0+0.19, 0.68, 0.96, mt)
			}
			// 侧面的服务标牌
			b.M.Box(0.16, 0.14, 1.00, 0.22, 0.36, 1.30, "sign.panel")
			if b.R.Chance(0.55) {
				b.M.Cylinder(0.82, 0.86, 0.05, 0, 0.90, 8, "metal.pipe")
				b.M.Box(0.70, 0.78, 0.90, 0.94, 0.94, 1.10, "car.yellow")
			}
		},
	}
}

// atNeedsGrilling 访谈茶馆（咖啡馆）：2x2 / H=3.0。
// 玻璃盒子 + 木格栅（竖向遮阳）+ 室外露台座椅。玻璃与格栅是全楼的全部语言。
// 剪影签名：低矮的透亮盒子，前场一排小圆桌。
func atNeedsGrilling() Def {
	return Def{
		Name: "at.needs-grilling", Label: "访谈茶馆", Kind: KindBuilding, Category: "商业",
		Footprint: [2]int{2, 2}, Height: 3.0, Variants: 3, Cost: 420, Level: 1,
		Build: func(b *B) {
			b.Lot("pad.tile", "concrete.curb")
			// 木栈露台（南侧 0.35 深）
			b.M.Box(0.10, 1.62, -0.012, 1.90, 1.94, 0.06, "crate.wood")
			b.M.DecalTop(0.07, 0.14, 1.66, 1.86, 1.90, "pad.tile", 0.05)
			// 玻璃盒主体
			b.M.Box(0.10, 0.10, 0, 1.90, 1.60, 2.60, "wall.glass")
			atGlassBands(b.M, 0.10, 0.10, 1.90, 1.60, 0.0, 2.60, 2, "trim.band")
			b.M.Box(0.10, 0.10, 0.0, 1.90, 1.60, 0.16, "concrete.curb")
			// 木格栅：只做东立面与正立面两端（中间留出整片玻璃，1x 下才读成「咖啡馆」）
			for y := 0.20; y < 1.60; y += 0.18 {
				b.M.Box(1.88, y-0.03, 0.18, 1.96, y+0.03, 2.58, "crate.wood")
			}
			for _, x := range [4]float64{0.20, 0.40, 1.60, 1.80} {
				b.M.Box(x-0.03, 1.58, 0.18, x+0.03, 1.66, 2.58, "crate.wood")
			}
			// 平屋面板 + 女儿墙 + 空调机组
			b.M.Box(0.06, 0.06, 2.60, 1.94, 1.64, 2.74, "concrete.curb")
			b.M.SlabTop(2.74, 0.10, 0.10, 1.90, 1.60, "roof.gravel")
			b.M.Parapet(0.10, 0.10, 1.90, 1.60, 2.74, 0.16, 0.06, "trim.band")
			b.M.Box(0.28, 0.28, 2.74, 0.74, 0.72, 2.96, "metal.dark")
			// 入口门 + 雨棚
			b.M.DecalY(1.60, 0.80, 1.20, 0.02, 1.24, "door.glass", 0.07)
			atSlab(b.M, 0.68, 1.60, 1.32, 2.00, 2.30, 0.12, "concrete.curb", "trim.band")
			b.M.Cylinder(0.76, 1.94, 0.04, 0, 2.30, 8, "metal.pipe")
			b.M.Cylinder(1.24, 1.94, 0.04, 0, 2.30, 8, "metal.pipe")
			// 露台：两张圆桌 + 四只凳子
			for i, tx := range [2]float64{0.42, 1.42} {
				b.M.Cylinder(tx, 1.78, 0.15, 0.06, 0.44, 10, "trim.white")
				b.M.Cylinder(tx, 1.78, 0.055, 0.44, 0.52, 8, "trim.cream")
				b.M.Cylinder(tx-0.22, 1.72+float64(i)*0.05, 0.075, 0.06, 0.26, 8, "metal.pipe")
				b.M.Cylinder(tx+0.22, 1.70+float64(i)*0.05, 0.075, 0.06, 0.26, 8, "metal.pipe")
			}
			TreeAt(b.M, 0.20, 0.20, 0, b.R, "leaf.spring")
			TreeAt(b.M, 1.82, 0.22, 0, b.R, "leaf.olive")
		},
	}
}

// atDone 凯旋广场（庆典广场）：3x2 / H=4.4。
// 几乎全平：铺装广场 + 一座抽象的纪念构筑（柱 + 顶板 + 白横幅）+ 旗阵。
// 剪影签名：一片低平的铺地上立起一根细高的纪念柱与一排旗。
func atDone() Def {
	return Def{
		Name: "at.done", Label: "凯旋广场", Kind: KindBuilding, Category: "公园",
		Footprint: [2]int{3, 2}, Height: 4.4, Variants: 3, Cost: 1300, Level: 2,
		Build: func(b *B) {
			b.Lot("pad.tile", "concrete.curb")
			// 铺装分区（三种灰）＋ 中轴
			b.M.DecalTop(0.02, 0.16, 0.16, 1.62, 1.84, "pad.concrete", 0.06)
			b.M.DecalTop(0.02, 1.72, 0.16, 2.84, 1.84, "pad.gravel", 0.06)
			b.M.DecalTop(0.04, 1.60, 0.16, 1.74, 1.84, "paint.white", 0.07)
			b.M.DecalTop(0.04, 0.16, 0.96, 2.84, 1.04, "paint.white", 0.07)
			// 纪念构筑：方形基座 + 细柱 + 悬挑顶板 + 白色横幅
			b.M.Box(0.92, 0.62, 0, 1.28, 1.02, 0.30, "concrete.curb")
			b.M.Box(0.98, 0.68, 0.30, 1.22, 0.96, 0.44, "trim.band")
			b.M.Box(1.06, 0.76, 0.44, 1.14, 0.88, 3.90, "wall.metal")
			b.M.Box(0.72, 0.56, 3.90, 1.48, 1.08, 4.06, "concrete.curb")
			b.M.Box(0.68, 0.52, 4.06, 1.52, 1.12, 4.18, "trim.band")
			b.M.DecalX(1.14, 0.80, 0.86, 1.30, 3.60, "paint.white", 0.06)
			b.M.DecalY(1.10, 1.06, 1.14, 1.30, 3.60, "paint.white", 0.06)
			// 顶部：小旗（最高点 4.40）
			b.M.Cylinder(1.10, 0.82, 0.022, 4.18, 4.40, 6, "metal.pipe")
			atFlag(b.M, 1.12, 0.82, 4.38, 0.30, 0.22, "awning.red")
			// 旗阵：五根旗杆排在轴线两侧
			for i := 0; i < 5; i++ {
				px := 0.36 + float64(i)*0.57
				if i == 2 {
					continue // 中轴留给纪念柱
				}
				b.M.Cylinder(px, 1.36, 0.025, 0, 3.30, 6, "metal.pipe")
				flagMat := []string{"car.yellow", "car.blue", "car.teal", "car.red"}[i%4]
				atFlag(b.M, px+0.02, 1.36, 3.28, 0.28, 0.20, flagMat)
			}
			// 低水池 + 长椅 + 树
			b.M.DecalTop(0.02, 2.10, 0.28, 2.76, 0.76, "water.shallow", 0.06)
			b.M.Parapet(2.06, 0.24, 2.80, 0.80, -0.012, 0.12, 0.06, "concrete.curb")
			benchAt(b.M, 0.52, 1.62, 0, "crate.wood")
			benchAt(b.M, 2.20, 1.56, 1, "crate.wood")
			TreeAt(b.M, 0.22, 0.22, 0, b.R, "leaf.spring")
			TreeAt(b.M, 2.78, 1.78, 0, b.R, "leaf.olive")
		},
	}
}

// atReady 待命驿站（交通枢纽）：2x2 / H=3.5。
// 站台雨棚 + 候车厅 + 公交湾：薄板雨棚 + 细柱 + 路缘标线。
// 剪影签名：一整片悬挑的薄屋面横在细柱上，屋面下是通透的候车空间。
func atReady() Def {
	return Def{
		Name: "at.ready", Label: "待命驿站", Kind: KindBuilding, Category: "市政",
		Footprint: [2]int{2, 2}, Height: 3.5, Variants: 3, Cost: 620, Level: 1,
		Build: func(b *B) {
			b.Lot("pad.concrete", "concrete.curb")
			band, wall := "trim.band", "wall.tile.white"
			if b.R.Chance(0.45) {
				band, wall = "trim.white", "wall.concrete"
			}
			// 公交湾：车道铺装 + 路缘 + 斑马线
			b.M.DecalTop(0.02, 0.14, 1.62, 1.86, 1.94, "pad.asphalt", 0.06)
			b.M.DecalTop(0.03, 0.14, 1.58, 1.86, 1.64, "paint.white", 0.07)
			atLaneMarks(b.M, 0.24, 1.74, 1.76, 6, 0.04, "paint.white")
			// 候车厅：背面实体 + 柱
			b.M.Box(0.16, 0.24, 0, 1.84, 0.62, 2.20, wall)
			b.M.DecalY(0.62, 0.26, 1.74, 0.24, 1.40, "window.hall", 0.04)
			b.M.DecalX(1.84, 0.70, 0.86, 0.14, 1.10, "door.glass", 0.05)
			// 候车厅上的站务间（屋面设备体）
			b.M.Box(0.90, 0.26, 2.20, 1.60, 0.58, 2.90, wall)
			b.M.Box(0.86, 0.22, 2.90, 1.64, 0.62, 3.02, band)
			// 站台雨棚：薄板 + 细柱（最高点 3.50）
			atSlab(b.M, 0.08, 0.20, 1.92, 1.42, 3.16, 0.12, "concrete.curb", band)
			for _, cx := range [3]float64{0.26, 1.00, 1.74} {
				b.M.Cylinder(cx, 1.30, 0.04, 0, 3.16, 8, "metal.pipe")
			}
			b.M.Box(0.30, 0.60, 3.28, 1.70, 1.28, 3.50, "wall.panel.teal")
			b.M.Box(0.30, 0.64, 3.36, 1.70, 1.24, 3.44, "wall.glass")
			// 站台设施：时刻牌 + 长椅 + 垃圾桶
			b.M.Box(0.18, 0.68, 0.20, 0.24, 1.02, 1.90, "sign.panel")
			b.M.DecalX(0.25, 0.72, 0.98, 1.30, 1.80, "paint.white", 0.06)
			benchAt(b.M, 0.70, 0.86, 0, "crate.wood")
			b.M.Cylinder(1.66, 0.80, 0.08, 0, 0.42, 8, "metal.dark")
			TreeAt(b.M, 0.22, 0.22, 0, b.R, "leaf.spring")
			TreeAt(b.M, 1.80, 1.82, 0, b.R, "leaf.olive")
		},
	}
}

// atWorking 综合工位（综合办公楼）：2x2 / H=5.5。
// 中庭（底层通高幕墙 + 门上雨棚）+ 标准层带窗 + 屋顶设备与楼梯间。
// 剪影签名：四平八稳的标准办公楼，正面中央一道通高玻璃中庭。
func atWorking() Def {
	return Def{
		Name: "at.working", Label: "综合工位", Kind: KindBuilding, Category: "工业",
		Footprint: [2]int{2, 2}, Height: 5.5, Variants: 3, Cost: 900, Level: 2,
		Build: func(b *B) {
			b.Lot("pad.concrete", "concrete.curb")
			wall, band := "wall.panel.teal", "trim.band"
			switch b.R.Pick(3) {
			case 0:
				wall, band = "wall.concrete", "trim.white"
			case 1:
				wall, band = "wall.tile.white", "trim.band"
			}
			// 标准层体量
			b.M.Box(0.12, 0.12, 0, 1.88, 1.88, 4.30, wall)
			b.Windows(0.60, 4.00, 4, 3, "window", band)
			// 中央通高玻璃中庭
			b.M.Box(0.86, 1.72, 0, 1.14, 1.88, 4.40, "wall.glass")
			atGlassBands(b.M, 0.86, 1.72, 1.14, 1.88, 0.0, 4.40, 4, band)
			b.M.DecalY(1.88, 0.90, 1.10, 0.03, 1.40, "door.glass", 0.08)
			// 底层柱廊（一遍柱列，雨天连廊）
			atCanopy(b.M, 0.24, 1.88, 1.76, 2.22, 3.00, 0.14, "metal.pipe", "concrete.curb", band)
			// 平屋顶 + 女儿墙 + 露台栏杆
			b.M.Box(0.06, 0.06, 4.30, 1.94, 1.94, 4.44, "concrete.curb")
			atRoofTerrace(b.M, 0.12, 0.12, 1.88, 1.88, 4.44, 0.20, 0.26, band, "metal.pipe")
			// 屋顶设备 + 楼梯间（最高点 5.50）
			b.M.Box(0.28, 1.20, 4.64, 0.86, 1.66, 4.88, "metal.dark")
			b.M.Cylinder(0.40, 1.40, 0.10, 4.88, 4.98, 8, "metal.pipe")
			b.M.Cylinder(0.72, 1.40, 0.10, 4.88, 4.98, 8, "metal.pipe")
			b.M.Box(1.26, 0.26, 4.44, 1.84, 0.86, 5.30, wall)
			b.M.Box(1.22, 0.22, 5.30, 1.88, 0.90, 5.44, band)
			b.M.Box(1.30, 0.30, 5.44, 1.80, 0.82, 5.50, "metal.dark")
			// 场地：遮阳棚下的自行车位与花钵
			b.M.Box(0.20, 1.92, 0, 1.10, 2.00, 0.06, "concrete.curb")
			for _, bx := range [3]float64{0.36, 0.66, 0.96} {
				b.M.Cylinder(bx, 1.98, 0.05, 0, 0.24, 8, "metal.pipe")
				b.M.Box(bx-0.09, 1.94, 0.24, bx+0.09, 2.02, 0.30, "metal.dark")
			}
			b.M.Cylinder(1.72, 1.98, 0.12, 0, 0.34, 8, "concrete.curb")
			b.M.Cylinder(1.72, 1.98, 0.10, 0.34, 0.52, 8, "hedge")
			TreeAt(b.M, 0.20, 0.20, 0, b.R, "leaf.spring")
			TreeAt(b.M, 1.80, 0.20, 0, b.R, "leaf.dark")
		},
	}
}

// atIdle 休息草坪（现代公园亭）：2x2 / H=2.6。
// 薄板屋面 + 细柱 + 草坪 + 长椅：屋面必须「薄」，才读得出是当代亭子。
// 剪影签名：一块悬在空中、几乎无厚度的白板。
func atIdle() Def {
	return Def{
		Name: "at.idle", Label: "休息草坪", Kind: KindBuilding, Category: "公园",
		Footprint: [2]int{2, 2}, Height: 2.6, Variants: 3, Cost: 460, Level: 1,
		Build: func(b *B) {
			b.Lot("pad.grass", "concrete.curb")
			band := "trim.band"
			if b.R.Chance(0.45) {
				band = "trim.white"
			}
			// 亭下硬质台面
			b.M.DecalTop(0.02, 0.42, 0.42, 1.58, 1.58, "pad.tile", 0.06)
			// 六根细柱（四角 + 两中）
			for _, p := range [6][2]float64{
				{0.44, 0.44}, {1.56, 0.44}, {0.44, 1.56}, {1.56, 1.56}, {1.00, 0.44}, {1.00, 1.56},
			} {
				b.M.Cylinder(p[0], p[1], 0.038, 0.02, 2.20, 8, "metal.pipe")
			}
			// 薄板屋面（最高点 2.60）
			b.M.Box(0.26, 0.26, 2.20, 1.74, 1.74, 2.32, "concrete.curb")
			b.M.Box(0.28, 0.28, 2.32, 1.72, 1.72, 2.42, band)
			// 屋面板上的矩形天窗
			b.M.Box(0.62, 0.62, 2.42, 1.38, 0.86, 2.58, "wall.glass")
			b.M.Box(0.58, 0.58, 2.42, 1.42, 0.90, 2.60, "metal.pipe")
			// 长椅 + 花钵 + 草坪
			benchAt(b.M, 1.36, 0.68, 0, "crate.wood")
			benchAt(b.M, 0.68, 1.36, 1, "crate.wood")
			b.M.Cylinder(1.78, 1.78, 0.16, 0, 0.30, 10, "concrete.curb")
			b.M.Cylinder(1.78, 1.78, 0.13, 0.30, 0.44, 8, "hedge")
			b.M.DecalTop(0.02, 0.14, 1.72, 0.34, 1.86, "pad.gravel", 0.05)
			TreeAt(b.M, 0.22, 0.22, 0, b.R, "leaf.spring")
			TreeAt(b.M, 1.80, 0.22, 0, b.R, "leaf.olive")
			TreeAt(b.M, 0.24, 1.80, 0, b.R, "leaf.spring")
		},
	}
}

// ---------------------------------------------------------------- 北区地标

// atPriority 优先级塔（地标塔）：2x2 / H=10.0。
// 全城最高：三段退台 + 顶部桅杆 + 红色航空障碍灯色块。
// 剪影签名：三节收分的方塔，顶上细杆与一枚红点 —— 整座小镇的竖直基准。
func atPriority() Def {
	return Def{
		Name: "at.priority", Label: "优先级塔", Kind: KindBuilding, Category: "市政",
		Footprint: [2]int{2, 2}, Height: 10.0, Variants: 3, Cost: 2200, Level: 3,
		Build: func(b *B) {
			b.Lot("pad.tile", "concrete.curb")
			body, band := "wall.tile.white", "trim.band"
			if b.R.Chance(0.45) {
				body, band = "wall.concrete", "trim.white"
			}
			accent := "wall.panel.teal"
			if b.R.Chance(0.5) {
				accent = "wall.brick"
			}
			// 台基 + 前广场台阶
			b.M.Box(0.02, 0.02, 0, 1.98, 1.98, 0.30, "concrete.curb")
			b.M.Box(0.10, 0.10, 0.30, 1.90, 1.90, 0.52, "concrete.pad")
			b.Stairs(0.72, 1.98, 1.28, 2.34, 0, 0.30, 4, false, "concrete.pad")
			// 第 1 段
			b.M.Box(0.18, 0.18, 0.52, 1.82, 1.82, 3.20, body)
			b.M.Box(0.24, 0.24, 0.70, 1.76, 1.76, 3.06, "wall.glass.blue")
			atGlassBands(b.M, 0.24, 0.24, 1.76, 1.76, 0.70, 3.06, 3, band)
			atFins(b.M, 0.18, 0.18, 1.82, 1.82, 0.52, 3.18, 0.36, band)
			b.M.Box(0.14, 0.14, 3.20, 1.86, 1.86, 3.34, band)
			// 第 2 段
			b.M.Box(0.30, 0.30, 3.34, 1.70, 1.70, 5.60, body)
			b.M.Box(0.36, 0.36, 3.50, 1.64, 1.64, 5.46, accent)
			atGlassBands(b.M, 0.36, 0.36, 1.64, 1.64, 3.50, 5.46, 3, band)
			atFins(b.M, 0.30, 0.30, 1.70, 1.70, 3.34, 5.58, 0.34, "trim.white")
			b.M.Box(0.26, 0.26, 5.60, 1.74, 1.74, 5.74, band)
			// 第 3 段（最小，退到中央）
			b.M.Box(0.46, 0.46, 5.74, 1.54, 1.54, 7.60, body)
			b.M.Box(0.52, 0.52, 5.86, 1.48, 1.48, 7.48, "wall.glass.blue")
			atGlassBands(b.M, 0.52, 0.52, 1.48, 1.48, 5.86, 7.48, 3, band)
			atFins(b.M, 0.46, 0.46, 1.54, 1.54, 5.74, 7.58, 0.30, band)
			// 冠部：露台 + 栏杆
			b.M.Box(0.40, 0.40, 7.60, 1.60, 1.60, 7.72, "concrete.curb")
			b.M.SlabTop(7.72, 0.46, 0.46, 1.54, 1.54, "roof.gravel")
			b.M.Parapet(0.46, 0.46, 1.54, 1.54, 7.72, 0.16, 0.05, band)
			// 桅杆 + 红色航空障碍灯（最高点 10.00）
			atMastAt(b.M, 1.00, 1.00, 7.72, 9.82, "trim.white")
			b.M.Cylinder(1.00, 1.00, 0.085, 9.82, 9.96, 10, "car.red")
			b.M.Box(0.92, 0.92, 9.96, 1.08, 1.08, 10.00, "car.red")
			// 塔身四面各挂一条桅杆（航空灯的第二处：色块要成组才读得出「塔」）
			for _, x := range [2]float64{0.34, 1.66} {
				b.M.Box(x-0.03, 0.30, 7.72, x+0.03, 0.36, 8.60, "metal.pipe")
				b.M.Box(x-0.06, 0.26, 8.60, x+0.06, 0.40, 8.74, "car.red")
			}
			// 入口：门 + 悬挑雨棚 + 铭牌
			b.M.DecalY(1.82, 0.84, 1.16, 0.52, 1.70, "door.glass", 0.06)
			atCanopy(b.M, 0.58, 1.82, 1.42, 2.16, 2.60, 0.14, "metal.pipe", "concrete.curb", band)
			b.M.Box(0.18, 0.34, 1.60, 0.24, 0.92, 2.10, accent)
			TreeAt(b.M, 0.22, 1.80, 0, b.R, "leaf.olive")
			TreeAt(b.M, 1.80, 0.22, 0, b.R, "leaf.spring")
		},
	}
}

// ---------------------------------------------------------------- 城镇道具

// atFountain 中央喷泉（现代水景）：2x2 / H=1.6。
// 低矮的方形几何水池 + 细水柱，池沿用光洁的混凝土而非石球。
// 剪影签名：一片矩形水面 + 中央一朵细水花。
func atFountain() Def {
	return Def{
		Name: "at.fountain", Label: "中央喷泉", Kind: KindProp, Category: "公园",
		Footprint: [2]int{2, 2}, Height: 1.6, Variants: 3,
		ShadowFP: []float64{0.06, 0.06, 1.94, 1.94},
		Build: func(b *B) {
			rim, band := "concrete.curb", "trim.band"
			if b.R.Chance(0.4) {
				rim, band = "wall.tile.white", "trim.white"
			}
			b.Lot("pad.tile", "concrete.curb")
			// 方形水池：池沿 + 水面
			b.M.Parapet(0.16, 0.16, 1.84, 1.84, 0, 0.30, 0.12, rim)
			b.M.DecalTop(0.02, 0.28, 0.28, 1.72, 1.72, "water", 0.06)
			b.M.Box(0.24, 0.24, 0, 0.32, 1.76, 0.10, band)
			b.M.Box(1.68, 0.24, 0, 1.76, 1.76, 0.10, band)
			// 中央方形水盘（几何层次）
			b.M.Box(0.78, 0.78, 0, 1.22, 1.22, 0.52, rim)
			b.M.DecalTop(0.53, 0.82, 0.82, 1.18, 1.18, "water.shallow", 0.06)
			// 细水柱（最高点 1.60）
			b.M.Cylinder(1.00, 1.00, 0.035, 0.53, 1.44, 8, "water.foam")
			b.Blob(1.00, 1.00, 1.50, 0.09, 0.09, 0.10, 3, 8, 0.16, "water.foam")
			// 四角细水柱
			for _, p := range [4][2]float64{{0.42, 0.42}, {1.58, 0.42}, {0.42, 1.58}, {1.58, 1.58}} {
				b.M.Box(p[0]-0.03, p[1]-0.03, 0.30, p[0]+0.03, p[1]+0.03, 0.40, "metal.pipe")
				b.M.Cylinder(p[0], p[1], 0.022, 0.40, 0.90, 6, "water.foam")
			}
			TreeAt(b.M, 1.80, 1.80, 0, b.R, "leaf.spring")
		},
	}
}

// atFlagpole 旗杆（石基座 + 细杆 + 旗）：1x1 / H=1.5。
// 与喷泉同属广场构件，做成现代的「石墩 + 不锈钢杆 + 顶球」。
func atFlagpole() Def {
	return Def{
		Name: "at.flagpole", Label: "旗杆", Kind: KindProp, Category: "公园",
		Footprint: [2]int{1, 1}, Height: 1.5, Variants: 3,
		ShadowFP: []float64{0.44, 0.44, 0.56, 0.56},
		Build: func(b *B) {
			b.Pad("pad.concrete")
			flag := "awning.red"
			switch b.R.Pick(3) {
			case 0:
				flag = "awning.teal"
			case 1:
				flag = "car.yellow"
			}
			b.M.Cylinder(0.5, 0.5, 0.20, 0, 0.14, 12, "concrete.curb")
			b.M.Cylinder(0.5, 0.5, 0.14, 0.14, 0.26, 12, "trim.band")
			b.M.Cylinder(0.5, 0.5, 0.03, 0.26, 1.44, 6, "metal.pipe")
			b.M.Cylinder(0.5, 0.5, 0.05, 1.44, 1.50, 8, "metal.pipe")
			atFlag(b.M, 0.52, 0.5, 1.40, 0.40, 0.30, flag)
		},
	}
}

// atPond 池塘（自然水体）：2x2 / H=0.6。
// 石岸 + 水面 + 汀步 + 芦苇。它是区块里唯一的「软」面，因此保持零构筑物。
func atPond() Def {
	return Def{
		Name: "at.pond", Label: "池塘", Kind: KindProp, Category: "公园",
		Footprint: [2]int{2, 2}, Height: 0.6, Variants: 3,
		ShadowFP: []float64{0.1, 0.1, 1.9, 1.9},
		Build: func(b *B) {
			b.Lot("pad.grass.dark", "concrete.curb")
			rim := "wall.stone"
			if b.R.Chance(0.4) {
				rim = "concrete.curb"
			}
			b.M.Parapet(0.12, 0.12, 1.88, 1.88, 0, 0.26, 0.16, rim)
			b.M.Box(0.26, 0.26, 0, 1.74, 1.74, 0.10, "water")
			b.M.DecalTop(0.11, 0.32, 0.32, 1.68, 1.68, "water.shallow", 0.06)
			// 汀步：三块踏石（现代的方板，不是自然石）
			for i := 0; i < 3; i++ {
				x := 0.52 + float64(i)*0.48
				b.M.Box(x-0.14, 1.00, 0.10, x+0.14, 1.26, 0.18, "concrete.pad")
				b.M.DecalTop(0.19, x-0.12, 1.02, x+0.12, 1.24, "pad.gravel", 0.05)
			}
			// 芦苇丛
			for _, p := range [4][2]float64{{0.34, 0.4}, {0.52, 0.26}, {1.6, 0.44}, {1.44, 1.6}} {
				for k := 0; k < 3; k++ {
					x := p[0] + float64(k)*0.04
					b.M.Box(x-0.016, p[1]-0.016, 0.1, x+0.016, p[1]+0.016, 0.42+float64(k)*0.06, "leaf.olive")
				}
			}
			// 睡莲
			b.M.Cylinder(0.66, 1.5, 0.2, 0.1, 0.14, 10, "leaf.dark")
			b.M.Cylinder(0.78, 1.42, 0.065, 0.14, 0.20, 8, "trim.white")
			b.M.Cylinder(1.46, 0.86, 0.17, 0.1, 0.13, 10, "leaf.dark")
			b.M.Cylinder(1.36, 0.94, 0.055, 0.13, 0.18, 8, "sign.neon.pink")
		},
	}
}

// atFarm 都市农园（社区农园）：3x2 / H=1.4。
// 规整的抬高种植床 + 小工具房 + 铁丝篱笆。全园的线条必须是直的
// —— 社区农园的秩序感就来自这个「直」。
// 剪影签名：三列等高、间距整齐的种植床，一头一间小工具房。
func atFarm() Def {
	return Def{
		Name: "at.farm", Label: "都市农园", Kind: KindProp, Category: "植被",
		Footprint: [2]int{3, 2}, Height: 1.4, Variants: 3,
		ShadowFP: []float64{0.05, 0.05, 2.95, 1.95},
		Build: func(b *B) {
			b.Lot("pad.gravel", "concrete.curb")
			// 作物分三档（青苗 / 成熟 / 收割后的残茬）
			crop, cropTop := "leaf.spring", "leaf.olive"
			switch b.R.Pick(3) {
			case 0:
				crop, cropTop = "leaf.spring", "leaf.spring"
			case 1:
				crop, cropTop = "leaf.olive", "pad.sand"
			}
			// 三列抬高的种植床（木框 + 土 + 作物）
			for c := 0; c < 3; c++ {
				y0 := 0.24 + float64(c)*0.56
				b.M.Box(0.16, y0, 0, 2.24, y0+0.44, 0.30, "crate.wood")
				b.M.DecalTop(0.31, 0.20, y0+0.04, 2.20, y0+0.40, "pad.dirt", 0.06)
				for i := 0; i < 6; i++ {
					x := 0.26 + float64(i)*0.33
					b.M.Box(x, y0+0.08, 0.30, x+0.24, y0+0.36, 0.62, crop)
					if b.R.Chance(0.45) {
						b.M.Box(x+0.05, y0+0.13, 0.62, x+0.19, y0+0.31, 0.84, cropTop)
					}
				}
			}
			// 工具房：方盒 + 单坡金属顶 + 门（最高点 1.40）
			b.M.Box(2.34, 0.30, 0, 2.86, 1.10, 1.20, "wall.metal")
			b.M.Box(2.30, 0.24, 1.20, 2.90, 1.16, 1.30, "metal.pipe")
			b.M.Box(2.34, 0.30, 1.30, 2.86, 0.76, 1.40, "roof.metal")
			b.M.DecalY(1.10, 2.44, 2.74, 0.02, 0.90, "door.metal", 0.05)
			// 水栓 + 堆肥箱 + 水桶
			b.M.Cylinder(2.44, 1.44, 0.05, 0, 0.86, 8, "metal.pipe")
			b.M.Box(2.36, 1.40, 0.86, 2.52, 1.50, 0.92, "metal.pipe")
			b.M.Box(2.62, 1.38, 0, 2.88, 1.72, 0.54, "crate.wood")
			b.M.DecalTop(0.55, 2.64, 1.40, 2.86, 1.70, "pad.dirt", 0.06)
			b.M.Cylinder(0.30, 1.72, 0.12, 0, 0.30, 10, "wall.metal")
			// 铁丝篱笆：立柱 + 两道横杆（沿南边与东边）
			for i := 0; i < 7; i++ {
				x := 0.14 + float64(i)*0.34
				b.M.Box(x-0.022, 1.86, 0, x+0.022, 1.92, 0.72, "metal.pipe")
			}
			b.M.Box(0.10, 1.86, 0.62, 2.20, 1.92, 0.68, "metal.pipe")
			b.M.Box(0.10, 1.86, 0.36, 2.20, 1.92, 0.42, "metal.pipe")
			TreeAt(b.M, 0.22, 0.16, 0, b.R, "leaf.olive")
		},
	}
}

// atFlowerbed 花坛（现代花池）：1x1 / H=0.94。
// 混凝土池沿 + 低矮花丛。几何感来自「整块混凝土 + 一处小灌木」。
// 剪影签名：一个干净的水泥方框，里面一点绿与一点花色。
func atFlowerbed() Def {
	return Def{
		Name: "at.flowerbed", Label: "花坛", Kind: KindProp, Category: "公园",
		Footprint: [2]int{1, 1}, Height: 0.94, Variants: 3,
		ShadowFP: []float64{0.1, 0.1, 0.9, 0.9},
		Build: func(b *B) {
			b.Pad("pad.tile")
			rim := "concrete.curb"
			if b.R.Chance(0.4) {
				rim = "wall.tile.white"
			}
			bloom := []string{"car.red", "car.yellow", "car.white", "car.teal", "sign.neon.pink"}
			base := b.R.Pick(len(bloom))
			// 池沿：整块混凝土框，顶面比周围高 0.34
			b.M.Parapet(0.10, 0.10, 0.90, 0.90, 0, 0.34, 0.09, rim)
			b.M.DecalTop(0.30, 0.19, 0.19, 0.81, 0.81, "pad.dirt", 0.06)
			// 低矮花丛（成组，不做散点）
			for i := 0; i < 6; i++ {
				x := 0.26 + float64(i%3)*0.20
				y := 0.30 + float64(i/3)*0.22
				b.M.Box(x-0.07, y-0.07, 0.32, x+0.07, y+0.07, 0.50, bloom[(base+i)%len(bloom)])
				b.M.Box(x-0.09, y-0.09, 0.30, x+0.09, y+0.09, 0.40, "leaf.spring")
			}
			// 中央小灌木（全池最高点 0.94）
			b.M.Box(0.44, 0.44, 0.32, 0.56, 0.56, 0.58, "trunk")
			b.M.Box(0.34, 0.34, 0.58, 0.66, 0.66, 0.80, "leaf.olive")
			b.M.Box(0.40, 0.40, 0.80, 0.60, 0.60, 0.94, "leaf.olive")
		},
	}
}
