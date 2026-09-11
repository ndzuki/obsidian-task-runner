package art

import (
	"image"
	"testing"
)

func TestBufBlendOver(t *testing.T) {
	b := NewBuf(2, 2)
	b.Blend(0, 0, 1, 0, 0, 1)   // 不透明红
	b.Blend(0, 0, 0, 0, 1, 0.5) // 半透明蓝叠上去
	if b.R[b.At(0, 0)] != 0.5 || b.B[b.At(0, 0)] != 0.5 {
		t.Fatalf("over 混合结果错误：R=%v B=%v", b.R[0], b.B[0])
	}
	if a := b.A[b.At(0, 0)]; a != 1 {
		t.Fatalf("alpha 应为 1，得到 %v", a)
	}
}

func TestDownsampleAverages(t *testing.T) {
	b := NewBuf(2, 2)
	b.SetRGB(0, 0, 1, 1, 1)
	b.SetRGB(1, 0, 0, 0, 0)
	b.SetRGB(0, 1, 0, 0, 0)
	b.SetRGB(1, 1, 1, 1, 1)
	d := b.Downsample(2)
	if d.W != 1 || d.H != 1 {
		t.Fatalf("降采样尺寸错误 %dx%d", d.W, d.H)
	}
	if got := d.R[0]; got != 0.5 {
		t.Fatalf("盒式降采样应得 0.5，得到 %v", got)
	}
}

func TestAlphaBoundsAndTrim(t *testing.T) {
	b := NewBuf(8, 8)
	b.SetRGB(3, 4, 1, 1, 1)
	b.SetRGB(5, 6, 1, 1, 1)
	r := b.AlphaBounds()
	want := image.Rect(3, 4, 6, 7)
	if r != want {
		t.Fatalf("包围盒应为 %v，得到 %v", want, r)
	}
	if empty := NewBuf(4, 4).AlphaBounds(); empty.Dx() != 0 {
		t.Fatalf("全透明画布应返回空包围盒，得到 %v", empty)
	}
}

// 量化后的精灵图必须只包含母板颜色（这是「整屏有限色板」的硬保证）。
func TestToNRGBAPaletteLocked(t *testing.T) {
	p := Pal()
	b := NewBuf(6, 6)
	for y := 0; y < 6; y++ {
		for x := 0; x < 6; x++ {
			b.SetRGB(x, y, float32(x)/6, float32(y)/6, 0.5)
		}
	}
	img := b.ToNRGBA(p, SpriteOpts{Dither: 1, EdgeFade: 0})
	allowed := map[[3]uint8]bool{}
	for _, c := range p.Colors {
		allowed[[3]uint8{c.R, c.G, c.B}] = true
	}
	for y := 0; y < 6; y++ {
		for x := 0; x < 6; x++ {
			i := img.PixOffset(x, y)
			if img.Pix[i+3] == 0 {
				continue
			}
			key := [3]uint8{img.Pix[i], img.Pix[i+1], img.Pix[i+2]}
			if !allowed[key] {
				t.Fatalf("(%d,%d) 出现非母板颜色 %v", x, y, key)
			}
		}
	}
}
