package catalog

import (
	"math"

	"isocity98/internal/art"
	"isocity98/internal/geom"
)

// AgentTownNPCs 是 Agent Town 的**角色资产**：21 位职业居民、小精灵、动物。
//
// 与建筑分开成集的原因：
//   - 角色是「动画」资产（4 向 × 若干帧），不是随机变体；
//   - 角色要在运行时按方向与步态相位取帧，命名必须是可预测的 `名字_f<dir*4+walk>`；
//   - 体量极小（1x 下约 16x22 像素），但数量多，单独一张图集更省。
//
// 命名约定：`Anim: true` + `Variants: 16`，帧号 = 方向*4 + 步态帧
// （方向 0=+X 东、1=+Y 南、2=-X 西、3=-Y 北）。
//
// ─────────────────────────────────────────────────────────────────────────────
// 本版重做的设计纪律（上一版被否掉的四条原因逐条对应）
//
//  1. 一个职业一个专属居民：21 个 stage 各有自己的角色，不再共用骨架配色。
//  2. 配件全部 ≥4x4 像素：1x 下 1 像素只是「一点颜色」，读不出形状；因此凡是要
//     被看见的东西（安全帽、画筒、纸箱、牌子、耳机、放大镜）都做到 4 像素以上，
//     并且用**高对比色**（亮黄/白/青/品红）压在深色工作服上。
//  3. 小精灵形状各异：12 只绑定 12 种语义，形体（灯泡/齿轮/纸夹/天平/羽毛/骰子/
//     沙漏/星火/云朵/拼图/钥匙/叶片）与动画节奏（悬浮幅度、旋转、脉冲相位）都不同。
//  4. 不靠脸：头部只有 6x6 像素，脸在 1x 下最多 1~2 像素。可读性全部由**体态**
//     （身高/体宽/含胸）、**头饰剪影**（安全帽/鸭舌帽/发髻/马尾/耳机/兜帽）与
//     **手持道具**承担；脸只保留「眼镜 / 不眼镜」这一档 1x 可读的信息。
//
// 建模纪律（与渲染管线的约定）：
//   - 姿势一律在「朝 +Y」的局部坐标里摆好，最后用 geom.RotateZ90(dir) 转向
//     （照抄 vehicles.go 的做法：先摆姿势后旋转）；
//   - 角色站在自己格子中心 (0.5,0.5)，原点 (0,0,0) 是该格北角；
//   - 走路只摆四肢，**不抬躯干、不抬脚**，于是「跨全部帧的最高点」恒等于
//     站立姿势的头顶 —— 这正是 Height 不变量测试要求的语义；
//   - Def.Height 不用手写常数，而是模型建完直接量出来（见 measureFrames），
//     避免改一处几何就悄悄漂移。
//   - 角色是运行时自绘接触阴影，因此 NoShadow: true 且 ShadowFP 留空。

// ─────────────────────────────────────────────────────────────────────────────
// 一、人体骨架常量（格高度单位；1 单位 = 8 像素）
//
// 目标：1x 下成人 20~23 像素高、7~9 像素宽，占地 0.3~0.4 格。
// 基准身高 2.62 单位 ≈ 21 像素，四档骨架缩放后落在 2.54~2.90（全部在规格区间内）。
//
// 刻意保留「头略大」的卡通比例（头高约占总高 1/5.5，比写实人体大一圈）：
// 1x 下头只有 5~6 像素，再缩小就没地方放眼镜、发际线与头饰了，
// 而头饰恰恰是 21 个居民最主要的剪影差异来源。这是有意的取舍，不是建模误差。
// ─────────────────────────────────────────────────────────────────────────────

const (
	figTopZ    = 2.62  // 基准身高（头顶）—— 实际高度由 measureFrames 量出
	figHipZ    = 0.56  // 髋关节（腿根）
	figShouldZ = 1.58  // 肩关节
	figNeckZ   = 1.74  // 颈顶（躯干与头之间）
	figHeadCZ  = 2.14  // 头颅中心
	figHeadR   = 0.225 // 头颅半径（近似球）
	figThigh   = 0.28  // 大腿长
	figShin    = 0.28  // 小腿长（大腿+小腿 = 髋高，站立时脚底贴地）
	figArm     = 0.42  // 肩到手腕
	figHalfW   = 0.14  // 基准半宽（躯干 X 向），占地 0.28 格
)

// 步态摆幅。腿必须摆够大：1x 下小腿位移要跨过 2~3 像素才看得出「在走」，
// 因此腿 34°、臂 26°（腿长 0.56 时脚前进 0.31 格 ≈ 2.5 像素）。
const (
	figLegSwing = 34 * math.Pi / 180
	figArmSwing = 26 * math.Pi / 180
)

// ---------------------------------------------------------------- 造型参数

// figBuild 是一位居民的全部造型参数。
//
// 骨架比例只有 4 档（标准/高瘦/敦实/含胸），但**头饰 + 道具 + 上下装两段色**
// 是逐人不同的，因此 1x 下是 21 个不同的剪影，而不是 21 份换色。
type figBuild struct {
	Label string

	// 骨架比例（见 bodyPlan）
	Plan string

	Skin  string // 皮肤
	Hair  string // 头发
	Top   string // 上衣主色（外套/衬衫）
	Inner string // 内衬（开襟外套时露出的第二段色）
	Pants string // 下装
	Foot  string // 鞋

	// 头饰 / 发型（剪影层）
	Hat   string // "" | hardhat | cap | beanie | band | phones | hood | visor | scarf | topknot | bun | tail | bob
	HoodM string // 兜帽材质（留空 = 用上衣色）
	Hair2 string // 发色覆盖（发髻/马尾用的第二种发色，""= 用 Hair）
	Beard bool   // 胡须（区分两人的第二档 1x 特征）

	// 1x 可读的**大**配件（每件都 ≥4x4 像素，用高对比色）
	Glasses bool   // 眼镜（浅色镜框横跨面部）
	Vest    string // 反光背心：一整圈亮色 + 两道横向反光条
	Stripe  string // 反光条/袖标色（配 Vest 或单独用）
	Apron   string // 围裙/工装胸兜
	Belt    string // 工具腰带（一圈深色 + 两侧挂袋）
	Strap   string // 斜挎背带（背包/画筒/公文袋）
	Pocket  string // 胸前口袋（口袋上沿一道亮线）

	// 手持 / 背负道具
	Hold   string // "" | brief | case | box | tray | bag | tablet | board | hboard
	HoldM  string // 道具主色
	Marker bool   // 手里另握一支记号笔（优先级评估师）
	Back   string // 背负筒状物（图纸筒/画筒）材质
	Flag   string // 举起的牌子颜色（""= 不举牌）
	FlagM  string // 牌子上的图案色
	Cup    string // 手持杯（咖啡/保温杯）

	// 服装细节
	Suit  bool // 西装：翻领 + 领带 + 裤线（审计师）
	Long  bool // 长外套：下摆过膝（仲裁员）
	Tray4 bool // 便签摞四层（任务拆分师）

	// 姿势覆盖
	ArmsUp    bool    // 双手抱头（阻塞等待者）
	RaiseHand float64 // 右手抬起的角度（举牌/举杯：约 1.1~1.35 弧度）
	CarryIn   bool    // 双手在身前捧物（抱卷宗/抱书/抱箱）
	Hunch     float64
}

// bodyPlan 返回骨架比例：整体高度缩放、体宽缩放、头径缩放、含胸量。
//
// 这是「不靠换色区分体态」的落地：高瘦的人屏占比更高、敦实的人更宽、
// 含胸的人头往前探 —— 三者在纯黑剪影下就能两两分开。
func bodyPlan(name string) (hz, wz, headR, hunch float64) {
	switch name {
	case "tall": // 高瘦：窄一点、更高、头略小（≈2.90，规格上限）
		return 1.19, 0.90, 0.96, 0.0
	case "stocky": // 敦实：矮宽、头略大（≈2.55，规格下限之上）
		return 1.04, 1.20, 1.06, 0.0
	case "stooped": // 含胸：头前探 + 肩前倾
		return 1.11, 1.02, 1.00, 0.055
	default: // 标准（≈2.68）
		return 1.12, 1.0, 1.0, 0.0
	}
}

// ---------------------------------------------------------------- 数据集

// AgentTownNPCs 汇总三类角色：21 位职业居民、小精灵、动物。
func AgentTownNPCs() []Def {
	out := residentDefs()
	out = append(out, spiritDefs()...)
	out = append(out, animalDefs()...)
	return out
}

// ---------------------------------------------------------------- A. 职业居民

// residentRoster 是 21 个职业的造型表 —— 顺序与 Agent Town 的 stage 顺序一致。
//
// 造型身份（逐个都在 1x 下靠**剪影 + 一件大配件**区分）：
//
//	npc.refiner     需求精炼师  卷袖衬衫 + 眼镜 + 抱卷宗（白纸卷压在胸前）
//	npc.planner     规划师      鸭舌帽 + 背后图纸筒（斜挎的亮色长筒）+ 手持平板
//	npc.reviewer-plan 计划评审官 放大镜 + 夹板（手持亮色板斜挑出身体轮廓）
//	npc.designer    架构设计师  兜帽衫 + 背后大画筒（比规划师更粗更长）
//	npc.splitter    任务拆分师  三色便签盒（抱在身前的一摞彩块）
//	npc.implementer 开发工程师  安全帽 + 反光背心（亮黄全身圈）+ 工具腰带
//	npc.reviewer    验收工程师  安全帽 + 卷尺带 + 记录板
//	npc.releaser    发布工程师  耳机 + 笔记本包 + 袖标
//	npc.worker      综合工作者  工装连体服（整身一色）+ 工具袋
//	npc.auditor     审计师      深色西装 + 公文包 + 袖标
//	npc.pm          统筹 PM     西装 + 领口证件牌（胸前亮色小牌）+ 手账
//	npc.librarian   知识管理员  抱一摞书 + 眼镜链（镜框两侧垂下的亮色短链）
//	npc.legislator  规范审查员  卷尺 + 规范手册（硬壳大书）
//	npc.prioritizer 优先级评估师 三色标签板 + 记号笔
//	npc.arbiter     仲裁员      长外套（下摆过膝）+ 卷起的文书
//	npc.archivist   归档管理员  抱纸箱 + 腰包
//	npc.standby     待命者      靠肩背包 + 手表（亮色表带）
//	npc.resting     休息居民    卫衣 + 咖啡杯
//	npc.blocked     阻塞等待者  双手抱头 + 举「等待」牌 + 反光条
//	npc.griller     待访谈者    举问号牌 + 保温杯
//	npc.celebrant   交付庆祝者  亮色外套 + 小旗 + 彩带
func residentRoster() []figBuild {
	return []figBuild{
		{ // 需求精炼师：卷起袖子、抱卷宗、眼镜；浅衬衫 + 深裤 + 青色胸袋
			Label: "需求精炼师", Plan: "standard",
			Skin: "wall.stucco", Hair: "wall.brick.dark", Top: "wall.siding.white", Inner: "wall.siding.green",
			Pants: "car.dark", Foot: "trim.dark",
			Glasses: true, CarryIn: true, Hold: "box", HoldM: "trim.cream", Pocket: "car.teal",
		},
		{ // 规划师：鸭舌帽 + 背后图纸筒 + 手持平板
			Label: "规划师", Plan: "tall",
			Skin: "wall.stucco.pink", Hair: "wall.brick.dark", Top: "wall.siding.blue", Inner: "trim.white",
			Pants: "car.dark", Foot: "trim.dark",
			Hat: "cap", Hold: "tablet", HoldM: "car.white", Back: "trim.cream",
		},
		{ // 计划评审官：放大镜（浅色镜框）+ 夹板；含胸、拿着东西看
			Label: "计划评审官", Plan: "stooped",
			Skin: "wall.stucco", Hair: "roof.tile.brown", Top: "wall.siding.sand", Inner: "trim.white",
			Pants: "car.dark", Foot: "car.dark",
			Glasses: true, Hold: "board", HoldM: "trim.cream", Pocket: "roof.tile.brown",
		},
		{ // 架构设计师：兜帽衫 + 背后大画筒；全身深色，青内衬点睛
			Label: "架构设计师", Plan: "tall",
			Skin: "wall.stucco.pink", Hair: "wall.brick.dark", Top: "wall.brick.dark", Inner: "car.teal",
			Pants: "car.dark", Foot: "trim.white",
			Hat: "hood", HoodM: "wall.brick", Hold: "board", HoldM: "car.yellow", Back: "car.teal",
		},
		{ // 任务拆分师：抱一摞彩色便签盒（三层厚、亮黄在最上）
			Label: "任务拆分师", Plan: "standard",
			Skin: "wall.stucco", Hair: "trim.dark", Top: "wall.siding.green", Inner: "trim.cream",
			Pants: "car.dark", Foot: "trim.dark",
			CarryIn: true, Hold: "tray", HoldM: "car.yellow", Tray4: true, Strap: "trim.dark",
		},
		{ // 开发工程师：安全帽 + 反光背心 + 工具腰带；敦实体型
			Label: "开发工程师", Plan: "stocky",
			Skin: "wall.stucco.pink", Hair: "trim.dark", Top: "car.dark", Inner: "wall.siding.sand",
			Pants: "car.dark", Foot: "roof.shingle",
			Hat: "hardhat", Vest: "car.yellow", Stripe: "trim.white", Belt: "car.dark",
		},
		{ // 验收工程师：安全帽 + 卷尺带 + 记录板；比开发工程师更瘦更高
			Label: "验收工程师", Plan: "standard",
			Skin: "wall.stucco", Hair: "roof.slate", Top: "wall.siding.sand", Inner: "trim.cream",
			Pants: "car.dark", Foot: "car.dark",
			Hat: "hardhat", Hold: "board", HoldM: "trim.white", Stripe: "car.yellow", Pocket: "car.yellow",
		},
		{ // 发布工程师：耳机 + 笔记本包 + 徽章
			Label: "发布工程师", Plan: "standard",
			Skin: "wall.stucco.pink", Hair: "wall.brick.dark", Top: "roof.slate", Inner: "trim.white",
			Pants: "car.dark", Foot: "trim.dark",
			Hat: "phones", Hold: "bag", HoldM: "car.dark", Pocket: "sign.neon.cyan",
		},
		{ // 综合工作者：工装连体服（整身一色）+ 工具袋 + 毛线帽
			Label: "综合工作者", Plan: "stocky",
			Skin: "wall.stucco", Hair: "crate.wood", Top: "wall.panel.teal", Inner: "wall.panel.teal",
			Pants: "wall.panel.teal", Foot: "trim.dark",
			Hat: "beanie", Apron: "wall.panel.teal", Belt: "crate.wood", Hold: "bag", HoldM: "crate.wood",
		},
		{ // 审计师：深西装 + 公文包 + 红色袖标；高瘦
			Label: "审计师", Plan: "tall",
			Skin: "wall.stucco", Hair: "trim.dark", Top: "roof.slate", Inner: "trim.white",
			Pants: "car.dark", Foot: "trim.dark",
			Pocket: "trim.white", Hold: "case", HoldM: "roof.tile.red",
			Stripe: "car.red", Suit: true,
		},
		{ // 统筹 PM：西装 + 领口证件牌 + 手账 + 发髻
			Label: "统筹 PM", Plan: "standard",
			Skin: "wall.stucco.pink", Hair: "trim.dark", Top: "wall.siding.blue", Inner: "trim.white",
			Pants: "car.dark", Foot: "trim.dark",
			Hat: "topknot", Hair2: "trim.dark", Hold: "case", HoldM: "trim.cream", Pocket: "trim.white",
		},
		{ // 知识管理员：抱一摞书 + 眼镜链；含胸
			Label: "知识管理员", Plan: "stooped",
			Skin: "wall.stucco", Hair: "roof.tile.brown", Top: "wall.siding.green", Inner: "trim.cream",
			Pants: "car.dark", Foot: "trim.dark",
			Glasses: true, CarryIn: true, Hold: "box", HoldM: "car.red", Pocket: "car.yellow",
		},
		{ // 规范审查员：卷尺 + 硬壳规范手册 + 胡须
			Label: "规范审查员", Plan: "standard",
			Skin: "wall.stucco.pink", Hair: "wall.brick.dark", Top: "trim.band", Inner: "trim.white",
			Pants: "car.dark", Foot: "trim.dark",
			Beard: true, Hold: "board", HoldM: "roof.tile.red", Belt: "car.yellow",
		},
		{ // 优先级评估师：三色标签板 + 记号笔
			Label: "优先级评估师", Plan: "standard",
			Skin: "wall.stucco", Hair: "trim.dark", Top: "wall.siding.white", Inner: "sign.panel",
			Pants: "car.dark", Foot: "trim.dark",
			Hold: "hboard", HoldM: "sign.panel", Marker: true,
		},
		{ // 仲裁员：长外套（下摆过膝）+ 卷起的文书 + 胡须
			Label: "仲裁员", Plan: "tall",
			Skin: "wall.stucco", Hair: "trim.dark", Top: "sign.panel", Inner: "trim.white",
			Pants: "car.dark", Foot: "car.dark",
			Beard: true, Apron: "sign.panel", Hold: "brief", HoldM: "crate.wood",
			Pocket: "car.yellow", Long: true,
		},
		{ // 归档管理员：抱纸箱 + 腰包 + 围巾；含胸
			Label: "归档管理员", Plan: "stooped",
			Skin: "wall.stucco.pink", Hair: "crate.wood", Top: "wall.siding.sand", Inner: "trim.cream",
			Pants: "car.dark", Foot: "trim.dark",
			Hat: "scarf", CarryIn: true, Hold: "box", HoldM: "crate.wood", Belt: "wall.brick.dark",
		},
		{ // 待命者：靠肩背包（亮黄背带）+ 手表
			Label: "待命者", Plan: "standard",
			Skin: "wall.stucco", Hair: "car.dark", Top: "wall.metal", Inner: "trim.white",
			Pants: "car.dark", Foot: "trim.dark",
			Strap: "car.yellow", Hat: "band", Hold: "bag", HoldM: "wall.metal",
		},
		{ // 休息居民：卫衣（兜帽）+ 咖啡杯；敦实
			Label: "休息居民", Plan: "stocky",
			Skin: "wall.stucco.pink", Hair: "roof.tile.brown", Top: "awning.teal", Inner: "trim.white",
			Pants: "wall.siding.sand", Foot: "car.dark",
			Hat: "hood", Cup: "trim.white", Pocket: "trim.cream",
		},
		{ // 阻塞等待者：双手抱头 + 举「等待」牌 + 反光条
			Label: "阻塞等待者", Plan: "standard",
			Skin: "wall.stucco", Hair: "trim.dark", Top: "trim.band", Inner: "trim.dark",
			Pants: "car.dark", Foot: "trim.dark",
			Stripe: "car.yellow", Vest: "trim.band", Flag: "awning.red", FlagM: "trim.white", ArmsUp: true,
		},
		{ // 待访谈者：举问号牌 + 保温杯 + 马尾
			Label: "待访谈者", Plan: "standard",
			Skin: "wall.stucco.pink", Hair: "roof.tile.brown", Top: "wall.siding.green", Inner: "trim.white",
			Pants: "car.dark", Foot: "trim.dark",
			Hat: "tail", Hair2: "roof.tile.brown", RaiseHand: 1.15,
			Flag: "wall.siding.white", FlagM: "car.blue", Cup: "car.teal",
		},
		{ // 交付庆祝者：亮红外套 + 小旗 + 彩带 + 金发
			Label: "交付庆祝者", Plan: "standard",
			Skin: "wall.stucco", Hair: "roof.tile.brown", Top: "car.red", Inner: "trim.cream",
			Pants: "car.dark", Foot: "trim.dark",
			RaiseHand: 1.05, Flag: "car.yellow", FlagM: "awning.red", Stripe: "car.red", Pocket: "trim.cream",
		},
	}
}

// residentNames 是与 residentRoster 一一对应的资产名。
var residentNames = []string{
	"npc.refiner", "npc.planner", "npc.reviewer-plan", "npc.designer", "npc.splitter",
	"npc.implementer", "npc.reviewer", "npc.releaser", "npc.worker",
	"npc.auditor", "npc.pm", "npc.librarian", "npc.legislator", "npc.prioritizer",
	"npc.arbiter", "npc.archivist", "npc.standby", "npc.resting",
	"npc.blocked", "npc.griller", "npc.celebrant",
}

// residentDefs 生成 21 位职业居民，每人 16 帧（4 向 × 4 步态）。
func residentDefs() []Def {
	roster := residentRoster()
	if len(roster) != len(residentNames) {
		panic("agenttown: 居民名单与造型表长度不一致")
	}
	out := make([]Def, 0, len(roster))
	for i, fb := range roster {
		fb := fb
		build := func(m *geom.Mesh, f int) { buildFigure(m, fb, f) }
		out = append(out, animDef(residentNames[i], fb.Label, "居民", 16, build,
			func(m *geom.Mesh, f int) {
				buildFigure(m, fb, f)
				m.RotateZ90(f / 4)
			}))
	}
	return out
}

// animDef 把「按帧建模」的函数包成一个 16 帧动画 Def。
//
// probe 只用来量高度（不旋转，四向同高，省 3/4 的建模量）；build 负责真实施加
// 方向旋转。两者用同一套几何，所以 Height 不会漂移。
func animDef(name, label, category string, frames int, probe func(m *geom.Mesh, f int), build func(m *geom.Mesh, f int)) Def {
	return Def{
		Name: name, Label: label, Kind: KindProp, Category: category,
		Footprint: [2]int{1, 1}, Height: measureFrames(frames, probe),
		Variants: frames, Anim: true, NoShadow: true,
		Build: func(b *B) {
			m := geom.NewMesh()
			f := ((b.Frame % frames) + frames) % frames
			build(m, f)
			b.M.Merge(m, geom.V(0, 0, 0))
		},
	}
}

// measureFrames 用与真实构建完全相同的几何量一遍全部帧，取最高点作为 Def.Height。
// 手工写高度迟早会和几何漂移（catalog_test 的 TestDeclaredHeightMatchesMesh 会抓）。
func measureFrames(frames int, build func(m *geom.Mesh, f int)) float64 {
	top := 0.0
	for f := 0; f < frames; f++ {
		m := geom.NewMesh()
		build(m, f)
		if _, mx := m.Bounds(); mx.Z > top {
			top = mx.Z
		}
	}
	return round2(top)
}

// ---------------------------------------------------------------- 居民建模

// buildFigure 造一位居民。f = 方向*4 + 步态帧。
//
// 全部几何在「朝 +Y」的局部坐标里摆好：+Y 是屏幕左下（受光面），
// 也就是正对镜头的方向 —— 脸、胸前的牌子、身前的道具都朝 +Y。
func buildFigure(m *geom.Mesh, fb figBuild, f int) {
	hz, wz, hr, hunch := bodyPlan(fb.Plan)
	hunch += fb.Hunch
	walk := f % 4
	cx := 0.5

	// 步态角：0 站立 / 1 左腿前 / 2 过渡 / 3 右腿前
	ll, rl, la, ra := figureGait(walk)

	// 关键高度（全部乘身高比，脚底保持在地面）
	hipZ, shZ, neckZ := figHipZ*hz, figShouldZ*hz, figNeckZ*hz
	headR := figHeadR * hz * hr
	headCZ := figHeadCZ*hz - hunch*0.30
	headY := cx + 0.02 + hunch // 含胸的人头往前探
	torsoW := figHalfW * wz
	torsoD := 0.11 * wz

	// ---- 腿与鞋（先画下半身，被躯干压住的部分自然挡住）----
	legX := torsoW*0.46 + 0.008
	buildLeg(m, cx-legX, hipZ, hz, ll, fb.Pants, fb.Foot)
	buildLeg(m, cx+legX, hipZ, hz, rl, fb.Pants, fb.Foot)

	// ---- 手臂 ----
	armX := torsoW + 0.075
	armZ0 := shZ - 0.10*hz
	lHand := buildArm(m, cx-armX, armZ0, hz, fb.Top, fb.Skin, la, 0, fb.ArmsUp)
	rHand := buildArm(m, cx+armX, armZ0, hz, fb.Top, fb.Skin, ra, fb.RaiseHand, fb.ArmsUp)

	// ---- 躯干：上衣 + 内衬（两段色是 1x 下「有穿衣服」的关键）----
	m.Box(cx-torsoW, cx-torsoD, hipZ-0.02, cx+torsoW, cx+torsoD, shZ, fb.Top)
	// 开襟内衬：正前方一条竖向亮带
	if fb.Inner != "" && fb.Inner != fb.Top {
		m.DecalY(cx+torsoD+0.012, cx-torsoW*0.34, cx+torsoW*0.34, hipZ+0.02, shZ-0.06, fb.Inner, 0.03)
	}
	// 躯干与手臂之间的暗色分界：1x 下没有它，摆动的胳膊会糊进躯干
	for _, sx := range [2]float64{-1, 1} {
		m.DecalY(cx+torsoD+0.014, cx+sx*torsoW-0.008, cx+sx*torsoW+0.008,
			hipZ+0.05, shZ-0.05, "trim.dark", 0.04)
	}
	// 脖子：用上衣色而不是肤色 —— 1x 下肤色脖子会读成「断开的头」
	m.Box(cx-0.05, cx-0.05, shZ-0.02, cx+0.05, cx+0.05, neckZ, fb.Top)

	// ---- 服装外层与职业配件 ----
	buildOutfit(m, fb, cx, hz, hipZ, shZ, torsoW, torsoD)

	// ---- 背负物（画在头之前，避免压住头部剪影）----
	buildBackpack(m, fb, cx, hz, shZ)

	// ---- 头 ----
	buildHead(m, fb, cx, headY, headCZ, headR, hz)

	// ---- 手持道具（由手的位置决定）----
	buildHandProp(m, fb, cx, hz, shZ, lHand, rHand, headCZ, headR)
}

// figureGait 返回四种步态帧的摆角（左腿、右腿、左臂、右臂），单位弧度。
//
//	0 = 站立（双腿并拢，头顶最高点由这一帧决定）
//	1 = 左腿前 / 右臂前
//	2 = 过渡（小幅度收腿，避免走路像卡帧）
//	3 = 右腿前 / 左臂前
//
// 只摆四肢：膝盖按「脚底贴地」反算弯曲角，所以跨帧头顶高度恒定。
func figureGait(walk int) (ll, rl, la, ra float64) {
	switch walk {
	case 1:
		return figLegSwing, -figLegSwing * 0.55, -figArmSwing, figArmSwing
	case 2:
		return figLegSwing * 0.30, -figLegSwing * 0.30, -figArmSwing * 0.30, figArmSwing * 0.30
	case 3:
		return -figLegSwing * 0.55, figLegSwing, figArmSwing, -figArmSwing
	}
	return 0, 0, 0, 0
}

// buildLeg 一条腿：大腿 + 小腿 + 鞋，绕髋关节摆动。
//
// θ 是髋角、膝角取 -2θ，于是脚踝的 z = 髋高 − 大腿·cosθ − 小腿·cos(2θ−θ)
// = 髋高 − 0.56·cos... 在 θ=0 时正好等于 0（脚底贴地），θ≠0 时略微抬起。
// 因为脚只会「抬」不会「沉」，四帧里最低点仍是 0，不会插进地面。
func buildLeg(m *geom.Mesh, x, hipZ, hz, theta float64, pants, foot string) {
	thigh := figThigh * hz
	shin := figShin * hz
	hip := geom.V(x, 0.5+0.01, hipZ)
	knee := rotYZ(geom.V(x, 0.5+0.01, hipZ-thigh), hip, theta)
	ankle := rotYZ(geom.V(knee.X, knee.Y, knee.Z-shin), knee, -2*theta)
	limbSeg(m, hip, knee, 0.10*hz, 0.085*hz, pants)
	limbSeg(m, knee, ankle, 0.085*hz, 0.075*hz, pants)
	buildShoe(m, ankle, foot, hz, 0.5+0.055)
}

// buildShoe 鞋：贴住踝关节的一块前伸的厚板（深色鞋底 + 鞋面两段色）。
func buildShoe(m *geom.Mesh, ankle geom.Vec3, foot string, hz, toeY float64) {
	h := 0.075 * hz
	m.Box(ankle.X-0.048, ankle.Y-0.045, ankle.Z, ankle.X+0.048, toeY, ankle.Z+h, foot)
	m.Box(ankle.X-0.050, ankle.Y-0.048, ankle.Z, ankle.X+0.050, toeY+0.004, ankle.Z+h*0.42, "trim.dark")
}

// buildArm 一条手臂：袖子（上衣色）→ 前臂（露出肤色/袖口）→ 手。
// 返回手心的位置，供手持道具对位。raise 是「举手持物」的抬臂角（0 = 自然下垂）。
func buildArm(m *geom.Mesh, x, shZ, hz float64, sleeve, skin string, theta, raise float64, armsUp bool) geom.Vec3 {
	if armsUp {
		// 抱头：上臂上举、前臂向内折，手落在头顶两侧
		sh := geom.V(x, 0.5+0.01, shZ)
		elbow := rotYZ(geom.V(x, 0.5+0.01, shZ+0.30*hz), sh, -0.22)
		hand := geom.V(elbow.X, elbow.Y-0.10, elbow.Z-0.10*hz)
		limbSeg(m, sh, elbow, 0.095*hz, 0.080*hz, sleeve)
		limbSeg(m, elbow, hand, 0.080*hz, 0.070*hz, skin)
		return hand
	}
	if raise != 0 {
		// 举持：肩角抬到 raise，肘再补一点，手落在胸前偏外侧 —— 举牌/举杯用
		sh := geom.V(x, 0.5+0.01, shZ)
		elbow := rotYZ(geom.V(x, 0.5+0.01, shZ-0.20*hz), sh, raise)
		hand := rotYZ(geom.V(elbow.X, elbow.Y, elbow.Z-0.22*hz), elbow, raise)
		limbSeg(m, sh, elbow, 0.095*hz, 0.080*hz, sleeve)
		limbSeg(m, elbow, hand, 0.080*hz, 0.070*hz, skin)
		return hand
	}
	sh := geom.V(x, 0.5+0.01, shZ)
	elbow := rotYZ(geom.V(x, 0.5+0.01, shZ-0.20*hz), sh, theta)
	hand := rotYZ(geom.V(elbow.X, elbow.Y, elbow.Z-0.22*hz), elbow, theta*0.9)
	limbSeg(m, sh, elbow, 0.095*hz, 0.080*hz, sleeve)
	limbSeg(m, elbow, hand, 0.078*hz, 0.070*hz, skin)
	return hand
}

// limbSeg 用一块定向长方体连起 p0→p1：把标准盒的盒轴从左/右转到 p0→p1 的方向。
//
// 这是角色建模里唯一需要「任意方向部件」的地方（腿摆动后的两段、举起的手臂），
// 用两块互相垂直的板拼出一个处处有面朝向相机的实体，避免纯薄片在某个角度看消失。
func limbSeg(m *geom.Mesh, p0, p1 geom.Vec3, w0, w1 float64, mat string) {
	axis := p1.Sub(p0)
	if axis.Mul(1).Z == 0 && axis.X == 0 && axis.Y == 0 {
		return
	}
	o := rotTo(axis)
	half := func(p geom.Vec3, w float64) [4]geom.Vec3 {
		// 横截面四角：±w 沿右向、±w*0.75 沿前向
		r := o.right.Mul(w)
		f := o.fwd.Mul(w * 0.78)
		return [4]geom.Vec3{p.Sub(r).Sub(f), p.Add(r).Sub(f), p.Add(r).Add(f), p.Sub(r).Add(f)}
	}
	a := half(p0, w0)
	b := half(p1, w1)
	// 四个侧面（顶/底由深度缓冲自然处理，但侧面必须齐全，否则细杆在某些方向会消失）
	for i := 0; i < 4; i++ {
		j := (i + 1) % 4
		m.Quad(mat, art.FaceTop, geom.ShadeAuto, a[i], a[j], b[j], b[i])
	}
	// 两端封口
	m.Quad(mat, art.FaceTop, geom.ShadeTop, a[0], a[1], a[2], a[3])
	m.Quad(mat, art.FaceTop, geom.ShadeBottom, b[0], b[1], b[2], b[3])
}

// basis 是一个局部坐标系（轴向 + 两个横截面方向）。
type basis struct {
	axis, right, fwd geom.Vec3
}

// rotTo 求一条线段的正交基：axis 为线段方向，right 取「尽量水平」的方向，
// fwd = axis × right。这样部件的「正面」总是尽量朝向 +Y/+X，1x 下有可读的面。
func rotTo(axis geom.Vec3) basis {
	n := math.Sqrt(axis.X*axis.X + axis.Y*axis.Y + axis.Z*axis.Z)
	if n < 1e-9 {
		return basis{geom.V(0, 0, 1), geom.V(1, 0, 0), geom.V(0, 1, 0)}
	}
	a := axis.Mul(1 / n)
	// 参考轴：与 a 夹角最大的坐标轴
	ref := geom.V(0, 0, 1)
	if math.Abs(a.Z) > 0.9 {
		ref = geom.V(0, 1, 0)
	}
	r := cross(ref, a)
	rn := math.Sqrt(r.X*r.X + r.Y*r.Y + r.Z*r.Z)
	if rn < 1e-9 {
		r = geom.V(1, 0, 0)
		rn = 1
	}
	r = r.Mul(1 / rn)
	f := cross(a, r)
	return basis{a, r, f}
}

func cross(a, b geom.Vec3) geom.Vec3 {
	return geom.V(a.Y*b.Z-a.Z*b.Y, a.Z*b.X-a.X*b.Z, a.X*b.Y-a.Y*b.X)
}

// rotYZ 把点 p 绕（平行 X 轴、过 pivot 的直线）在 y-z 平面内旋转 theta 弧度。
// 这是走姿的唯一旋转机制：手臂与腿都只在「前后」这个平面里摆。
func rotYZ(p, pivot geom.Vec3, theta float64) geom.Vec3 {
	dy, dz := p.Y-pivot.Y, p.Z-pivot.Z
	c, s := math.Cos(theta), math.Sin(theta)
	return geom.V(p.X, pivot.Y+dy*c-dz*s, pivot.Z+dy*s+dz*c)
}

// ---------------------------------------------------------------- 服装外层

// buildOutfit 画职业服装与「1x 可读」的大配件。
//
// 排序原则：先画会改变剪影的大件（背心/围裙/长外套下摆），再画贴在身上的
// 细节（反光条/腰带/袖标/口袋/证件牌）。所有贴面都带 bias，避免共面闪烁。
func buildOutfit(m *geom.Mesh, fb figBuild, cx, hz, hipZ, shZ, torsoW, torsoD float64) {
	const (
		bDecal = 0.030 // 贴在躯干正面（y = torsoD）上
		bOuter = 0.045 // 贴在比躯干更靠外的一层上
	)
	yF := cx + torsoD
	zTop := shZ

	// 长外套下摆（仲裁员）：过膝的整圈深色，走起来也不动，纯粹是剪影
	if fb.Long {
		m.Box(cx-torsoW-0.014, cx-torsoD-0.014, hipZ-0.34, cx+torsoW+0.014, cx+torsoD+0.014, hipZ+0.14, fb.Top)
		m.Box(cx-torsoW-0.014, cx-torsoD-0.014, hipZ-0.34, cx+torsoW+0.014, cx+torsoD+0.014, hipZ-0.29, "car.dark")
	}

	// 反光背心：比躯干大一圈的亮色壳 + 两道横向反光条（工地上最抢眼的信号）
	if fb.Vest != "" {
		m.Box(cx-torsoW-0.014, cx-torsoD-0.014, hipZ+0.06, cx+torsoW+0.014, cx+torsoD+0.014, zTop+0.015, fb.Vest)
		stripe := fb.Stripe
		if stripe == "" {
			stripe = "trim.white"
		}
		for _, z := range [2]float64{hipZ + 0.26, hipZ + 0.48} {
			z *= hz / 1.0
			m.Box(cx-torsoW-0.020, cx+0.02, z, cx+torsoW+0.020, cx+torsoD+0.022, z+0.05, stripe)
		}
	}

	// 围裙 / 工装胸兜（综合工作者与归档管理员用）：下摆 + 胸兜 + 一条亮边
	if fb.Apron != "" && fb.Apron != fb.Top {
		m.Box(cx-torsoW-0.006, cx-torsoD-0.010, hipZ-0.12, cx+torsoW+0.006, cx+torsoD+0.014, hipZ+0.16, fb.Apron)
		m.Box(cx-torsoW*0.62, cx+torsoD-0.004, hipZ+0.14, cx+torsoW*0.62, cx+torsoD+0.016, zTop-0.10, fb.Apron)
		m.Box(cx-torsoW*0.62, cx+torsoD-0.002, hipZ+0.26, cx+torsoW*0.62, cx+torsoD+0.018, hipZ+0.30, "trim.white")
	}

	// 袖标 / 反光臂带：横跨一只前臂的亮带（审计、发布、验收的一眼辨识点）
	if fb.Stripe != "" && fb.Vest == "" {
		m.Box(cx-torsoW-0.060, cx+torsoD*0.2, hipZ+0.42, cx-torsoW-0.010, cx+torsoD+0.030, hipZ+0.50, fb.Stripe)
	}

	// 斜挎背带（背包/画筒/公文袋的悬挂方式）
	if fb.Strap != "" || fb.Back != "" {
		st := fb.Strap
		if st == "" {
			st = fb.Back
		}
		m.Quad(st, art.FacePosY, geom.ShadeLeft,
			geom.V(cx-torsoW*0.9, yF+0.016, zTop-0.02),
			geom.V(cx-torsoW*0.9+0.055, yF+0.016, zTop-0.02),
			geom.V(cx+torsoW*0.7, yF+0.016, hipZ+0.12),
			geom.V(cx+torsoW*0.7+0.055, yF+0.016, hipZ+0.12))
	}

	// 工具腰带：一圈深色带 + 两侧挂袋（1x 下把「工装」和「文职」分开）
	if fb.Belt != "" {
		m.Box(cx-torsoW-0.016, cx-torsoD-0.016, hipZ+0.02, cx+torsoW+0.016, cx+torsoD+0.016, hipZ+0.10, fb.Belt)
		for _, sx := range [2]float64{-1, 1} {
			m.Box(cx+sx*(torsoW+0.018), cx-0.05, hipZ-0.14, cx+sx*(torsoW+0.060), cx+0.05, hipZ+0.06, fb.Belt)
		}
	}

	// 胸前口袋 / 徽章 / 证件牌 / 领带（1x 下唯一的「职业身份小信号」）
	if fb.Pocket != "" {
		m.Box(cx-torsoW*0.72, yF+0.018, shZ-0.30, cx-torsoW*0.10, yF+0.030, shZ-0.18, fb.Pocket)
		m.Box(cx-torsoW*0.72, yF+0.020, shZ-0.30, cx-torsoW*0.10, yF+0.032, shZ-0.275, "trim.white")
	}
	if fb.Suit {
		// 西装翻领：胸前两条浅色斜带 + 中央一条领带（1x 下是「有领子」的唯一读法）
		m.Box(cx-torsoW*0.62, yF+0.018, shZ-0.42, cx-torsoW*0.10, yF+0.030, shZ-0.02, "trim.white")
		m.Box(cx+torsoW*0.10, yF+0.018, shZ-0.42, cx+torsoW*0.62, yF+0.030, shZ-0.02, "trim.white")
		m.Box(cx-0.024, yF+0.032, shZ-0.40, cx+0.024, yF+0.044, shZ-0.10, "car.red")
		// 裤线：腿外侧一条亮线
		for _, sx := range [2]float64{-1, 1} {
			m.Box(cx+sx*(torsoW*0.46+0.055), yF-0.12, hipZ-0.46, cx+sx*(torsoW*0.46+0.075), yF-0.09, hipZ-0.04, "trim.white")
		}
	}
}

// buildBackpack 背负的筒状物（图纸筒 / 大画筒）：斜挎在背后，剪影上多一根斜杆。
//
// 用 seg 造（不是水平圆柱），因此俯视角度下也读得出「背后有一根管子」；
// 管口加一圈亮色盖子，1x 下就是顶端 2 像素的亮点。
func buildBackpack(m *geom.Mesh, fb figBuild, cx, hz, shZ float64) {
	if fb.Back == "" {
		return
	}
	p0 := geom.V(cx-0.20, cx-0.13, shZ*0.62)
	p1 := geom.V(cx+0.10, cx-0.20, shZ+0.62)
	limbSeg(m, p0, p1, 0.055, 0.055, fb.Back)
	limbSeg(m, geom.V(p1.X, p1.Y, p1.Z), geom.V(p1.X+0.02, p1.Y-0.01, p1.Z+0.07), 0.070, 0.070, "trim.cream")
}

// ---------------------------------------------------------------- 手持道具

// buildHandProp 手持/举起的道具。位置由手的位置决定，所以摆臂时道具跟着手走。
//
// 所有道具都做到 1x 下 4 像素以上，并用**亮色**压在深色工作服上 ——
// 这是上一版被否掉的核心问题（1~2 像素的配件等于没有）。
func buildHandProp(m *geom.Mesh, fb figBuild, cx, hz, shZ float64, lHand, rHand geom.Vec3, headCZ, headR float64) {
	// 举起的牌子优先于手持道具（举牌的那只手已经被抬臂姿势占用）
	if fb.Flag != "" {
		buildSign(m, fb, rHand, headCZ, headR)
	} else if fb.Cup != "" {
		// 保温杯/咖啡杯：举在手上的小圆筒 + 白色杯盖
		m.Box(rHand.X-0.060, rHand.Y-0.055, rHand.Z-0.05, rHand.X+0.060, rHand.Y+0.055, rHand.Z+0.12, fb.Cup)
		m.Box(rHand.X-0.065, rHand.Y-0.060, rHand.Z+0.12, rHand.X+0.065, rHand.Y+0.060, rHand.Z+0.17, "trim.white")
	}
	if fb.Hold == "" {
		return
	}
	lx, ly, lz := lHand.X, lHand.Y, lHand.Z
	rx, ry, rz := rHand.X, rHand.Y, rHand.Z
	switch fb.Hold {
	case "brief": // 卷起的文书/图纸：抱在左手（贴住身体，不改变剪影）
		m.Box(lx-0.10, ly-0.06, lz-0.16, lx+0.10, ly+0.10, lz-0.02, fb.HoldM)
		m.Box(lx-0.10, ly-0.06, lz-0.16, lx+0.10, ly+0.10, lz-0.125, "trim.dark")
		m.Box(lx-0.10, ly-0.05, lz-0.075, lx+0.10, ly+0.11, lz-0.045, "car.yellow")
	case "case": // 公文包：提在左手外侧、落到腿边（剪影上多出一块方形）
		m.Box(lx-0.16, ly-0.06, lz-0.66, lx+0.10, ly+0.14, lz-0.26, fb.HoldM)
		m.Box(lx-0.16, ly-0.06, lz-0.66, lx+0.10, ly+0.14, lz-0.625, "trim.dark")
		m.Box(lx-0.16, ly-0.065, lz-0.53, lx+0.10, ly+0.145, lz-0.495, "trim.white")
		m.Box(lx-0.06, ly-0.03, lz-0.26, lx+0.01, ly+0.11, lz-0.18, "trim.dark")
	case "box": // 纸箱/抱着的书堆：抱在身前（不出格子边界）
		buildBoxStack(m, fb, cx, hz, lz)
	case "tray": // 便签盒/标签板：双手端在腰前，三层彩色（不遮脸、不出格）
		y0, y1 := cx+0.10, cx+0.28
		layers := []string{fb.HoldM, "car.yellow", "car.teal", "awning.red"}
		n := 3
		if fb.Tray4 {
			n = 4
		}
		for i := 0; i < n; i++ {
			z0 := lz - 0.34 + float64(i)*0.044
			m.Box(cx-0.050, y0+0.010, z0, cx+0.050, y1-0.010, z0+0.036, layers[i%len(layers)])
		}
	case "bag": // 单肩工具袋/笔记本包：挎在右手侧
		m.Box(rx-0.03, ry-0.16, rz-0.34, rx+0.13, ry+0.13, rz-0.06, fb.HoldM)
		m.Box(rx-0.03, ry-0.16, rz-0.34, rx+0.13, ry+0.13, rz-0.30, "trim.dark")
		m.Box(rx+0.01, ry-0.10, rz-0.26, rx+0.09, ry+0.07, rz-0.18, "car.yellow")
	case "tablet": // 平板/手账：左手托在身前，亮面朝上（1x 下是一块白）
		m.Box(lx-0.10, ly-0.10, lz-0.06, lx+0.12, ly+0.16, lz-0.01, fb.HoldM)
		m.Box(lx-0.10, ly-0.10, lz-0.015, lx+0.12, ly+0.16, lz+0.005, "wall.glass.blue")
	case "hboard": // 竖举的优先级板：一列彩色标签，剪影是一块立在体侧之外的高板
		bz1 := shZ + 0.30
		bz0 := bz1 - 0.66
		m.Box(cx-0.30, ly-0.14, bz0, cx-0.10, ly+0.10, bz1, fb.HoldM)
		strip := []string{"car.red", "car.yellow", "wall.siding.green"}
		for i, st := range strip {
			z := bz1 - 0.08 - float64(i)*0.125
			m.Box(cx-0.315, ly-0.155, z, cx-0.085, ly+0.115, z+0.075, st)
		}
		if fb.Marker {
			// 记号笔从板上方戳出来：剪影上多一根细杆
			m.Box(cx-0.235, ly-0.03, bz1, cx-0.175, ly+0.05, bz1+0.30, "car.dark")
			m.Box(cx-0.235, ly-0.03, bz1+0.30, cx-0.175, ly+0.05, bz1+0.38, "car.red")
		}
	case "board": // 夹板/记录板：左手托在腰侧、板面朝上（不遮脸、不出格）
		m.Box(lx-0.100, ly-0.13, lz-0.32, lx+0.115, ly+0.14, lz-0.275, fb.HoldM)
		m.Box(lx-0.100, ly-0.13, lz-0.275, lx+0.115, ly+0.14, lz-0.264, "trim.white")
		m.Box(lx-0.082, ly-0.11, lz-0.264, lx+0.097, ly-0.04, lz-0.246, "trim.dark")
	}
}

// buildBoxStack 抱在身前的物品：纸箱 / 一摞书 / 卷宗。
//
// CarryIn 的人用双手托住（两侧各露出一只手），否则只有左手托。
func buildBoxStack(m *geom.Mesh, fb figBuild, cx, hz, handZ float64) {
	y0, y1 := cx+0.08, cx+0.28
	z0 := handZ - 0.10
	m.Box(cx-0.17, y0, z0, cx+0.17, y1, z0+0.30, fb.HoldM)
	m.Box(cx-0.17, y0, z0+0.30, cx+0.17, y1, z0+0.345, "trim.cream")
	// 侧面一道亮色捆扎带 + 顶面标签：1x 下把「纸箱」和「一摞书」分开
	m.Box(cx-0.175, y0, z0+0.10, cx+0.175, y1, z0+0.155, "car.yellow")
	m.Box(cx-0.10, y1-0.01, z0+0.22, cx+0.10, y1+0.005, z0+0.295, "trim.white")
	// 上层第二件（一摞书的效果）：稍窄的一层，露出不同颜色
	m.Box(cx-0.14, y0+0.02, z0+0.345, cx+0.14, y1-0.01, z0+0.42, "car.teal")
}

// buildSign 举起的牌子：一根竖杆 + 一块亮色牌面 + 牌面上的图案（问号/感叹号/条纹）。
//
// 牌顶刻意压在头顶之下（headCZ + headR 是站立姿势的最高点），
// 这样「举牌」不会抬高 Def.Height，也不会破坏站立帧的锚点。
func buildSign(m *geom.Mesh, fb figBuild, hand geom.Vec3, headCZ, headR float64) {
	top := headCZ + headR*0.92 // 牌顶上限：略低于头顶
	bot := top - 0.34
	// 杆
	m.Box(hand.X-0.022, hand.Y-0.022, hand.Z-0.04, hand.X+0.022, hand.Y+0.022, top, "trim.cream")
	if fb.ArmsUp {
		// 抱头 + 顶牌：牌横跨双手之间（阻塞等待者）
		m.Box(hand.X-0.30, hand.Y-0.05, bot, hand.X+0.30, hand.Y+0.05, top, fb.Flag)
		m.Box(hand.X-0.30, hand.Y-0.05, bot+0.10, hand.X+0.30, hand.Y+0.05, bot+0.15, fb.FlagM)
		m.Box(hand.X-0.30, hand.Y-0.05, top-0.07, hand.X+0.30, hand.Y+0.05, top-0.02, fb.FlagM)
		return
	}
	// 单臂举牌（待访谈者）：牌面朝 +Y，图案用高对比色画成「问号」
	m.Box(hand.X-0.19, hand.Y-0.04, bot, hand.X+0.19, hand.Y+0.10, top, fb.Flag)
	m.Box(hand.X-0.05, hand.Y+0.095, bot+0.19, hand.X+0.05, hand.Y+0.125, bot+0.28, fb.FlagM)
	m.Box(hand.X-0.10, hand.Y+0.095, bot+0.09, hand.X+0.10, hand.Y+0.125, bot+0.19, fb.FlagM)
	m.Box(hand.X-0.10, hand.Y+0.095, bot+0.03, hand.X+0.05, hand.Y+0.125, bot+0.09, fb.FlagM)
}

// buildHead 造头 + 发型 + 头饰 + 脸。
//
// 规格明确「不要靠脸」，所以脸只做两件事：画一对眼睛（朝向信息）、按需画眼镜
// （1x 下唯一可读的面部特征：浅色镜框横跨深色皮肤，约 4x1 像素）。
func buildHead(m *geom.Mesh, fb figBuild, cx, hy, hz, r, hzScale float64) {
	m.Ellipsoid(cx, hy, hz, r, r*0.96, r, 4, 10, 0.02, 917, fb.Skin)
	buildFace(m, fb, cx, hy, hz, r)
	buildHair(m, fb, cx, hy, hz, r)
	buildHat(m, fb, cx, hy, hz, r, hzScale)
}

// ---------------------------------------------------------------- 头部

// buildFace 只画朝 +Y 的那一面：双眼 + 按需的眼镜。
func buildFace(m *geom.Mesh, fb figBuild, cx, hy, hz, r float64) {
	yf := hy + r*0.86
	ex := r * 0.42
	m.DecalY(yf, cx-ex-r*0.20, cx-ex+r*0.02, hz-r*0.16, hz+r*0.12, "trim.dark", 0.03)
	m.DecalY(yf, cx+ex-r*0.02, cx+ex+r*0.20, hz-r*0.16, hz+r*0.12, "trim.dark", 0.03)
	if fb.Beard {
		m.Box(cx-r*0.62, hy+r*0.30, hz-r*0.92, cx+r*0.62, hy+r*0.60, hz-r*0.50, fb.Hair)
	}
	if !fb.Glasses {
		return
	}
	// 眼镜：两片浅色粗框 + 鼻梁 + 垂下的眼镜链，横跨整张脸（约 4x2 像素）
	yf = hy + r*1.02
	m.DecalY(yf, cx-ex-r*0.36, cx-ex+r*0.30, hz-r*0.14, hz+r*0.22, "trim.white", 0.05)
	m.DecalY(yf, cx+ex-r*0.30, cx+ex+r*0.36, hz-r*0.14, hz+r*0.22, "trim.white", 0.05)
	m.DecalY(yf, cx-r*0.10, cx+r*0.10, hz+r*0.02, hz+r*0.18, "trim.white", 0.06)
	for _, sx := range [2]float64{-1, 1} {
		m.Box(cx+sx*(ex+r*0.30), hy+r*0.30, hz-r*1.00, cx+sx*(ex+r*0.46), hy+r*0.62, hz-r*0.10, "car.yellow")
	}
}

// buildHair 发型：颅顶发盖 + 前额刘海。发顶刻意与颅顶齐平 ——
// 头顶网格最高点由头颅决定，换发型不会动 Def.Height。
func buildHair(m *geom.Mesh, fb figBuild, cx, hy, hz, r float64) {
	hair := fb.Hair
	cap := r*1.00 + 0.002
	m.Ellipsoid(cx, hy, hz+r*0.30, cap, cap*0.97, r*0.76, 3, 10, 0.02, 233, hair)
	// 前额刘海：贴住 +Y 面的一条发际线（1x 下就是「颅顶一块深色」）
	m.DecalY(hy+r*0.97, cx-cap, cx+cap, hz+r*0.26, hz+r*0.62, hair, 0.04)
	// 鬓角：贴住头两侧的薄片，让正面也读得出头发（不越出头宽）
	m.Box(cx-cap*0.96, hy-r*0.40, hz+r*0.02, cx-cap*0.74, hy+r*0.58, hz+r*0.40, hair)
	m.Box(cx+cap*0.74, hy-r*0.40, hz+r*0.02, cx+cap*0.96, hy+r*0.58, hz+r*0.40, hair)
}

// buildHat 头饰与特殊发型 —— 这是 21 个居民最主要的剪影差异来源。
//
// 每种头饰都做到 4 像素以上，并且刻意越出头部轮廓（帽檐、兜帽、耳机杯），
// 这样在纯黑剪影下也能靠「头顶多出来的一块」把两个人分开。
func buildHat(m *geom.Mesh, fb figBuild, cx, hy, hz, r, hzScale float64) {
	hair := fb.Hair
	if fb.Hair2 != "" {
		hair = fb.Hair2
	}
	switch fb.Hat {
	case "hardhat": // 安全帽：扁圆顶 + 宽帽檐 + 顶部加强筋
		m.Ellipsoid(cx, hy, hz+r*0.42, r*1.14, r*1.14, r*0.74, 3, 10, 0.02, 431, "car.yellow")
		m.Box(cx-r*1.55, hy-r*1.55, hz+r*0.30, cx+r*1.55, hy+r*1.55, hz+r*0.48, "car.yellow")
		m.Box(cx-r*0.16, hy-r*1.30, hz+r*0.50, cx+r*0.16, hy+r*1.30, hz+r*0.62, "trim.cream")
	case "cap": // 鸭舌帽：帽盖 + 前伸的帽舌（剪影上朝 +Y 多出一块）
		m.Ellipsoid(cx, hy, hz+r*0.34, r*1.08, r*1.08, r*0.86, 3, 10, 0.02, 447, "car.blue")
		m.Box(cx-r*0.86, hy+r*0.82, hz+r*0.26, cx+r*0.86, hy+r*1.95, hz+r*0.44, "car.blue")
	case "beanie": // 毛线帽：厚帽圈 + 顶球
		m.Ellipsoid(cx, hy, hz+r*0.40, r*1.06, r*1.06, r*0.92, 3, 10, 0.02, 449, "car.yellow")
		m.Box(cx-r*1.10, hy-r*1.10, hz+r*0.18, cx+r*1.10, hy+r*1.10, hz+r*0.44, "roof.tile.red")
		m.Ellipsoid(cx, hy, hz+r*1.30, r*0.22, r*0.22, r*0.22, 2, 6, 0.02, 451, "trim.white")
	case "band": // 发带/头巾：额前一圈亮色横带
		m.Box(cx-r*1.06, hy-r*1.02, hz+r*0.20, cx+r*1.06, hy+r*1.06, hz+r*0.46, "car.red")
	case "phones": // 耳机：头梁 + 两侧耳罩（横向占满，剪影最宽的头饰）
		m.Box(cx-r*1.32, hy-r*0.18, hz+r*0.86, cx+r*1.32, hy+r*0.18, hz+r*1.10, "trim.dark")
		for _, sx := range [2]float64{-1, 1} {
			m.Box(cx+sx*r*1.34, hy-r*0.24, hz-r*0.22, cx+sx*r*1.56, hy+r*0.24, hz+r*0.30, "trim.dark")
			m.Box(cx+sx*r*1.40, hy-r*0.16, hz-r*0.10, cx+sx*r*1.60, hy+r*0.16, hz+r*0.20, "car.yellow")
		}
	case "hood": // 兜帽：脑后一整块厚垫 + 两侧包边
		hm := fb.Top
		if fb.HoodM != "" {
			hm = fb.HoodM
		}
		m.Ellipsoid(cx, hy-r*0.58, hz+r*0.18, r*1.24, r*0.86, r*1.06, 3, 10, 0.02, 457, hm)
		for _, sx := range [2]float64{-1, 1} {
			m.Box(cx+sx*r*0.86, hy-r*1.10, hz-r*0.42, cx+sx*r*1.24, hy+r*0.52, hz+r*0.72, hm)
		}
	case "visor": // 遮阳帽：整圈宽帽檐（比安全帽更平更宽）
		m.Ellipsoid(cx, hy, hz+r*0.30, r*1.04, r*1.04, r*0.60, 3, 10, 0.02, 459, "wall.siding.sand")
		m.Box(cx-r*1.90, hy-r*1.90, hz+r*0.20, cx+r*1.90, hy+r*1.90, hz+r*0.34, "wall.siding.sand")
	case "scarf": // 围巾：脖上一圈 + 前面垂下的一截
		m.Box(cx-r*0.96, hy-r*0.96, hz-r*0.62, cx+r*0.96, hy+r*0.96, hz-r*0.28, "awning.red")
		m.Box(cx-r*0.26, hy+r*0.70, hz-r*1.30, cx+r*0.26, hy+r*1.02, hz-r*0.50, "awning.red")
	case "topknot": // 发髻：颅顶一个圆髻（高瘦款的第二剪影）
		m.Ellipsoid(cx, hy-r*0.10, hz+r*0.98, r*0.42, r*0.42, r*0.40, 3, 8, 0.03, 461, hair)
	case "bun": // 低发髻：脑后一个圆髻
		m.Ellipsoid(cx, hy-r*0.92, hz+r*0.30, r*0.46, r*0.40, r*0.46, 3, 8, 0.03, 463, hair)
	case "tail": // 马尾：脑后一块长发披到肩
		m.Ellipsoid(cx, hy-r*0.72, hz+r*0.10, r*0.86, r*0.62, r*0.90, 3, 10, 0.03, 467, hair)
		m.Box(cx-r*0.34, hy-r*1.24, hz-r*1.90, cx+r*0.34, hy-r*0.62, hz+r*0.20, hair)
	case "bob": // 短发 + 波波头：发丝包到下颌
		m.Ellipsoid(cx, hy-r*0.10, hz-r*0.10, r*1.06, r*1.08, r*0.94, 3, 10, 0.03, 469, hair)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// B. 小精灵
//
// 12 只，每只绑定一种语义，**形状与动画节奏都不同**：
//
//	spirit.idea      灵感   灯泡形，顶端火花随相位闪烁
//	spirit.gear      机制   齿轮环，缓速自转（每帧 22°）
//	spirit.lens      检视   放大镜，镜片发光 + 手柄摆动
//	spirit.scale     权衡   天平，横梁按正弦左右倾斜、两盘反向升降
//	spirit.quill     记录   羽毛笔，笔身摆动 + 笔尖拖尾三粒
//	spirit.dice      抉择   骰子，四帧四次翻面（点数逐个变大）
//	spirit.hourglass 等待   沙漏，沙粒分四帧下落（相位错开）
//	spirit.spark     庆祝   星火，四臂快速抖动 + 中心脉冲
//	spirit.cloud     沟通   云朵，极缓漂浮（四帧几乎不动，只有气泡上浮）
//	spirit.puzzle    拆分   拼图块，两半分合（相位 0 合、2 分）
//	spirit.key       授权   钥匙，缓速旋转（每帧 18°）
//	spirit.leaf      生息   叶片，飘摆（绕叶柄摆动 + 侧向漂移）
//
// 帧号仍是 f = dir*4 + phase，最后 RotateZ90(dir) 转向 —— 与居民同一套约定，
// 运行时取帧逻辑不需要为小精灵写特例。
// ─────────────────────────────────────────────────────────────────────────────

// spiritSpec 是一只小精灵的图纸：语义 + 建模函数。
type spiritSpec struct {
	Name  string
	Label string
	Build func(m *geom.Mesh, f int)
}

func spiritDefs() []Def {
	specs := []spiritSpec{
		{"spirit.idea", "灵感精灵", buildSpiritIdea},
		{"spirit.gear", "机制精灵", buildSpiritGear},
		{"spirit.lens", "检视精灵", buildSpiritLens},
		{"spirit.scale", "权衡精灵", buildSpiritScale},
		{"spirit.quill", "记录精灵", buildSpiritQuill},
		{"spirit.dice", "抉择精灵", buildSpiritDice},
		{"spirit.hourglass", "等待精灵", buildSpiritHourglass},
		{"spirit.spark", "庆祝精灵", buildSpiritSpark},
		{"spirit.cloud", "沟通精灵", buildSpiritCloud},
		{"spirit.puzzle", "拆分精灵", buildSpiritPuzzle},
		{"spirit.key", "授权精灵", buildSpiritKey},
		{"spirit.leaf", "生息精灵", buildSpiritLeaf},
	}
	out := make([]Def, 0, len(specs))
	for _, sp := range specs {
		sp := sp
		out = append(out, animDef(sp.Name, sp.Label, "小精灵", 16,
			func(m *geom.Mesh, f int) { sp.Build(m, f) },
			func(m *geom.Mesh, f int) {
				sp.Build(m, f)
				m.RotateZ90(f / 4)
			}))
	}
	return out
}

// ------------------------------------------------------------------ 公共件

// spiritBase 是每只小精灵的**基准悬浮高度**。
//
// 除了形状，高度也是「一眼分开」的手段：12 只精灵分布在 0.44~0.74 五个高度档上，
// 于是 1x 面板里它们不会挤在同一条水平线上（相邻语义的精灵刻意分到不同档）。
// 最低 0.44、最高 0.74+0.11=0.85，仍落在规格要求的离地 0.3~0.9 之内。
var spiritBase = map[string]float64{
	"spirit.cloud":     0.74, // 云最高
	"spirit.key":       0.70,
	"spirit.idea":      0.67,
	"spirit.spark":     0.64,
	"spirit.leaf":      0.62,
	"spirit.lens":      0.61,
	"spirit.puzzle":    0.60,
	"spirit.hourglass": 0.59,
	"spirit.gear":      0.58,
	"spirit.scale":     0.56,
	"spirit.dice":      0.54,
	"spirit.quill":     0.44, // 羽毛最低（笔尖几乎垂到地面）
}

// spiritBob 悬浮高度：基准（按语义分档）+ 按相位在 ±amp 内起伏。
// amp 最大 0.11，因此最低 0.44-0.03 = 0.41，最高 0.74+0.08 = 0.82。
func spiritBob(f int, amp float64, base, phase float64) float64 {
	t := 2 * math.Pi * (float64(f%4)/4 + phase)
	return base + amp*math.Sin(t)
}

// spiritSegment 把两点连成一根方杆（`limbSeg` 的简写，供小精灵的把手/支架复用）。
func spiritSegment(m *geom.Mesh, p0, p1 geom.Vec3, w float64, mat string) {
	limbSeg(m, p0, p1, w, w*0.92, mat)
}

// spiritPrism 沿 z 轴生成一个 n 棱柱（比 Cylinder 更省面，且半径可随高度变化）。
func spiritPrism(m *geom.Mesh, cx, cy, z0, z1, r0, r1 float64, sides int, mat string) {
	for i := 0; i < sides; i++ {
		a0 := 2 * math.Pi * float64(i) / float64(sides)
		a1 := 2 * math.Pi * float64(i+1) / float64(sides)
		m.Quad(mat, art.FaceTop, geom.ShadeAuto,
			geom.V(cx+r0*math.Cos(a0), cy+r0*math.Sin(a0), z0),
			geom.V(cx+r0*math.Cos(a1), cy+r0*math.Sin(a1), z0),
			geom.V(cx+r1*math.Cos(a1), cy+r1*math.Sin(a1), z1),
			geom.V(cx+r1*math.Cos(a0), cy+r1*math.Sin(a0), z1))
	}
	if r1 > 1e-6 {
		pts := make([]geom.Vec3, 0, sides)
		for i := 0; i < sides; i++ {
			a := 2 * math.Pi * float64(i) / float64(sides)
			pts = append(pts, geom.V(cx+r1*math.Cos(a), cy+r1*math.Sin(a), z1))
		}
		for i := 1; i+1 < len(pts); i++ {
			m.Tri(mat, art.FaceTop, geom.ShadeTop, pts[0], pts[i], pts[i+1])
		}
	}
}

// ------------------------------------------------------------------ 1 灵感

// buildSpiritIdea 灵感精灵：灯泡 —— 玻璃泡 + 金属灯头 + 顶端火花。
// 动画：火花锯齿状跳动（不是正弦，所以节奏和其他精灵明显不同）。
func buildSpiritIdea(m *geom.Mesh, f int) {
	z := spiritBob(f, 0.05, spiritBase["spirit.idea"], 0.0)
	m.Ellipsoid(0.5, 0.5, z+0.10, 0.16, 0.16, 0.165, 4, 10, 0.02, 1013, "wall.glass")
	// 灯头：螺纹金属座 + 一圈亮色
	m.Cylinder(0.5, 0.5, 0.095, z-0.12, z-0.015, 8, "chrome")
	m.Box(0.5-0.105, 0.5-0.105, z-0.065, 0.5+0.105, 0.5+0.105, z-0.030, "car.yellow")
	// 灯丝：泡内一个小十字
	m.Box(0.5-0.02, 0.5-0.02, z+0.06, 0.5+0.02, 0.5+0.02, z+0.15, "car.yellow")
	m.Box(0.5-0.075, 0.5-0.02, z+0.10, 0.5+0.075, 0.5+0.02, z+0.13, "car.yellow")
	// 顶端火花：四帧在两个位置之间跳（1x 下就是「忽明忽灭」）
	jx, jz := 0.0, 0.0
	switch f % 4 {
	case 1:
		jx, jz = 0.035, 0.03
	case 2:
		jx, jz = -0.02, 0.015
	case 3:
		jx, jz = 0.01, 0.05
	}
	sx, sz := 0.5+jx, z+0.30+jz
	m.Box(sx-0.055, 0.5-0.055, sz-0.035, sx+0.055, 0.5+0.055, sz+0.035, "trim.white")
	m.Box(sx-0.022, 0.5-0.022, sz-0.075, sx+0.022, 0.5+0.022, sz+0.075, "car.yellow")
	if f%4 != 2 {
		m.Box(sx-0.075, 0.5-0.022, sz-0.022, sx+0.075, 0.5+0.022, sz+0.022, "car.yellow")
	}
}

// ------------------------------------------------------------------ 2 机制

// buildSpiritGear 机制精灵：内环 + 中心亮核 + 8 枚齿。
// 动画：每帧 22° 自转 —— 齿轮是唯一「整体旋转」的精灵，节奏最机械。
func buildSpiritGear(m *geom.Mesh, f int) {
	z := spiritBob(f, 0.04, spiritBase["spirit.gear"], 0.25)
	const rIn = 0.128
	spiritPrism(m, 0.5, 0.5, z-0.055, z+0.055, rIn, rIn, 12, "metal.dark")
	for i := 0; i < 8; i++ {
		a := 2*math.Pi*float64(i)/8 + 22*math.Pi/180*float64(f%4)
		cx, cy := 0.5+rIn*math.Cos(a), 0.5+rIn*math.Sin(a)
		dx, dy := math.Cos(a), math.Sin(a)
		px, py := -dy, dx
		w := 0.040
		m.Quad("chrome", art.FaceTop, geom.ShadeTop,
			geom.V(cx+px*w, cy+py*w, z-0.045), geom.V(cx+dx*0.075+px*w, cy+dy*0.075+py*w, z-0.045),
			geom.V(cx+dx*0.075-px*w, cy+dy*0.075-py*w, z-0.045), geom.V(cx-px*w, cy-py*w, z-0.045))
		m.Box(cx+dx*0.075-w, cy+dy*0.075-w, z-0.045, cx+dx*0.075+w, cy+dy*0.075+w, z+0.045, "chrome")
	}
	m.Ellipsoid(0.5, 0.5, z, 0.055, 0.055, 0.055, 3, 8, 0.02, 1021, "car.yellow")
	m.Ellipsoid(0.5, 0.5, z+0.03, 0.028, 0.028, 0.028, 2, 6, 0.02, 1022, "trim.white")
	// 轴心标记：一个小偏心点，旋转时能看出在转
	a := 80 * math.Pi / 180 * float64(f%4)
	m.Box(0.5+0.085*math.Cos(a)-0.02, 0.5+0.085*math.Sin(a)-0.02, z+0.03,
		0.5+0.085*math.Cos(a)+0.02, 0.5+0.085*math.Sin(a)+0.02, z+0.062, "trim.white")
}

// ------------------------------------------------------------------ 3 检视

// buildSpiritLens 检视精灵：放大镜 —— 亮框圆镜 + 斜向手柄 + 镜面高光。
// 动画：手柄角度按相位摆动，镜面高光沿镜面滑动。
func buildSpiritLens(m *geom.Mesh, f int) {
	z := spiritBob(f, 0.05, spiritBase["spirit.lens"], 0.4)
	r := 0.155
	m.Ellipsoid(0.5, 0.5, z, r, r, r*0.42, 3, 12, 0.0, 1031, "chrome")
	m.Ellipsoid(0.5, 0.5, z+0.015, r*0.82, r*0.82, r*0.34, 3, 12, 0.0, 1032, "wall.glass.blue")
	// 镜面高光：一块随相位滑动的小白片（「镜片在反光」的唯一读法）
	hx := 0.5 + (float64(f%4)-1.5)*0.035
	m.Box(hx-0.045, 0.5-0.045, z+0.045, hx+0.045, 0.5+0.045, z+0.062, "trim.white")
	// 手柄：绕镜框向外下方伸出，逐帧摆动
	a := -55*math.Pi/180 + 12*math.Pi/180*float64(f%4)
	p0 := geom.V(0.5+r*0.95*math.Cos(a), 0.5+r*0.95*math.Sin(a), z-0.02)
	p1 := geom.V(0.5+(r+0.30)*math.Cos(a), 0.5+(r+0.30)*math.Sin(a), z-0.16)
	spiritSegment(m, p0, p1, 0.038, "crate.wood")
	m.Box(p1.X-0.05, p1.Y-0.05, p1.Z-0.03, p1.X+0.05, p1.Y+0.05, p1.Z+0.03, "car.yellow")
}

// ------------------------------------------------------------------ 4 权衡

// buildSpiritScale 权衡精灵：立柱 + 可倾斜横梁 + 两只吊盘。
// 动画：横梁角度 = 22°·sin(相位)，两盘反向升降 —— 唯一「往复摆动」的精灵。
func buildSpiritScale(m *geom.Mesh, f int) {
	z := spiritBob(f, 0.03, spiritBase["spirit.scale"], 0.1)
	// 立柱
	m.Box(0.5-0.028, 0.5-0.028, z-0.16, 0.5+0.028, 0.5+0.028, z+0.28, "metal.dark")
	m.Box(0.5-0.075, 0.5-0.075, z-0.20, 0.5+0.075, 0.5+0.075, z-0.15, "metal.dark")
	m.Box(0.5-0.035, 0.5-0.035, z+0.28, 0.5+0.035, 0.5+0.035, z+0.33, "car.yellow")
	// 横梁
	tilt := 22 * math.Pi / 180 * math.Sin(2*math.Pi*float64(f%4)/4+0.6)
	beam(m, z+0.26, tilt)
	// 两盘：挂在梁两端，随梁倾斜反向升降
	for _, s := range [2]float64{-1, 1} {
		ex := 0.5 + s*0.19
		ez := z + 0.26 + s*0.19*math.Sin(tilt)
		m.Box(ex-0.012, 0.5-0.012, ez-0.16, ex+0.012, 0.5+0.012, ez-0.02, "chrome")
		m.Cylinder(ex, 0.5, 0.055, ez-0.10, ez-0.065, 8, "car.yellow")
		m.Box(ex-0.05, 0.5-0.05, ez-0.065, ex+0.05, 0.5+0.05, ez-0.055, "trim.white")
	}
}

// beam 天平横梁：绕立柱顶点的倾斜杆。
func beam(m *geom.Mesh, z, tilt float64) {
	p0 := geom.V(0.5-0.20, 0.5, z-0.20*math.Sin(tilt))
	p1 := geom.V(0.5+0.20, 0.5, z+0.20*math.Sin(tilt))
	spiritSegment(m, p0, p1, 0.030, "car.yellow")
	m.Box(0.5-0.04, 0.5-0.04, z+0.30, 0.5+0.04, 0.5+0.04, z+0.37, "car.yellow")
}

// ------------------------------------------------------------------ 5 记录

// buildSpiritQuill 记录精灵：羽毛笔 —— 笔杆 + 羽面 + 笔尖 + 三粒拖尾。
//
// 它是全场唯一「垂到接近地面」的悬浮物（基准 0.44），笔尖在最低相位几乎触地，
// 因此 1x 下与其它 11 只在竖直位置上就分开了。
func buildSpiritQuill(m *geom.Mesh, f int) {
	z := spiritBob(f, 0.045, spiritBase["spirit.quill"], 0.2)
	sway := 0.045 * math.Sin(2*math.Pi*float64(f%4)/4+1.1)
	// 笔杆：从右上到左下（一条实心方杆，比薄片在 1x 下多出 4~6 个像素）
	p0 := geom.V(0.66+sway, 0.42, z+0.32)
	p1 := geom.V(0.38+sway, 0.56, z-0.24)
	spiritSegment(m, p0, p1, 0.040, "trim.cream")
	// 羽面：沿笔杆的一片厚实的尖叶（用定向方杆而不是薄片，逐段收窄）
	pts := [][2]float64{{0.655, 0.428}, {0.615, 0.442}, {0.560, 0.468}, {0.492, 0.508}, {0.432, 0.546}}
	ws := []float64{0.030, 0.105, 0.120, 0.095, 0.045}
	for i := 0; i+1 < len(pts); i++ {
		a := geom.V(pts[i][0]+sway, pts[i][1], z+0.115)
		b := geom.V(pts[i+1][0]+sway, pts[i+1][1], z+0.115)
		spiritSegment(m, a, b, ws[i], "wall.tile.white")
	}
	// 羽轴：一条亮色细线压在羽面中央（1x 下就是叶片中间一列亮点）
	spiritSegment(m, geom.V(0.665+sway, 0.425, z+0.125), geom.V(0.412+sway, 0.556, z+0.125), 0.018, "car.yellow")
	// 笔尖
	m.Box(0.335+sway, 0.560, z-0.30, 0.395+sway, 0.590, z-0.22, "car.yellow")
	// 拖尾：三粒渐小的光点，随相位上升
	for i := 0; i < 3; i++ {
		t := float64(i)/3 + float64(f%4)/12
		d := 0.06 + 0.16*math.Mod(t, 1.0)
		m.Box(0.38-d*0.35+sway-0.032, 0.56+d*0.2-0.032, z-0.36-d,
			0.38-d*0.35+sway+0.032, 0.56+d*0.2+0.032, z-0.30-d, "car.yellow")
	}
}

// ------------------------------------------------------------------ 6 抉择

// buildSpiritDice 抉择精灵：骰子 —— 立方 + 点数贴片。
// 动画：四帧四次「翻面」，高点贴片的点数 1→3→5→6 变化，靠点数位置而非旋转传达翻动。
func buildSpiritDice(m *geom.Mesh, f int) {
	z := spiritBob(f, 0.06, spiritBase["spirit.dice"], 0.55)
	h := 0.095
	m.Box(0.5-h, 0.5-h, z-h, 0.5+h, 0.5+h, z+h, "trim.white")
	m.Box(0.5-h, 0.5-h, z-h, 0.5+h, 0.5+h, z-h+0.02, "trim.dark")
	// 圆角：四角各切一个小方块，让方形轮廓不那么像素化
	for _, sx := range [2]float64{-1, 1} {
		for _, sy := range [2]float64{-1, 1} {
			m.Box(0.5+sx*h-0.012, 0.5+sy*h-0.012, z-h+0.03, 0.5+sx*h, 0.5+sy*h, z+h-0.03, "trim.dark")
		}
	}
	// 顶面点数：按帧变化的 1..6 布局（1x 下就是顶上几粒暗点）
	pips := [4][][2]float64{
		{{0, 0}},
		{{-1, -1}, {1, 1}},
		{{-1, -1}, {0, 0}, {1, 1}},
		{{-1, -1}, {1, -1}, {-1, 1}, {1, 1}},
	}[f%4]
	for _, p := range pips {
		px := 0.5 + p[0]*0.042
		py := 0.5 + p[1]*0.042
		m.Box(px-0.020, py-0.020, z+h-0.002, px+0.020, py+0.020, z+h+0.012, "trim.dark")
	}
	// 朝向摄像机的一面也点两粒
	m.Box(0.585, 0.5-0.042-0.02, z-0.042-0.02, 0.602, 0.5-0.042+0.02, z-0.042+0.02, "trim.dark")
	m.Box(0.585, 0.5+0.042-0.02, z+0.042-0.02, 0.602, 0.5+0.042+0.02, z+0.042+0.02, "trim.dark")
}

// ------------------------------------------------------------------ 7 等待

// buildSpiritHourglass 等待精灵：上下木盖 + 玻璃细腰 + 落沙。
// 动画：沙粒分四帧下落（每帧相位不同），是唯一「有粒子在动」的精灵。
func buildSpiritHourglass(m *geom.Mesh, f int) {
	z := spiritBob(f, 0.035, spiritBase["spirit.hourglass"], 0.7)
	top, bot := z+0.30, z-0.28
	m.Cylinder(0.5, 0.5, 0.185, top-0.045, top, 8, "crate.wood")
	m.Cylinder(0.5, 0.5, 0.185, bot, bot+0.045, 8, "crate.wood")
	// 玻璃壳：上锥 + 下锥（隐约可见，让沙粒有「容器」）
	spiritPrism(m, 0.5, 0.5, z+0.06, top-0.045, 0.035, 0.155, 8, "wall.glass")
	spiritPrism(m, 0.5, 0.5, bot+0.045, z-0.045, 0.155, 0.035, 8, "wall.glass")
	// 上仓残沙 + 下仓沙堆（沙堆随帧长高）
	m.Box(0.5-0.10, 0.5-0.10, z+0.14+0.02*float64(f%4), 0.5+0.10, 0.5+0.10, z+0.20, "car.yellow")
	m.Box(0.5-0.09, 0.5-0.09, bot+0.045, 0.5+0.09, 0.5+0.09, bot+0.045+0.035*float64(f%4+1), "car.yellow")
	// 落沙：一束细流 + 两粒在下落中
	m.Box(0.5-0.010, 0.5-0.010, z-0.05, 0.5+0.010, 0.5+0.010, z+0.14, "car.yellow")
	for i := 0; i < 2; i++ {
		t := math.Mod(float64(f%4)/4+float64(i)*0.5, 1.0)
		py := z + 0.12 - 0.30*t
		m.Box(0.5-0.022, 0.5-0.022, py, 0.5+0.022, 0.5+0.022, py+0.04, "trim.white")
	}
}

// ------------------------------------------------------------------ 8 庆祝

// buildSpiritSpark 庆祝精灵：四臂星火 + 亮核。
// 动画：四臂长度按相位脉冲（最快的一只），核心逐帧抖动。
func buildSpiritSpark(m *geom.Mesh, f int) {
	z := spiritBob(f, 0.07, spiritBase["spirit.spark"], 0.3)
	ph := float64(f % 4)
	pulse := 0.055 * math.Sin(2*math.Pi*ph/4)
	jx := 0.02 * math.Sin(2*math.Pi*ph/3)
	for i := 0; i < 4; i++ {
		a := math.Pi / 2 * float64(i)
		dx, dy := math.Cos(a), math.Sin(a)
		px, py := -dy, dx
		r0, r1 := 0.05, 0.22+pulse
		w := 0.055
		m.Quad("car.yellow", art.FaceTop, geom.ShadeTop,
			geom.V(0.5+dx*r0+px*w, 0.5+dy*r0+py*w, z),
			geom.V(0.5+dx*r1, 0.5+dy*r1, z),
			geom.V(0.5+dx*r0-px*w, 0.5+dy*r0-py*w, z),
			geom.V(0.5+dx*r0-px*w, 0.5+dy*r0-py*w, z))
		// 臂尖亮点
		m.Box(0.5+dx*r1-0.025, 0.5+dy*r1-0.025, z-0.02, 0.5+dx*r1+0.025, 0.5+dy*r1+0.025, z+0.02, "trim.white")
	}
	m.Ellipsoid(0.5, 0.5, z, 0.075, 0.075, 0.075, 3, 8, 0.02, 1081, "trim.white")
	m.Ellipsoid(0.5+jx, 0.5+jx, z+0.03, 0.04, 0.04, 0.04, 2, 6, 0.02, 1082, "car.yellow")
}

// ------------------------------------------------------------------ 9 沟通

// buildSpiritCloud 沟通精灵：云朵 —— 四团叠起的椭球 + 上浮的对话气泡。
// 动画：极缓漂浮（振幅最小），节奏靠气泡上浮区分。
func buildSpiritCloud(m *geom.Mesh, f int) {
	z := spiritBob(f, 0.02, spiritBase["spirit.cloud"], 0.0)
	m.Ellipsoid(0.5, 0.5, z, 0.22, 0.20, 0.115, 3, 10, 0.05, 1091, "trim.white")
	m.Ellipsoid(0.40, 0.47, z+0.055, 0.115, 0.115, 0.09, 3, 8, 0.05, 1092, "trim.white")
	m.Ellipsoid(0.58, 0.45, z+0.045, 0.105, 0.105, 0.085, 3, 8, 0.05, 1093, "trim.white")
	m.Ellipsoid(0.50, 0.62, z+0.03, 0.10, 0.10, 0.08, 3, 8, 0.05, 1094, "trim.white")
	m.Box(0.5-0.14, 0.5-0.14, z-0.085, 0.5+0.14, 0.5+0.14, z-0.055, "wall.siding.white")
	// 三个气泡：按相位依次上浮（点状，1x 下是云朵上方的小亮块）
	for i := 0; i < 3; i++ {
		t := math.Mod(float64(f%4)/4+float64(i)/3, 1.0)
		bz := z + 0.20 + 0.16*t
		m.Box(0.5-0.03, 0.5-0.03, bz, 0.5+0.03, 0.5+0.03, bz+0.05, "car.teal")
	}
}

// ------------------------------------------------------------------ 10 拆分

// buildSpiritPuzzle 拆分精灵：两块拼图各自是 L 形，一左一右。
// 动画：两半按相位分开/合拢（相位 0 合、2 分），是唯一「形变」的精灵。
func buildSpiritPuzzle(m *geom.Mesh, f int) {
	z := spiritBob(f, 0.03, spiritBase["spirit.puzzle"], 0.15)
	sep := 0.10 * math.Sin(2*math.Pi*float64(f%4)/4)
	t := 0.062 // 半厚
	// 左半：竖条 + 顶部向右的横臂 + 一个凸耳
	lx := 0.5 - 0.145 - sep
	m.Box(lx-t, 0.5-t, z-0.15, lx+t, 0.5+t, z+0.15, "car.teal")
	m.Box(lx, 0.5-t, z+0.07, lx+0.095, 0.5+t, z+0.15, "car.teal")
	m.Box(lx+0.095-t*0.6, 0.5-t*0.7, z-0.02, lx+0.095+t*1.2, 0.5+t*0.7, z+0.07, "car.yellow")
	// 右半：竖条 + 底部向左的横臂 + 与凸耳互补的缺口
	rx := 0.5 + 0.145 + sep
	m.Box(rx-t, 0.5-t, z-0.15, rx+t, 0.5+t, z+0.15, "car.teal")
	m.Box(rx-0.095, 0.5-t, z-0.15, rx, 0.5+t, z-0.07, "car.teal")
	m.Box(rx-0.095-t*1.2, 0.5-t*0.7, z-0.15, rx-0.095+t*0.6, 0.5+t*0.7, z-0.10, "car.yellow")
}

// ------------------------------------------------------------------ 11 授权

// buildSpiritKey 授权精灵：钥匙 —— 圆环头 + 长杆 + 两枚齿 + 侧旁一枚锁孔片。
// 动画：每帧 18° 缓速旋转（转一整圈要 20 帧，比齿轮慢）。
func buildSpiritKey(m *geom.Mesh, f int) {
	z := spiritBob(f, 0.04, spiritBase["spirit.key"], 0.45)
	ring(m, 0.5, 0.5, z+0.20, 0.10, 18*math.Pi/180*float64(f%4))
	spiritSegment(m, geom.V(0.5, 0.5, z+0.10), geom.V(0.5, 0.5, z-0.24), 0.030, "car.yellow")
	// 齿：杆下端的两枚横向短齿
	m.Box(0.5, 0.5-0.022, z-0.20, 0.5+0.085, 0.5+0.022, z-0.155, "car.yellow")
	m.Box(0.5, 0.5-0.022, z-0.115, 0.5+0.065, 0.5+0.022, z-0.075, "car.yellow")
	// 锁孔片：钥匙旁边浮着一块带孔的亮片（授权语义）
	m.Box(0.5+0.20, 0.5-0.055, z-0.06, 0.5+0.33, 0.5+0.055, z+0.06, "chrome")
	m.Box(0.5+0.245, 0.5-0.055, z-0.03, 0.5+0.285, 0.5+0.055, z+0.03, "trim.dark")
}

// ring 一个由小方块排成的圆环（材质表里没有环形件），绕中心逐块旋转。
func ring(m *geom.Mesh, cx, cy, cz, r, phase float64) {
	const seg = 12
	for i := 0; i < seg; i++ {
		a := 2*math.Pi*float64(i)/seg + phase
		px, py := cx+r*math.Cos(a), cy+r*math.Sin(a)
		d := 0.03
		m.Box(px-d, py-d, cz-d, px+d, py+d, cz+d, "car.yellow")
	}
	// 一个偏心亮点，让「在旋转」看得出来
	a := phase * 1.7
	m.Box(cx+r*0.55*math.Cos(a)-0.022, cy+r*0.55*math.Sin(a)-0.022, cz+0.01,
		cx+r*0.55*math.Cos(a)+0.022, cy+r*0.55*math.Sin(a)+0.022, cz+0.05, "trim.white")
}

// ------------------------------------------------------------------ 12 生息

// buildSpiritLeaf 生息精灵：叶片 + 叶柄。
// 动画：绕叶柄摆动 + 侧向漂移（像被风吹），是唯一「横向漂移」的精灵。
func buildSpiritLeaf(m *geom.Mesh, f int) {
	z := spiritBob(f, 0.05, spiritBase["spirit.leaf"], 0.6)
	sway := 0.06 * math.Sin(2*math.Pi*float64(f%4)/4)
	m.Quad("leaf.spring", art.FaceTop, geom.ShadeTop,
		geom.V(0.34+sway, 0.5, z+0.06), geom.V(0.50+sway, 0.5, z+0.20),
		geom.V(0.70+sway, 0.5, z+0.05), geom.V(0.52+sway, 0.5, z-0.13))
	m.Quad("leaf.dark", art.FaceTop, geom.ShadeTop,
		geom.V(0.34+sway, 0.5, z+0.06), geom.V(0.52+sway, 0.5, z-0.13),
		geom.V(0.70+sway, 0.5, z+0.05), geom.V(0.50+sway, 0.5, z+0.01))
	// 叶脉：一条亮色细线（1x 下就是叶片中间的一列亮点）
	m.Box(0.36+sway, 0.5-0.012, z+0.055, 0.68+sway, 0.5+0.012, z+0.075, "trim.white")
	// 叶柄
	spiritSegment(m, geom.V(0.36+sway, 0.5, z+0.06), geom.V(0.28+sway*0.6, 0.5, z-0.02), 0.022, "trunk")
	// 两粒飘落的花粉
	for i := 0; i < 2; i++ {
		t := math.Mod(float64(f%4)/4+float64(i)*0.5, 1.0)
		m.Box(0.5+sway-0.02+t*0.18, 0.5-0.02, z-0.16-0.10*t, 0.5+sway+0.02+t*0.18, 0.5+0.02, z-0.12-0.10*t, "car.yellow")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// C. 动物
//
// 四只，全部朝 +Y 建模（脸朝镜头），再用 RotateZ90(dir) 转向：
//
//	animal.cat   猫   8 帧 = 4 向 × 2（静 / 走），低伏 + 竖尾 + 背部虎斑
//	animal.dog   狗   8 帧 = 4 向 × 2（静 / 走），粗壮 + 立耳 + 宽吻 + 项圈
//	animal.bird  鸟  12 帧 = 4 向 × 3 振翅相位，悬浮（离地 0.62）
//	animal.duck  鸭   8 帧 = 4 向 × 2（静 / 摇），扁嘴 + 长颈 + 橙蹼
//
// 立体感来自「躯干 + 头 + 四条腿 + 尾」四段独立体量，而不是一块扁盒子：
// 猫/狗/鸭的腿有膝关节（两段），翼类有翼面与尾羽。
// ─────────────────────────────────────────────────────────────────────────────

// animalSpec 是一只动物的图纸。
type animalSpec struct {
	Name    string
	Label   string
	Frames  int
	DirStep int // 每几个帧换一个方向（猫狗鸭 = 2，鸟 = 3）
	Build   func(m *geom.Mesh, f int)
}

func animalDefs() []Def {
	specs := []animalSpec{
		{"animal.cat", "猫", 8, 2, buildCat},
		{"animal.dog", "狗", 8, 2, buildDog},
		{"animal.duck", "鸭", 8, 2, buildDuck},
		{"animal.bird", "鸟", 12, 3, buildBird},
	}
	out := make([]Def, 0, len(specs))
	for _, sp := range specs {
		sp := sp
		out = append(out, animDef(sp.Name, sp.Label, "动物", sp.Frames,
			func(m *geom.Mesh, f int) { sp.Build(m, f) },
			func(m *geom.Mesh, f int) {
				sp.Build(m, f)
				m.RotateZ90(f / sp.DirStep)
			}))
	}
	return out
}

// animalLeg 一条四足动物的腿：上段（粗）+ 下段（细）+ 爪。
// knee 让腿在走动帧里有一点弯，静止帧完全伸直（脚底贴地）。
func animalLeg(m *geom.Mesh, x, y, hipZ, theta, w float64, fur, paw string) {
	hip := geom.V(x, y, hipZ)
	knee := rotYZ(geom.V(x, y, hipZ*0.55), hip, theta)
	foot := rotYZ(geom.V(x, y, 0.035), knee, -theta*0.7)
	limbSeg(m, hip, knee, w, w*0.82, fur)
	limbSeg(m, knee, foot, w*0.82, w*0.72, fur)
	m.Box(x-w, y-w*1.5, 0, x+w, y+w*1.7, 0.045, paw)
}

// animalTail 一条上翘的尾巴：三段渐细的方杆绕尾根摆。
func animalTail(m *geom.Mesh, x, y, z, swing float64, mat string) {
	p0 := geom.V(x, y, z)
	a1 := swing - 0.75
	a2 := swing - 0.35
	p1 := geom.V(p0.X, p0.Y+0.13*math.Cos(a1), p0.Z+0.13*math.Sin(a1)+0.10)
	p2 := geom.V(p1.X, p1.Y+0.11*math.Cos(a2), p1.Z+0.11*math.Sin(a2)+0.10)
	limbSeg(m, p0, p1, 0.028, 0.024, mat)
	limbSeg(m, p1, p2, 0.024, 0.018, mat)
}

// ------------------------------------------------------------------ 猫

// buildCat 猫：躯干 + 头 + 三角耳 + 竖尾 + 背部虎斑。
// frame 0 = 静止（腿伸直、尾巴停），frame 1 = 走（对角步、尾巴抬起）。
func buildCat(m *geom.Mesh, f int) {
	walk := f%2 == 1
	// 躯干：略扁的椭球（比狗更窄更伏）
	m.Ellipsoid(0.5, 0.50, 0.30, 0.135, 0.245, 0.135, 3, 10, 0.03, 1201, "wall.metal")
	// 背部虎斑：三道深色横带（1x 下把猫和鸭/狗分开的关键）
	for i, y := range []float64{0.36, 0.50, 0.64} {
		z := 0.42 - math.Abs(y-0.50)*0.25
		m.Box(0.5-0.10, y-0.028, z, 0.5+0.10, y+0.028, z+0.025, "trim.dark")
		_ = i
	}
	// 胸/腹白斑
	m.Box(0.5-0.085, 0.70, 0.16, 0.5+0.085, 0.79, 0.30, "trim.white")
	// 头
	m.Ellipsoid(0.5, 0.72, 0.47, 0.115, 0.115, 0.105, 3, 10, 0.03, 1202, "wall.metal")
	// 三角耳（压扁椭球，比狗更小更尖）
	for _, sx := range [2]float64{-1, 1} {
		m.Ellipsoid(0.5+sx*0.068, 0.685, 0.545, 0.040, 0.032, 0.055, 2, 6, 0.05, 1203, "wall.metal")
		m.Ellipsoid(0.5+sx*0.068, 0.700, 0.545, 0.022, 0.018, 0.030, 2, 6, 0.03, 1204, "car.dark")
	}
	// 眼睛
	m.DecalY(0.822, 0.436, 0.468, 0.490, 0.522, "car.dark", 0.03)
	m.DecalY(0.822, 0.532, 0.564, 0.490, 0.522, "car.dark", 0.03)
	// 鼻头
	m.Box(0.5-0.026, 0.822, 0.432, 0.5+0.026, 0.842, 0.462, "car.dark")
	// 四足：对角步
	th := 0.30
	if !walk {
		th = 0
	}
	animalLeg(m, 0.415, 0.665, 0.34, th, 0.034, "wall.metal", "trim.white")
	animalLeg(m, 0.585, 0.665, 0.34, -th, 0.034, "wall.metal", "trim.white")
	animalLeg(m, 0.415, 0.360, 0.34, -th, 0.034, "wall.metal", "trim.white")
	animalLeg(m, 0.585, 0.360, 0.34, th, 0.034, "wall.metal", "trim.white")
	// 尾巴：静止时直竖，走时向后甩
	sw := 0.0
	if walk {
		sw = 0.55
	}
	animalTail(m, 0.5, 0.245, 0.34, sw, "wall.metal")
}

// ------------------------------------------------------------------ 狗

// buildDog 狗：比猫大、吻部长、立耳、项圈 + 吊牌，尾巴短而直。
func buildDog(m *geom.Mesh, f int) {
	walk := f%2 == 1
	m.Ellipsoid(0.5, 0.50, 0.38, 0.165, 0.275, 0.170, 3, 10, 0.03, 1211, "roof.tile.brown")
	// 背部深色鞍纹
	m.Box(0.5-0.13, 0.36, 0.515, 0.5+0.13, 0.62, 0.545, "wall.brick.dark")
	// 头 + 宽吻
	m.Ellipsoid(0.5, 0.775, 0.60, 0.135, 0.135, 0.125, 3, 10, 0.03, 1212, "roof.tile.brown")
	m.Box(0.5-0.075, 0.86, 0.545, 0.5+0.075, 0.985, 0.645, "roof.tile.brown")
	m.Box(0.5-0.052, 0.975, 0.585, 0.5+0.052, 1.000, 0.630, "car.dark")
	// 立耳（比猫更大更挺）
	for _, sx := range [2]float64{-1, 1} {
		m.Box(0.5+sx*0.105-0.032, 0.735, 0.665, 0.5+sx*0.105+0.032, 0.800, 0.775, "wall.brick.dark")
	}
	// 眼
	m.DecalY(0.905, 0.432, 0.472, 0.625, 0.665, "car.dark", 0.03)
	m.DecalY(0.905, 0.528, 0.568, 0.625, 0.665, "car.dark", 0.03)
	// 项圈 + 吊牌（1x 下是脖子上的一道亮线）
	m.Box(0.5-0.115, 0.82, 0.500, 0.5+0.115, 0.855, 0.545, "car.red")
	m.Box(0.5-0.03, 0.855, 0.455, 0.5+0.03, 0.885, 0.510, "car.yellow")
	// 四足
	th := 0.32
	if !walk {
		th = 0
	}
	for _, p := range [][3]float64{{0.40, 0.68, 1}, {0.60, 0.68, -1}, {0.40, 0.35, -1}, {0.60, 0.35, 1}} {
		animalLeg(m, p[0], p[1], 0.42, th*p[2], 0.042, "roof.tile.brown", "wall.siding.sand")
	}
	sw := 0.0
	if walk {
		sw = -0.45
	}
	animalTail(m, 0.5, 0.235, 0.44, sw, "wall.brick.dark")
}

// ------------------------------------------------------------------ 鸭

// buildDuck 鸭：肥圆躯干 + 长颈 + 扁长嘴 + 橙蹼，走路左右摇摆。
func buildDuck(m *geom.Mesh, f int) {
	walk := f%2 == 1
	lean := 0.0
	if walk {
		lean = 0.045
	}
	// 躯干（肥圆）+ 尾羽
	m.Ellipsoid(0.5, 0.47, 0.36, 0.155, 0.240, 0.175, 3, 10, 0.03, 1221, "trim.white")
	m.Quad("wall.siding.white", art.FaceTop, geom.ShadeTop,
		geom.V(0.5-0.10, 0.24, 0.44), geom.V(0.5+0.10, 0.24, 0.44),
		geom.V(0.5+0.16, 0.14, 0.50), geom.V(0.5-0.16, 0.14, 0.50))
	// 颈 + 头（向前上方探）
	limbSeg(m, geom.V(0.5, 0.62, 0.50), geom.V(0.5, 0.70+lean*0.4, 0.72), 0.075, 0.070, "trim.white")
	m.Ellipsoid(0.5, 0.74+lean*0.5, 0.78, 0.105, 0.100, 0.098, 3, 10, 0.03, 1222, "car.teal")
	// 扁长嘴（招牌特征）
	m.Box(0.5-0.085, 0.80, 0.742, 0.5+0.085, 0.965, 0.792, "car.yellow")
	m.Box(0.5-0.085, 0.80, 0.772, 0.5+0.085, 0.965, 0.792, "roof.tile.brown")
	// 眼
	m.DecalY(0.842, 0.424, 0.462, 0.800, 0.838, "car.dark", 0.03)
	m.DecalY(0.842, 0.538, 0.576, 0.800, 0.838, "car.dark", 0.03)
	// 两腿 + 橙蹼（走路时左右腿反向摆）
	th := 0.34
	if !walk {
		th = 0
	}
	for _, p := range [][2]float64{{0.42, 1}, {0.58, -1}} {
		x := p[0]
		hip := geom.V(x, 0.50, 0.30)
		knee := rotYZ(geom.V(x, 0.50, 0.14), hip, th*p[1])
		limbSeg(m, hip, knee, 0.032, 0.028, "car.yellow")
		m.Box(x-0.06, knee.Y-0.02, 0, x+0.06, knee.Y+0.12, 0.035, "car.yellow")
	}
}

// ------------------------------------------------------------------ 鸟

// buildBird 鸟：悬浮飞行，翅膀三相位（上举 / 平展 / 下压）。
// f = dir*3 + wing（DirStep = 3），所以 12 帧 = 4 向 × 3 相位。
func buildBird(m *geom.Mesh, f int) {
	wing := f % 3
	lift := 0.72
	if wing == 0 {
		lift = 0.78
	} else if wing == 2 {
		lift = 0.66
	}
	// 身体
	m.Ellipsoid(0.5, 0.48, lift+0.10, 0.105, 0.155, 0.115, 3, 10, 0.03, 1231, "car.teal")
	// 尾羽
	m.Box(0.5-0.075, 0.28, lift+0.06, 0.5+0.075, 0.38, lift+0.13, "wall.tile.teal")
	// 头 + 喙 + 冠羽
	m.Ellipsoid(0.5, 0.665, lift+0.19, 0.085, 0.085, 0.082, 3, 10, 0.03, 1232, "car.teal")
	m.Box(0.5-0.036, 0.725, lift+0.165, 0.5+0.036, 0.815, lift+0.215, "car.yellow")
	m.Box(0.5-0.030, 0.640, lift+0.255, 0.5+0.030, 0.700, lift+0.320, "car.red")
	// 眼
	m.DecalY(0.748, 0.442, 0.470, lift+0.185, lift+0.222, "car.dark", 0.03)
	m.DecalY(0.748, 0.530, 0.558, lift+0.185, lift+0.222, "car.dark", 0.03)
	// 双翼：三相位上下扑动（左翼 +、右翼 -，读起来是「扑」而不是「扇」）
	wa := [3]float64{-40, -2, 34}[wing]
	birdWing(m, 0.44, lift+0.15, wa, "wall.tile.white")
	birdWing(m, 0.56, lift+0.15, -wa, "wall.siding.white")
	// 收起的双爪
	m.Box(0.5-0.075, 0.500, lift-0.01, 0.5-0.02, 0.545, lift+0.04, "car.yellow")
	m.Box(0.5+0.02, 0.500, lift-0.01, 0.5+0.075, 0.545, lift+0.04, "car.yellow")
}

// birdWing 一片鸟翼：绕体侧枢轴在 y-z 平面内扑动。
func birdWing(m *geom.Mesh, x, z, deg float64, mat string) {
	a := deg * math.Pi / 180
	// 翼面在 y-z 平面里张开（鸟朝 +Y，所以翼是横向的）
	m.Quad(mat, art.FaceTop, geom.ShadeTop,
		geom.V(x-0.02, 0.44, z-0.02), geom.V(x+0.02, 0.44, z-0.02),
		geom.V(x+0.02, 0.44+0.22*math.Cos(a), z+0.22*math.Sin(a)),
		geom.V(x-0.02, 0.44+0.22*math.Cos(a), z+0.22*math.Sin(a)))
	m.Quad(mat, art.FaceTop, geom.ShadeTop,
		geom.V(x-0.02, 0.52, z-0.02), geom.V(x+0.02, 0.52, z-0.02),
		geom.V(x+0.02, 0.52+0.22*math.Cos(a), z+0.22*math.Sin(a)),
		geom.V(x-0.02, 0.52+0.22*math.Cos(a), z+0.22*math.Sin(a)))
	m.Quad(mat, art.FaceTop, geom.ShadeTop,
		geom.V(x-0.02, 0.44, z-0.02), geom.V(x-0.02, 0.52, z-0.02),
		geom.V(x-0.02, 0.52+0.22*math.Cos(a), z+0.22*math.Sin(a)),
		geom.V(x-0.02, 0.44+0.22*math.Cos(a), z+0.22*math.Sin(a)))
}
