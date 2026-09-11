package art

// Face 表示立方体某个面的朝向，用于采样「程序化图案」时选择不产生错切的 2D 参数。
type Face int

const (
	FaceTop  Face = iota
	FacePosX      // 屏幕右下方向（背光面）
	FacePosY      // 屏幕左下方向（受光面）
	FaceNegX
	FaceNegY
	FaceBottom
)

// texelsPerTile 是每格的纹素数，等于 1x 下的每格像素数。由渲染器按像素密度设置，
// 这样「4 纹素一块砖」在 1x 与 2x 下占格子的比例完全一致。
var texelsPerTile = 32.0

// SetTexelsPerTile 由渲染器调用，跟随像素密度。
func SetTexelsPerTile(v float64) {
	if v < 1 {
		v = 1
	}
	texelsPerTile = v
}

// FaceUV 返回面上的 2D 参数（单位为「纹素」，1x 下 1 格 = 32 纹素）。
// 采用纹素而不是世界单位，是为了让所有图案的周期都是整数像素，避免摩尔纹。
func FaceUV(face Face, x, y, z float64) (u, v float64) {
	tex := texelsPerTile
	switch face {
	case FaceTop, FaceBottom:
		return x * tex, y * tex
	case FacePosX, FaceNegX:
		return y * tex, -z * tex // z 向上为负 v，保证图案不被上下翻转
	case FacePosY, FaceNegY:
		return x * tex, -z * tex
	}
	return x * tex, y * tex
}

// PatCtx 是程序化图案的采样上下文。
type PatCtx struct {
	U, V float64 // 面内 2D 参数（纹素）
	X, Y float64 // 世界格坐标（用于跨面一致的散点噪波）
	Z    float64 // 世界高度
	Face Face
}

// Pattern 返回 [-1,1] 的明暗偏移量，乘以 PatternAmp 后叠加到色阶位置上。
//
// 这是本管线的「材质系统」：不用任何位图贴图，只靠有序网点（ordered dither）
// 在两档色阶之间打出规则网点来表现砖缝、木纹、草地、玻璃反光。
// 用有序网点而不是白噪声，是为了得到 DOS/PC-98 时代那种规整的网点质感。
type Pattern func(c PatCtx) float32

// PatternAmp 是图案偏移幅度（单位：色阶内插位置）。
// 0.15 约等于 5 档色阶的「半档」，即网点会在相邻两档之间跳，形成经典双色抖动。
const PatternAmp = 0.15

// bayer4 是 4x4 有序矩阵（0..15）。
var bayer4 = [16]uint8{
	0, 8, 2, 10,
	12, 4, 14, 6,
	3, 11, 1, 9,
	15, 7, 13, 5,
}

// ord 以 cell 个纹素为网点尺寸，采样 4x4 有序矩阵，返回 [-1,1]。
// cell=1 → 每像素一个网点；cell=2 → 2x2 像素的网点块（1x 下最经典）。
func ord(cell int, u, v int) float32 {
	if cell < 1 {
		cell = 1
	}
	ix := floorDiv(u, cell) & 3
	iy := floorDiv(v, cell) & 3
	return float32(bayer4[iy*4+ix])/15*2 - 1
}

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func iu(c PatCtx) int { return int(c.U) }
func iv(c PatCtx) int { return int(c.V) }

// patFuncs 是内置图案表。命名与材质一一对应。
var patFuncs = map[string]Pattern{
	// 平整面：零图案，得到干净的平涂色（PC-98 大色块的观感）。
	"none": func(c PatCtx) float32 { return 0 },

	// 抹灰/粉刷墙：极轻的 4x4 网点。
	"plaster": func(c PatCtx) float32 {
		return ord(4, iu(c), iv(c)) * 0.45
	},

	// 混凝土：每 8 纹素一道浇注分缝 + 细网点。
	"concrete": func(c PatCtx) float32 {
		if iv(c)%8 == 0 {
			return -0.85
		}
		return ord(3, iu(c), iv(c)) * 0.5
	},

	// 砖墙：4x2 纹素错缝砖，勾缝压暗，砖面留细网点。
	"brick": func(c PatCtx) float32 {
		row := iv(c) >> 1
		off := 0
		if row&1 == 1 {
			off = 2
		}
		if iv(c)&1 == 1 {
			return -0.9 // 水平灰缝
		}
		if (iu(c)+off)%4 == 3 {
			return -0.7 // 竖向灰缝
		}
		return ord(2, iu(c), iv(c)) * 0.35
	},

	// 石块墙：6x4 纹素大块，深缝。
	"stonewall": func(c PatCtx) float32 {
		row := iv(c) >> 2
		off := 0
		if row&1 == 1 {
			off = 3
		}
		if iv(c)&3 == 3 {
			return -0.9
		}
		if (iu(c)+off)%6 == 5 {
			return -0.7
		}
		return ord(3, iu(c)+off, iv(c)) * 0.55
	},

	// 横向壁板（美式住宅）：每 3 纹素一道阴影线。
	"siding": func(c PatCtx) float32 {
		if iv(c)%3 == 2 {
			return -0.8
		}
		return ord(4, iu(c), iv(c)) * 0.3
	},

	// 玻璃幕墙：楼层横梁 + 窗框 + 按列变化的反射条纹。
	"glass": func(c PatCtx) float32 {
		s := float32(0)
		if iv(c)%4 == 3 {
			s -= 0.7 // 楼板
		}
		if iu(c)%4 == 3 {
			s -= 0.35 // 窗框
		}
		s += (float32((iu(c)>>2)%3) - 1) * 0.2 // 相邻列明暗差，模拟反射
		return s
	},

	// 屋顶油毡/瓦楞：每 2 纹素一道横向瓦楞。
	"roofing": func(c PatCtx) float32 {
		if iv(c)&1 == 1 {
			return -0.55
		}
		return ord(2, iu(c), iv(c))*0.4 + 0.10
	},

	// 金属：竖向拉丝。
	"metal": func(c PatCtx) float32 {
		switch floorMod(iu(c), 5) {
		case 0:
			return 0.45
		case 4:
			return -0.45
		}
		return ord(2, iu(c), iv(c)) * 0.25
	},

	// 沥青：2x2 细网点 + 偶发粗颗粒。
	"asphalt": func(c PatCtx) float32 {
		s := ord(2, iu(c), iv(c)) * 0.75
		if bayer4[(floorDiv(iv(c), 6)&3)*4+(floorDiv(iu(c), 6)&3)] > 12 {
			s += 0.35
		}
		return s
	},

	// 碎石/沙地：3x3 网点粗散布。
	"gravel": func(c PatCtx) float32 {
		return ord(3, iu(c), iv(c))*0.85 + ord(6, iu(c)+5, iv(c)+3)*0.3
	},

	// 草地：2x2 网点 + 4x4 大网点叠出「草丛」层次。
	"turf": func(c PatCtx) float32 {
		return ord(2, iu(c), iv(c))*0.7 + ord(8, iu(c), iv(c))*0.55
	},

	// 树冠：团块状明暗（4x4 大网点 + 少量受光团）。
	"foliage": func(c PatCtx) float32 {
		s := ord(4, iu(c), iv(c))*0.5 + ord(12, iu(c)+7, iv(c)+2)*0.8
		return s
	},

	// 水面：水平波纹（相位由调用方通过 UOff 控制，用于逐帧动画）。
	"water": func(c PatCtx) float32 {
		s := float32(0)
		if iv(c)%4 == 0 {
			s += 0.75
		}
		if iv(c)%4 == 2 {
			s -= 0.35
		}
		if floorMod(iu(c), 16) < 4 && floorMod(iv(c), 8) < 2 {
			s += 0.5 // 波峰高光
		}
		return s
	},

	// 木料：竖向纹理。
	"plank": func(c PatCtx) float32 {
		s := ord(1, iu(c), iv(c)) * 0.3
		if floorMod(iu(c), 3) == 2 {
			s -= 0.45
		}
		return s
	},

	// 瓷砖/马赛克格。
	"tile": func(c PatCtx) float32 {
		if floorMod(iu(c), 4) == 3 || floorMod(iv(c), 4) == 3 {
			return -0.7
		}
		return ord(2, iu(c), iv(c)) * 0.35
	},

	// 霓虹/发光面的网格骨架。
	"neon": func(c PatCtx) float32 {
		if floorMod(iu(c), 3) == 0 || floorMod(iv(c), 3) == 0 {
			return 0.7
		}
		return -0.3
	},

	// 夜间窗格：每 6x6 纹素一扇窗，用确定性散列决定「亮/灭」。
	// 返回值 <0 表示灭灯（改用 OffRamp 的暗玻璃），>0 表示点亮强度。
	"windowlit": func(c PatCtx) float32 {
		if floorMod(iv(c), 6) == 5 || floorMod(iu(c), 6) == 5 {
			return -1 // 窗框永远不发光
		}
		h := Hashf(floorDiv(iu(c), 6), floorDiv(iv(c), 6), 71)
		if h < 0.40 {
			return -1
		}
		return 0.10 + (h-0.40)*1.30
	},

	// 小窗格版本（住宅/小店面），点亮率更高、变化更细。
	"windowlit2": func(c PatCtx) float32 {
		if floorMod(iv(c), 4) == 3 || floorMod(iu(c), 4) == 3 {
			return -1
		}
		h := Hashf(floorDiv(iu(c), 4), floorDiv(iv(c), 4), 137)
		if h < 0.30 {
			return -1
		}
		return 0.14 + (h-0.30)*1.30
	},
}

func floorMod(a, m int) int {
	r := a % m
	if r < 0 {
		r += m
	}
	return r
}

// Pat 按名取得图案，未知名称返回 nil（调用方按无图案处理）。
func Pat(name string) Pattern { return patFuncs[name] }
