package main

import (
	"fmt"
	"image"

	"isocity98/internal/art"
	"isocity98/internal/render"
	"isocity98/internal/sheet"
)

// 参考城市场景合成器：与网页端运行时使用**完全相同**的绘制顺序与锚点算法，
// 因此它既是美术审阅工具，也是运行时行为的可执行规格说明。
//
// 绘制顺序（等距画家算法）：
//   1. 按 d = x+y 升序逐格绘制地形、侧壁、道路；
//   2. 单体按「占地东南角」的 d 值排序插入绘制，
//      这样多格单体的精灵总是覆盖其占地内所有格子的地形。

type scenObj struct {
	Name   string
	X, Y   int
	FW, FH int
}

type scenMap struct {
	W, H int
	Hgt  []int
	Ter  []string
	Road []int // -1 = 无路，否则为位掩码
	Obj  []scenObj
	// Stations 是「职业 → 到岗位置」的映射，供运行时把 agent 送到对应建筑。
	// 由布局函数在自动排位时填好 —— Go 侧是布局的唯一事实源。
	Stations []npcStation
}

// npcStation 是某个 stage 的居民站位。
type npcStation struct {
	Stage  string `json:"stage"`  // STAGE key，如 "refining"
	Sprite string `json:"sprite"` // 精灵原型名，如 "npc.refiner"
	X, Y   int    `json:"-"`
	Dir    int    `json:"-"`
}

func newScenMap(w, h int, ter string) *scenMap {
	m := &scenMap{W: w, H: h}
	m.Hgt = make([]int, w*h)
	m.Ter = make([]string, w*h)
	m.Road = make([]int, w*h)
	for i := range m.Road {
		m.Road[i] = -1
		m.Ter[i] = ter
	}
	return m
}

func (m *scenMap) idx(x, y int) int { return y*m.W + x }

func (m *scenMap) inside(x, y int) bool { return x >= 0 && y >= 0 && x < m.W && y < m.H }

func (m *scenMap) SetH(x, y, h int) {
	if m.inside(x, y) {
		m.Hgt[m.idx(x, y)] = h
	}
}

// At 返回格子的地形高度；地图外返回 0（即与基准地面齐平），
// 于是地图边缘只会因为「抬高的地块」而露出侧壁。
func (m *scenMap) At(x, y int) int {
	if !m.inside(x, y) {
		return 0
	}
	return m.Hgt[m.idx(x, y)]
}

func (m *scenMap) Paint(x0, y0, x1, y1 int, ter string) {
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			if m.inside(x, y) {
				m.Ter[m.idx(x, y)] = ter
			}
		}
	}
}

func (m *scenMap) RoadH(y, x0, x1 int) {
	for x := x0; x <= x1; x++ {
		if m.inside(x, y) {
			m.Road[m.idx(x, y)] = 0
		}
	}
}

func (m *scenMap) RoadV(x, y0, y1 int) {
	for y := y0; y <= y1; y++ {
		if m.inside(x, y) {
			m.Road[m.idx(x, y)] = 0
		}
	}
}

func (m *scenMap) Add(name string, x, y, fw, fh int) {
	m.Obj = append(m.Obj, scenObj{Name: name, X: x, Y: y, FW: fw, FH: fh})
}

// roadMask 计算位掩码：bit0=+X bit1=+Y bit2=-X bit3=-Y。
func (m *scenMap) roadMask(x, y int) int {
	dirs := [4][2]int{{1, 0}, {0, 1}, {-1, 0}, {0, -1}}
	mask := 0
	for i, d := range dirs {
		nx, ny := x+d[0], y+d[1]
		if !m.inside(nx, ny) {
			continue
		}
		if m.Road[m.idx(nx, ny)] >= 0 {
			mask |= 1 << uint(i)
		}
	}
	return mask
}

var cliffName = [4]string{"cliff.soil.e", "cliff.soil.s", "cliff.soil.w", "cliff.soil.n"}

// RenderScene 把地图合成为一张 PNG。
func RenderScene(m *scenMap, set *sheet.Set, pal *art.Palette, scale int) *image.NRGBA {
	const padX, padY = 26, 60
	minSX, maxSX := 1<<30, -(1 << 30)
	minSY, maxSY := 1<<30, -(1 << 30)
	for y := 0; y < m.H; y++ {
		for x := 0; x < m.W; x++ {
			sx := (x - y) * (render.TileW / 2)
			sy := (x+y)*(render.TileH/2) - m.At(x, y)*render.ZUnit
			minSX, maxSX = min(minSX, sx-20), max(maxSX, sx+20)
			minSY, maxSY = min(minSY, sy-70), max(maxSY, sy+30)
		}
	}
	W := maxSX - minSX + padX*2
	H := maxSY - minSY + padY*2
	base := image.NewNRGBA(image.Rect(0, 0, W, H))
	sheet.Sky(base, pal, "sky", "sky")
	dst := image.NewNRGBA(image.Rect(0, 0, W*scale, H*scale))
	scaleBlit(dst, base, scale)

	cam := sheet.Cam{OX: -minSX + padX, OY: -minSY + padY, Scale: scale}
	draw := func(name string, cx, cy, z float64) {
		set.Draw(dst, cam, name, cx, cy, z)
	}

	maxD := m.W + m.H
	for d := 0; d <= maxD; d++ {
		// 1) 地形与道路
		for x := 0; x < m.W; x++ {
			y := d - x
			if !m.inside(x, y) {
				continue
			}
			h := m.At(x, y)
			ter := m.Ter[m.idx(x, y)]
			// 变体号必须安全回退：各地形的变体数不一致，硬拼会留下透底的空洞
			set.DrawV(dst, cam, ter, (x*7+y*13)%3, float64(x), float64(y), float64(h))
			// 侧壁：只画邻居更低的那几层
			dirs := [4][2]int{{1, 0}, {0, 1}, {-1, 0}, {0, -1}}
			for i, dd := range dirs {
				nh := m.At(x+dd[0], y+dd[1])
				for k := 0; k < h-nh; k++ {
					// 侧壁精灵带变体后缀（cliff.soil.e_0/_1），而 cliffName 是基础名。
					// 用精确查找会整片取不到 —— 参考图会「没有侧壁」，画面上只是少了立体感，
					// 不报错、不空洞，极难发现（运行时侧壁正常，反倒暴露了离线图的缺失）。
					set.DrawV(dst, cam, cliffName[i], (x+y+k)%2, float64(x), float64(y), float64(h-k))
				}
			}
			if mask := m.Road[m.idx(x, y)]; mask >= 0 {
				draw(fmt.Sprintf("road.asphalt.%x", m.roadMask(x, y)), float64(x), float64(y), float64(h))
			}
		}
		// 2) 单体：按占地东南角的 d 值插入
		for _, o := range m.Obj {
			if o.X+o.FW-1+o.Y+o.FH-1 != d {
				continue
			}
			draw(o.Name, float64(o.X), float64(o.Y), float64(m.At(o.X, o.Y)))
		}
	}
	return dst
}

// DemoScene 构造一座用于审阅的小城。
func DemoScene(set *sheet.Set, pal *art.Palette, scale int) *image.NRGBA {
	m := newScenMap(19, 17, "terrain.grass")
	// 湖与沙滩（西南）
	for y := 12; y <= 16; y++ {
		for x := 0; x <= 5; x++ {
			m.SetH(x, y, -1)
			m.Ter[m.idx(x, y)] = "terrain.water"
		}
	}
	m.Paint(4, 11, 6, 12, "terrain.sand")
	m.SetH(5, 12, -1)
	m.Ter[m.idx(5, 12)] = "terrain.water"
	// 台地（东北）与山丘（西北）
	for y := 0; y <= 3; y++ {
		for x := 14; x <= 18; x++ {
			m.SetH(x, y, 1)
			m.Ter[m.idx(x, y)] = "terrain.gravel"
		}
	}
	for y := 0; y <= 1; y++ {
		for x := 16; x <= 18; x++ {
			m.SetH(x, y, 2)
			m.Ter[m.idx(x, y)] = "terrain.rock"
		}
	}
	m.Paint(0, 0, 3, 2, "terrain.meadow")
	m.SetH(0, 0, 1)
	m.SetH(1, 0, 1)
	m.SetH(0, 1, 1)
	// 农田（东南）
	for y := 12; y <= 16; y++ {
		for x := 12; x <= 18; x++ {
			m.Ter[m.idx(x, y)] = "terrain.farm"
		}
	}
	// 道路网
	m.RoadH(6, 2, 18)
	m.RoadH(12, 6, 18)
	m.RoadV(8, 0, 16)
	m.RoadV(13, 0, 5)
	// 广场
	m.Paint(9, 9, 11, 10, "terrain.pave")
	m.Paint(2, 3, 6, 5, "terrain.pave")
	m.Paint(14, 7, 17, 10, "terrain.concrete")

	// 市中心
	m.Add("office.tower_0", 9, 2, 2, 2)
	m.Add("office.block_0", 11, 2, 2, 2)
	m.Add("office.block_1", 9, 4, 2, 2)
	m.Add("shop.row_0", 11, 4, 2, 1)
	m.Add("townhall_0", 14, 2, 2, 2)
	m.Add("apartment.tower_0", 6, 2, 2, 2)
	m.Add("apartment.walkup_0", 2, 4, 2, 2)
	m.Add("shop.corner_0", 6, 4, 1, 1)
	m.Add("shop.corner_1", 7, 5, 1, 1)
	// 湖滨住宅
	m.Add("house.villa_0", 6, 13, 2, 2)
	m.Add("house.small_0", 7, 15, 1, 1)
	m.Add("house.suburban_1", 4, 7, 1, 1)
	m.Add("house.small_2", 3, 8, 1, 1)
	m.Add("house.townhouse_0", 5, 8, 2, 1)
	m.Add("house.suburban_0", 6, 9, 1, 1)
	m.Add("park.small_0", 3, 10, 1, 1)
	m.Add("park.small_1", 5, 10, 1, 1)
	// 工业区
	m.Add("warehouse_0", 14, 8, 3, 2)
	m.Add("factory_0", 14, 11, 3, 3)
	m.Add("warehouse_1", 10, 14, 3, 2)
	// 台地上的学校
	m.Add("school_0", 15, 1, 3, 2)

	// 绿化
	trees := []struct{ x, y int }{
		{2, 6}, {4, 6}, {2, 11}, {6, 12}, {7, 12}, {2, 7}, {3, 3}, {5, 3},
		{8, 5}, {12, 7}, {13, 7}, {12, 10}, {17, 6}, {18, 6}, {18, 12}, {11, 13},
		{0, 3}, {1, 3}, {0, 4}, {2, 2}, {1, 5}, {18, 4}, {17, 3}, {13, 12},
	}
	for i, t := range trees {
		m.Add(fmt.Sprintf("tree.oak_%d", i%5), t.x, t.y, 1, 1)
	}
	for i := 0; i < 6; i++ {
		m.Add(fmt.Sprintf("tree.pine_%d", i%3), 17+i%2, 1+i/2, 1, 1)
	}
	return RenderScene(m, set, pal, scale)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func scaleBlit(dst *image.NRGBA, src *image.NRGBA, scale int) {
	for y := 0; y < src.Rect.Dy(); y++ {
		for x := 0; x < src.Rect.Dx(); x++ {
			i := src.PixOffset(x, y)
			for sy := 0; sy < scale; sy++ {
				for sx := 0; sx < scale; sx++ {
					j := dst.PixOffset(x*scale+sx, y*scale+sy)
					copy(dst.Pix[j:j+4], src.Pix[i:i+4])
				}
			}
		}
	}
}

func drawScaledAt(dst *image.NRGBA, src *image.NRGBA, ox, oy, scale int) {
	for y := 0; y < src.Rect.Dy(); y++ {
		for x := 0; x < src.Rect.Dx(); x++ {
			si := src.PixOffset(x, y)
			a := src.Pix[si+3]
			if a == 0 {
				continue
			}
			for sy := 0; sy < scale; sy++ {
				for sx := 0; sx < scale; sx++ {
					dx, dy := ox+x*scale+sx, oy+y*scale+sy
					if dx < 0 || dy < 0 || dx >= dst.Rect.Dx() || dy >= dst.Rect.Dy() {
						continue
					}
					blendPix(dst, dx, dy, src.Pix[si], src.Pix[si+1], src.Pix[si+2], a)
				}
			}
		}
	}
}

func blendPix(dst *image.NRGBA, x, y int, r, g, b, a uint8) {
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
	mix := func(s, d uint32) uint8 { return uint8((s*sa + d*da*ia/255) / oa) }
	dst.Pix[i] = mix(uint32(r), uint32(dst.Pix[i]))
	dst.Pix[i+1] = mix(uint32(g), uint32(dst.Pix[i+1]))
	dst.Pix[i+2] = mix(uint32(b), uint32(dst.Pix[i+2]))
	dst.Pix[i+3] = uint8(oa)
}
