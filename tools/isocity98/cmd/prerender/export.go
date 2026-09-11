package main

import (
	"encoding/json"
	"fmt"
	"os"

	"isocity98/internal/art"
	"isocity98/internal/catalog"
)

// ─────────────────────────────────────────────────────────────────────────────
// 把小镇布局 + 色板重映射表导出给运行时（面板）。
//
// 设计原则：**Go 侧是布局与配色的唯一事实源**。
// 运行时不该重写一遍「秋天要怎么调色」——那是两份会漂移的实现。
// 所以这里直接把 4 季 x 4 时段 x 5 天气 = 80 张色板查表算好导出，
// JS 侧只负责「查表 + 换图 + 画粒子」。
// ─────────────────────────────────────────────────────────────────────────────

// townExport 是交给运行时的完整小镇描述。
type townExport struct {
	Tile  [3]int             `json:"tile"`
	W     int                `json:"w"`
	H     int                `json:"h"`
	Hgt   []int              `json:"height"`
	Ter   []string           `json:"-"`
	TerT  []string           `json:"terrainTable"`
	TerI  []int              `json:"terrain"` // 地形名在 TerT 中的下标，省掉 900 个字符串
	Road  []int              `json:"road"`
	Objs  []objOut           `json:"objects"`
	Stat  []stOut            `json:"stations"`
	Pal   []palOut           `json:"palette"`
	Remap map[string][]uint8 `json:"remaps"`
}

type objOut struct {
	Name string `json:"name"`
	X    int    `json:"x"`
	Y    int    `json:"y"`
	FW   int    `json:"fw"`
	FH   int    `json:"fh"`
}

type stOut struct {
	Stage  string `json:"stage"`
	Sprite string `json:"sprite"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Dir    int    `json:"dir"`
}

type palOut struct {
	R uint8 `json:"r"`
	G uint8 `json:"g"`
	B uint8 `json:"b"`
}

// writeTown 导出小镇 JSON。
func writeTown(path string, m *scenMap, pal *art.Palette, tile [3]int) {
	te := townExport{W: m.W, H: m.H, Hgt: m.Hgt}
	// 注意：scenMap.Road 只存「这里有没有路」的标记，真正的连通掩码要按邻格现算。
	// 直接导出原值会得到一片 0 —— 运行时会把每条路都画成孤立路桩。
	// 所以逐格调用 roadMask()，导出 -1（无路）/ 0..15（连通掩码）。
	te.Road = make([]int, len(m.Road))
	for y := 0; y < m.H; y++ {
		for x := 0; x < m.W; x++ {
			i := m.idx(x, y)
			if m.Road[i] < 0 {
				te.Road[i] = -1
				continue
			}
			te.Road[i] = m.roadMask(x, y)
		}
	}
	// 地形名去重成表
	idx := map[string]int{}
	for _, t := range m.Ter {
		if _, ok := idx[t]; !ok {
			idx[t] = len(te.TerT)
			te.TerT = append(te.TerT, t)
		}
		te.TerI = append(te.TerI, idx[t])
	}
	for _, o := range m.Obj {
		te.Objs = append(te.Objs, objOut{Name: o.Name, X: o.X, Y: o.Y, FW: o.FW, FH: o.FH})
	}
	for _, s := range m.Stations {
		te.Stat = append(te.Stat, stOut{Stage: s.Stage, Sprite: s.Sprite, X: s.X, Y: s.Y, Dir: s.Dir})
	}
	for _, c := range pal.Colors {
		te.Pal = append(te.Pal, palOut{R: c.R, G: c.G, B: c.B})
	}
	// 80 张色板查表
	te.Remap = map[string][]uint8{}
	for _, se := range catalog.Seasons {
		for _, td := range []string{"dawn", "day", "dusk", "night"} {
			for _, wx := range Weathers {
				rules := append(seasonRules(se), timeRules(td)...)
				rules = append(rules, weatherRules(wx)...)
				te.Remap[se+"|"+td+"|"+wx] = buildRemap(pal, rules)
			}
		}
	}
	te.Tile = tile
	b, err := json.Marshal(te)
	if err != nil {
		fail(err.Error())
	}
	ensureDir(path)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		fail(err.Error())
	}
	fmt.Printf("小镇布局已导出：%s（%d 格，%d 个单体，%d 个岗位，%d 张色板查表，%.0f KB）\n",
		path, m.W*m.H, len(te.Objs), len(te.Stat), len(te.Remap), float64(len(b))/1024)
}

// spriteOut 是给运行时的精简精灵表：只留渲染必需的字段。
// 完整 manifest 里有 label/category/cost/pop/... 一大串面板用不到的东西（342KB），
// 精简后约 1/4 —— 而它是要 base64 内联进单文件 HTML 的，体积直接翻 4/3。
type spriteOut struct {
	Name string `json:"n"`
	Rect [4]int `json:"r"` // sheet, x, y（宽高从 atlas 里按锚点推不出来，一并带上）
	WH   [2]int `json:"wh"`
	Ax   int    `json:"ax"`
	Ay   int    `json:"ay"`
}

type spritesDoc struct {
	Tile    [3]int               `json:"tile"`
	Sheets  [][2]int             `json:"sheets"`
	Sprites map[string]spriteOut `json:"sprites"`
}

// writeSprites 导出精简精灵表。
func writeSprites(path string, sprites []clipSprite, sheets [][2]int, tile [3]int) {
	doc := spritesDoc{Tile: tile, Sheets: sheets, Sprites: map[string]spriteOut{}}
	for _, s := range sprites {
		out := spriteOut{
			Name: s.Name,
			Rect: [4]int{s.Sheet, s.X, s.Y, 0},
			WH:   [2]int{s.W, s.H},
			Ax:   s.AX,
			Ay:   s.AY,
		}
		doc.Sprites[s.Name] = out
		// 夜间档单独一张表；这里记成 <name>#n 便于运行时直接取
		if s.NightSheet >= 0 {
			n := out
			n.Rect[0], n.Rect[1], n.Rect[2] = s.NightSheet, s.NightX, s.NightY
			doc.Sprites[s.Name+"#n"] = n
		}
	}
	b, err := json.Marshal(doc)
	if err != nil {
		fail(err.Error())
	}
	ensureDir(path)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		fail(err.Error())
	}
	fmt.Printf("精简精灵表已导出：%s（%d 项，%.0f KB）\n", path, len(doc.Sprites), float64(len(b))/1024)
}
