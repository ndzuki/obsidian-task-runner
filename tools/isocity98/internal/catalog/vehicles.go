package catalog

import (
	"isocity98/internal/geom"
)

// 车辆用「同一模型 + 90° 步进旋转」生成四个朝向的变体，
// 因此在目录里是 4 个变体而不是 4 个原型。
func vehicleProps() []Def {
	var out []Def
	for _, spec := range []struct {
		Key   string
		Label string
		Paint string
		Truck bool
	}{
		{"car.red", "红色轿车", "car.red", false},
		{"car.blue", "蓝色轿车", "car.blue", false},
		{"car.yellow", "黄色出租车", "car.yellow", false},
		{"car.white", "白色轿车", "car.white", false},
		{"car.teal", "青色轿车", "car.teal", false},
		{"car.dark", "黑色轿车", "car.dark", false},
		{"truck", "厢式货车", "car.blue", true},
	} {
		sp := spec
		// 高度直接由网格量出来：轿车与货车不同高，写死一个共用值必然有一方对不上，
		// 而 Height 决定运行时/烘焙阴影的长度（见 catalog_test 的 Height 不变量）。
		probe := geom.NewMesh()
		if sp.Truck {
			buildTruck(probe, sp.Paint)
		} else {
			buildCar(probe, sp.Paint)
		}
		_, top := probe.Bounds()
		out = append(out, Def{
			Name: sp.Key, Label: sp.Label, Kind: KindProp, Category: "车辆",
			Footprint: [2]int{1, 1}, Height: top.Z, Variants: 4,
			ShadowFP: []float64{0.26, 0.10, 0.74, 0.90},
			Build: func(b *B) {
				m := geom.NewMesh()
				if sp.Truck {
					buildTruck(m, sp.Paint)
				} else {
					buildCar(m, sp.Paint)
				}
				m.RotateZ90(b.Frame)
				b.M.Merge(m, geom.V(0, 0, 0))
			},
		})
	}
	// 小船（码头边）
	out = append(out, Def{
		Name: "boat", Label: "帆船", Kind: KindProp, Category: "车辆",
		Footprint: [2]int{1, 1}, Height: 0.66, Variants: 4,
		ShadowFP: []float64{0.32, 0.12, 0.68, 0.88}, Water: true,
		Build: func(b *B) {
			m := geom.NewMesh()
			buildBoat(m)
			m.RotateZ90(b.Frame)
			b.M.Merge(m, geom.V(0, 0, 0))
		},
	})
	return out
}

// buildCar 造一辆朝 +Y 方向的小轿车。车身 0.44 x 0.78 格。
func buildCar(m *geom.Mesh, paint string) {
	m.Box(0.30, 0.12, 0.10, 0.70, 0.88, 0.26, paint)       // 底盘/车身
	m.Box(0.33, 0.26, 0.26, 0.67, 0.70, 0.44, "car.glass") // 舱室（玻璃）
	m.Box(0.30, 0.20, 0.44, 0.70, 0.76, 0.50, paint)       // 车顶
	m.Box(0.30, 0.10, 0.18, 0.70, 0.14, 0.24, "chrome")    // 前保险杠
	m.Box(0.30, 0.86, 0.18, 0.70, 0.90, 0.24, "chrome")    // 后保险杠
	m.Box(0.26, 0.20, 0.02, 0.32, 0.32, 0.14, "tire")      // 四轮
	m.Box(0.68, 0.20, 0.02, 0.74, 0.32, 0.14, "tire")
	m.Box(0.26, 0.66, 0.02, 0.32, 0.78, 0.14, "tire")
	m.Box(0.68, 0.66, 0.02, 0.74, 0.78, 0.14, "tire")
	// 车灯
	m.DecalY(0.12, 0.34, 0.42, 0.16, 0.22, "paint.white", 0.02)
	m.DecalY(0.12, 0.58, 0.66, 0.16, 0.22, "paint.white", 0.02)
}

// buildTruck 造一辆厢式货车（更长的货箱 + 驾驶室）。
func buildTruck(m *geom.Mesh, paint string) {
	m.Box(0.28, 0.10, 0.12, 0.72, 0.34, 0.42, paint)        // 驾驶室
	m.Box(0.30, 0.14, 0.42, 0.70, 0.30, 0.50, "car.glass")  // 前风挡
	m.Box(0.26, 0.36, 0.12, 0.74, 0.92, 0.62, "wall.metal") // 货箱
	m.Box(0.26, 0.36, 0.62, 0.74, 0.92, 0.66, "metal.dark")
	m.Box(0.24, 0.14, 0.02, 0.32, 0.28, 0.14, "tire")
	m.Box(0.68, 0.14, 0.02, 0.76, 0.28, 0.14, "tire")
	m.Box(0.24, 0.70, 0.02, 0.32, 0.84, 0.14, "tire")
	m.Box(0.68, 0.70, 0.02, 0.76, 0.84, 0.14, "tire")
}

// buildBoat 造一条停泊的小船。
func buildBoat(m *geom.Mesh) {
	m.Box(0.34, 0.14, 0.02, 0.66, 0.86, 0.18, "crate.wood") // 船体
	m.Box(0.38, 0.20, 0.18, 0.62, 0.80, 0.26, "crate.wood")
	m.Box(0.46, 0.24, 0.26, 0.54, 0.52, 0.62, "trim.white")  // 桅杆
	m.Box(0.52, 0.30, 0.30, 0.56, 0.74, 0.66, "paint.white") // 帆
	m.Box(0.46, 0.24, 0.62, 0.54, 0.28, 0.66, "trim.teal")
}

// vehicleDefs 汇总车辆类单体（作为可摆放道具，四方向由变体表达）。
func vehicleDefs() []Def { return vehicleProps() }
