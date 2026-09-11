package catalog

import (
	"sort"
	"strings"
	"testing"

	"isocity98/internal/art"
	"isocity98/internal/render"
)

// ─────────────────────────────────────────────────────────────────────────────
// 「个性化」的可量化门禁。
//
// 上一版角色被否掉的原因是：单看每个都还行，但**放到 1x 面板里彼此分不出来**。
// 当时没有任何检查能发现这件事 —— 「更精细/更有个性」是形容词，形容词不进 CI。
//
// 这条测试把它变成数字：任意两个角色在 1x 的**待机帧**上，以脚底锚点对齐后
// 必须差异足够多的像素。差异不够就是同一团色块，不管代码里写了多少配件。
// ─────────────────────────────────────────────────────────────────────────────

// charWindow 是以脚底锚点为中心的取样窗口（宽 x 高，锚点在窗口内的位置）。
// 取足够大以容纳最高的角色，又足够小以只比较「角色本体」。
const (
	winW, winH   = 30, 36
	winAX, winAY = 15, 30
	// minCharDiff 两个角色至少要有这么多像素不同才算「看得出是两个人」。
	// 一个配件（帽子/托盘/纸箱）在 1x 下大约占 8~20 像素，
	// 所以阈值定在 10：明显小于此值说明差异只体现在配色或一两个像素上。
	minCharDiff = 10
)

type charSample struct {
	name string
	pix  []uint32 // winW*winH，0 = 透明
}

// idleFrame 返回待机帧号：16 帧 = 4 向 x 4 步态（取朝向 +Y 的站立帧），
// 8 帧 = 4 向 x 2 相位，6 帧 = 2 向 x 3 振翅。
func idleFrame(d Def) int {
	switch {
	case d.Variants >= 16:
		return 4
	case d.Variants == 8:
		return 4
	default:
		return 0
	}
}

// windowOf 把一张精灵按锚点对齐采样进固定窗口（角色与建筑共用）。
func windowOf(sp *render.Sprite) []uint32 {
	w := make([]uint32, winW*winH)
	for y := 0; y < winH; y++ {
		for x := 0; x < winW; x++ {
			sx := sp.AnchorX + (x - winAX)
			sy := sp.AnchorY + (y - winAY)
			if sx < 0 || sy < 0 || sx >= sp.Img.Rect.Dx() || sy >= sp.Img.Rect.Dy() {
				continue
			}
			i := sp.Img.PixOffset(sx, sy)
			if sp.Img.Pix[i+3] < 128 {
				continue
			}
			w[y*winW+x] = uint32(sp.Img.Pix[i])<<16 | uint32(sp.Img.Pix[i+1])<<8 | uint32(sp.Img.Pix[i+2])
		}
	}
	return w
}

// sampleChars 把每个角色渲染成「以脚底锚点对齐」的小窗口。
func sampleChars(t *testing.T) []charSample {
	t.Helper()
	r := NewTestRenderer(art.Pal())
	var out []charSample
	for _, d := range AgentTownNPCs() {
		sp, err := r.Render(d.Name, BuildMesh(d, idleFrame(d)), testOptions(d))
		if err != nil {
			t.Fatalf("%s 渲染失败：%v", d.Name, err)
		}
		w := make([]uint32, winW*winH)
		for y := 0; y < winH; y++ {
			for x := 0; x < winW; x++ {
				// 窗口坐标 → 图像坐标（锚点在 (winAX, winAY)）
				sx := sp.AnchorX + (x - winAX)
				sy := sp.AnchorY + (y - winAY)
				if sx < 0 || sy < 0 || sx >= sp.Img.Rect.Dx() || sy >= sp.Img.Rect.Dy() {
					continue
				}
				i := sp.Img.PixOffset(sx, sy)
				if sp.Img.Pix[i+3] < 128 {
					continue
				}
				w[y*winW+x] = uint32(sp.Img.Pix[i])<<16 |
					uint32(sp.Img.Pix[i+1])<<8 | uint32(sp.Img.Pix[i+2])
			}
		}
		out = append(out, charSample{name: d.Name, pix: w})
	}
	return out
}

func charDiff(a, b charSample) int {
	n := 0
	for i := range a.pix {
		if a.pix[i] != b.pix[i] {
			n++
		}
	}
	return n
}

func TestAgentTownCharactersAreDistinct(t *testing.T) {
	ss := sampleChars(t)
	// 21 个职业各有一个专属居民，加上小精灵与动物，总量不该低于 34。
	// 上一版只有 15 个（21 个职业共用 9 个居民），直接被这条挡住。
	if len(ss) < 34 {
		t.Fatalf("角色资产只有 %d 个，太少：需要 21 个职业居民（每人一个）\n"+
			"+ ≥10 个小精灵 + ≥4 个动物，合计至少 34 个", len(ss))
	}
	type pair struct {
		a, b string
		d    int
	}
	var worst []pair
	for i := 0; i < len(ss); i++ {
		for j := i + 1; j < len(ss); j++ {
			worst = append(worst, pair{ss[i].name, ss[j].name, charDiff(ss[i], ss[j])})
		}
	}
	sort.Slice(worst, func(i, j int) bool { return worst[i].d < worst[j].d })
	const show = 12
	for i := 0; i < show && i < len(worst); i++ {
		t.Logf("最相似的组合 %2d: %-18s vs %-18s  差异像素 %3d",
			i+1, worst[i].a, worst[i].b, worst[i].d)
	}
	for _, p := range worst {
		if p.d < minCharDiff {
			t.Errorf("%s 与 %s 在 1x 下只差 %d 个像素（要求 ≥%d）：面板里会读成同一团色块",
				p.a, p.b, p.d, minCharDiff)
		}
	}
	t.Logf("共 %d 个角色，%d 对组合，最小差异 %d 像素", len(ss), len(worst), worst[0].d)
}

// 每个角色都必须真的渲染出内容，且体量与它的声明高度相称。
//
// 阈值不能一刀切：小孩只有成人的 0.7 倍，小精灵与动物本来就小。
// 面积随高度平方缩放，所以用 20*h² 作为下限（成人 2.31 → ~107 像素），
// 既拦得住「糊成一团」也拦得住「小得看不见」。
// 两对已知容易迟疑的角色，单独要求更高的差异下限。
// 全量门禁是 10 像素（防「同一团色块」），这两对要的是「一眼分得清」。
func TestAgentTownConfusableCharactersDiffer(t *testing.T) {
	ss := sampleChars(t)
	idx := map[string][]uint32{}
	for _, s := range ss {
		idx[s.name] = s.pix
	}
	for _, p := range []struct {
		a, b string
		min  int
	}{
		{"npc.refiner", "npc.librarian", 45}, // 都是浅上衣+深裤+抱物+眼镜
		{"npc.resting", "npc.griller", 45},   // 都是青绿系
	} {
		pa, ok1 := idx[p.a]
		pb, ok2 := idx[p.b]
		if !ok1 || !ok2 {
			t.Fatalf("找不到角色 %s / %s", p.a, p.b)
		}
		d := charDiff(charSample{name: p.a, pix: pa}, charSample{name: p.b, pix: pb})
		if d < p.min {
			t.Errorf("%s 与 %s 只差 %d 像素（要求 ≥%d）：1x 下会同屏迟疑",
				p.a, p.b, d, p.min)
		} else {
			t.Logf("%s vs %s：差异 %d 像素（下限 %d）", p.a, p.b, d, p.min)
		}
	}
}

func TestAgentTownCharactersHaveSubstance(t *testing.T) {
	ss := sampleChars(t)
	heights := map[string]float64{}
	for _, d := range AgentTownNPCs() {
		heights[d.Name] = d.Height
	}
	for _, s := range ss {
		opaque := 0
		for _, p := range s.pix {
			if p != 0 {
				opaque++
			}
		}
		h := heights[s.name]
		minPx := int(20 * h * h)
		if minPx < 8 {
			minPx = 8
		}
		if opaque < minPx {
			t.Errorf("%s（高 %.2f）在 1x 下只有 %d 个不透明像素，少于下限 %d：太小或糊成一团",
				s.name, h, opaque, minPx)
		}
		if opaque > 620 {
			t.Errorf("%s 在 1x 下占了 %d 个像素，体量过大（要求 ≤620，否则会盖住相邻格）", s.name, opaque)
		}
	}
}

// 剖面模式必须真的切掉一截，又不能把建筑切没。
//
// 「切了一截」这件事很容易自我欺骗：按材质删屋面时实测只削掉 5~20 个面、
// 高度几乎不变（现代建筑的墙一路砌到屋顶），画面上完全看不出剖面。
// 所以用它当门禁：保留高度必须落在 45%~75% 之间。
func TestAgentTownCutawayKeepsReadableMass(t *testing.T) {
	full := map[string]float64{}
	for _, d := range AgentTownDefs() {
		full[d.Name] = d.Height
	}
	opens := AgentTownOpenDefs()
	if len(opens) != 21 {
		t.Fatalf("剖面资产应有 21 个（每个职业建筑一个），实际 %d", len(opens))
	}
	for _, d := range opens {
		base := strings.TrimSuffix(d.Name, ".open")
		fh, ok := full[base]
		if !ok {
			t.Errorf("%s 找不到对应的完整建筑 %s", d.Name, base)
			continue
		}
		if fh <= 0.6 {
			continue // 本来就很矮（广场、服务亭），剖切无意义
		}
		ratio := d.Height / fh
		if ratio > 0.75 {
			t.Errorf("%s 剖面几乎没切：保留 %.0f%% 高度（应 ≤75%%）", d.Name, ratio*100)
		}
		if ratio < 0.45 {
			t.Errorf("%s 剖面切过头：只剩 %.0f%% 高度（应 ≥45%%）", d.Name, ratio*100)
		}
	}
}

// 建筑之间在 1x 下的可辨性。
//
// 与角色不同，建筑靠体量与材质区分，所以门禁只针对**已知容易混**的那几对
// （§5.6 列出的），而不是全部 21x20 对 —— 后者里「住宅 vs 工厂」本来就该不像，
// 强行要求差异只会逼出无意义的造型。
func TestAgentTownConfusableBuildingsDiffer(t *testing.T) {
	// 每对：(a, b, 最少差异像素)
	//
	// 下限分两类：
	//   - 真正的「同屏容易混」对（两座塔、两个方正低层）设得高，要求明确拉开；
	//   - 水景一对实测 86 像素：喷泉与池塘本来就该靠**位置**而不是形状区分
	//     （喷泉在广场、池塘在公园），所以下限钉在略低于实测值处，
	//     作用是**回归护栏** —— 防止哪天它们被改成同一套模型。
	pairs := [][3]interface{}{
		{"at.audit", "at.priority", 120},      // 两座退台塔：天际线双塔，同屏概率最高
		{"at.fountain", "at.pond", 80},        // 水景：回归护栏（实测 86）
		{"at.closed", "at.split", 100},        // 都是方正低层
		{"at.refining", "at.conventions", 60}, // 都是「浅色墙 + 带窗」
	}
	r := NewTestRenderer(art.Pal())
	render := func(name string) charSample {
		for _, d := range AgentTownDefs() {
			if d.Name != name {
				continue
			}
			sp, err := r.Render(d.Name, BuildMesh(d, 0), testOptions(d))
			if err != nil {
				t.Fatalf("%s 渲染失败：%v", name, err)
			}
			return charSample{name: name, pix: windowOf(sp)}
		}
		t.Fatalf("找不到建筑原型 %s", name)
		return charSample{}
	}
	for _, p := range pairs {
		a, b := p[0].(string), p[1].(string)
		mina := p[2].(int)
		sa, sb := render(a), render(b)
		d := charDiff(sa, sb)
		if d < mina {
			t.Errorf("%s 与 %s 在 1x 下只差 %d 个像素（要求 ≥%d）：同屏时会看混",
				a, b, d, mina)
		} else {
			t.Logf("%s vs %s：差异 %d 像素（下限 %d）", a, b, d, mina)
		}
	}
}
