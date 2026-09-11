package main

import (
	"image"

	"isocity98/internal/art"
)

// 季节 / 昼夜的「调色板重映射」验证。
//
// 这是整套方案能不能成立的关键：素材是**有限色板 + 有序抖动**烘焙出来的，
// 因此换季/换时段不必重渲精灵图 —— 只要把每一档色阶重新映射到另一档，
// 整座小镇就换了颜色。代价是一次全图 LUT 扫描（1MB 图集 ≈ 26 万像素，可忽略），
// 而收益是 4 季 × 4 时段 = 16 种外观只对应 1 套烘焙资产。
//
// 注意这里只改「颜色」，不改「形体」——所以是重映射而非重绘：
// 常青树的形状不会因为入冬就变成光秃树，屋顶积雪要另用覆盖层（见文档）。

// tintRule 描述一档色阶如何被重映射：把 ramp 上第 i 档映到第 i 档的 offset 位置。
type tintRule struct {
	ramp   string
	offset int     // 整条色阶平移（正 = 变亮）
	amount float32 // 与目标色混合强度 0..1
	mix    art.RGBA
}

// seasonRules 是四季的色阶重映射规则。
//
// 强度标定：**目标是「一眼看出是哪个季节」，不是「好像有点不一样」**。
// 所以偏移给到 ±2、混色比例给到 0.5~0.85，并且覆盖足够多的色阶
// （草/树叶/天空/屋顶/土/石/灰/水），而不是只碰一两条。
func seasonRules(season string) []tintRule {
	switch season {
	case "spring": // 春樱：嫩绿 + 樱花粉，天光转暖
		return []tintRule{
			{ramp: "grass", offset: 1, amount: 0.35, mix: art.Hex("a8d060")},
			{ramp: "foliage", offset: 1, amount: 0.55, mix: art.Hex("e884c0")},
			{ramp: "sky", offset: 1, amount: 0.22, mix: art.Hex("bcd8f0")},
			{ramp: "dirt", offset: 1, amount: 0.20, mix: art.Hex("9c7a52")},
			{ramp: "roof", offset: 0, amount: 0.22, mix: art.Hex("e0a0b0")},
			{ramp: "brick", offset: 0, amount: 0.18, mix: art.Hex("c98a86")},
			{ramp: "wood", offset: 0, amount: 0.20, mix: art.Hex("8f7a5a")},
			// 全局：整个小镇罩一层春日的暖粉，建筑与人物一并偏暖
			{ramp: "*", amount: 0.20, mix: art.Hex("f0c8dc")},
		}
	case "summer": // 夏：高饱和深绿，天最蓝
		return []tintRule{
			{ramp: "grass", offset: 0, amount: 0.30, mix: art.Hex("2f7a28")},
			{ramp: "foliage", offset: 0, amount: 0.28, mix: art.Hex("1f5c1c")},
			{ramp: "sky", offset: 1, amount: 0.32, mix: art.Hex("3f8fe0")},
			{ramp: "water", offset: 0, amount: 0.30, mix: art.Hex("1f6f9c")},
			{ramp: "sand", offset: 0, amount: 0.25, mix: art.Hex("d8c070")},
			{ramp: "brick", offset: 0, amount: 0.20, mix: art.Hex("c86a4a")},
			// 全局：盛夏的高饱和暖调
			{ramp: "*", amount: 0.16, mix: art.Hex("ffe08a")},
		}
	case "autumn": // 秋叶：树转橙红、草转稻草黄、天光偏琥珀
		return []tintRule{
			{ramp: "foliage", offset: 0, amount: 0.85, mix: art.Hex("d4611f")},
			{ramp: "grass", offset: 0, amount: 0.55, mix: art.Hex("c9a25a")},
			{ramp: "sky", offset: 0, amount: 0.28, mix: art.Hex("d9a24a")},
			{ramp: "dirt", offset: 0, amount: 0.30, mix: art.Hex("a86a2a")},
			{ramp: "roof", offset: 0, amount: 0.38, mix: art.Hex("b0562a")},
			{ramp: "brick", offset: 0, amount: 0.42, mix: art.Hex("b06a2a")},
			{ramp: "wood", offset: 0, amount: 0.45, mix: art.Hex("a06a30")},
			{ramp: "sand", offset: 0, amount: 0.42, mix: art.Hex("c98a3a")},
			{ramp: "stone", offset: 0, amount: 0.32, mix: art.Hex("c08a4a")},
			{ramp: "grey", offset: 0, amount: 0.28, mix: art.Hex("b08a5a")},
			// 全局：秋日的琥珀金，建筑与人物一并染上秋色
			{ramp: "*", amount: 0.32, mix: art.Hex("e09a3a")},
		}
	default: // winter 冬雪：整体压成蓝白，草/屋顶/树全部覆雪感
		return []tintRule{
			{ramp: "grass", offset: 0, amount: 0.80, mix: art.Hex("dfe6f0")},
			{ramp: "foliage", offset: 0, amount: 0.60, mix: art.Hex("8f9bb0")},
			{ramp: "sky", offset: 0, amount: 0.40, mix: art.Hex("cfe2f2")},
			{ramp: "roof", offset: 0, amount: 0.55, mix: art.Hex("eef2f8")},
			{ramp: "stone", offset: 0, amount: 0.25, mix: art.Hex("cfd6e0")},
			{ramp: "grey", offset: 0, amount: 0.22, mix: art.Hex("cfd6e0")},
			{ramp: "water", offset: 0, amount: 0.45, mix: art.Hex("bcd4e4")},
			{ramp: "brick", offset: 0, amount: 0.45, mix: art.Hex("b8c0cc")},
			{ramp: "wood", offset: 0, amount: 0.40, mix: art.Hex("9aa4b0")},
			{ramp: "sand", offset: 0, amount: 0.50, mix: art.Hex("dfe6f0")},
			{ramp: "asphalt", offset: 1, amount: 0.35, mix: art.Hex("9aa4b0")},
			// 全局：冬日的冷蓝白，整个小镇都像结了霜
			{ramp: "*", amount: 0.34, mix: art.Hex("cfe0f0")},
		}
	}
}

// timeRules 是四个时段的色阶重映射（叠加在季节之后）。
// 同样按「一眼看出」标定：清晨/黄昏是**强暖色压暗**，夜晚是**强冷色压暗**。
func timeRules(band string) []tintRule {
	switch band {
	case "dawn": // 清晨：低角度暖阳，整体偏橙金
		return []tintRule{
			{ramp: "stone", offset: 1, amount: 0.42, mix: art.Hex("e0a050")},
			{ramp: "grey", offset: 0, amount: 0.35, mix: art.Hex("d08a48")},
			{ramp: "roof", offset: 0, amount: 0.30, mix: art.Hex("e09a4a")},
			{ramp: "grass", offset: 0, amount: 0.25, mix: art.Hex("a8963a")},
			{ramp: "sky", offset: 0, amount: 0.60, mix: art.Hex("e08a5a")},
			{ramp: "water", offset: 0, amount: 0.35, mix: art.Hex("c88a50")},
		}
	case "day", "": // 白天 / 未指定：原样
		// 注意 "" 必须走这里：早先它落进 default 分支被当成**夜晚**，
		// 于是「不传 -tod」的渲染会整张变暗，而画面上完全看不出是配置错误。
		return nil
	case "dusk": // 黄昏：橙红长影、晚霞
		return []tintRule{
			{ramp: "stone", offset: -1, amount: 0.45, mix: art.Hex("b04a30")},
			{ramp: "grey", offset: -1, amount: 0.42, mix: art.Hex("9c3f30")},
			{ramp: "roof", offset: 0, amount: 0.38, mix: art.Hex("c0503c")},
			{ramp: "grass", offset: -1, amount: 0.32, mix: art.Hex("8a5a20")},
			{ramp: "sky", offset: 0, amount: 0.72, mix: art.Hex("c0503c")},
			{ramp: "water", offset: 0, amount: 0.45, mix: art.Hex("a8503c")},
		}
	default: // night：强冷蓝压暗，窗户自发光仍由烘焙的夜间档保留
		return []tintRule{
			{ramp: "stone", offset: -2, amount: 0.58, mix: art.Hex("2a3050")},
			{ramp: "grey", offset: -2, amount: 0.58, mix: art.Hex("2a3050")},
			{ramp: "grass", offset: -2, amount: 0.52, mix: art.Hex("243050")},
			{ramp: "foliage", offset: -2, amount: 0.50, mix: art.Hex("243050")},
			{ramp: "roof", offset: -2, amount: 0.50, mix: art.Hex("2a3050")},
			{ramp: "dirt", offset: -1, amount: 0.45, mix: art.Hex("2a3050")},
			{ramp: "sky", offset: -2, amount: 0.82, mix: art.Hex("141a2c")},
			{ramp: "water", offset: -1, amount: 0.55, mix: art.Hex("1a2438")},
		}
	}
}

// Weathers 是支持的天气。
var Weathers = []string{"clear", "rain", "snow", "fog", "petals"}

// seasonWeather 返回每个季节的**典型天气**。
//
// 天气不是任意组合，而是跟着季节走 —— 这是「气候」而不是「开关」：
// 春天飘樱瓣、夏天落阵雨、秋天起雾、冬天飞雪。
// 显式传 -weather 可以覆盖（比如专门看冬天的晴天）。
func seasonWeather(season string) string {
	switch season {
	case "spring":
		return "petals"
	case "summer":
		return "rain"
	case "autumn":
		return "fog"
	default:
		return "snow"
	}
}

// weatherRules 是天气的色阶重映射（叠加在季节与时段之后）。
func weatherRules(weather string) []tintRule {
	switch weather {
	case "rain": // 雨天：整体去饱和压暗偏冷，湿冷感
		return []tintRule{
			{ramp: "sky", offset: -1, amount: 0.55, mix: art.Hex("59606e")},
			{ramp: "grey", offset: -1, amount: 0.30, mix: art.Hex("4a5260")},
			{ramp: "stone", offset: -1, amount: 0.28, mix: art.Hex("55606c")},
			{ramp: "grass", offset: 0, amount: 0.30, mix: art.Hex("3f5a44")},
			{ramp: "asphalt", offset: 0, amount: 0.25, mix: art.Hex("2a3038")},
		}
	case "snow": // 雪天：提亮去饱和，天光发白
		return []tintRule{
			{ramp: "sky", offset: 0, amount: 0.55, mix: art.Hex("c8d4e2")},
			{ramp: "grey", offset: 1, amount: 0.35, mix: art.Hex("e2e8f0")},
			{ramp: "stone", offset: 1, amount: 0.30, mix: art.Hex("e2e8f0")},
			{ramp: "grass", offset: 0, amount: 0.45, mix: art.Hex("e8eef6")},
			{ramp: "roof", offset: 0, amount: 0.40, mix: art.Hex("f0f4f8")},
		}
	case "fog": // 雾天：抬黑位、降对比，远山近树糊成一片
		return []tintRule{
			{ramp: "ink", offset: 2, amount: 0.55, mix: art.Hex("9aa4b0")},
			{ramp: "shadow", offset: 2, amount: 0.55, mix: art.Hex("9aa4b0")},
			{ramp: "grey", offset: 1, amount: 0.40, mix: art.Hex("b0b8c2")},
			{ramp: "sky", offset: 0, amount: 0.60, mix: art.Hex("ccd4dc")},
			{ramp: "grass", offset: 0, amount: 0.35, mix: art.Hex("a8b8a8")},
		}
	case "petals": // 花信风：漫天樱瓣
		return []tintRule{
			{ramp: "sky", offset: 1, amount: 0.30, mix: art.Hex("e8c0d8")},
			{ramp: "foliage", offset: 0, amount: 0.35, mix: art.Hex("e884c0")},
			{ramp: "grass", offset: 1, amount: 0.20, mix: art.Hex("b8d878")},
		}
	default:
		return nil
	}
}

// buildRemap 把一组规则编译成「色板下标 → 色板下标」的查表。
//
// 规则分两层，顺序不能颠倒：
//  1. **色阶规则**（ramp 指定）：把某条色阶整体平移/混色 —— 用于「草变黄、树变红」；
//  2. **全局规则**（ramp == "*"）：对**全色板**叠一层色调 —— 用于「整个季节的色温」。
//
// 全局规则必须作用在色阶规则**之后**：否则草/树的定向改色会被全局色调冲掉，
// 而且建筑与人物（用的是 car.* / trim.* / wall.* 这些没被点名的色阶）永远不变 ——
// 这正是上一版「只有草和树在换季、房子和人纹丝不动」的原因。
func buildRemap(pal *art.Palette, rules []tintRule) []uint8 {
	tgt := make([]art.RGBA, pal.Len())
	for i := range tgt {
		tgt[i] = pal.Colors[i]
	}
	for _, r := range rules {
		if r.ramp == "*" {
			continue
		}
		ramp := pal.Ramp(r.ramp)
		for i, idx := range ramp {
			j := i + r.offset
			if j < 0 {
				j = 0
			}
			if j >= len(ramp) {
				j = len(ramp) - 1
			}
			c := pal.Colors[ramp[j]]
			if r.amount > 0 {
				c = art.Lerp(c, r.mix, r.amount)
			}
			tgt[idx] = c
		}
	}
	for _, r := range rules {
		if r.ramp != "*" {
			continue
		}
		for i := range tgt {
			if r.amount > 0 {
				tgt[i] = art.Lerp(tgt[i], r.mix, r.amount)
			}
		}
	}
	lut := make([]uint8, pal.Len())
	for i := range tgt {
		lut[i] = uint8(pal.Nearest(float32(tgt[i].R), float32(tgt[i].G), float32(tgt[i].B)))
	}
	return lut
}

// applyRemap 用重映射表重写整张画布（逐像素一次查表）。
func applyRemap(src *image.NRGBA, pal *art.Palette, lut []uint8) *image.NRGBA {
	// 先把 RGBA 映射回色板下标（一次性建反查表），再查重映射表
	type key [3]uint8
	rev := make(map[key]uint8, pal.Len())
	for i, c := range pal.Colors {
		rev[key{c.R, c.G, c.B}] = uint8(i)
	}
	out := image.NewNRGBA(src.Rect)
	copy(out.Pix, src.Pix)
	for i := 0; i < len(out.Pix); i += 4 {
		if out.Pix[i+3] == 0 {
			continue
		}
		idx, ok := rev[key{out.Pix[i], out.Pix[i+1], out.Pix[i+2]}]
		if !ok {
			continue
		}
		c := pal.Colors[lut[idx]]
		out.Pix[i], out.Pix[i+1], out.Pix[i+2] = c.R, c.G, c.B
	}
	return out
}

// TintGrid 产出 4 季 × 4 时段的对比图，用于验证「一套资产换 16 种外观」。
func TintGrid(town *image.NRGBA, pal *art.Palette, scale int) *image.NRGBA {
	seasons := []string{"spring", "summer", "autumn", "winter"}
	bands := []string{"dawn", "day", "dusk", "night"}
	cw, ch := town.Rect.Dx(), town.Rect.Dy()
	// 缩到 1/2 再拼 4x4，不然图太大
	half := image.NewNRGBA(image.Rect(0, 0, cw/2, ch/2))
	for y := 0; y < ch/2; y++ {
		for x := 0; x < cw/2; x++ {
			si := town.PixOffset(x*2, y*2)
			di := half.PixOffset(x, y)
			copy(half.Pix[di:di+4], town.Pix[si:si+4])
		}
	}
	hw, hh := half.Rect.Dx(), half.Rect.Dy()
	out := image.NewNRGBA(image.Rect(0, 0, hw*4, hh*4))
	for r, season := range seasons {
		for c, band := range bands {
			rules := append(seasonRules(season), timeRules(band)...)
			lut := buildRemap(pal, rules)
			img := applyRemap(half, pal, lut)
			for y := 0; y < hh; y++ {
				for x := 0; x < hw; x++ {
					si := img.PixOffset(x, y)
					di := out.PixOffset(c*hw+x, r*hh+y)
					copy(out.Pix[di:di+4], img.Pix[si:si+4])
				}
			}
		}
	}
	_ = scale
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// 天气的可见部分：粒子与大气。
//
// 色阶重映射只能改变「颜色」，雨/雪/雾/花瓣必须**画出来**才成立 ——
// 雨要有斜线、雪要有落点、雾要糊掉远处、花瓣要有飘的轨迹。
// 这里的实现是**静态合成**（探针用），运行时换成逐帧粒子即可，观感一致。
// ─────────────────────────────────────────────────────────────────────────────

// hash32 是确定性伪随机：同一张图每次渲染的雨点/雪花位置必须一致，
// 否则每次跑出来的对比图都不一样，没法比对。
func hash32(i int) uint32 {
	x := uint32(i)*2654435761 + 1013904223
	x ^= x >> 15
	x *= 2246822519
	x ^= x >> 13
	x *= 3266489917
	x ^= x >> 16
	return x
}

// blendPx 半透明混合一个像素（不做 alpha 计算，直接对 RGB 插值）。
func blendPx(dst *image.NRGBA, x, y int, r, g, b uint8, a float64) {
	if x < 0 || y < 0 || x >= dst.Rect.Dx() || y >= dst.Rect.Dy() || a <= 0 {
		return
	}
	i := dst.PixOffset(x, y)
	if dst.Pix[i+3] == 0 {
		return
	}
	ia := 1 - a
	dst.Pix[i] = uint8(float64(dst.Pix[i])*ia + float64(r)*a)
	dst.Pix[i+1] = uint8(float64(dst.Pix[i+1])*ia + float64(g)*a)
	dst.Pix[i+2] = uint8(float64(dst.Pix[i+2])*ia + float64(b)*a)
}

// drawWeather 把天气的可见特征画在合成图上。
func drawWeather(dst *image.NRGBA, weather string, scale int) {
	if weather == "" || weather == "clear" {
		return
	}
	if scale < 1 {
		scale = 1
	}
	w, h := dst.Rect.Dx(), dst.Rect.Dy()
	switch weather {
	case "rain":
		// 斜向雨丝：条数按面积定，长短随机，越往下越密（近大远小）
		n := w * h / 700
		for i := 0; i < n; i++ {
			px := int(hash32(i*3) % uint32(w))
			py := int(hash32(i*3+1) % uint32(h))
			ln := (5 + int(hash32(i*3+2)%6)) * scale
			for k := 0; k < ln; k++ {
				blendPx(dst, px-k/3, py+k, 190, 210, 235, 0.45)
			}
		}
	case "snow":
		// 雪花：大小两档，大颗更亮（层次感）
		n := w * h / 1400
		for i := 0; i < n; i++ {
			px := int(hash32(i*2+7) % uint32(w))
			py := int(hash32(i*2+8) % uint32(h))
			big := hash32(i*5)%3 == 0
			a := 0.55
			r := scale
			if big {
				a, r = 0.85, scale*2
			}
			for dy := 0; dy < r; dy++ {
				for dx := 0; dx < r; dx++ {
					blendPx(dst, px+dx, py+dy, 245, 250, 255, a)
				}
			}
		}
	case "fog":
		// 雾：横向条带，中上部更浓（远处糊掉），近处留一点清晰
		for y := 0; y < h; y++ {
			t := float64(y) / float64(h)
			a := 0.42 * (1 - t*0.55)
			if a <= 0.02 {
				continue
			}
			for x := 0; x < w; x++ {
				// 用一点正弦扰动，避免完全均匀的死板
				aa := a * (0.85 + 0.15*float64(int(hash32(x/12+y/9)%100))/100)
				blendPx(dst, x, y, 205, 212, 220, aa)
			}
		}
	case "petals":
		// 花信风：粉白花瓣，带一点横向漂移的轨迹
		n := w * h / 1800
		for i := 0; i < n; i++ {
			px := int(hash32(i*4+11) % uint32(w))
			py := int(hash32(i*4+12) % uint32(h))
			for k := 0; k < 2*scale; k++ {
				blendPx(dst, px+k, py+k/2, 245, 190, 220, 0.75)
			}
		}
	}
}
