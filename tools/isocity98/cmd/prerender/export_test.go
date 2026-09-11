package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 面板资产包的契约测试。
//
// 这一组检查针对的是一类**导出成功、格式合法、值全错**的问题：
// 例如 road 数组曾整片导出成 0（连通掩码没算），运行时会画出一堆孤立路桩，
// 而导出日志一切正常。契约测试能在运行前把它拦住。
//
// 资产不存在时跳过（与 manifest 测试一致），因此 CI 上没跑过 `make agenttown` 也不会红。
func TestPanelBundleIsSelfConsistent(t *testing.T) {
	dir := filepath.Join("..", "..", "build", "panel")
	spritesRaw, err := os.ReadFile(filepath.Join(dir, "sprites.json"))
	if err != nil {
		t.Skip("没有面板资产包（先跑 make agenttown）")
	}
	townRaw, err := os.ReadFile(filepath.Join(dir, "town.json"))
	if err != nil {
		t.Fatalf("有 sprites.json 却没有 town.json：%v", err)
	}

	var doc struct {
		Tile    [3]int `json:"tile"`
		Sheets  [][2]int
		Sprites map[string]struct {
			Rect [4]int `json:"r"`
			WH   [2]int `json:"wh"`
			Ax   int    `json:"ax"`
			Ay   int    `json:"ay"`
		} `json:"sprites"`
	}
	if err := json.Unmarshal(spritesRaw, &doc); err != nil {
		t.Fatalf("sprites.json 解析失败：%v", err)
	}

	// 1) 图集尺寸要与 json 里声明的一致（否则运行时贴图会错位/越界）
	f, err := os.Open(filepath.Join(dir, "atlas_0.png"))
	if err != nil {
		t.Fatalf("打不开图集：%v", err)
	}
	cfg, _, err := image.DecodeConfig(f)
	f.Close()
	if err != nil {
		t.Fatalf("图集解码失败：%v", err)
	}
	if len(doc.Sheets) == 0 || doc.Sheets[0][0] != cfg.Width || doc.Sheets[0][1] != cfg.Height {
		t.Fatalf("图集实际 %dx%d，但 sprites.json 声明 %v", cfg.Width, cfg.Height, doc.Sheets)
	}

	// 2) 每个精灵的矩形必须落在图集内
	for name, sp := range doc.Sprites {
		sx, sy := sp.Rect[1], sp.Rect[2]
		if sx < 0 || sy < 0 || sx+sp.WH[0] > cfg.Width || sy+sp.WH[1] > cfg.Height {
			t.Errorf("%s 的矩形 (%d,%d)+%v 超出图集 %dx%d", name, sx, sy, sp.WH, cfg.Width, cfg.Height)
		}
		// 锚点允许略微落在裁剪后的图像**之外**：把内容裁到边界后，锚点（格子的北角）
		// 本来就可能落在内容上方或左侧（例如悬空的鸟、下沉的灌木）。
		// 绘制公式是 `anchorY - ay`，负值时图像整体下移，语义正确。
		// 但偏移不能离谱 —— 那说明几何建在了远离本格的地方。
		const margin = 8
		if sp.Ax < -margin || sp.Ax > sp.WH[0]+margin || sp.Ay < -margin || sp.Ay > sp.WH[1]+margin {
			t.Errorf("%s 的锚点 (%d,%d) 偏离图像 %v 超过 %d 像素", name, sp.Ax, sp.Ay, sp.WH, margin)
		}
	}

	// 3) 解析器：与运行时同一套回退规则。
	//
	// 两类资产的命名规则**不同**，这里必须都认：
	//   建筑/道具 → `基名_变体号`（如 at.pm_0）
	//   角色     → `基名_f帧号`（如 npc.pm_f4；帧号 = 方向*4 + 步态）
	//   季节资产 → `基名_季节_摇摆帧`（如 at.tree.oak_winter_1）
	resolve := func(base string) bool {
		for _, n := range []string{base, base + "_0", base + "_f0", base + "_summer_0"} {
			if _, ok := doc.Sprites[n]; ok {
				return true
			}
		}
		for _, se := range []string{"spring", "summer", "autumn", "winter"} {
			for sway := 0; sway < 3; sway++ {
				if _, ok := doc.Sprites[fmt.Sprintf("%s_%s_%d", base, se, sway)]; ok {
					return true
				}
			}
		}
		return false
	}

	var town struct {
		W, H     int
		Terrain  []int             `json:"terrain"`
		TerTable []string          `json:"terrainTable"`
		Road     []int             `json:"road"`
		Objects  []objOut          `json:"objects"`
		Stations []stOut           `json:"stations"`
		Palette  []palOut          `json:"palette"`
		Remaps   map[string]string `json:"remaps"`
	}
	if err := json.Unmarshal(townRaw, &town); err != nil {
		t.Fatalf("town.json 解析失败：%v", err)
	}
	if len(town.Terrain) != town.W*town.H || len(town.Road) != town.W*town.H {
		t.Fatalf("地形/道路数组长度 %d/%d，应为 %d", len(town.Terrain), len(town.Road), town.W*town.H)
	}

	// 4) 地形瓦片必须都能取到（每个用到的变体）
	for _, ti := range town.Terrain {
		if ti < 0 || ti >= len(town.TerTable) {
			t.Fatalf("地形下标 %d 越界（表长 %d）", ti, len(town.TerTable))
		}
	}
	// 5) 道路掩码必须是 -1 或 0..15，且**至少要有几种不同掩码** ——
	//    全是 0 正是「掩码没算」的典型症状
	masks := map[int]int{}
	for _, m := range town.Road {
		if m < -1 || m > 15 {
			t.Fatalf("道路掩码 %d 非法（应为 -1 或 0..15）", m)
		}
		masks[m]++
	}
	distinct := 0
	for m, n := range masks {
		if m >= 0 && n > 0 {
			distinct++
		}
	}
	if masks[-1] == 0 {
		t.Fatal("没有任何道路格")
	}
	if distinct < 3 {
		t.Fatalf("道路只有 %v 种连通掩码 —— 十字大道的直段/转角/丁字口至少该有 3 种；"+
			"全是 0 说明导出时没调用 roadMask()", masks)
	}

	// 6) 每个单体、每个岗位都要能取到精灵（走回退规则）
	for _, o := range town.Objects {
		if !resolve(o.Name) {
			t.Errorf("单体 %s 在精灵表里取不到（试过 %s / %s_0）", o.Name, o.Name, o.Name)
		}
	}
	for _, s := range town.Stations {
		if !resolve(s.Sprite) {
			t.Errorf("岗位 %s 的精灵 %s 取不到", s.Stage, s.Sprite)
		}
		if s.Sprite[:4] != "npc." {
			t.Errorf("岗位 %s 的精灵 %s 不是角色", s.Stage, s.Sprite)
		}
	}
	if len(town.Stations) != 21 {
		t.Errorf("岗位数 %d，应为 21（21 个 stage 各一个）", len(town.Stations))
	}

	// 7) 色板与查表：4 季 x 4 时段 x 5 天气，每张 97 字节
	if len(town.Palette) != 97 {
		t.Fatalf("调色板 %d 色，应为 97", len(town.Palette))
	}
	want := 4 * 4 * 5
	if len(town.Remaps) != want {
		t.Fatalf("色板查表 %d 张，应为 %d（4 季 x 4 时段 x 5 天气）", len(town.Remaps), want)
	}
	for k, b64 := range town.Remaps {
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			t.Fatalf("%s 的查表不是合法 base64：%v", k, err)
		}
		if len(raw) != 97 {
			t.Fatalf("%s 的查表 %d 字节，应为 97", k, len(raw))
		}
		for i, v := range raw {
			if int(v) >= len(town.Palette) {
				t.Fatalf("%s 查表第 %d 项指向色板下标 %d（越界）", k, i, v)
			}
		}
		if !bytes.Contains(townRaw, []byte(k)) {
			t.Fatalf("查表键 %s 没有出现在 town.json 里", k)
		}
	}

	// 8) 季节资产：树/灌木/花坛/水面必须四季齐全（名字里带季节）
	for _, base := range []string{"at.tree.oak", "at.bush", "at.water"} {
		for _, se := range []string{"spring", "summer", "autumn", "winter"} {
			found := false
			for name := range doc.Sprites {
				if strings.HasPrefix(name, base+"_"+se) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s 缺少 %s 季的资产", base, se)
			}
		}
	}
	t.Logf("面板资产契约通过：图集 %dx%d，%d 个精灵，%d 个单体，%d 个岗位，%d 张查表",
		cfg.Width, cfg.Height, len(doc.Sprites), len(town.Objects), len(town.Stations), len(town.Remaps))
}
