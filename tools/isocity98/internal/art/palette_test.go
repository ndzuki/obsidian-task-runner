package art

import (
	"math"
	"testing"
)

// 母板是整条管线的地基：颜色必须唯一，否则量化结果会不稳定；
// 色阶必须由暗到亮单调，否则材质表的「明暗位置」会失去意义。
func TestPaletteInvariants(t *testing.T) {
	p := Pal()
	if p.Len() < 32 || p.Len() > 256 {
		t.Fatalf("母板色数 %d 超出预期区间 [32,256]", p.Len())
	}
	seen := map[string]int{}
	for i, c := range p.Colors {
		key := c.String()
		if prev, dup := seen[key]; dup {
			t.Fatalf("颜色重复：%s 同时出现在 %d 与 %d", key, prev, i)
		}
		seen[key] = i
	}
	for _, name := range []string{
		"ink", "shadow", "grey", "stone", "brick", "roof", "wood", "dirt",
		"sand", "grass", "foliage", "water", "sky", "asphalt", "steel", "gold",
	} {
		if !p.HasRamp(name) {
			t.Fatalf("缺少色阶 %s", name)
		}
		r := p.Ramp(name)
		if len(r) < 2 {
			t.Fatalf("色阶 %s 只有 %d 档", name, len(r))
		}
		for i := 1; i < len(r); i++ {
			if luma(p.Colors[r[i]]) <= luma(p.Colors[r[i-1]]) {
				t.Fatalf("色阶 %s 不是由暗到亮：第 %d 档 %s 不比前一档 %s 亮",
					name, i, p.Colors[r[i]], p.Colors[r[i-1]])
			}
		}
	}
}

// 每个母板颜色量化后必须稳定回到自己（LUT 的 5 位截断可能破坏这一点）。
func TestQuantizeRoundTrip(t *testing.T) {
	p := Pal()
	p.BuildLUT()
	for i, c := range p.Colors {
		got := p.Quantize(float32(c.R)/255, float32(c.G)/255, float32(c.B)/255, 0)
		if got != i {
			t.Fatalf("色板第 %d 项 %s 量化后变成了第 %d 项 %s",
				i, c, got, p.Colors[got])
		}
	}
}

// 抖动只能改变结果，不能越界。
func TestQuantizeNeverOutOfRange(t *testing.T) {
	p := Pal()
	p.BuildLUT()
	for _, thr := range []float32{-0.5, -0.1, 0, 0.1, 0.5} {
		for _, v := range []float32{0, 0.25, 0.5, 0.75, 1} {
			got := p.Quantize(v, v, v, thr)
			if got < 0 || got >= p.Len() {
				t.Fatalf("量化越界：v=%v thr=%v → %d", v, thr, got)
			}
		}
	}
}

// Snap 必须落在色阶内且随位置单调不减。
func TestSnapMonotonic(t *testing.T) {
	p := Pal()
	prev := -1.0
	for i := 0; i <= 20; i++ {
		pos := float32(i) / 20
		c := p.Snap("grass", pos)
		l := luma(c)
		if l < prev-1e-9 {
			t.Fatalf("Snap 在 pos=%v 处变暗了：%v < %v", pos, l, prev)
		}
		prev = l
	}
	if got := p.Snap("grass", -5); got != p.Colors[p.Ramp("grass")[0]] {
		t.Fatalf("Snap 未夹取下界：%v", got)
	}
	if got := p.Snap("grass", 5); got != p.Colors[p.Ramp("grass")[len(p.Ramp("grass"))-1]] {
		t.Fatalf("Snap 未夹取上界：%v", got)
	}
}

func TestBayerRange(t *testing.T) {
	minV, maxV := float32(math.MaxFloat32), float32(-math.MaxFloat32)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			v := Bayer(x, y)
			if v < -0.5 || v > 0.5 {
				t.Fatalf("Bayer(%d,%d)=%v 超出 (-0.5,0.5]", x, y, v)
			}
			minV = float32(math.Min(float64(minV), float64(v)))
			maxV = float32(math.Max(float64(maxV), float64(v)))
		}
	}
	if maxV-minV < 0.9 {
		t.Fatalf("Bayer 动态范围过小：%v..%v", minV, maxV)
	}
}

// 每个内置图案都必须有界，否则色阶位置会溢出到荒唐的档位。
func TestPatternsBounded(t *testing.T) {
	for name, fn := range patFuncs {
		for _, face := range []Face{FaceTop, FacePosX, FacePosY} {
			for u := 0; u < 64; u += 7 {
				for v := 0; v < 64; v += 5 {
					got := fn(PatCtx{U: float64(u), V: float64(v), X: float64(u) / 32, Y: float64(v) / 32, Face: face})
					if got < -2.5 || got > 2.5 || got != got {
						t.Fatalf("图案 %s 在 (%d,%d,%v) 返回越界值 %v", name, u, v, face, got)
					}
				}
			}
		}
	}
}

// LUT 的位数决定了「两色至少要差多少」才不会落进同一个查找格。
// 这是有限色板管线的硬约束：6 位 → 至少差 4。
func TestPaletteMinSeparation(t *testing.T) {
	p := Pal()
	const minD = 256 / lutLevels
	for i := 0; i < p.Len(); i++ {
		for j := i + 1; j < p.Len(); j++ {
			a, b := p.Colors[i], p.Colors[j]
			dr := int(a.R) - int(b.R)
			dg := int(a.G) - int(b.G)
			db := int(a.B) - int(b.B)
			if abs(dr) < minD && abs(dg) < minD && abs(db) < minD {
				t.Errorf("颜色过近：%s(%s) 与 %s(%s) 三个通道差 (%d,%d,%d)，都小于 %d",
					p.Names[i], a, p.Names[j], b, dr, dg, db, minD)
			}
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
