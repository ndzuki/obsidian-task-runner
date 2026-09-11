package catalog

import (
	"isocity98/internal/art"
	"isocity98/internal/geom"
	"isocity98/internal/render"
)

// TileBounds 是所有地面瓦片（地形/道路）共用的画布范围。
// 统一画布 + 统一锚点，才能保证瓦片在运行时严丝合缝地拼接。
// 数值随像素密度缩放（1x 下即是 34x18）。
var TileBounds = render.Rect{MinX: -17, MinY: -1, MaxX: 17, MaxY: 17}

// CliffBounds 是侧壁瓦片的画布范围（含向下 1 个高度单位）。
var CliffBounds = render.Rect{MinX: -17, MinY: -1, MaxX: 17, MaxY: 26}

// SetScale 让瓦片画布跟随像素密度：几何不变，只是每格占的像素变多。
// 运行时的「统一画布 + 统一锚点」约定在任意密度下都成立。
func SetScale(s int) {
	if s < 1 {
		s = 1
	}
	TileBounds = render.Rect{MinX: -17 * s, MinY: -1 * s, MaxX: 17 * s, MaxY: 17 * s}
	CliffBounds = render.Rect{MinX: -17 * s, MinY: -1 * s, MaxX: 17 * s, MaxY: 26 * s}
}

// terrain 定义一块平地面瓦片。
func terrain(name, label, mat string, variants int) Def {
	return Def{
		Name: name, Label: label, Kind: KindTerrain, Category: "地形",
		Footprint: [2]int{1, 1}, Variants: variants, NoShadow: true,
		Build: func(b *B) {
			b.M.SlabTop(0, 0, 0, 1, 1, mat)
			if b.Frame > 0 {
				// 用图案相位做出「同一地形不同纹理」的变体
				for i := range b.M.Quads {
					b.M.Quads[i].UOff = float64(b.Frame) * 5
					b.M.Quads[i].VOff = float64(b.Frame) * 3
				}
			}
		},
	}
}

func terrainDefs() []Def {
	out := []Def{
		terrain("terrain.grass", "草地", "pad.grass", 3),
		terrain("terrain.meadow", "深草地", "pad.grass.dark", 3),
		terrain("terrain.dirt", "裸土", "pad.dirt", 2),
		terrain("terrain.sand", "沙地", "pad.sand", 2),
		terrain("terrain.rock", "岩石地", "pad.rock", 2),
		terrain("terrain.gravel", "砾石地", "pad.gravel", 2),
		terrain("terrain.farm", "农田", "pad.farm", 2),
		terrain("terrain.pave", "铺装地", "pad.tile", 2),
		terrain("terrain.concrete", "水泥地", "pad.concrete", 2),
	}
	// 水面：4 帧循环动画，靠图案相位偏移做出波纹流动
	water := Def{
		Name: "terrain.water", Label: "水面", Kind: KindTerrain, Category: "地形",
		Footprint: [2]int{1, 1}, Variants: 4, Anim: true, NoShadow: true,
		Build: func(b *B) {
			b.M.Add(geom.Quad{
				P: [4]geom.Vec3{
					geom.V(0, 0, 0), geom.V(1, 0, 0), geom.V(1, 1, 0), geom.V(0, 1, 0),
				},
				Face: art.FaceTop, Shade: geom.ShadeTop, Mat: "water",
				UOff: float64(b.Frame) * 4,
			})
			// 靠岸浅水：用更亮的色阶再叠一层抖动网
			b.M.Add(geom.Quad{
				P: [4]geom.Vec3{
					geom.V(0.04, 0.04, 0.004), geom.V(0.96, 0.04, 0.004),
					geom.V(0.96, 0.96, 0.004), geom.V(0.04, 0.96, 0.004),
				},
				Face: art.FaceTop, Shade: geom.ShadeTop, Mat: "water.shallow",
				Bias: 0.01, UOff: float64(b.Frame)*4 + 9,
			})
		},
	}
	out = append(out, water)
	return out
}

// 侧壁朝向：0=+X(屏幕右下) 1=+Y(屏幕左下) 2=-X 3=-Y
var cliffSides = []struct {
	Name string
	Face art.Face
	Sh   int8
	P    [4]geom.Vec3
}{
	{"e", art.FacePosX, geom.ShadeRight, [4]geom.Vec3{
		geom.V(1, 0, 0), geom.V(1, 1, 0), geom.V(1, 1, -1), geom.V(1, 0, -1)}},
	{"s", art.FacePosY, geom.ShadeLeft, [4]geom.Vec3{
		geom.V(1, 1, 0), geom.V(0, 1, 0), geom.V(0, 1, -1), geom.V(1, 1, -1)}},
	{"w", art.FaceNegX, geom.ShadeLeft, [4]geom.Vec3{
		geom.V(0, 1, 0), geom.V(0, 0, 0), geom.V(0, 0, -1), geom.V(0, 1, -1)}},
	{"n", art.FaceNegY, geom.ShadeRight, [4]geom.Vec3{
		geom.V(0, 0, 0), geom.V(1, 0, 0), geom.V(1, 0, -1), geom.V(0, 0, -1)}},
}

// cliffDefs 生成「1 个高度单位」的侧壁瓦片；运行时按落差层数堆叠。
func cliffDefs() []Def {
	specs := []struct {
		Name  string
		Label string
		Mat   string
		Var   int
	}{
		{"cliff.soil", "土坡", "pad.dirt", 2},
		{"cliff.rock", "岩壁", "wall.rock", 2},
		{"cliff.sand", "沙坡", "pad.sand", 2},
		{"cliff.pave", "挡土墙", "concrete.curb", 2},
		{"cliff.farm", "田埂", "pad.dirt", 2},
	}
	var out []Def
	for _, sp := range specs {
		for si, side := range cliffSides {
			name := sp.Name + "." + side.Name
			mat := sp.Mat
			face := side.Face
			sh := side.Sh
			p := side.P
			iv := si
			out = append(out, Def{
				Name: name, Label: sp.Label, Kind: KindTerrain, Category: "地形",
				Footprint: [2]int{1, 1}, Variants: sp.Var, NoShadow: true, Cliff: true,
				Build: func(b *B) {
					off := float64(b.Frame) * 4
					_ = iv
					b.M.Add(geom.Quad{P: p, Face: face, Shade: sh, Mat: mat, UOff: off})
					// 坡脚压暗一条窄边，让侧壁与地面之间有一道「接地线」
					b.M.Add(geom.Quad{
						P: [4]geom.Vec3{
							p[3], p[2],
							geom.V(p[2].X, p[2].Y, p[2].Z-0.06),
							geom.V(p[3].X, p[3].Y, p[3].Z-0.06),
						},
						Face: face, Shade: geom.ShadeBottom, Mat: mat, Bias: 0.03, UOff: off,
					})
				},
			})
		}
	}
	return out
}
