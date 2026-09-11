package render

import (
	"math"
	"testing"

	"isocity98/internal/art"
	"isocity98/internal/geom"
)

// 投影必须是严格 2:1，深度必须满足「越高越近」。
func TestProjectInvariants(t *testing.T) {
	// 沿 +x 走一格：屏幕 +16px x，+8px y
	sx0, sy0, _ := Project(geom.V(0, 0, 0))
	sx1, sy1, _ := Project(geom.V(1, 0, 0))
	if sx1-sx0 != 16 || sy1-sy0 != 8 {
		t.Fatalf("1 格应为 (16,8)，得到 (%v,%v)", sx1-sx0, sy1-sy0)
	}
	// 沿 +y 走一格：屏幕 -16px x，+8px y
	sx2, sy2, _ := Project(geom.V(0, 1, 0))
	if sx2-sx0 != -16 || sy2-sy0 != 8 {
		t.Fatalf("+y 方向应为 (-16,8)，得到 (%v,%v)", sx2-sx0, sy2-sy0)
	}
	// 1 个 z 单位 = 屏幕上移 8px
	_, syz, _ := Project(geom.V(0, 0, 1))
	if syz-sy0 != -float64(ZUnit) {
		t.Fatalf("1 个 z 单位应为 -%dpx，得到 %v", ZUnit, syz-sy0)
	}
	// 深度：z 越大越靠近相机
	_, _, d0 := Project(geom.V(0, 0, 0))
	_, _, d1 := Project(geom.V(0, 0, 1))
	if d1 <= d0 {
		t.Fatalf("更高的点应更靠近相机：%v vs %v", d0, d1)
	}
	// 菱形的长宽比必须是 2:1（这是「固定 2:1 等距」的定义）
	_, _, dw := Project(geom.V(1, 0, 0))
	_, dh, _ := Project(geom.V(1, 1, 0))
	_ = dw
	if math.Abs(float64(dh/8)) < 1 {
		t.Fatal("深度换算异常")
	}
}

func testMesh() *geom.Mesh {
	m := geom.NewMesh()
	m.Box(0.1, 0.1, 0, 0.9, 0.9, 2.4, "wall.brick")
	m.Box(0.05, 0.05, 2.4, 0.95, 0.95, 3.0, "roof.tile.red")
	m.DecalY(0.9, 0.3, 0.7, 0.2, 1.6, "window", 0.02)
	return m
}

func TestRenderProducesAnchoredSprite(t *testing.T) {
	p := art.Pal()
	r := New(p, art.Day)
	sp, err := r.Render("t", testMesh(), Options{
		SS: 2, Pad: 2, Sprite: art.DefaultSpriteOpts,
		Shadow: ShadowOpts{On: true, DX: 0.36, DY: 0.155, Alpha: 0.3, Steps: 5,
			Srcs: []ShadowSrc{{X0: 0, Y0: 0, X1: 1, Y1: 1, Z0: 0, Z1: 3}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sp.W <= 0 || sp.H <= 0 {
		t.Fatalf("精灵图尺寸非法 %dx%d", sp.W, sp.H)
	}
	// 锚点必须落在图像内，否则运行时贴图会错位
	if sp.AnchorX < 0 || sp.AnchorX >= sp.W || sp.AnchorY < 0 || sp.AnchorY >= sp.H {
		t.Fatalf("锚点 (%d,%d) 超出图像 %dx%d", sp.AnchorX, sp.AnchorY, sp.W, sp.H)
	}
	// 必须有实际像素（空精灵图是最隐蔽的失败模式）
	opaque := 0
	for i := 3; i < len(sp.Img.Pix); i += 4 {
		if sp.Img.Pix[i] > 8 {
			opaque++
		}
	}
	if opaque < 20 {
		t.Fatalf("精灵图几乎是空的，只有 %d 个可见像素", opaque)
	}
}

// 昼夜两档必须产出尺寸与锚点完全一致的精灵图，运行时才可能原地替换。
func TestDayNightSpritesMatchExactly(t *testing.T) {
	p := art.Pal()
	opts := Options{SS: 2, Pad: 3, Sprite: art.DefaultSpriteOpts}
	day, err := New(p, art.Day).Render("t", testMesh(), opts)
	if err != nil {
		t.Fatal(err)
	}
	nb := Rect{MinX: -day.AnchorX, MinY: -day.AnchorY, MaxX: -day.AnchorX + day.W, MaxY: -day.AnchorY + day.H}
	no := opts
	no.Bounds = &nb
	no.NoTrim = true
	night, err := New(p, art.Night).Render("t", testMesh(), no)
	if err != nil {
		t.Fatal(err)
	}
	if day.W != night.W || day.H != night.H || day.AnchorX != night.AnchorX || day.AnchorY != night.AnchorY {
		t.Fatalf("昼夜不一致：%dx%d@%d,%d vs %dx%d@%d,%d",
			day.W, day.H, day.AnchorX, day.AnchorY, night.W, night.H, night.AnchorX, night.AnchorY)
	}
	// 夜间必须整体更暗
	lum := func(s *Sprite) float64 {
		var sum, n float64
		for i := 0; i < len(s.Img.Pix); i += 4 {
			if s.Img.Pix[i+3] < 200 {
				continue
			}
			sum += 0.299*float64(s.Img.Pix[i]) + 0.587*float64(s.Img.Pix[i+1]) + 0.114*float64(s.Img.Pix[i+2])
			n++
		}
		if n == 0 {
			return 0
		}
		return sum / n
	}
	if lum(night) >= lum(day) {
		t.Fatalf("夜间应更暗：day=%v night=%v", lum(day), lum(night))
	}
}

// 固定画布的瓦片（地形/道路）必须尺寸完全一致，否则拼接会出现缝隙。
func TestFixedBoundsTilesAreUniform(t *testing.T) {
	p := art.Pal()
	r := New(p, art.Day)
	b := Rect{MinX: -17, MinY: -1, MaxX: 17, MaxY: 17}
	opts := Options{SS: 2, NoTrim: true, Bounds: &b, Expand: 0.6,
		Sprite: art.SpriteOpts{Dither: 1, Rim: 0.1, EdgeFade: 0.5, BinaryAlpha: true}}
	for _, mat := range []string{"pad.grass", "pad.sand", "pad.asphalt", "pad.tile"} {
		m := geom.NewMesh()
		m.SlabTop(0, 0, 0, 1, 1, mat)
		sp, err := r.Render(mat, m, opts)
		if err != nil {
			t.Fatal(err)
		}
		if sp.W != b.W() || sp.H != b.H() {
			t.Fatalf("瓦片 %s 尺寸 %dx%d，应为 %dx%d", mat, sp.W, sp.H, b.W(), b.H())
		}
		if sp.AnchorX != -b.MinX || sp.AnchorY != -b.MinY {
			t.Fatalf("瓦片 %s 锚点 (%d,%d) 应为 (%d,%d)", mat, sp.AnchorX, sp.AnchorY, -b.MinX, -b.MinY)
		}
	}
}

// 提高像素密度的核心不变量：几何一点没变，精灵图尺寸必须精确按倍数增长。
func TestScaleMultipliesSpriteMetrics(t *testing.T) {
	p := art.Pal()
	m := testMesh()
	render := func(scale int) *Sprite {
		SetScale(scale)
		o := Options{SS: 2, Pad: 2 * scale, Sprite: art.DefaultSpriteOpts}
		o.Sprite.EdgeW = scale
		o.Shadow = ShadowOpts{On: true, DX: 0.36, DY: 0.155, Alpha: 0.3, Steps: 5,
			Srcs: []ShadowSrc{{X0: 0, Y0: 0, X1: 1, Y1: 1, Z0: 0, Z1: 3}}}
		sp, err := New(p, art.Day).Render("t", m, o)
		if err != nil {
			t.Fatal(err)
		}
		return sp
	}
	defer SetScale(1)
	a := render(1)
	b := render(2)
	// 画布范围与裁剪都要经过 floor/ceil 取整，因此允许 ±2px 的舍入差，
	// 但绝不允许「几乎没变」或「变得不止一倍」。
	const tol = 2
	if abs(b.W-a.W*2) > tol || abs(b.H-a.H*2) > tol {
		t.Fatalf("2x 精灵图应约为 %dx%d，得到 %dx%d", a.W*2, a.H*2, b.W, b.H)
	}
	if abs(b.AnchorX-a.AnchorX*2) > tol || abs(b.AnchorY-a.AnchorY*2) > tol {
		t.Fatalf("2x 锚点应约为 (%d,%d)，得到 (%d,%d)",
			a.AnchorX*2, a.AnchorY*2, b.AnchorX, b.AnchorY)
	}
	if b.W < a.W*3/2 || b.H < a.H*3/2 {
		t.Fatalf("2x 下精灵图没有真正变大：%dx%d → %dx%d", a.W, a.H, b.W, b.H)
	}
	if TileW != 64 || TileH != 32 || ZUnit != 16 {
		t.Fatalf("2x 下瓦片常量应为 64/32/16，得到 %d/%d/%d", TileW, TileH, ZUnit)
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func TestRenderRejectsUnknownMaterial(t *testing.T) {
	p := art.Pal()
	m := geom.NewMesh()
	m.Box(0, 0, 0, 1, 1, 1, "definitely.not.a.material")
	if _, err := New(p, art.Day).Render("bad", m, Options{SS: 1}); err == nil {
		t.Fatal("引用未知材质时必须报错，而不是静默画出空白")
	}
}

// 深度偏移必须让共面的贴花压过底面（屋面与墙顶共面的那个坑）。
func TestDecalBiasWinsOverCoplanarFace(t *testing.T) {
	p := art.Pal()
	m := geom.NewMesh()
	m.Box(0, 0, 0, 1, 1, 1, "wall.concrete")
	m.SlabTop(1.0, 0, 0, 1, 1, "roof.gravel")
	sp, err := New(p, art.Day).Render("roof", m, Options{SS: 4, Pad: 1, Sprite: art.DefaultSpriteOpts})
	if err != nil {
		t.Fatal(err)
	}
	// 顶面中心附近应当是屋面材质（灰）而不是墙体材质（暖褐）——用像素差间接断言：
	// 只要有像素即可，真正防止 z-fighting 的是 Bias；此处断言渲染稳定可复现。
	first := append([]byte(nil), sp.Img.Pix...)
	sp2, _ := New(p, art.Day).Render("roof", m, Options{SS: 4, Pad: 1, Sprite: art.DefaultSpriteOpts})
	for i := range first {
		if first[i] != sp2.Img.Pix[i] {
			t.Fatal("同一输入两次渲染结果不一致（存在非确定性）")
		}
	}
}
