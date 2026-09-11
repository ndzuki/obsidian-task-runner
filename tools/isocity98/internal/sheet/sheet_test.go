package sheet

import (
	"image"
	"testing"
)

type placedBox struct {
	sheet int
	x, y  int
	w, h  int
}

func (a placedBox) overlaps(b placedBox) bool {
	if a.sheet != b.sheet {
		return false
	}
	return a.x < b.x+b.w && b.x < a.x+a.w && a.y < b.y+b.h && b.y < a.y+a.h
}

func solid(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	return img
}

// 图集装箱绝不能重叠：重叠意味着运行时画出来的精灵是错的，
// 而这类错误在小图集上肉眼很难发现。
func TestPackerNeverOverlaps(t *testing.T) {
	const W, maxH = 512, 4096
	p := NewPacker(W, maxH)
	sizes := [][2]int{{40, 30}, {30, 60}, {64, 20}, {20, 20}, {80, 50}, {10, 90}, {50, 50}, {120, 120}}
	var handles []int
	for i := 0; i < 60; i++ {
		sz := sizes[i%len(sizes)]
		h, ok := p.Add(solid(sz[0], sz[1]))
		if !ok {
			t.Fatalf("第 %d 张图应当放得下", i)
		}
		handles = append(handles, h)
	}
	sheets := p.Sheets()

	var placed []placedBox
	for i, h := range handles {
		pl := p.At(h)
		sz := sizes[i%len(sizes)]
		b := placedBox{pl.Sheet, pl.X, pl.Y, sz[0], sz[1]}
		if pl.Sheet < 0 || pl.Sheet >= len(sheets) {
			t.Fatalf("第 %d 张图引用了不存在的图集 %d", i, pl.Sheet)
		}
		sh := sheets[pl.Sheet]
		if b.x < 0 || b.y < 0 || b.x+b.w > sh.Rect.Dx() || b.y+b.h > sh.Rect.Dy() {
			t.Fatalf("第 %d 张图 %v 超出图集 %dx%d", i, b, sh.Rect.Dx(), sh.Rect.Dy())
		}
		for j, prev := range placed {
			if b.overlaps(prev) {
				t.Fatalf("第 %d 张图 %v 与第 %d 张图 %v 重叠", i, b, j, prev)
			}
		}
		placed = append(placed, b)
	}
}

// 装箱后像素内容必须原样保留（错位一像素就是花屏）。
func TestPackerPreservesPixels(t *testing.T) {
	p := NewPacker(64, 256)
	img := image.NewNRGBA(image.Rect(0, 0, 5, 3))
	for i := 0; i < 5*3; i++ {
		img.Pix[i*4] = 200
		img.Pix[i*4+1] = 100
		img.Pix[i*4+3] = 255
	}
	h, _ := p.Add(img)
	pl := p.At(h)
	dst := p.Sheets()[pl.Sheet]
	for yy := 0; yy < 3; yy++ {
		for xx := 0; xx < 5; xx++ {
			i := dst.PixOffset(pl.X+xx, pl.Y+yy)
			if dst.Pix[i] != 200 || dst.Pix[i+1] != 100 || dst.Pix[i+3] != 255 {
				t.Fatalf("(%d,%d) 像素在装箱后改变", xx, yy)
			}
		}
	}
}

// 单张图大于整张图集时必须明确失败，而不是越界写内存（曾经的 panic 点）。
func TestPackerRejectsOversizedImage(t *testing.T) {
	p := NewPacker(32, 32)
	if _, ok := p.Add(solid(64, 64)); ok {
		t.Fatal("超大图应返回 ok=false")
	}
}

// 图集必须按「实际用到的高度」分配：白白开一张整高的大图会在高像素密度下
// 浪费几十 MB（这正是 3x 下从两张 4096² 降到一张 4096x5400 的原因）。
func TestPackerTrimsSheetHeight(t *testing.T) {
	const W, maxH = 256, 8192
	p := NewPacker(W, maxH)
	for i := 0; i < 20; i++ {
		if _, ok := p.Add(solid(40, 20)); !ok {
			t.Fatal("应当放得下")
		}
	}
	sheets := p.Sheets()
	if len(sheets) != 1 {
		t.Fatalf("这些图应当装进 1 张图集，实际 %d 张", len(sheets))
	}
	// 20 个 40x20（+1 内边距）在 256 宽下大约排 6 列 × 4 行，高度远小于上限
	if h := sheets[0].Rect.Dy(); h > 120 {
		t.Fatalf("图集高度应裁剪到实际用量（约 84），得到 %d", h)
	}
	if u := p.Utilization(); u < 0.7 {
		t.Fatalf("利用率应高于 70%%，得到 %.0f%%", u*100)
	}
}

// 按高度降序摆放是货架装箱的关键；顺序不同不应改变「不重叠」这一正确性。
func TestPackerOrderIndependent(t *testing.T) {
	build := func(desc bool) *Packer {
		p := NewPacker(128, 4096)
		sizes := [][2]int{{10, 80}, {60, 12}, {30, 30}, {20, 55}}
		for i := 0; i < 24; i++ {
			sz := sizes[i%len(sizes)]
			if desc {
				sz = sizes[(len(sizes)-1)-(i%len(sizes))]
			}
			p.Add(solid(sz[0], sz[1]))
		}
		return p
	}
	a, b := build(true), build(false)
	if len(a.Sheets()) != len(b.Sheets()) {
		t.Fatalf("不同加入顺序应得到相同张数，%d vs %d", len(a.Sheets()), len(b.Sheets()))
	}
}
