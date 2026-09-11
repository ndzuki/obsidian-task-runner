// Package sheet 提供精灵图集装箱、调色板自检用的「接触印相图」，
// 以及一个用 Go 合成参考城市场景的预览器 —— 后者让美术质量可以在不开浏览器的情况下迭代。
package sheet

import (
	"fmt"
	"image"
	"sort"

	"isocity98/internal/art"
	"isocity98/internal/render"
)

// ---------------------------------------------------------------- 装箱

// Placement 是一张图在图集中的落位。
type Placement struct {
	Sheet int
	X, Y  int
}

type packItem struct {
	img *image.NRGBA
	h   int
}

// Packer 是**批量**货架式装箱器。
//
// 与逐张摆放的朴素做法相比，它做两件对内存很关键的事：
//
//  1. 收集完所有图之后再按高度降序摆放 —— 货架装箱对顺序极其敏感，
//     高度接近的图排在一起，右侧参差浪费会小很多；
//  2. 每张图集按**实际用到的高度**分配，而不是一律开一张 W×maxH 的大图。
//     提高像素密度时图集面积按平方增长，这一步能省下三到四成。
//
// 用法：Add 收集 → Finish 一次性摆放与绘制 → At 查询落位。
type Packer struct {
	W, H, Pad int // W 是图集宽度，H 是单张图集的高度上限
	items     []packItem
	places    []Placement
	sheets    []*image.NRGBA
	finished  bool
}

// NewPacker 创建装箱器；w 为图集宽度，maxH 为单张图集的高度上限。
func NewPacker(w, maxH int) *Packer {
	p := &Packer{W: w, H: maxH, Pad: 1}
	return p
}

// Add 收集一张图，返回句柄；尺寸超过一张图集时返回 ok=false。
func (p *Packer) Add(img *image.NRGBA) (handle int, ok bool) {
	if p.finished {
		panic("sheet: Add 不能在 Finish 之后调用")
	}
	w, h := img.Rect.Dx()+p.Pad, img.Rect.Dy()+p.Pad
	if w > p.W || h > p.H {
		return 0, false
	}
	p.items = append(p.items, packItem{img: img, h: h})
	return len(p.items) - 1, true
}

// Finish 执行摆放并绘制出图集。可重复调用（幂等）。
func (p *Packer) Finish() {
	if p.finished {
		return
	}
	p.finished = true
	p.places = make([]Placement, len(p.items))

	// 高度降序；同高按句柄升序，保证结果稳定可复现
	order := make([]int, len(p.items))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ia, ib := order[a], order[b]
		if p.items[ia].h != p.items[ib].h {
			return p.items[ia].h > p.items[ib].h
		}
		return ia < ib
	})

	// 第一趟：只算摆放位置与每张图集的最终高度
	heights := []int{p.Pad}
	sheet, shelfY, shelfH, cursorX := 0, p.Pad, 0, p.Pad
	for _, idx := range order {
		it := p.items[idx]
		w, h := it.img.Rect.Dx()+p.Pad, it.h
		if cursorX+w > p.W {
			shelfY += shelfH + p.Pad
			cursorX = p.Pad
			shelfH = 0
		}
		if shelfY+h > p.H {
			heights[sheet] = shelfY + shelfH + p.Pad
			sheet++
			heights = append(heights, p.Pad)
			shelfY, cursorX, shelfH = p.Pad, p.Pad, 0
		}
		p.places[idx] = Placement{Sheet: sheet, X: cursorX, Y: shelfY}
		cursorX += w
		if h > shelfH {
			shelfH = h
		}
	}
	heights[sheet] = shelfY + shelfH + p.Pad

	// 第二趟：按实际高度分配并绘制
	p.sheets = make([]*image.NRGBA, len(heights))
	for i, hh := range heights {
		if hh > p.H {
			hh = p.H
		}
		p.sheets[i] = image.NewNRGBA(image.Rect(0, 0, p.W, hh))
	}
	for i, it := range p.items {
		pl := p.places[i]
		blit(p.sheets[pl.Sheet], it.img, pl.X, pl.Y)
	}
}

// At 返回句柄对应的落位（须先 Finish）。
func (p *Packer) At(handle int) Placement {
	p.Finish()
	return p.places[handle]
}

// Sheets 返回全部图集（须先 Finish）。
func (p *Packer) Sheets() []*image.NRGBA {
	p.Finish()
	return p.sheets
}

// Utilization 返回精灵图实际像素占图集总像素的比例，用于流水线自检。
func (p *Packer) Utilization() float64 {
	p.Finish()
	var used, total float64
	for _, it := range p.items {
		used += float64(it.img.Rect.Dx() * it.img.Rect.Dy())
	}
	for _, s := range p.sheets {
		total += float64(s.Rect.Dx() * s.Rect.Dy())
	}
	if total == 0 {
		return 0
	}
	return used / total
}

// blit 逐像素拷贝，并夹取到目标画布范围内（防御性：绝不允许越界写）。
func blit(dst *image.NRGBA, src *image.NRGBA, ox, oy int) {
	b := dst.Rect
	for y := 0; y < src.Rect.Dy(); y++ {
		dy := oy + y
		if dy < b.Min.Y || dy >= b.Max.Y {
			continue
		}
		for x := 0; x < src.Rect.Dx(); x++ {
			dx := ox + x
			if dx < b.Min.X || dx >= b.Max.X {
				continue
			}
			si := src.PixOffset(src.Rect.Min.X+x, src.Rect.Min.Y+y)
			di := dst.PixOffset(dx, dy)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
}

// ---------------------------------------------------------------- 接触印相图

// Contact 把精灵图排成网格（棋盘底 + 每格 1px 边框 + 放大），用于人工审阅美术质量。
func Contact(sprites []*render.Sprite, cols, scale int, pal *art.Palette) *image.NRGBA {
	if cols < 1 {
		cols = 8
	}
	cellW, cellH := 0, 0
	for _, s := range sprites {
		if s.W > cellW {
			cellW = s.W
		}
		if s.H > cellH {
			cellH = s.H
		}
	}
	cellW += 8
	cellH += 8
	rows := (len(sprites) + cols - 1) / cols
	W := cols * cellW * scale
	H := rows * cellH * scale
	out := image.NewNRGBA(image.Rect(0, 0, W, H))
	checker(out, 8*scale)
	for i, s := range sprites {
		cx := (i % cols) * cellW * scale
		cy := (i / cols) * cellH * scale
		drawScaled(out, s.Img, cx+(cellW-s.W)*scale/2, cy+(cellH-s.H)*scale/2, scale)
	}
	return out
}

func checker(dst *image.NRGBA, cell int) {
	a := art.Hex("2b2f44")
	b := art.Hex("3b4059")
	for y := 0; y < dst.Rect.Dy(); y++ {
		for x := 0; x < dst.Rect.Dx(); x++ {
			c := a
			if ((x/cell)+(y/cell))%2 == 0 {
				c = b
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = c.R, c.G, c.B, 255
		}
	}
}

func drawScaled(dst *image.NRGBA, src *image.NRGBA, ox, oy, scale int) {
	for y := 0; y < src.Rect.Dy(); y++ {
		for x := 0; x < src.Rect.Dx(); x++ {
			si := src.PixOffset(src.Rect.Min.X+x, src.Rect.Min.Y+y)
			if src.Pix[si+3] == 0 {
				continue
			}
			for sy := 0; sy < scale; sy++ {
				for sx := 0; sx < scale; sx++ {
					dx, dy := ox+x*scale+sx, oy+y*scale+sy
					if dx < 0 || dy < 0 || dx >= dst.Rect.Dx() || dy >= dst.Rect.Dy() {
						continue
					}
					blend(dst, dx, dy, src.Pix[si], src.Pix[si+1], src.Pix[si+2], src.Pix[si+3])
				}
			}
		}
	}
}

// blend 是 8 位 over 合成。
func blend(dst *image.NRGBA, x, y int, r, g, b, a uint8) {
	if a == 0 {
		return
	}
	i := dst.PixOffset(x, y)
	if a == 255 {
		dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = r, g, b, 255
		return
	}
	sa := uint32(a)
	da := uint32(dst.Pix[i+3])
	ia := 255 - sa
	oa := sa + da*ia/255
	if oa == 0 {
		return
	}
	mix := func(s, d uint32) uint8 {
		return uint8((s*sa + d*da*ia/255) / oa)
	}
	dst.Pix[i] = mix(uint32(r), uint32(dst.Pix[i]))
	dst.Pix[i+1] = mix(uint32(g), uint32(dst.Pix[i+1]))
	dst.Pix[i+2] = mix(uint32(b), uint32(dst.Pix[i+2]))
	dst.Pix[i+3] = uint8(oa)
}

// ---------------------------------------------------------------- 参考场景合成

// Set 是精灵图集合，用于 Go 端参考合成。
type Set struct {
	Sprites map[string]*render.Sprite
}

// Cam 是「格坐标 → 画布像素」的相机：画布原点偏移 + 放大倍率。
// 把这一步收在一处，避免每个合成器各写一遍投影公式（漏掉偏移就会把整张图
// 画到画布左上角之外）。
type Cam struct {
	OX, OY int // 世界原点（格 (0,0) 在 z=0 处）落在画布上的像素位置
	Scale  int // 放大倍率，1 = 原生
}

func (c Cam) project(cx, cy, z float64, ax, ay int) (int, int) {
	sx := (c.OX + int((cx-cy)*float64(render.TileW/2)) - ax) * c.Scale
	sy := (c.OY + int((cx+cy)*float64(render.TileH/2)) - int(z*float64(render.ZUnit)) - ay) * c.Scale
	return sx, sy
}

// DrawV 按「基础名 + 变体号」贴图，并**安全回退**：变体不存在时退到 `_0`、再退到基础名。
//
// 不能直接拼 `base_2` 就画：各地形的变体数并不一致（草地 3 个、农田/铺装只有 2 个），
// 拼出不存在的名字会让这一格**什么都不画**，在画面上留下透出背景的空洞。
func (s *Set) DrawV(dst *image.NRGBA, cam Cam, base string, variant int, cx, cy, z float64) bool {
	for _, n := range []string{
		fmt.Sprintf("%s_%d", base, variant), base + "_0", base,
	} {
		if _, ok := s.Sprites[n]; ok {
			return s.Draw(dst, cam, n, cx, cy, z)
		}
	}
	return false
}

// Draw 把精灵图按「格坐标 + 高度」贴到画布上，锚点与运行时完全一致。
func (s *Set) Draw(dst *image.NRGBA, cam Cam, name string, cx, cy, z float64) bool {
	sp, ok := s.Sprites[name]
	if !ok {
		return false
	}
	sx, sy := cam.project(cx, cy, z, sp.AnchorX, sp.AnchorY)
	drawScaled(dst, sp.Img, sx, sy, cam.Scale)
	return true
}

func round(v float64) float64 {
	if v < 0 {
		return float64(int(v - 0.5))
	}
	return float64(int(v + 0.5))
}

// Sky 用色阶 + 有序抖动画出渐变天空背景（与运行时同一套算法）。
// 必须是连续渐变：任何分段跳变在 CRT 扫描线下都会变成显眼的横线。
func Sky(dst *image.NRGBA, pal *art.Palette, topRamp, botRamp string) {
	h := dst.Rect.Dy()
	for y := 0; y < h; y++ {
		t := float64(y) / float64(h-1)
		ramp := topRamp
		var pos float32
		if t > 0.62 {
			ramp = botRamp
			pos = float32(0.16 * (1 - (t-0.62)/0.38))
		} else {
			pos = float32(0.16 + (0.62-t)/0.62*0.78)
		}
		for x := 0; x < dst.Rect.Dx(); x++ {
			thr := float32(art.Bayer(x, y))
			c := pal.LerpRamp(ramp, pos)
			idx := pal.QuantizeAmp(float32(c.R)/255, float32(c.G)/255, float32(c.B)/255, thr, 46)
			cc := pal.Colors[idx]
			i := dst.PixOffset(x, y)
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = cc.R, cc.G, cc.B, 255
		}
	}
}
