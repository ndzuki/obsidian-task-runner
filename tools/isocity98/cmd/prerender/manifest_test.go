package main

import (
	"encoding/json"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"isocity98/internal/art"
	"isocity98/internal/catalog"
	"isocity98/internal/render"
)

// 资产一致性测试：把生成出来的 manifest.json / palette.json 与代码里的
// 真实目录、母板对一遍。这类漂移在运行时往往只表现为「某块地画不出来」，
// 很难靠肉眼发现，所以必须由测试守着。
//
// 未生成资产时跳过（先跑 make assets）。

type tManifest struct {
	Scale  int                   `json:"scale"`
	Tile   struct{ W, H, Z int } `json:"tile"`
	Sheets []struct {
		File string `json:"file"`
		W    int    `json:"w"`
		H    int    `json:"h"`
	} `json:"sheets"`
	PalCount int `json:"palCount"`
	Sprites  []struct {
		Name       string `json:"name"`
		Kind       string `json:"kind"`
		Category   string `json:"category"`
		Sheet      int    `json:"sheet"`
		X, Y, W, H int
		AX, AY     int
		NightSheet int `json:"nightSheet"`
		NightX     int `json:"nightX"`
		NightY     int `json:"nightY"`
	} `json:"sprites"`
}

type tPalette struct {
	Count  int `json:"count"`
	Colors []struct {
		Hex string `json:"hex"`
		R   int    `json:"r"`
		G   int    `json:"g"`
		B   int    `json:"b"`
	} `json:"colors"`
	Ramps  [][]int        `json:"ramps"`
	RampID map[string]int `json:"rampId"`
}

func loadJSON(t *testing.T, path string, v any) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("解析 %s 失败：%v", path, err)
	}
	return true
}

func assetsDir() string { return filepath.Join("..", "..", "web", "assets") }

func TestManifestMatchesCode(t *testing.T) {
	dir := assetsDir()
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		t.Skip("尚未生成资产，跳过（先执行 make assets）")
	}
	var man tManifest
	if !loadJSON(t, filepath.Join(dir, "manifest.json"), &man) {
		t.Fatal("无法读取 manifest.json")
	}

	// 资产是按某个像素密度产出的：先让代码侧对齐同一密度，否则瓦片尺寸对不上
	if man.Scale < 1 {
		man.Scale = 1
	}
	render.SetScale(man.Scale)
	catalog.SetScale(man.Scale)
	defer func() { render.SetScale(1); catalog.SetScale(1) }()

	if man.Tile.W != render.TileW || man.Tile.H != render.TileH || man.Tile.Z != render.ZUnit {
		t.Fatalf("manifest 的瓦片尺寸 %dx%d/%d 与代码侧 %dx%d/%d 不一致",
			man.Tile.W, man.Tile.H, man.Tile.Z, render.TileW, render.TileH, render.ZUnit)
	}

	// 1) 图集矩形必须落在画布内，且互不重叠
	type rect struct{ sheet, x, y, w, h int }
	var rects []rect
	for _, s := range man.Sprites {
		sheets := []struct {
			idx     int
			x, y    int
			w, h    int
			isNight bool
		}{
			{s.Sheet, s.X, s.Y, s.W, s.H, false},
		}
		if s.NightSheet >= 0 {
			sheets = append(sheets, struct {
				idx     int
				x, y    int
				w, h    int
				isNight bool
			}{s.NightSheet, s.NightX, s.NightY, s.W, s.H, true})
		}
		for _, r := range sheets {
			if r.idx < 0 || r.idx >= len(man.Sheets) {
				t.Fatalf("%s 引用了不存在的图集 %d", s.Name, r.idx)
			}
			sh := man.Sheets[r.idx]
			if r.x < 0 || r.y < 0 || r.x+r.w > sh.W || r.y+r.h > sh.H {
				t.Fatalf("%s 的矩形 (%d,%d,%d,%d) 超出图集 %dx%d",
					s.Name, r.x, r.y, r.w, r.h, sh.W, sh.H)
			}
			// 锚点即「网格局部原点在图像中的位置」，裁剪后可能略微越界，
			// 只要不离谱（几何没有建到远离原点的地方）即可。
			margin := s.W / 2
			if s.H/2 > margin {
				margin = s.H / 2
			}
			if margin < 16 {
				margin = 16
			}
			if s.AX < -margin || s.AX > s.W+margin || s.AY < -margin || s.AY > s.H+margin {
				t.Fatalf("%s 的锚点 (%d,%d) 距离图像 %dx%d 过远", s.Name, s.AX, s.AY, s.W, s.H)
			}
			rects = append(rects, rect{r.idx, r.x, r.y, r.w, r.h})
		}
	}
	for i := 0; i < len(rects); i++ {
		for j := i + 1; j < len(rects); j++ {
			a, b := rects[i], rects[j]
			if a.sheet != b.sheet {
				continue
			}
			if a.x < b.x+b.w && b.x < a.x+a.w && a.y < b.y+b.h && b.y < a.y+a.h {
				t.Fatalf("图集矩形重叠：%v 与 %v", a, b)
			}
		}
	}

	// 2) 图集 PNG 必须真的存在，且尺寸与声明一致
	for _, sh := range man.Sheets {
		f, err := os.Open(filepath.Join(dir, sh.File))
		if err != nil {
			t.Fatalf("图集文件缺失：%s", sh.File)
		}
		cfg, _, err := image.DecodeConfig(f)
		f.Close()
		if err != nil {
			t.Fatalf("图集 %s 不是合法图片：%v", sh.File, err)
		}
		if cfg.Width != sh.W || cfg.Height != sh.H {
			t.Fatalf("图集 %s 实际 %dx%d，manifest 声明 %dx%d",
				sh.File, cfg.Width, cfg.Height, sh.W, sh.H)
		}
	}

	// 3) manifest 里的每个精灵名都必须能在目录里找到对应原型
	byName := map[string]bool{}
	for _, d := range catalog.All() {
		for v := 0; v < d.Variants; v++ {
			byName[variantLabelForTest(d, v)] = true
		}
	}
	for _, s := range man.Sprites {
		if !byName[s.Name] {
			t.Fatalf("manifest 里的 %s 在目录里找不到对应原型（资产已过期？）", s.Name)
		}
		delete(byName, s.Name)
	}
	if len(byName) != 0 {
		missing := make([]string, 0, len(byName))
		for n := range byName {
			missing = append(missing, n)
		}
		t.Fatalf("目录里有 %d 个原型没有出现在 manifest 里（需要重跑 make assets）：%v", len(missing), missing)
	}

	// 3.5) 同类瓦片尺寸必须与目录声明的统一画布一致（随像素密度缩放）
	byNameTile := map[string]catalog.Def{}
	for _, d := range catalog.All() {
		byNameTile[d.Name] = d
	}
	for _, sp := range man.Sprites {
		base := sp.Name
		for i := 0; i < 12; i++ {
			if strings.HasSuffix(base, "_"+itoa(i)) && byNameTile[strings.TrimSuffix(base, "_"+itoa(i))].Name != "" {
				base = strings.TrimSuffix(base, "_"+itoa(i))
				break
			}
		}
		d, ok := byNameTile[base]
		if !ok || (d.Kind != catalog.KindTerrain && d.Kind != catalog.KindRoad) {
			continue
		}
		want := catalog.TileBounds
		if d.Cliff {
			want = catalog.CliffBounds
		}
		if sp.W != want.W() || sp.H != want.H() {
			t.Fatalf("%s 尺寸 %dx%d，应为 %dx%d（像素密度 %dx）",
				sp.Name, sp.W, sp.H, want.W(), want.H(), man.Scale)
		}
	}

	// 4) 运行时依赖的关键瓦片必须齐全
	need := []string{"terrain.grass_0", "terrain.water_f0", "cliff.soil.e_0", "road.asphalt.f"}
	have := map[string]bool{}
	for _, s := range man.Sprites {
		have[s.Name] = true
	}
	for _, n := range need {
		if !have[n] {
			t.Fatalf("运行时依赖的瓦片 %s 缺失", n)
		}
	}
}

func TestPaletteFileMatchesCode(t *testing.T) {
	dir := assetsDir()
	if _, err := os.Stat(filepath.Join(dir, "palette.json")); err != nil {
		t.Skip("尚未生成资产，跳过（先执行 make assets）")
	}
	var pj tPalette
	if !loadJSON(t, filepath.Join(dir, "palette.json"), &pj) {
		t.Fatal("无法读取 palette.json")
	}
	p := art.Pal()
	if pj.Count != p.Len() {
		t.Fatalf("palette.json 有 %d 色，代码里是 %d 色（需要重跑 make assets）", pj.Count, p.Len())
	}
	if len(pj.Colors) != p.Len() {
		t.Fatalf("palette.json colors 长度 %d 与 count %d 不一致", len(pj.Colors), pj.Count)
	}
	for i, c := range pj.Colors {
		want := p.Colors[i]
		if c.R != int(want.R) || c.G != int(want.G) || c.B != int(want.B) {
			t.Fatalf("第 %d 色不一致：palette.json %s，代码 %s", i, c.Hex, want)
		}
	}
	// 色阶表也必须一致，否则运行时的 snap/lerpRamp 会取错颜色
	for name, idx := range pj.RampID {
		if idx < 0 || idx >= len(pj.Ramps) {
			t.Fatalf("色阶 %s 的索引 %d 越界", name, idx)
		}
		got := pj.Ramps[idx]
		want := p.Ramp(name)
		if len(got) != len(want) {
			t.Fatalf("色阶 %s 长度不一致：%d vs %d", name, len(got), len(want))
		}
		for k := range got {
			if got[k] != want[k] {
				t.Fatalf("色阶 %s 第 %d 档不一致：%d vs %d", name, k, got[k], want[k])
			}
		}
	}
}

// variantLabelForTest 复刻 prerender 的命名规则，用于反查目录覆盖情况。
func variantLabelForTest(d catalog.Def, v int) string {
	if d.Anim {
		return d.Name + "_f" + itoa(v)
	}
	if d.Variants == 1 {
		return d.Name
	}
	return d.Name + "_" + itoa(v)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
