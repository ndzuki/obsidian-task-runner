// Package art 提供离线预渲染管线的基础美术设施：
// 有限色板、有序抖动、程序化纹理图案，以及预乘浮点画布。
package art

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// RGBA 是 8 位非线性 sRGB 颜色。A=0 表示完全透明。
type RGBA struct{ R, G, B, A uint8 }

// Hex 解析 "rrggbb" / "#rrggbb" 形式的颜色，可选 8 位 alpha 后缀。
func Hex(s string) RGBA {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		panic(fmt.Sprintf("art: 非法颜色 %q: %v", s, err))
	}
	switch len(s) {
	case 6:
		return RGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}
	case 8:
		return RGBA{uint8(v >> 24), uint8(v >> 16), uint8(v >> 8), uint8(v)}
	default:
		panic(fmt.Sprintf("art: 颜色长度非法 %q", s))
	}
}

func (c RGBA) String() string {
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}

// Float 返回未预乘的浮点分量（0..1）。
func (c RGBA) Float() (r, g, b, a float32) {
	return float32(c.R) / 255, float32(c.G) / 255, float32(c.B) / 255, float32(c.A) / 255
}

// Lerp 在两个颜色之间线性插值。
func Lerp(a, b RGBA, t float32) RGBA {
	t = Clamp01(t)
	return RGBA{
		R: uint8(math.Round(float64(float32(a.R) + (float32(b.R)-float32(a.R))*t))),
		G: uint8(math.Round(float64(float32(a.G) + (float32(b.G)-float32(a.G))*t))),
		B: uint8(math.Round(float64(float32(a.B) + (float32(b.B)-float32(a.B))*t))),
		A: uint8(math.Round(float64(float32(a.A) + (float32(b.A)-float32(a.A))*t))),
	}
}

// Scale 按系数缩放亮度（不做 gamma 校正，保持 8 位空间的直白观感）。
func Scale(c RGBA, k float32) RGBA {
	f := func(v uint8) uint8 {
		x := float32(v) * k
		if x < 0 {
			x = 0
		}
		if x > 255 {
			x = 255
		}
		return uint8(math.Round(float64(x)))
	}
	return RGBA{f(c.R), f(c.G), f(c.B), c.A}
}

func Clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func ClampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func ClampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
