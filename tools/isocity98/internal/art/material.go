package art

// 明暗槽位：本管线的光照是美术定向的（不是物理光照），
// 固定「顶面最亮 → +Y 面（屏幕左下，受光侧）次之 → +X 面（屏幕右下，背光侧）最暗」，
// 这是 PC-98 / 90 年代等距美术的通行惯例。
const (
	ShadeTop    = iota
	ShadeLeft   // +Y 面
	ShadeRight  // +X 面
	ShadeBottom // 底面，正常不可见
)

// Material 是一个「平面着色材质」：色阶 + 面朝向明暗位置 + 程序化图案。
// 颜色永远从母板色阶上取，所以精灵图天然被限制在有限色板内。
//
// 明暗位置约定（见 tune 系数）：
//
//	顶面 ≈ 0.9 档、受光侧 ≈ 0.5 档、背光侧 ≈ 0.2 档，
//	这样 5~6 档色阶上正好落成「亮/中/暗」三个分离的色调，
//	而不是挤在相邻两档里（那会让立方体看起来是平的）。
type Material struct {
	Name    string
	Ramp    string
	Shade   [4]float32 // 各朝向在色阶上的位置 0..1
	Pattern string
	Alpha   float32

	// 自发光（夜间窗户、霓虹招牌）：图案返回值 >0 时点亮，<0 时用 OffRamp。
	Emissive bool
	OffRamp  string
	Glow     float32
}

// Profile 是一次渲染的「光照档」，同一套材质在不同档下呈现白天/夜晚。
// 这类风格化管线不做真实光照，只做整体的明暗缩放 + 色调偏移。
type Profile struct {
	Name       string
	ShadeScale float32 // 整体明暗缩放
	Tint       RGBA    // 色调偏移目标
	TintAmt    float32 // 偏移强度
	WindowsLit bool    // 是否把窗户材质替换为自发光
	GlowGain   float32 // 自发光辉光强度
	ShadowA    float32 // 投影 alpha
	Mats       map[string]Material
}

func (p *Profile) Get(name string) (Material, bool) {
	m, ok := p.Mats[name]
	return m, ok
}

// Names 返回该档下所有材质名（用于流水线自检）。
func (p *Profile) Names() []string {
	out := make([]string, 0, len(p.Mats))
	for k := range p.Mats {
		out = append(out, k)
	}
	return out
}

func mat(name, ramp, pattern string, top, left, right float32) Material {
	return Material{
		Name: name, Ramp: ramp, Pattern: pattern, Alpha: 1,
		Shade: [4]float32{top, left, right, 0.12},
	}
}

// baseMaterials 是白天档的完整材质表；夜晚档在此基础上做两次加工：
// 整体压暗偏蓝（Profile 的 ShadeScale/Tint），并把窗户/招牌换成自发光版本。
var baseMaterials = []Material{
	// ---- 墙体 ----
	mat("wall.concrete", "grey", "concrete", 0.92, 0.50, 0.20),
	mat("wall.concrete.warm", "stone", "concrete", 0.84, 0.50, 0.22),
	mat("wall.stucco", "stone", "plaster", 0.92, 0.54, 0.24),
	mat("wall.stucco.pink", "roof", "plaster", 0.74, 0.44, 0.18),
	mat("wall.stucco.mint", "copper", "plaster", 0.82, 0.48, 0.22),
	mat("wall.brick", "brick", "brick", 0.82, 0.48, 0.20),
	mat("wall.brick.dark", "brick", "brick", 0.58, 0.34, 0.14),
	mat("wall.siding.blue", "sky", "siding", 0.64, 0.40, 0.18),
	mat("wall.siding.green", "grass", "siding", 0.74, 0.46, 0.20),
	mat("wall.siding.sand", "sand", "siding", 0.80, 0.48, 0.22),
	mat("wall.siding.white", "grey", "siding", 0.90, 0.52, 0.22),
	mat("wall.wood", "wood", "plank", 0.82, 0.48, 0.22),
	mat("wall.stone", "stone", "stonewall", 0.78, 0.44, 0.18),
	mat("wall.rock", "dirt", "stonewall", 0.62, 0.36, 0.14),
	mat("wall.tile.white", "grey", "tile", 0.92, 0.54, 0.24),
	mat("wall.tile.teal", "teal", "tile", 0.74, 0.44, 0.20),
	mat("wall.glass", "water", "glass", 0.74, 0.44, 0.18),
	mat("wall.glass.blue", "sky", "glass", 0.78, 0.46, 0.20),
	mat("wall.panel.teal", "teal", "metal", 0.74, 0.44, 0.20),
	mat("wall.metal", "steel", "metal", 0.76, 0.44, 0.18),
	mat("wall.metal.rust", "brick", "metal", 0.56, 0.34, 0.14),
	mat("wall.plaster.white", "grey", "plaster", 0.88, 0.54, 0.24),

	// ---- 屋顶 ----
	mat("roof.tile.red", "roof", "roofing", 0.86, 0.50, 0.22),
	mat("roof.tile.brown", "brick", "roofing", 0.80, 0.46, 0.20),
	mat("roof.slate", "steel", "roofing", 0.44, 0.26, 0.10),
	mat("roof.copper", "copper", "roofing", 0.86, 0.50, 0.22),
	mat("roof.gravel", "grey", "gravel", 0.86, 0.50, 0.22),
	mat("roof.shingle", "wood", "roofing", 0.76, 0.44, 0.18),
	mat("roof.metal", "steel", "roofing", 0.68, 0.40, 0.16),
	mat("roof.teal", "teal", "roofing", 0.82, 0.48, 0.20),
	mat("roof.felt", "steel", "asphalt", 0.36, 0.20, 0.06),
	mat("roof.green", "grass", "gravel", 0.80, 0.48, 0.20),

	// ---- 开口 ----
	mat("window", "sky", "glass", 0.30, 0.16, 0.02),
	mat("window.small", "sky", "glass", 0.26, 0.13, 0.02),
	mat("door.wood", "wood", "plank", 0.62, 0.38, 0.16),
	mat("door.metal", "steel", "metal", 0.62, 0.36, 0.14),
	mat("door.glass", "sky", "glass", 0.62, 0.38, 0.14),
	mat("garage", "grey", "siding", 0.72, 0.42, 0.18),

	// ---- 线脚 / 构件 ----
	mat("trim.white", "grey", "none", 0.96, 0.62, 0.30),
	mat("trim.cream", "stone", "none", 0.88, 0.56, 0.26),
	mat("trim.band", "grey", "none", 0.46, 0.30, 0.12),
	mat("trim.dark", "shadow", "none", 0.86, 0.56, 0.30),
	mat("trim.teal", "teal", "none", 0.80, 0.48, 0.22),
	mat("metal.pipe", "steel", "metal", 0.72, 0.42, 0.18),
	mat("metal.dark", "asphalt", "metal", 0.74, 0.46, 0.22),
	mat("awning.red", "roof", "siding", 0.82, 0.48, 0.20),
	mat("awning.green", "grass", "siding", 0.82, 0.48, 0.20),
	mat("awning.teal", "teal", "siding", 0.82, 0.48, 0.20),
	mat("crate.wood", "wood", "plank", 0.78, 0.46, 0.20),
	mat("concrete.pad", "grey", "concrete", 0.88, 0.50, 0.22),
	mat("concrete.curb", "grey", "concrete", 0.94, 0.56, 0.24),
	mat("sign.panel", "teal", "tile", 0.82, 0.48, 0.22),
	mat("sign.panel.dark", "shadow", "none", 0.70, 0.42, 0.18),
	mat("paint.white", "neon", "none", 0.50, 0.30, 0.10),

	// ---- 地面 ----
	mat("pad.grass", "grass", "turf", 0.92, 0.56, 0.26),
	mat("pad.grass.dark", "grass", "turf", 0.66, 0.40, 0.18),
	mat("pad.dirt", "dirt", "gravel", 0.82, 0.48, 0.22),
	mat("pad.sand", "sand", "gravel", 0.86, 0.50, 0.24),
	mat("pad.gravel", "sand", "gravel", 0.68, 0.40, 0.18),
	mat("pad.asphalt", "asphalt", "asphalt", 0.62, 0.38, 0.16),
	mat("pad.concrete", "grey", "concrete", 0.84, 0.50, 0.22),
	mat("pad.tile", "stone", "tile", 0.90, 0.52, 0.24),
	mat("pad.rock", "stone", "stonewall", 0.68, 0.40, 0.18),
	mat("pad.farm", "dirt", "plank", 0.78, 0.46, 0.20),
	mat("pad.snow", "grey", "turf", 1.00, 0.62, 0.30),
	mat("pad.ice", "sky", "glass", 0.86, 0.56, 0.30),
	mat("water", "water", "water", 0.72, 0.44, 0.20),
	mat("water.shallow", "water", "water", 0.90, 0.58, 0.30),
	mat("water.foam", "teal", "turf", 0.92, 0.62, 0.32),

	// ---- 植被 ----
	mat("trunk", "wood", "plank", 0.66, 0.40, 0.16),
	mat("leaf.spring", "foliage", "foliage", 0.88, 0.54, 0.24),
	mat("leaf.dark", "foliage", "foliage", 0.64, 0.40, 0.16),
	mat("leaf.olive", "grass", "foliage", 0.80, 0.48, 0.20),
	mat("leaf.autumn", "roof", "foliage", 0.88, 0.54, 0.24),
	mat("leaf.pine", "copper", "foliage", 0.82, 0.50, 0.22),
	mat("leaf.palm", "grass", "foliage", 0.86, 0.52, 0.24),
	mat("hedge", "foliage", "foliage", 0.80, 0.48, 0.20),

	// ---- 车辆 ----
	mat("car.red", "roof", "none", 0.90, 0.56, 0.26),
	mat("car.blue", "sky", "none", 0.86, 0.54, 0.24),
	mat("car.yellow", "gold", "none", 0.88, 0.54, 0.24),
	mat("car.white", "grey", "none", 0.96, 0.60, 0.28),
	mat("car.teal", "teal", "none", 0.86, 0.54, 0.24),
	mat("car.dark", "shadow", "none", 0.72, 0.46, 0.22),
	mat("car.glass", "sky", "glass", 0.56, 0.34, 0.12),
	mat("tire", "shadow", "none", 0.56, 0.34, 0.14),
	mat("chrome", "steel", "metal", 0.94, 0.64, 0.32),
}

// litMaterials 是夜间替换/新增的自发光材质。
var litMaterials = []Material{
	{Name: "window", Ramp: "gold", OffRamp: "water", Pattern: "windowlit",
		Shade: [4]float32{0.80, 0.62, 0.44, 0.3}, Emissive: true, Alpha: 1, Glow: 1.0},
	{Name: "window.small", Ramp: "gold", OffRamp: "water", Pattern: "windowlit2",
		Shade: [4]float32{0.78, 0.60, 0.42, 0.3}, Emissive: true, Alpha: 1, Glow: 0.9},
	{Name: "door.glass", Ramp: "gold", OffRamp: "water", Pattern: "windowlit2",
		Shade: [4]float32{0.70, 0.54, 0.36, 0.3}, Emissive: true, Alpha: 1, Glow: 0.7},
	{Name: "window.hall", Ramp: "neon", OffRamp: "water", Pattern: "windowlit2",
		Shade: [4]float32{0.70, 0.52, 0.34, 0.3}, Emissive: true, Alpha: 1, Glow: 0.7},
	{Name: "sign.neon.pink", Ramp: "magenta", OffRamp: "violet", Pattern: "neon",
		Shade: [4]float32{0.95, 0.70, 0.45, 0.3}, Emissive: true, Alpha: 1, Glow: 1.4},
	{Name: "sign.neon.cyan", Ramp: "teal", OffRamp: "water", Pattern: "neon",
		Shade: [4]float32{0.95, 0.70, 0.45, 0.3}, Emissive: true, Alpha: 1, Glow: 1.4},
	{Name: "streetlamp", Ramp: "gold", OffRamp: "steel", Pattern: "none",
		Shade: [4]float32{0.95, 0.74, 0.50, 0.3}, Emissive: true, Alpha: 1, Glow: 1.6},
}

func cloneMats(src []Material) map[string]Material {
	out := make(map[string]Material, len(src))
	for _, m := range src {
		out[m.Name] = m
	}
	return out
}

// Day 是白天渲染档。
var Day = &Profile{
	Name:       "day",
	ShadeScale: 1.0,
	WindowsLit: false,
	GlowGain:   0.0,
	ShadowA:    0.30,
	Mats:       cloneMats(baseMaterials),
}

// Night 是夜间渲染档：整体压暗并偏冷蓝，窗户/招牌自发光。
var Night = &Profile{
	Name:       "night",
	ShadeScale: 0.58,
	Tint:       Hex("3b4059"),
	TintAmt:    0.34,
	WindowsLit: true,
	GlowGain:   1.0,
	ShadowA:    0.20,
	Mats:       cloneMats(baseMaterials),
}

// 白天档的霓虹招牌与夜间档的备用材质（保证两档材质名集合完全一致）。
var dayExtra = []Material{
	{Name: "sign.neon.pink", Ramp: "magenta", OffRamp: "violet", Pattern: "none",
		Shade: [4]float32{0.66, 0.46, 0.30, 0.2}, Alpha: 1},
	{Name: "sign.neon.cyan", Ramp: "teal", OffRamp: "water", Pattern: "none",
		Shade: [4]float32{0.66, 0.46, 0.30, 0.2}, Alpha: 1},
	{Name: "window.hall", Ramp: "water", Pattern: "glass",
		Shade: [4]float32{0.60, 0.42, 0.26, 0.2}, Alpha: 1},
	{Name: "streetlamp", Ramp: "steel", Pattern: "metal",
		Shade: [4]float32{0.84, 0.54, 0.26, 0.2}, Alpha: 1},
}

func init() {
	for _, m := range dayExtra {
		Day.Mats[m.Name] = m
	}
	for _, m := range litMaterials {
		Night.Mats[m.Name] = m
	}
	validateProfiles()
}

// validateProfiles 保证两档材质名完全一致（几何体用材质名索引，缺一个就会渲染出错）。
func validateProfiles() {
	for name := range Day.Mats {
		if _, ok := Night.Mats[name]; !ok {
			panic("art: 夜间档缺少材质 " + name)
		}
	}
	for name := range Night.Mats {
		if _, ok := Day.Mats[name]; !ok {
			panic("art: 白天档缺少材质 " + name)
		}
	}
	for _, p := range []*Profile{Day, Night} {
		for _, m := range p.Mats {
			if !Pal().HasRamp(m.Ramp) {
				panic("art: 材质 " + m.Name + " 引用了未知色阶 " + m.Ramp)
			}
			if m.Pattern != "" && Pat(m.Pattern) == nil {
				panic("art: 材质 " + m.Name + " 引用了未知图案 " + m.Pattern)
			}
			if m.Emissive && m.OffRamp != "" && !Pal().HasRamp(m.OffRamp) {
				panic("art: 材质 " + m.Name + " 引用了未知熄灯色阶 " + m.OffRamp)
			}
		}
	}
}

// MaterialNames 返回全部材质名（两档一致）。
func MaterialNames() []string {
	out := make([]string, 0, len(Day.Mats))
	for k := range Day.Mats {
		out = append(out, k)
	}
	return out
}
