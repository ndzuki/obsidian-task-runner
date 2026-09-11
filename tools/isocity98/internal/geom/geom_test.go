package geom

import (
	"math"
	"testing"

	"isocity98/internal/art"
)

func TestBoxFaces(t *testing.T) {
	m := NewMesh()
	m.Box(0, 0, 0, 1, 1, 2, "wall.brick")
	if len(m.Quads) != 5 {
		t.Fatalf("长方体应有 5 个面（不含底面），得到 %d", len(m.Quads))
	}
	// 每个面的法向都必须朝外（顶面 z=2，四个侧面）
	faces := map[art.Face]int{}
	for _, q := range m.Quads {
		faces[q.Face]++
	}
	for _, f := range []art.Face{art.FaceTop, art.FacePosX, art.FacePosY, art.FaceNegX, art.FaceNegY} {
		if faces[f] != 1 {
			t.Fatalf("面 %v 数量应为 1，得到 %d", f, faces[f])
		}
	}
	if _, ok := faces[art.FaceBottom]; ok {
		t.Fatal("长方体不应生成底面")
	}
}

func TestBoxNormalizesReversedCoords(t *testing.T) {
	a := NewMesh()
	a.Box(0, 0, 0, 1, 1, 1, "trim.white")
	b := NewMesh()
	b.Box(1, 1, 1, 0, 0, 0, "trim.white")
	for i := range a.Quads {
		for j := 0; j < 4; j++ {
			if a.Quads[i].P[j] != b.Quads[i].P[j] {
				t.Fatalf("坐标顺序颠倒时几何应该一致：quad %d vert %d", i, j)
			}
		}
	}
}

// 棱台是屋顶家族的通用件：inset=0 是平顶，某个方向退化成一条线就是双坡。
func TestFrustumFamilies(t *testing.T) {
	slab := NewMesh()
	slab.Frustum(0, 0, 2, 2, 0, 1, 0, 0, "roof.slate", "roof.slate")
	if len(slab.Quads) != 5 {
		t.Fatalf("平屋顶（inset=0）应有 4 坡面 + 1 顶面，得到 %d", len(slab.Quads))
	}
	gable := NewMesh()
	gable.Frustum(0, 0, 2, 2, 0, 1, 1, 0, "roof.slate", "roof.slate")
	if len(gable.Quads) != 4 {
		t.Fatalf("双坡屋顶（x 向退化）应有 4 个面，得到 %d", len(gable.Quads))
	}
	pyramid := NewMesh()
	pyramid.Frustum(0, 0, 1, 1, 0, 1, 0.5, 0.5, "roof.slate", "roof.slate")
	if len(pyramid.Quads) != 4 {
		t.Fatalf("攒尖顶应有 4 个三角面，得到 %d", len(pyramid.Quads))
	}
}

// 绕 Z 轴转四次必须回到原位（车辆四方向复用同一模型的基础）。
func TestRotateZ90Identity(t *testing.T) {
	m := NewMesh()
	m.Box(0.1, 0.2, 0, 0.4, 0.9, 1.3, "car.red")
	m.DecalX(0.4, 0.3, 0.7, 0.2, 0.6, "car.glass", 0.02)
	orig := m.Clone()
	rot := m.Clone().RotateZ90(4)
	for i := range orig.Quads {
		for j := 0; j < 4; j++ {
			a, b := orig.Quads[i].P[j], rot.Quads[i].P[j]
			if math.Abs(a.X-b.X) > 1e-9 || math.Abs(a.Y-b.Y) > 1e-9 || math.Abs(a.Z-b.Z) > 1e-9 {
				t.Fatalf("旋转 4 次未还原：quad %d vert %d %v vs %v", i, j, a, b)
			}
		}
	}
}

func TestBoundsAndMerge(t *testing.T) {
	m := NewMesh()
	m.Box(0, 0, 0, 1, 2, 3, "wall.metal")
	min, max := m.Bounds()
	if min != (Vec3{0, 0, 0}) || max != (Vec3{1, 2, 3}) {
		t.Fatalf("包围盒错误：%v %v", min, max)
	}
	other := NewMesh()
	other.Box(0, 0, 0, 1, 1, 1, "trim.white")
	m.Merge(other, V(5, 0, 0))
	_, max2 := m.Bounds()
	if max2.X != 6 {
		t.Fatalf("Merge 后包围盒应为 6，得到 %v", max2.X)
	}
}

// 面朝向决定程序化图案的 UV 投影；错切会让砖缝在侧面歪掉。
func TestFaceUVOrthogonal(t *testing.T) {
	// 顶面：u 随 x 增长，v 随 y 增长
	u0, v0 := art.FaceUV(art.FaceTop, 0, 0, 0)
	u1, v1 := art.FaceUV(art.FaceTop, 1, 0, 0)
	_, v2 := art.FaceUV(art.FaceTop, 0, 1, 0)
	if u1 <= u0 || v2 <= v0 || v1 != v0 {
		t.Fatalf("顶面 UV 投影错误：(%v,%v) (%v,%v)", u0, v0, u1, v1)
	}
	// 竖直面：v 随 z 减小（图案不能上下翻转）
	_, vz0 := art.FaceUV(art.FacePosX, 0, 0, 0)
	_, vz1 := art.FaceUV(art.FacePosX, 0, 0, 1)
	if vz1 >= vz0 {
		t.Fatalf("竖直面 UV 的 v 应随高度下降：(%v → %v)", vz0, vz1)
	}
}

// 四向旋转必须绕**格子中心**：绕原点会把模型甩出去一整格，
// 于是「同一辆车/同一个人」的四个朝向分别落在相邻格子，
// 运行时按锚点绘制就成了「停在旁边那格」。这是实打实踩过的 bug。
//
// 不变量：绕格子中心旋转会改变模型的偏心方向（这是对的），
// 但**离格子中心的距离必须守恒**，且始终不越出本格。
func TestRotateZ90KeepsModelOnItsTile(t *testing.T) {
	build := func() *Mesh {
		m := NewMesh()
		m.Box(0.60, 0.20, 0, 0.80, 0.40, 0.5, "x")
		return m
	}
	m0 := build()
	min0, max0 := m0.Bounds()
	dist0 := math.Hypot((min0.X+max0.X)/2-0.5, (min0.Y+max0.Y)/2-0.5)
	if dist0 < 0.1 {
		t.Fatalf("测试用的盒子不够偏心（%.3f），测不出甩格问题", dist0)
	}
	for k := 1; k < 4; k++ {
		m := build()
		m.RotateZ90(k)
		min, max := m.Bounds()
		dist := math.Hypot((min.X+max.X)/2-0.5, (min.Y+max.Y)/2-0.5)
		if math.Abs(dist-dist0) > 0.01 {
			t.Fatalf("旋转 %d 步后模型离格子中心的距离从 %.3f 变成 %.3f（应守恒）", k, dist0, dist)
		}
		if min.X < -0.11 || min.Y < -0.11 || max.X > 1.11 || max.Y > 1.11 {
			t.Fatalf("旋转 %d 步后模型越出本格：%v..%v", k, min, max)
		}
	}
	m := build()
	m.RotateZ90(4)
	if got, _ := m.Bounds(); math.Abs(got.X-min0.X) > 0.001 || math.Abs(got.Y-min0.Y) > 0.001 {
		t.Fatalf("旋转 4 步后没有回到原位：%v vs %v", got, min0)
	}
}
