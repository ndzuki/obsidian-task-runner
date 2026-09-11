package catalog

import (
	"isocity98/internal/art"
	"isocity98/internal/render"
)

// NewTestRenderer 用较低的超采样跑测试，保持测试速度。
func NewTestRenderer(p *art.Palette) *render.Renderer { return render.New(p, art.Day) }

// testOptions 复刻 cmd/prerender 里按种类分派的渲染参数。
func testOptions(d Def) render.Options {
	o := render.Options{SS: 2, Pad: 2, Sprite: art.DefaultSpriteOpts}
	switch d.Kind {
	case KindTerrain, KindRoad:
		o.Sprite = art.SpriteOpts{Dither: 1, Rim: 0.1, EdgeFade: 0.5, BinaryAlpha: true}
		o.Expand = 0.6
		o.NoTrim = true
		o.NoGlow = true
		if d.Cliff {
			b := CliffBounds
			o.Bounds = &b
		} else if d.Bounds != nil {
			o.Bounds = d.Bounds
		} else {
			b := TileBounds
			o.Bounds = &b
		}
	default:
		x0, y0 := 0.0, 0.0
		x1, y1 := float64(d.Footprint[0]), float64(d.Footprint[1])
		if len(d.ShadowFP) == 4 {
			x0, y0, x1, y1 = d.ShadowFP[0], d.ShadowFP[1], d.ShadowFP[2], d.ShadowFP[3]
		}
		o.Shadow = render.ShadowOpts{
			On: !d.NoShadow, DX: 0.36, DY: 0.155, Alpha: 0.3, Steps: 5,
			Srcs: []render.ShadowSrc{{X0: x0, Y0: y0, X1: x1, Y1: y1, Z0: 0, Z1: d.Height}},
		}
	}
	return o
}
