// Command prerender 是 ISO-CITY '98 的离线预渲染管线入口。
//
// 它把 catalog 里每个程序化单体在白天/夜晚两档光照下渲染成透明背景精灵图，
// 装箱成图集 PNG，并写出运行时需要的 manifest.json 与 palette.json。
// 运行时（网页端）只做精灵图合成，不做任何实时建模或光照。
//
//	go run ./cmd/prerender -out web/assets -build build
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"isocity98/internal/art"
	"isocity98/internal/catalog"
	"isocity98/internal/render"
	"isocity98/internal/sheet"
)

type clipSprite struct {
	Name       string  `json:"name"`
	Label      string  `json:"label"`
	Kind       string  `json:"kind"`
	Category   string  `json:"category"`
	Variant    int     `json:"variant"`
	Variants   int     `json:"variants"`
	Anim       bool    `json:"anim"`
	Seasonal   bool    `json:"seasonal,omitempty"`
	Season     string  `json:"season,omitempty"`
	Sheet      int     `json:"sheet"`
	X          int     `json:"x"`
	Y          int     `json:"y"`
	W          int     `json:"w"`
	H          int     `json:"h"`
	AX         int     `json:"ax"`
	AY         int     `json:"ay"`
	NightSheet int     `json:"nightSheet"`
	NightX     int     `json:"nightX"`
	NightY     int     `json:"nightY"`
	FP         [2]int  `json:"fp"`
	Height     float64 `json:"h3d"`
	Cost       int     `json:"cost,omitempty"`
	Pop        int     `json:"pop,omitempty"`
	Jobs       int     `json:"jobs,omitempty"`
	Level      int     `json:"level,omitempty"`
	Water      bool    `json:"water,omitempty"`

	// 装箱句柄：Finish 之后才解析成 Sheet/X/Y
	dayH, nightH int
}

type manifest struct {
	Version   string       `json:"version"`
	Generated string       `json:"generated"`
	Palette   string       `json:"palette"`
	Scale     int          `json:"scale"`
	Tile      tileSpec     `json:"tile"`
	Sheets    []sheetSpec  `json:"sheets"`
	Sprites   []clipSprite `json:"sprites"`
	PalCount  int          `json:"palCount"`
}

type tileSpec struct {
	W int `json:"w"`
	H int `json:"h"`
	Z int `json:"z"`
}

type sheetSpec struct {
	File string `json:"file"`
	W    int    `json:"w"`
	H    int    `json:"h"`
}

func main() {
	outDir := flag.String("out", "web/assets", "资产输出目录")
	buildDir := flag.String("build", "build", "调试图输出目录")
	only := flag.String("only", "", "只渲染名称匹配该正则的单体")
	set := flag.String("set", "game", "资产集：game = 城市建造沙盒；agenttown = Agent Town 监控面板")
	noNight := flag.Bool("no-night", false, "跳过夜间档")
	ss := flag.Int("ss", 4, "超采样倍率")
	scaleFlag := flag.Int("scale", 2, "像素密度：1 = 1 格 32x16 像素，2 = 64x32（分辨率翻倍）")
	atlasSize := flag.Int("atlas", 0, "图集边长；0 = 按像素密度自动（2048*scale，上限 4096）")
	sheetPath := flag.String("sheet", "", "输出接触印相图路径")
	scenePath := flag.String("scene", "", "输出参考城市场景预览路径")
	tintPath := flag.String("tint", "", "输出 4 季 x 4 时段调色板重映射对比图（agenttown 专用）")
	sunPath := flag.String("sun", "", "输出 清晨/正午/黄昏 三档运行时阴影对比图（agenttown 专用）")
	season := flag.String("season", "summer", "预览所用季节：spring/summer/autumn/winter")
	tod := flag.String("tod", "day", "预览所用时段：dawn/day/dusk/night")
	weather := flag.String("weather", "", "预览所用天气：clear/rain/snow/fog/petals；留空则跟随季节的典型天气")
	weatherPath := flag.String("weathers", "", "输出天气对比图（agenttown 专用）")
	todsPath := flag.String("tods", "", "输出昼夜四段对比图（agenttown 专用）")
	townPath := flag.String("town", "", "导出小镇布局 JSON（供面板运行时使用，agenttown 专用）")
	seasonsPath := flag.String("seasons", "", "输出四季对比图（2x2，agenttown 专用）")
	sheetCols := flag.Int("cols", 10, "接触印相图列数")
	scale := flag.Int("zoom", 2, "接触印相图/场景预览的放大倍率")
	flag.Parse()

	// 像素密度必须在取任何几何/画布范围之前设定
	pxScale := *scaleFlag
	render.SetScale(pxScale)
	catalog.SetScale(pxScale)
	atlas := *atlasSize
	if atlas <= 0 {
		atlas = 2048 * pxScale
		if atlas > 4096 {
			atlas = 4096
		}
	}

	pal := art.Pal()
	pal.BuildLUT()

	// 资产集：两个题材共用同一套管线与通用瓦片，只换「建筑」这一层
	var defs []catalog.Def
	switch *set {
	case "agenttown":
		defs = append(catalog.AgentTownDefs(), catalog.GenericDefs()...)
		defs = append(defs, catalog.AgentTownNPCs()...)
		defs = append(defs, catalog.AgentTownSeasonal()...)
		defs = append(defs, catalog.AgentTownOpenDefs()...)
		defs = append(defs, catalog.AgentTownFillerDefs()...)
	case "game":
		defs = catalog.All()
	default:
		fail("未知资产集 " + *set + "（可选 game / agenttown）")
	}
	if *only != "" {
		re := regexp.MustCompile(*only)
		var keep []catalog.Def
		for _, d := range defs {
			if re.MatchString(d.Name) {
				keep = append(keep, d)
			}
		}
		defs = keep
	}
	if len(defs) == 0 {
		fail("没有匹配的单体")
	}

	day := render.New(pal, art.Day)
	night := render.New(pal, art.Night)

	// 单张图集的高度上限压在 4096：超过它就可能越过 GPU 纹理尺寸上限，
	// 导致浏览器退回软件合成、每次 drawImage 都变慢。
	// 宁可多开一张图集，也不要把单张撑到 4096 以上。
	packer := sheet.NewPacker(atlas, 4096)
	var manifestSprites []clipSprite
	var all []*render.Sprite
	byName := map[string]*render.Sprite{}

	start := time.Now()
	total := 0
	for _, d := range defs {
		opts := optionsFor(d, *ss, art.Day)
		nopts := optionsFor(d, *ss, art.Night)
		// 形状随季节变化的资产（树/灌木/花坛/水面）按季节各烘一套；
		// 其余资产只烘一套 —— 季节的颜色差异交给运行时调色板重映射。
		seasonList := []string{""}
		if d.Seasonal {
			seasonList = catalog.Seasons
		}
		for _, sn := range seasonList {
			if sn != "" {
				catalog.SetSeason(sn)
			}
			for v := 0; v < d.Variants; v++ {
				mesh := catalog.BuildMesh(d, v)
				// 季节资产允许在某些季节**根本不存在**：例如地面积雪覆盖层在夏天
				// 就是没有的。空网格在这里是「本季无此物」的正常表达，不是错误。
				if len(mesh.Quads) == 0 && d.Seasonal {
					continue
				}
				name := variantLabel(d, v)
				if sn != "" {
					name = seasonalLabel(d, v, sn)
				}
				sp, err := day.Render(name, mesh, opts)
				if err != nil {
					fail(fmt.Sprintf("%s: %v", name, err))
				}
				sp.Name = name
				cs := clipSprite{
					NightSheet: -1, NightX: 0, NightY: 0, dayH: -1, nightH: -1,
					Name: name, Label: d.Label, Kind: d.Kind, Category: d.Category,
					Variant: v, Variants: d.Variants, Anim: d.Anim,
					Seasonal: d.Seasonal, Season: sn,
					W: sp.W, H: sp.H, AX: sp.AnchorX, AY: sp.AnchorY,
					FP: d.Footprint, Height: d.Height,
					Cost: d.Cost, Pop: d.Pop, Jobs: d.Jobs, Level: d.Level, Water: d.Water,
				}
				var ok bool
				cs.dayH, ok = packer.Add(sp.Img)
				if !ok {
					fail(fmt.Sprintf("%s: 精灵图 %dx%d 超过图集尺寸，请调大 -atlas", name, sp.W, sp.H))
				}
				if !*noNight {
					// 夜间档复用白天裁剪后的画布，保证两张图尺寸与锚点完全一致，
					// 这样运行时可原样替换，不必做任何偏移修正。
					nb := render.Rect{
						MinX: -sp.AnchorX, MinY: -sp.AnchorY,
						MaxX: -sp.AnchorX + sp.W, MaxY: -sp.AnchorY + sp.H,
					}
					nopts.Bounds = &nb
					nopts.NoTrim = true
					nsp, err := night.Render(name, mesh, nopts)
					if err != nil {
						fail(fmt.Sprintf("%s(night): %v", name, err))
					}
					if nsp.W != sp.W || nsp.H != sp.H || nsp.AnchorX != sp.AnchorX || nsp.AnchorY != sp.AnchorY {
						fail(fmt.Sprintf("%s: 昼/夜精灵不一致 %dx%d@%d,%d vs %dx%d@%d,%d",
							name, sp.W, sp.H, sp.AnchorX, sp.AnchorY, nsp.W, nsp.H, nsp.AnchorX, nsp.AnchorY))
					}
					var nok bool
					cs.nightH, nok = packer.Add(nsp.Img)
					if !nok {
						fail(fmt.Sprintf("%s: 夜间精灵图超过图集尺寸", name))
					}
				}
				manifestSprites = append(manifestSprites, cs)
				all = append(all, sp)
				byName[name] = sp
				total++
			}
		}
		if d.Seasonal {
			catalog.SetSeason("summer")
		}
	}

	// 装箱完成后才能解析落位；每张图集按实际用到的高度分配
	packer.Finish()
	for i := range manifestSprites {
		cs := &manifestSprites[i]
		pl := packer.At(cs.dayH)
		cs.Sheet, cs.X, cs.Y = pl.Sheet, pl.X, pl.Y
		if cs.nightH >= 0 {
			np := packer.At(cs.nightH)
			cs.NightSheet, cs.NightX, cs.NightY = np.Sheet, np.X, np.Y
		}
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fail(err.Error())
	}
	// 图集
	var sheets []sheetSpec
	for i, s := range packer.Sheets() {
		f := fmt.Sprintf("atlas_%d.png", i)
		if err := art.SavePNG(filepath.Join(*outDir, f), s); err != nil {
			fail(err.Error())
		}
		sheets = append(sheets, sheetSpec{File: f, W: s.Rect.Dx(), H: s.Rect.Dy()})
	}
	// 色板
	pj, err := pal.ToJSON("ISO-CITY '98 master palette")
	if err != nil {
		fail(err.Error())
	}
	writeFile(filepath.Join(*outDir, "palette.json"), pj)

	man := manifest{
		Version:   "1.0",
		Generated: time.Now().Format(time.RFC3339),
		Palette:   "palette.json",
		Scale:     pxScale,
		Tile:      tileSpec{W: render.TileW, H: render.TileH, Z: render.ZUnit},
		Sheets:    sheets,
		Sprites:   manifestSprites,
		PalCount:  pal.Len(),
	}
	sort.Slice(man.Sprites, func(i, j int) bool { return man.Sprites[i].Name < man.Sprites[j].Name })
	mj, err := json.MarshalIndent(man, "", " ")
	if err != nil {
		fail(err.Error())
	}
	writeFile(filepath.Join(*outDir, "manifest.json"), mj)

	var atlasPx int64
	for _, s := range sheets {
		atlasPx += int64(s.W) * int64(s.H)
	}
	fmt.Printf("已渲染 %d 张精灵图（%d 张图集，共 %.1fM 像素 ≈ %.0fMB RGBA，利用率 %.0f%%，像素密度 %dx），耗时 %s\n",
		total*(boolToInt(!*noNight)+1), len(sheets),
		float64(atlasPx)/1e6, float64(atlasPx)*4/1048576, packer.Utilization()*100, pxScale,
		time.Since(start).Round(time.Millisecond))

	if *sheetPath != "" {
		contact := sheet.Contact(all, *sheetCols, *scale, pal)
		ensureDir(*sheetPath)
		if err := art.SavePNG(*sheetPath, contact); err != nil {
			fail(err.Error())
		}
		idx := ""
		for i, s := range all {
			idx += fmt.Sprintf("%3d  %s\n", i, s.Name)
		}
		writeFile(*sheetPath+".txt", []byte(idx))
		fmt.Printf("接触印相图：%s（%d 张，%dx%d）\n", *sheetPath, len(all), contact.Rect.Dx(), contact.Rect.Dy())
	}

	if *scenePath != "" {
		spriteSet := &sheet.Set{Sprites: byName}
		var sc *image.NRGBA
		if *set == "agenttown" {
			sc = AgentTownSceneSun(spriteSet, pal, *scale, atSceneOpts{Defs: defs, Season: *season, Time: *tod, Weather: *weather})
		} else {
			sc = DemoScene(spriteSet, pal, *scale)
		}
		ensureDir(*scenePath)
		if err := art.SavePNG(*scenePath, sc); err != nil {
			fail(err.Error())
		}
		fmt.Printf("参考城市场景：%s（%dx%d）\n", *scenePath, sc.Rect.Dx(), sc.Rect.Dy())
	}
	if *townPath != "" {
		spriteSet := &sheet.Set{Sprites: byName}
		tm := agentTownMap(defs)
		if probs := checkAgentTownLayout(tm); len(probs) > 0 {
			fail(fmt.Sprintf("布局自检失败（%d 处）", len(probs)))
		}
		_ = spriteSet
		writeTown(*townPath, tm, pal, [3]int{render.TileW, render.TileH, render.ZUnit})
		// 同目录再出一份精简精灵表，运行时只需要它
		var sh [][2]int
		for _, sp := range sheets {
			sh = append(sh, [2]int{sp.W, sp.H})
		}
		writeSprites(filepath.Join(filepath.Dir(*townPath), "sprites.json"),
			manifestSprites, sh, [3]int{render.TileW, render.TileH, render.ZUnit})
	}
	if *todsPath != "" {
		spriteSet := &sheet.Set{Sprites: byName}
		bands := []string{"dawn", "day", "dusk", "night"}
		var panels []*image.NRGBA
		for _, b := range bands {
			panels = append(panels, AgentTownSceneSun(spriteSet, pal, 1,
				atSceneOpts{Defs: defs, Season: *season, Time: b, Weather: *weather}))
		}
		hw, hh := panels[0].Rect.Dx(), panels[0].Rect.Dy()
		grid := image.NewNRGBA(image.Rect(0, 0, hw*2, hh*2))
		for i, p := range panels {
			ox, oy := (i%2)*hw, (i/2)*hh
			for y := 0; y < hh; y++ {
				si := p.PixOffset(0, y)
				di := grid.PixOffset(ox, oy+y)
				copy(grid.Pix[di:di+hw*4], p.Pix[si:si+hw*4])
			}
		}
		ensureDir(*todsPath)
		if err := art.SavePNG(*todsPath, grid); err != nil {
			fail(err.Error())
		}
		fmt.Printf("昼夜对比（左上清晨/右上白天/左下黄昏/右下夜晚）：%s（%dx%d）\n",
			*todsPath, grid.Rect.Dx(), grid.Rect.Dy())
	}
	if *weatherPath != "" {
		spriteSet := &sheet.Set{Sprites: byName}
		var panels []*image.NRGBA
		for _, wx := range Weathers {
			panels = append(panels, AgentTownSceneSun(spriteSet, pal, 1,
				atSceneOpts{Defs: defs, Season: *season, Time: *tod, Weather: wx}))
		}
		// 全尺寸拼 3x2：天气靠细节（雨丝/雪花/雾带）辨认，缩一半就看不清了
		hw, hh := panels[0].Rect.Dx(), panels[0].Rect.Dy()
		rows := (len(panels) + 2) / 3
		grid := image.NewNRGBA(image.Rect(0, 0, hw*3, hh*rows))
		for i, p := range panels {
			ox, oy := (i%3)*hw, (i/3)*hh
			for y := 0; y < hh; y++ {
				si := p.PixOffset(0, y)
				di := grid.PixOffset(ox, oy+y)
				copy(grid.Pix[di:di+hw*4], p.Pix[si:si+hw*4])
			}
		}
		ensureDir(*weatherPath)
		if err := art.SavePNG(*weatherPath, grid); err != nil {
			fail(err.Error())
		}
		fmt.Printf("天气对比（晴/雨/雪 上排，雾/花信风 下排）：%s（%dx%d）\n",
			*weatherPath, grid.Rect.Dx(), grid.Rect.Dy())
	}
	if *seasonsPath != "" {
		spriteSet := &sheet.Set{Sprites: byName}
		var panels []*image.NRGBA
		for _, sn := range catalog.Seasons {
			panels = append(panels, AgentTownSceneSun(spriteSet, pal, 1,
				atSceneOpts{Defs: defs, Season: sn, Time: *tod, Weather: *weather}))
		}
		// 每张缩到 1/2 再拼 2x2，否则四张全尺寸拼起来太大不便审阅
		hw, hh := panels[0].Rect.Dx()/2, panels[0].Rect.Dy()/2
		grid := image.NewNRGBA(image.Rect(0, 0, hw*2, hh*2))
		for i, p := range panels {
			ox, oy := (i%2)*hw, (i/2)*hh
			for y := 0; y < hh; y++ {
				for x := 0; x < hw; x++ {
					si := p.PixOffset(x*2, y*2)
					di := grid.PixOffset(ox+x, oy+y)
					copy(grid.Pix[di:di+4], p.Pix[si:si+4])
				}
			}
		}
		ensureDir(*seasonsPath)
		if err := art.SavePNG(*seasonsPath, grid); err != nil {
			fail(err.Error())
		}
		fmt.Printf("四季对比（左上春/右上夏/左下秋/右下冬）：%s（%dx%d）\n",
			*seasonsPath, grid.Rect.Dx(), grid.Rect.Dy())
	}
	if *sunPath != "" {
		spriteSet := &sheet.Set{Sprites: byName}
		presets := []string{"morning", "noon", "dusk"}
		panels := make([]*image.NRGBA, 0, len(presets))
		for _, p := range presets {
			panels = append(panels, AgentTownSceneSun(spriteSet, pal, 1, atSceneOpts{Defs: defs, Sun: p, Season: *season, Time: *tod, Weather: *weather}))
		}
		w, h := panels[0].Rect.Dx(), panels[0].Rect.Dy()
		strip := image.NewNRGBA(image.Rect(0, 0, w, h*len(panels)))
		for i, p := range panels {
			for y := 0; y < h; y++ {
				si := p.PixOffset(0, y)
				di := strip.PixOffset(0, i*h+y)
				copy(strip.Pix[di:di+w*4], p.Pix[si:si+w*4])
			}
		}
		ensureDir(*sunPath)
		if err := art.SavePNG(*sunPath, strip); err != nil {
			fail(err.Error())
		}
		fmt.Printf("运行时阴影对比（清晨/正午/黄昏）：%s（%dx%d）\n", *sunPath, w, h*len(panels))
	}
	if *tintPath != "" {
		spriteSet := &sheet.Set{Sprites: byName}
		town := AgentTownSceneSun(spriteSet, pal, 1, atSceneOpts{Defs: defs, Season: *season, Time: *tod, Weather: *weather})
		grid := TintGrid(town, pal, 1)
		ensureDir(*tintPath)
		if err := art.SavePNG(*tintPath, grid); err != nil {
			fail(err.Error())
		}
		fmt.Printf("季节/时段重映射对比：%s（%dx%d）\n", *tintPath, grid.Rect.Dx(), grid.Rect.Dy())
	}
	_ = os.MkdirAll(*buildDir, 0o755)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// seasonalLabel 给季节资产命名：<原型>_<季节>[_<变体>]。
// 运行时按名字取图（季节是名字的一部分），因此 manifest 结构不需要为季节增加维度。
func seasonalLabel(d catalog.Def, v int, season string) string {
	base := d.Name + "_" + season
	if d.Variants == 1 {
		return base
	}
	return fmt.Sprintf("%s_%d", base, v)
}

func variantLabel(d catalog.Def, v int) string {
	if d.Anim {
		return fmt.Sprintf("%s_f%d", d.Name, v)
	}
	if d.Variants == 1 {
		return d.Name
	}
	return fmt.Sprintf("%s_%d", d.Name, v)
}

// optionsFor 按种类给出渲染参数：瓦片类必须固定画布并关掉裁剪与辉光。
func optionsFor(d catalog.Def, ss int, prof *art.Profile) render.Options {
	o := render.Options{SS: ss, Pad: 1, Sprite: art.DefaultSpriteOpts}
	switch d.Kind {
	case catalog.KindTerrain, catalog.KindRoad:
		rim := float32(0)
		if d.Cliff {
			rim = 0.16 // 侧壁顶缘提亮，帮助读出高差
		}
		o.Sprite = art.SpriteOpts{Dither: 1, Outline: 0, Rim: rim, EdgeFade: 0.5, BinaryAlpha: true, EdgeW: render.Scale}
		o.Expand = 0.6
		o.NoTrim = true
		o.NoGlow = true
		if d.Cliff {
			b := catalog.CliffBounds
			o.Bounds = &b
		} else if d.Bounds != nil {
			o.Bounds = d.Bounds
		} else {
			b := catalog.TileBounds
			o.Bounds = &b
		}
	default:
		o.Pad = 3 * render.Scale // 留出夜间辉光溢出的空间（随像素密度缩放）
		o.Sprite.EdgeW = render.Scale
		o.Shadow = render.ShadowOpts{
			On: !d.NoShadow, DX: 0.36, DY: 0.155,
			Alpha: prof.ShadowA, Steps: 7,
			Srcs: shadowSources(d),
		}
	}
	return o
}

func shadowSources(d catalog.Def) []render.ShadowSrc {
	if d.NoShadow || d.Height <= 0 {
		return nil
	}
	x0, y0 := 0.0, 0.0
	x1, y1 := float64(d.Footprint[0]), float64(d.Footprint[1])
	if len(d.ShadowFP) == 4 {
		x0, y0, x1, y1 = d.ShadowFP[0], d.ShadowFP[1], d.ShadowFP[2], d.ShadowFP[3]
	}
	return []render.ShadowSrc{{X0: x0, Y0: y0, X1: x1, Y1: y1, Z0: 0, Z1: d.Height}}
}

func writeFile(path string, data []byte) {
	if err := os.WriteFile(path, data, 0o644); err != nil {
		fail(err.Error())
	}
}

func ensureDir(path string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fail(err.Error())
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "prerender: "+msg)
	os.Exit(1)
}
