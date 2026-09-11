package art

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// rampDef 定义一条色阶：由暗到亮。整个管线（精灵图、地形、UI、CRT 渐变）
// 共用这一份母板，这是「和谐观感」的根源。
type rampDef struct {
	Name  string
	Hexes []string
}

// rampDefs 是 97 色母板（21 条色阶去重后）。色相与明度按 DOS/VGA 与 PC-98 时代的共性挑选：
// 低饱和的暖灰/砖红/土黄 + 冷灰/青绿，外加三组 PC-98 标志性的高饱和青/品红点缀。
var rampDefs = []rampDef{
	{"ink", []string{"0a0a0e", "13131b", "1d1e2a", "282a39", "35384c"}},
	{"shadow", []string{"2b2f44", "3b4059", "4c5272", "5f668b"}},
	{"grey", []string{"5c6070", "6d7280", "848a98", "9ba1ae", "b3b9c5", "c9cdd7"}},
	{"stone", []string{"6f6152", "877665", "a08d79", "b8a48f", "cebba7", "e2d3c1"}},
	{"brick", []string{"5e2b20", "7d3a29", "9d4c35", "ba6346", "d07f60"}},
	{"roof", []string{"6d2424", "8c2f2c", "ab4038", "c75447", "dc6d5b"}},
	{"wood", []string{"3f2b1c", "553a26", "6f4d31", "8a6340", "a67d52"}},
	{"dirt", []string{"3a2d1f", "4e3d29", "654f34", "7e6442", "98805a"}},
	{"sand", []string{"7d6841", "978054", "b09a6b", "c7b283", "dcc999"}},
	{"grass", []string{"2b4426", "38572c", "476d36", "588441", "6b9c50", "82b465"}},
	{"foliage", []string{"1b3520", "27482c", "345a37", "426f43", "52874f"}},
	{"water", []string{"122a38", "1a3d4c", "245363", "2f6b7a", "3c8492", "4d9da8"}},
	{"sky", []string{"3f5f80", "5c7f9e", "7ba0bb", "9dc0d4", "c2dbe8"}},
	{"asphalt", []string{"1e2026", "2d3038", "3e424c", "515662"}},
	{"steel", []string{"475064", "69717f", "858e9c", "a2abb8", "c0c8d3"}},
	{"gold", []string{"4a3510", "7d5a17", "b4861f", "ddb038", "f7dc7a"}},
	{"copper", []string{"2f6b5a", "3f8a72", "58a98c"}},
	{"teal", []string{"1f8a8a", "2fbfb0", "7fdccb"}},
	{"magenta", []string{"8a2f7a", "c0449f", "e07fc4"}},
	{"violet", []string{"3d2a5c", "57407e", "7360a3"}},
	{"neon", []string{"ffffff", "f3ead6", "9fe8e0"}},
}

// Palette 是不可变的有序色板。
type Palette struct {
	Colors []RGBA
	Names  []string // 与 Colors 等长，形如 "brick/2"
	ramps  map[string][]int
	index  map[string]int
	lut    *LUT
	Source []string // 与 Colors 等长的 "ramp/step" 出处
}

var palette = newPalette()

// Pal 返回全局母板。
func Pal() *Palette { return palette }

func newPalette() *Palette {
	p := &Palette{
		ramps: map[string][]int{},
		index: map[string]int{},
	}
	seen := map[string]int{}
	for _, rd := range rampDefs {
		for i, h := range rd.Hexes {
			c := Hex(h)
			key := fmt.Sprintf("%02x%02x%02x", c.R, c.G, c.B)
			idx, ok := seen[key]
			if !ok {
				idx = len(p.Colors)
				seen[key] = idx
				p.Colors = append(p.Colors, c)
				p.Names = append(p.Names, fmt.Sprintf("%s/%d", rd.Name, i))
				p.Source = append(p.Source, rd.Name)
				p.index[rd.Name+"/"+fmt.Sprint(i)] = idx
			}
			p.ramps[rd.Name] = append(p.ramps[rd.Name], idx)
		}
	}
	// 让每条色阶都单调由暗到亮，便于按明度定位。
	for name, r := range p.ramps {
		sort.SliceStable(r, func(i, j int) bool { return luma(p.Colors[r[i]]) < luma(p.Colors[r[j]]) })
		p.ramps[name] = r
	}
	return p
}

func luma(c RGBA) float64 {
	return 0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)
}

// Len 返回色板条目数。
func (p *Palette) Len() int { return len(p.Colors) }

// Ramp 返回指定色阶的色板下标（由暗到亮）。
func (p *Palette) Ramp(name string) []int {
	r, ok := p.ramps[name]
	if !ok {
		panic("art: 未知色阶 " + name)
	}
	return r
}

// HasRamp 报告色阶是否存在。
func (p *Palette) HasRamp(name string) bool { _, ok := p.ramps[name]; return ok }

// Color 解析 "ramp" (取中间色) 或 "ramp/i" 形式。
func (p *Palette) Color(spec string) RGBA {
	if i, ok := p.index[spec]; ok {
		return p.Colors[i]
	}
	if r, ok := p.ramps[spec]; ok {
		return p.Colors[r[len(r)/2]]
	}
	panic("art: 未知名色 " + spec)
}

// Snap 把明暗位置 pos(0..1) 映射到色阶上的某一档（硬边，不改色相）。
func (p *Palette) Snap(ramp string, pos float32) RGBA {
	r := p.Ramp(ramp)
	n := len(r)
	if n == 1 {
		return p.Colors[r[0]]
	}
	i := int(math.Round(float64(Clamp01(pos) * float32(n-1))))
	return p.Colors[r[ClampInt(i, 0, n-1)]]
}

// LerpRamp 在色阶上做连续插值，用于需要平滑渐变的场合（再由抖动量化成网点）。
func (p *Palette) LerpRamp(ramp string, pos float32) RGBA {
	r := p.Ramp(ramp)
	n := len(r)
	if n == 1 {
		return p.Colors[r[0]]
	}
	pos = Clamp01(pos)
	f := pos * float32(n-1)
	i := int(f)
	if i >= n-1 {
		return p.Colors[r[n-1]]
	}
	return Lerp(p.Colors[r[i]], p.Colors[r[i+1]], f-float32(i))
}

// Nearest 在母板上做加权最近色查找（权重 2/4/3 近似人眼敏感度）。
func (p *Palette) Nearest(r, g, b float32) int {
	best, bestD := 0, float32(math.MaxFloat32)
	for i, c := range p.Colors {
		dr := r - float32(c.R)
		dg := g - float32(c.G)
		db := b - float32(c.B)
		d := 2*dr*dr + 4*dg*dg + 3*db*db
		if d < bestD {
			bestD, best = d, i
		}
	}
	return best
}

// LUT 是 6-6-6 位的最近色查找表，供逐像素量化使用。
// 用 6 位而不是 5 位：母板里存在若干「近邻色」（如 ink 与 asphalt 的暗阶），
// 5 位截断会把它们折进同一格，量化往返就会漂移。
type LUT struct {
	Idx []uint8
}

// LUTBits 是查找表每通道的位数。
const LUTBits = 6

const lutLevels = 1 << LUTBits

// BuildLUT 构建查找表（一次性，约 25M 次距离比较）。
func (p *Palette) BuildLUT() *LUT {
	l := &LUT{Idx: make([]uint8, lutLevels*lutLevels*lutLevels)}
	shift := 8 - LUTBits
	for r := 0; r < lutLevels; r++ {
		for g := 0; g < lutLevels; g++ {
			for b := 0; b < lutLevels; b++ {
				idx := p.Nearest(
					float32(r)*255/(lutLevels-1),
					float32(g)*255/(lutLevels-1),
					float32(b)*255/(lutLevels-1))
				l.Idx[r<<(2*LUTBits)|g<<LUTBits|b] = uint8(idx)
			}
		}
	}
	_ = shift
	// 每个色板颜色直接钉住自己所在的格：
	// 这样「把母板颜色再量化一次」必然回到自身，不会因为截断漂到邻近色。
	// 前提是任意两色至少差 4（见 TestPaletteMinSeparation）。
	const sh = 8 - LUTBits
	for i, c := range p.Colors {
		l.Idx[(int(c.R)>>sh)<<(2*LUTBits)|(int(c.G)>>sh)<<LUTBits|int(c.B)>>sh] = uint8(i)
	}
	p.lut = l
	return l
}

// LUT 返回（必要时构建）查找表。
func (p *Palette) LUTRef() *LUT {
	if p.lut == nil {
		return p.BuildLUT()
	}
	return p.lut
}

// Quantize 把浮点颜色量化到色板，thr 是 [-0.5,0.5] 的有序抖动阈值。
//
// 抖动幅度刻意取得比「半档色阶」小：平涂面必须量化回同一档（保持干净的色块），
// 只有抗锯齿边、半透明阴影这类连续渐变才需要网点过渡。
func (p *Palette) Quantize(r, g, b, thr float32) int {
	return p.QuantizeAmp(r, g, b, thr, 7)
}

// QuantizeAmp 用指定的抖动幅度量化。
// 平涂面用小幅度（保持干净色块），大面积渐变色（天空、水面）需要接近一档色阶的
// 大幅度，否则相邻色阶之间会出现硬边横带。
func (p *Palette) QuantizeAmp(r, g, b, thr float32, amp float32) int {
	r = clamp255(r*255 + thr*amp)
	g = clamp255(g*255 + thr*amp)
	b = clamp255(b*255 + thr*amp)
	l := p.LUTRef()
	const sh = 8 - LUTBits
	return int(l.Idx[(int(r)>>sh)<<(2*LUTBits)|(int(g)>>sh)<<LUTBits|int(b)>>sh])
}

func clamp255(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// ---------- 导出给 Web 运行时 ----------

type jsonColor struct {
	Hex  string `json:"hex"`
	Name string `json:"name"`
	Ramp string `json:"ramp"`
	R    int    `json:"r"`
	G    int    `json:"g"`
	B    int    `json:"b"`
}

type jsonPalette struct {
	Name   string         `json:"name"`
	Count  int            `json:"count"`
	Colors []jsonColor    `json:"colors"`
	Ramps  [][]int        `json:"ramps"`
	RampID map[string]int `json:"rampId"`
}

// ToJSON 输出运行时可直接加载的色板描述。
func (p *Palette) ToJSON(name string) ([]byte, error) {
	out := jsonPalette{Name: name, Count: len(p.Colors)}
	for i, c := range p.Colors {
		ramp := strings.Split(p.Names[i], "/")[0]
		out.Colors = append(out.Colors, jsonColor{
			Hex: c.String(), Name: p.Names[i], Ramp: ramp,
			R: int(c.R), G: int(c.G), B: int(c.B),
		})
	}
	keys := make([]string, 0, len(p.ramps))
	for k := range p.ramps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out.RampID = map[string]int{}
	for _, k := range keys {
		out.RampID[k] = len(out.Ramps)
		out.Ramps = append(out.Ramps, p.ramps[k])
	}
	return json.MarshalIndent(out, "", "  ")
}
