package catalog

import (
	"isocity98/internal/art"
)

// Pad 在场地铺一层地面（略低于 z=0，保证墙脚压在地面之上）。
// 建筑自带的场地让城市「连片」而不是散落的盒子。
func (b *B) Pad(mat string) *B {
	b.M.SlabTop(-0.012, b.X0, b.Y0, b.X1, b.Y1, mat)
	return b
}

// Lot 是「场地 + 人行道沿」的组合。
func (b *B) Lot(padMat, curbMat string) *B {
	b.M.Box(b.X0-0.06, b.Y0-0.06, -0.05, b.X1+0.06, b.Y1+0.06, -0.012, curbMat)
	b.Pad(padMat)
	return b
}

// wallScheme 是一套配色：墙体、屋顶、线脚、门、树叶。
// 同一原型换配色即可得到「同一街区里的不同人家」。
type wallScheme struct {
	wall  string
	roof  string
	band  string
	doorM string
	leaf  string
}

var wallSchemes = []wallScheme{
	{"wall.siding.blue", "roof.slate", "trim.white", "door.wood", "leaf.spring"},
	{"wall.siding.sand", "roof.tile.red", "trim.white", "door.wood", "leaf.olive"},
	{"wall.siding.green", "roof.shingle", "trim.cream", "door.wood", "leaf.dark"},
	{"wall.siding.white", "roof.slate", "trim.cream", "door.metal", "leaf.spring"},
	{"wall.brick", "roof.slate", "trim.cream", "door.wood", "leaf.autumn"},
	{"wall.stucco", "roof.tile.red", "trim.white", "door.glass", "leaf.palm"},
	{"wall.stucco.pink", "roof.tile.brown", "trim.white", "door.wood", "leaf.spring"},
	{"wall.brick.dark", "roof.gravel", "trim.dark", "door.metal", "leaf.dark"},
}

func pickScheme(r *art.Rand) wallScheme { return wallSchemes[r.Pick(len(wallSchemes))] }

// altWall 取一个与给定墙材同色系但更深的搭配（联排住宅用）。
func altWall(w string) string {
	switch w {
	case "wall.brick":
		return "wall.brick.dark"
	case "wall.siding.blue":
		return "wall.siding.white"
	case "wall.siding.sand":
		return "wall.stucco"
	case "wall.stucco":
		return "wall.stucco.pink"
	case "wall.siding.white":
		return "wall.siding.sand"
	}
	return "wall.brick"
}
