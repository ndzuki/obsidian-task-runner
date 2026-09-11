package art

import (
	"image"
	"image/png"
	"math"
	"os"
)

// Buf 是预乘 alpha 的浮点 RGBA 画布，所有合成/降采样都在预乘空间做，
// 这样在有半透明像素（阴影、辉光）时不会出现黑边。
type Buf struct {
	W, H       int
	R, G, B, A []float32
}

func NewBuf(w, h int) *Buf {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	n := w * h
	return &Buf{
		W: w, H: h,
		R: make([]float32, n), G: make([]float32, n),
		B: make([]float32, n), A: make([]float32, n),
	}
}

func (b *Buf) At(x, y int) int { return y*b.W + x }

func (b *Buf) Inside(x, y int) bool { return x >= 0 && y >= 0 && x < b.W && y < b.H }

// Set 直接写入（覆盖）一个已预乘的像素。
func (b *Buf) Set(x, y int, r, g, bl, a float32) {
	if !b.Inside(x, y) {
		return
	}
	i := b.At(x, y)
	b.R[i], b.G[i], b.B[i], b.A[i] = r, g, bl, a
}

// SetRGB 以不透明方式覆盖写入（光栅化主通道用）。
func (b *Buf) SetRGB(x, y int, r, g, bl float32) {
	b.Set(x, y, r, g, bl, 1)
}

// Blend 以 over 方式混合一个未预乘颜色。
func (b *Buf) Blend(x, y int, r, g, bl, a float32) {
	if a <= 0 || !b.Inside(x, y) {
		return
	}
	if a > 1 {
		a = 1
	}
	i := b.At(x, y)
	inv := 1 - a
	b.R[i] = r*a + b.R[i]*inv
	b.G[i] = g*a + b.G[i]*inv
	b.B[i] = bl*a + b.B[i]*inv
	b.A[i] = a + b.A[i]*inv
}

// Add 以加法方式叠加（用于辉光），系数 k 缩放来源。
func (b *Buf) Add(x, y int, r, g, bl, a, k float32) {
	if !b.Inside(x, y) {
		return
	}
	f := a * k
	i := b.At(x, y)
	b.R[i] += r * f
	b.G[i] += g * f
	b.B[i] += bl * f
	if b.A[i] < a {
		b.A[i] = a
	}
}

// Composite 把 src 以 over 方式画到 (ox,oy)。
func (b *Buf) Composite(src *Buf, ox, oy int) {
	for y := 0; y < src.H; y++ {
		dy := oy + y
		if dy < 0 || dy >= b.H {
			continue
		}
		for x := 0; x < src.W; x++ {
			dx := ox + x
			if dx < 0 || dx >= b.W {
				continue
			}
			si := src.At(x, y)
			a := src.A[si]
			if a <= 0 {
				continue
			}
			di := b.At(dx, dy)
			inv := 1 - a
			b.R[di] = src.R[si] + b.R[di]*inv
			b.G[di] = src.G[si] + b.G[di]*inv
			b.B[di] = src.B[si] + b.B[di]*inv
			b.A[di] = a + b.A[di]*inv
		}
	}
}

// Sub 返回矩形子画布（越界部分裁掉，不补白）。
func (b *Buf) Sub(r image.Rectangle) *Buf {
	r = r.Intersect(image.Rect(0, 0, b.W, b.H))
	out := NewBuf(r.Dx(), r.Dy())
	for y := 0; y < r.Dy(); y++ {
		for x := 0; x < r.Dx(); x++ {
			si := b.At(r.Min.X+x, r.Min.Y+y)
			di := out.At(x, y)
			out.R[di], out.G[di], out.B[di], out.A[di] = b.R[si], b.G[si], b.B[si], b.A[si]
		}
	}
	return out
}

// Downsample 做 f x f 的盒式降采样（超采样抗锯齿的最后一步）。
func (b *Buf) Downsample(f int) *Buf {
	if f <= 1 {
		return b
	}
	w := b.W / f
	h := b.H / f
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	out := NewBuf(w, h)
	inv := 1 / float32(f*f)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, bl, a float32
			for sy := 0; sy < f; sy++ {
				row := (y*f + sy) * b.W
				for sx := 0; sx < f; sx++ {
					i := row + x*f + sx
					r += b.R[i]
					g += b.G[i]
					bl += b.B[i]
					a += b.A[i]
				}
			}
			di := out.At(x, y)
			out.R[di], out.G[di], out.B[di], out.A[di] = r*inv, g*inv, bl*inv, a*inv
		}
	}
	return out
}

// Blur 返回模糊后的副本（三次盒式模糊近似高斯），仅用于辉光/阴影软化。
func (b *Buf) Blur(radius int) *Buf {
	if radius < 1 {
		return b
	}
	cur := b
	for pass := 0; pass < 3; pass++ {
		cur = boxBlurH(cur, radius)
		cur = boxBlurV(cur, radius)
	}
	return cur
}

func boxBlurH(src *Buf, r int) *Buf {
	out := NewBuf(src.W, src.H)
	for y := 0; y < src.H; y++ {
		for x := 0; x < src.W; x++ {
			var sr, sg, sb, sa float32
			n := float32(0)
			for k := -r; k <= r; k++ {
				xx := x + k
				if xx < 0 || xx >= src.W {
					continue
				}
				i := src.At(xx, y)
				sr += src.R[i]
				sg += src.G[i]
				sb += src.B[i]
				sa += src.A[i]
				n++
			}
			di := out.At(x, y)
			out.R[di], out.G[di], out.B[di], out.A[di] = sr/n, sg/n, sb/n, sa/n
		}
	}
	return out
}

func boxBlurV(src *Buf, r int) *Buf {
	out := NewBuf(src.W, src.H)
	for y := 0; y < src.H; y++ {
		for x := 0; x < src.W; x++ {
			var sr, sg, sb, sa float32
			n := float32(0)
			for k := -r; k <= r; k++ {
				yy := y + k
				if yy < 0 || yy >= src.H {
					continue
				}
				i := src.At(x, yy)
				sr += src.R[i]
				sg += src.G[i]
				sb += src.B[i]
				sa += src.A[i]
				n++
			}
			di := out.At(x, y)
			out.R[di], out.G[di], out.B[di], out.A[di] = sr/n, sg/n, sb/n, sa/n
		}
	}
	return out
}

// AlphaBounds 返回 alpha > 0.02 的像素包围盒；全透明时返回空矩形。
func (b *Buf) AlphaBounds() image.Rectangle {
	minX, minY, maxX, maxY := b.W, b.H, -1, -1
	for y := 0; y < b.H; y++ {
		for x := 0; x < b.W; x++ {
			if b.A[b.At(x, y)] > 0.02 {
				if x < minX {
					minX = x
				}
				if y < minY {
					minY = y
				}
				if x > maxX {
					maxX = x
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	if maxX < 0 {
		return image.Rectangle{}
	}
	return image.Rect(minX, minY, maxX+1, maxY+1)
}

// Opacity 返回平均 alpha 覆盖率，用于流水线自检（空精灵会暴露出来）。
func (b *Buf) Opacity() float64 {
	var s float64
	for i := range b.A {
		s += float64(b.A[i])
	}
	return s / float64(b.W*b.H)
}

// SpriteOpts 控制最终「像素化」阶段的处理。
type SpriteOpts struct {
	Dither      float32 // 抖动阈值缩放（1 = 标准 8x8 有序抖动）
	Outline     float32 // 轮廓压暗强度，0 关闭
	Rim         float32 // 受光侧提亮强度，0 关闭
	EdgeFade    float32 // 平均 alpha 低于此值的像素直接丢弃
	BinaryAlpha bool    // 边缘 alpha 二值化（瓦片用，避免拼接时透出底色）
	// EdgeW 是描边/提亮的像素宽度。提高分辨率时必须同步调大，
	// 否则轮廓会显得越来越细（2x 下应设 2）。
	EdgeW int
}

// DefaultSpriteOpts 是常规建筑/道具的默认像素化参数。
var DefaultSpriteOpts = SpriteOpts{Dither: 1, Outline: 0.42, Rim: 0.18, EdgeFade: 0.04}

// ToNRGBA 把画布转成 8 位 RGBA 图像：先描边/提亮，再按母板量化 + 有序抖动。
// 保留抗锯齿产生的半透明边缘，因此缩放时依旧干净。
func (b *Buf) ToNRGBA(p *Palette, o SpriteOpts) *image.NRGBA {
	// 1) 解预乘，得到浮点直色
	r := make([]float32, len(b.A))
	g := make([]float32, len(b.A))
	bl := make([]float32, len(b.A))
	for i := range b.A {
		a := b.A[i]
		if a > 0 {
			r[i] = b.R[i] / a
			g[i] = b.G[i] / a
			bl[i] = b.B[i] / a
		}
	}
	// 2) 轮廓压暗 + 受光侧提亮
	if o.Outline > 0 || o.Rim > 0 {
		applyEdges(b, r, g, bl, o)
	}
	// 3) 量化 + 抖动
	out := image.NewNRGBA(image.Rect(0, 0, b.W, b.H))
	for y := 0; y < b.H; y++ {
		for x := 0; x < b.W; x++ {
			i := b.At(x, y)
			a := b.A[i]
			if a <= o.EdgeFade {
				continue
			}
			thr := Bayer(x, y) * o.Dither
			idx := p.Quantize(r[i], g[i], bl[i], thr)
			c := p.Colors[idx]
			ai := uint8(math.Round(math.Min(1, float64(a)) * 255))
			if ai == 0 {
				ai = 1
			}
			j := out.PixOffset(x, y)
			if o.BinaryAlpha {
				if a < 0.5 {
					continue
				}
				ai = 255
			}
			out.Pix[j] = c.R
			out.Pix[j+1] = c.G
			out.Pix[j+2] = c.B
			out.Pix[j+3] = ai
		}
	}
	return out
}

func applyEdges(b *Buf, r, g, bl []float32, o SpriteOpts) {
	alphas := make([]float32, len(b.A))
	copy(alphas, b.A)
	w := o.EdgeW
	if w < 1 {
		w = 1
	}
	for y := 0; y < b.H; y++ {
		for x := 0; x < b.W; x++ {
			i := b.At(x, y)
			if alphas[i] < 0.5 {
				continue
			}
			// 轮廓：切比雪夫距离 w 内出现透明像素则压暗
			minA := float32(1)
			for dy := -w; dy <= w && minA > 0; dy++ {
				for dx := -w; dx <= w; dx++ {
					if dx == 0 && dy == 0 {
						continue
					}
					nx, ny := x+dx, y+dy
					if !b.Inside(nx, ny) {
						minA = 0
						break
					}
					if a := alphas[b.At(nx, ny)]; a < minA {
						minA = a
					}
				}
			}
			if o.Outline > 0 && minA < 0.5 {
				k := 1 - o.Outline*(1-minA)
				r[i] *= k
				g[i] *= k
				bl[i] *= k
			}
			// 受光侧提亮：上/左上透明说明这里是轮廓外侧的受光边
			if o.Rim > 0 {
				upClear := false
				for _, d := range [3][2]int{{0, -1}, {-1, -1}, {-1, 0}} {
					nx, ny := x+d[0]*w, y+d[1]*w
					if !b.Inside(nx, ny) || alphas[b.At(nx, ny)] < 0.4 {
						upClear = true
						break
					}
				}
				if upClear {
					k := 1 + o.Rim
					r[i] = clamp01f(r[i] * k)
					g[i] = clamp01f(g[i] * k)
					bl[i] = clamp01f(bl[i] * k)
				}
			}
		}
	}
}

func clamp01f(v float32) float32 {
	if v > 1 {
		return 1
	}
	if v < 0 {
		return 0
	}
	return v
}

// SavePNG 写出 PNG 文件。
func SavePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	return enc.Encode(f, img)
}
