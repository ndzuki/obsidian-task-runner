package art

// Bayer8 是标准 8x8 有序抖动矩阵（值域 0..63）。
// 用有序抖动而非误差扩散，是为了得到 DOS/PC-98 时代那种「规则网点」而非噪点。
var Bayer8 = [64]uint8{
	0, 32, 8, 40, 2, 34, 10, 42,
	48, 16, 56, 24, 50, 18, 58, 26,
	12, 44, 4, 36, 14, 46, 6, 38,
	60, 28, 52, 20, 62, 30, 54, 22,
	3, 35, 11, 43, 1, 33, 9, 41,
	51, 19, 59, 27, 49, 17, 57, 25,
	15, 47, 7, 39, 13, 45, 5, 37,
	63, 31, 55, 23, 61, 29, 53, 21,
}

// Bayer 返回 (x,y) 处的抖动阈值，严格落在 (-0.5, 0.5) 内。
// 用 (v+0.5)/64-0.5 而不是 v/64-0.5，保证正负两侧对称、均值为 0。
func Bayer(x, y int) float32 {
	if x < 0 {
		x = -x
	}
	if y < 0 {
		y = -y
	}
	return (float32(Bayer8[(y&7)<<3|(x&7)])+0.5)/64 - 0.5
}

// BayerLevel 返回 (x,y) 处的抖动层级，范围 0..63。
func BayerLevel(x, y int) int {
	if x < 0 {
		x = -x
	}
	if y < 0 {
		y = -y
	}
	return int(Bayer8[(y&7)<<3|(x&7)])
}

// Hash 是确定性的整数散列（位置 → 伪随机），用于程序化「贴图」的散点纹理。
func Hash(x, y, z int) uint32 {
	h := uint32(x)*374761393 + uint32(y)*668265263 + uint32(z)*2246822519
	h = (h ^ (h >> 13)) * 1274126177
	return h ^ (h >> 16)
}

// Hashf 返回 [0,1) 的确定性伪随机数。
func Hashf(x, y, z int) float32 {
	return float32(Hash(x, y, z)&0xffffff) / float32(0x1000000)
}

// Rand 是一个小巧的确定性 PRNG（xorshift32），用于程序化建模时的稳定变化。
type Rand struct{ s uint32 }

func NewRand(seed uint32) *Rand {
	if seed == 0 {
		seed = 0x9e3779b9
	}
	return &Rand{s: seed}
}

func (r *Rand) Next() uint32 {
	r.s ^= r.s << 13
	r.s ^= r.s >> 17
	r.s ^= r.s << 5
	return r.s
}

// Float 返回 [0,1) 的随机数。
func (r *Rand) Float() float64 { return float64(r.Next()&0xffffff) / float64(0x1000000) }

// Range 返回 [lo,hi) 的随机浮点。
func (r *Rand) Range(lo, hi float64) float64 { return lo + r.Float()*(hi-lo) }

// Int 返回 [lo,hi] 的随机整数。
func (r *Rand) Int(lo, hi int) int {
	if hi <= lo {
		return lo
	}
	return lo + int(r.Next()%uint32(hi-lo+1))
}

// Chance 以概率 p 返回 true。
func (r *Rand) Chance(p float64) bool { return r.Float() < p }

// Pick 从候选列表中随机取一个。
func (r *Rand) Pick(n int) int { return r.Int(0, n-1) }
