# ISO-CITY '98

一个复古 DOS / PC-98 风格的**等距（isometric）城市建造沙盒**。整座城市的美术资产由 Go 编写的**离线预渲染管线**程序化生成：没有 Blender、没有实时 PBR、没有实时光照，网页端只做「把已经画好的精灵图按顺序贴上去」这一件事。

- 渲染管线：`cmd/prerender` 用 Go 程序化建模 + 自研 Z-buffer 光栅化器，把每个单体渲染成透明背景精灵图，装箱成图集。
- 运行时：`web/` 是纯 Canvas 2D 的 ES module 前端，只做精灵图合成、UI 与 CRT 后处理。
- 玩法：只有地图编辑与建造，没有财政与分区压力。

---

## 特性清单

| 环节 | 做法 |
| --- | --- |
| 建模 | 纯 Go 代码，用长方体 / 棱台 / n 棱柱 / 棱锥 / 低多边形椭球 / 贴花面拼装，每个原型带 2~5 个确定性随机变体 |
| 投影 | 固定 2:1 等距**正交**投影，1 格 = 32x16 **基础**像素 × 像素密度，1 高度单位 = 8 像素 × 密度 |
| 分辨率 | 内部帧缓冲 640x400 × 像素密度（默认 `SCALE=3` → 1920x1200），整数倍最近邻放大；放不下时平滑缩放到刚好铺满 |
| 光栅化 | 自研 Z-buffer 平面着色三角形光栅化器，**不做背面剔除**（高度场侧壁需要） |
| 抗锯齿 | 4x 超采样（`make assets` 默认 `SS=4`，可用 `SS=` 覆盖），盒式降采样回目标密度 |
| 光照 | **美术定向**：明暗 = 材质表里手调的色阶位置，不是光照方程；固定「左上光源」 |
| 阴影 | 烘焙接触阴影，美术指定的固定方向扫掠体（`DX=0.36, DY=0.155`，7 步） |
| 贴图 | 零位图贴图，全靠有序网点（Bayer 4x4）在两档色阶之间打点，模拟砖缝 / 木纹 / 玻璃 / 草地 |
| 夜间 | 同一网格在第二套 lighting profile 下再渲一遍：整体压暗偏冷蓝 + 窗户/招牌自发光 + 辉光溢出 |
| 量化 | 97 色统一母板 + 8x8 有序抖动，整屏（场景 + UI）压回有限色板 |
| UI | DOS/VGA 式双线凸凹镶边、2x2 网点填充、系统字体二值化成点阵、小地图、启动画面 |
| 运行时 | 每帧直接绘制可见范围（正确的画家序，水面会被崖壁/建筑正常遮挡）；实测 3x 下约 1650 次 drawImage ≈ 15ms |
| 后处理 | CRT 三档：OFF / 仅扫描线（纯 canvas 合成，~1.6ms）/ 全效果（逐像素辉光+扫描线+荫罩+暗角+有限色板抖动） |

当前产物（`SCALE=3`）：**326 个精灵条目**（每条目含日/夜两张，共 652 张图）、**2 张图集共 20.8M 像素（79 MB RGBA，约 3.8 MB PNG，装箱利用率 91%）**、**97 色母板**。
目录规模：**42 个建筑原型**（住宅 6 / 商业 12 / 工业 8 / 市政 9 / 公园 7）+ **24 个道具原型**（含 8 种车辆四向变体）+ 16 种连通形态 × 3 类道路 + 10 种地表（含水面的 4 帧动画）。

---

## 快速开始

```sh
make assets     # 渲染全部精灵图与图集到 web/assets（默认像素密度 2x，约 13 秒）
make serve      # 启动静态服务器
```

### 分辨率 / 像素密度

整条管线（离线渲染 + 运行时 + UI 布局）由**一个像素密度倍数**驱动，几何建模、色板、
程序化图案的相对比例都完全不变：

```sh
make assets SCALE=1   # 1 格 = 32x16 像素，内部帧缓冲 640x400（PC-98 原生分辨率）
make assets SCALE=2   # 1 格 = 64x32 像素，内部帧缓冲 1280x800
make assets SCALE=3   # 默认：1 格 = 96x48 像素，内部帧缓冲 1920x1200
```

离线侧由 `cmd/prerender -scale N` 控制，运行时不写死分辨率：它从 `manifest.json` 里读
`scale` 与 `tile`，据此配置瓦片尺寸、界面布局（`configureLayout`）与帧缓冲大小。
放大按**物理像素**取整数倍（`devicePixelRatio = 1.25 / 1.5` 这类屏幕上也不会被重采样而发虚）；
窗口装不下时改为平滑缩放铺满，而不是裁掉画面。

代价（实测，326 个精灵条目；内存为解码后的 RGBA 占用）：

| 密度 | 内部帧缓冲 | 图集 | PNG | 离线渲染 | 运行时内存 | 每帧 |
| --- | --- | --- | --- | --- | --- | --- |
| 1x | 640x400 | 1 张 / 2.4M px / **9 MB** | 0.9 MB | 4.5 s | ~40 MB | ~3 ms |
| 2x | 1280x800 | 1 张 / 9.5M px / **36 MB** | 2.1 MB | 13.6 s | ~70 MB | ~7 ms |
| 3x | 1920x1200 | 2 张 / 20.8M px / **79 MB** | 3.8 MB | 27.5 s | ~110 MB | ~16 ms |

**内存在这里主要不是「越大越细」的必然代价，而是两处可以优化的浪费**：
图集按高度降序装箱、并裁剪到实际用到的行高（利用率 56% → 91%）；
世界渲染不再缓存整张地图（3x 下那张缓存要 122 MB，且每次编辑都要 94 ms 重建），改为逐帧直绘可见范围。

高像素密度下 CRT 默认走「仅扫描线」档：全效果的逐像素循环在 1920x1200 上要 ~21 ms，
而 3x 的屏幕像素数本就是 1x 的 9 倍。按 `C` 可随时切回全效果。

- **窗口太小时会自动缩放铺满**：3x 的内部帧缓冲是 1920x1200，窗口小于它时按比例缩小显示
  （整数倍时用最近邻，缩小时用平滑），不会裁掉画面。

然后浏览器打开 <http://127.0.0.1:8098/>。

- **Go 版本要求**：`go.mod` 声明 `go 1.22`，需要 Go 1.22 或更高（已在 1.26.6 与 1.27.1 上实测）。
- **沙箱里 GOCACHE 必须可写**：Makefile 把 Go 缓存固定在项目内（`GOCACHE=$(CURDIR)/.cache/go-build`、`GOMODCACHE=$(CURDIR)/.cache/gomod`）并 `export`，因为很多沙箱/CI 环境的 `~/.cache` 是只读的。如果你用 `go run`/`go build` 直接跑而不经过 make，需要自己指定：

  ```sh
  GOCACHE=$PWD/.cache/go-build GOMODCACHE=$PWD/.cache/gomod go run ./cmd/prerender -out web/assets
  ```

- **必须用 HTTP 访问**：页面用 ES module + `fetch` 加载图集，`file://` 会被浏览器同源策略挡住，所以 `cmd/serve` 提供的就是一个零依赖静态服务器。
- **改完 JS 不需要清缓存**：`cmd/serve` 对所有响应打 `Cache-Control: no-store`，改完刷新即可。
- **`web/assets/` 里的产物随项目一起提供**：只改前端 JS 时不必重跑 `make assets`。

其他入口：

| 命令 | 用途 |
| --- | --- |
| `make sheet` | 输出接触印相图到 `build/contact.png`（美术审阅，附带 `build/contact.png.txt` 名称索引） |
| `make scene` | 输出参考城市场景到 `build/scene.png`（离线审阅整体观感，不开浏览器） |
| `make check` | `build` + `vet` + `test` + `assets` + `verify`，一次性全量校验 |
| `make shot` | 无头 Chrome 截图到 `build/shot.png`（自带 console 报错收集，有错则退出码非 0） |
| `make e2e` | 端到端交互冒烟：起服务器 → 用真实鼠标事件跑 20 项断言 → 截图 |
| `make shots` | 重新截取 `build/hd3_day.png` 与 `hd3_night.png`（1920x1200 原生） |
| `make fmtcheck` | 检查 `gofmt` |
| `make clean` | 清理 `build/` 与 `.cache/` |

---

## 目录结构

```
isocity98/
├── Makefile                    所有可用命令（含项目内 GOCACHE 固定）
├── go.mod                      module isocity98, go 1.27
├── cmd/
│   ├── prerender/              离线预渲染管线入口
│   │   ├── main.go             渲染循环、图集装箱、manifest/palette 输出
│   │   └── scene.go            参考场景合成器 + DemoScene 小城 + 缩放合成
│   └── serve/                  零依赖静态服务器（禁缓存）
├── internal/
│   ├── art/                    美术基础设施
│   │   ├── palette.go          97 色母板、Snap/LerpRamp、6-6-6 最近色 LUT
│   │   ├── dither.go           Bayer8 矩阵、确定性 Hash、xorshift32 PRNG
│   │   ├── patterns.go         18 个程序化图案 + FaceUV（面内参数化）
│   │   ├── material.go         材质表、明暗槽位、Day/Night 两套 lighting profile
│   │   ├── buffer.go           预乘浮点画布、降采样、模糊、像素化/描边、PNG 输出
│   │   └── color.go            RGBA / Hex 解析 / Lerp / Clamp
│   ├── geom/geom.go            几何词汇表：Box/Frustum/Cylinder/Cone/Ellipsoid/Decal/WindowGrid/Stairs
│   ├── render/render.go        投影、Z-buffer 光栅化、阴影、辉光、裁剪与锚点
│   ├── catalog/                程序化建模目录（唯一出现建筑造型的地方）
│   │   ├── builder.go          Def/B 定义、BuildMesh、全部建模辅助方法
│   │   ├── catalog.go          All() 汇总 + 道路瓦片（含连通掩码）
│   │   ├── terrain.go          地形瓦片、侧壁瓦片、共用画布范围
│   │   ├── buildings_res.go    住宅 6 原型
│   │   ├── buildings_com.go    商业 4 原型
│   │   ├── buildings_industrial.go  工业 2 原型
│   │   ├── buildings_civic.go  市政 2 原型
│   │   ├── buildings_park.go   公园 1 原型
│   │   ├── props.go            绿化道具 2 原型
│   │   ├── common.go           场地/人行道沿、住宅配色方案
│   │   └── vehicles.go         车辆目录（当前为空，见下）
│   └── sheet/sheet.go          货架式图集装箱器、接触印相图、渐变天空、Set.Draw
├── web/
│   ├── index.html              640x400 canvas + 启动画面
│   ├── assets/                 manifest.json / palette.json / atlas_0.png（预渲染产物）
│   └── js/                     运行时（见 docs/architecture.md）
└── scripts/shot.mjs            零依赖无头 Chrome 截图 + console 错误收集
```

---

## 全部 make 目标

| 目标 | 命令 | 说明 |
| --- | --- | --- |
| `make help` | — | 打印可用目标 |
| `make assets` | `go run ./cmd/prerender -ss 4 -out web/assets -build build` | 重新渲染全部精灵图与图集。`SS` 变量控制超采样倍率（默认 4） |
| `make sheet` | `go run ./cmd/prerender -out build/probe -build build -sheet build/contact.png -cols 10 -scale 2 -no-night` | 接触印相图（10 列，2x 放大，只渲白天档） |
| `make scene` | `go run ./cmd/prerender -out build/probe -build build -scene build/scene.png -scale 2 -no-night` | 参考城市场景预览 |
| `make serve` | `go run ./cmd/serve -addr 127.0.0.1:8098 -dir web` | 静态服务器，`ADDR` 变量可覆盖 |
| `make build` | `go build ./...` | 编译全部包 |
| `make test` | `go test ./...` | 跑测试 |
| `make vet` | `go vet ./...` | 静态检查 |
| `make fmt` | `gofmt -l -w cmd internal` | 格式化 |
| `make verify` | 检查 `manifest.json` / `palette.json` / `atlas_0.png` 非空 | 资产完整性校验 |
| `make check` | `build vet test assets verify` | 全量门禁 |
| `make clean` | `rm -rf build .cache` | 清理产物 |

`cmd/prerender` 还接受这些未被 make 包装的 flag：

| flag | 默认 | 说明 |
| --- | --- | --- |
| `-out` | `web/assets` | 资产输出目录 |
| `-build` | `build` | 调试图输出目录 |
| `-ss` | `4` | 超采样倍率 |
| `-atlas` | `2048` | 图集边长 |
| `-only` | 空 | 只渲染名称匹配该正则的单体（如 `-only '^house\.'`） |
| `-no-night` | false | 跳过夜间档（只输出日间，`nightSheet=-1`） |
| `-sheet` | 空 | 输出接触印相图路径 |
| `-scene` | 空 | 输出参考城市场景路径 |
| `-cols` | `10` | 接触印相图列数 |
| `-scale` | `2` | 预览放大倍率 |

---

## 操作键位

| 输入 | 行为 |
| --- | --- |
| 左键 | 施工（当前工具）/ 放置 |
| 右键 | 拆除 |
| 左键拖动 | 连续施工（沿拖拽路径补齐中间格） |
| 中键拖动 / 空格 + 左键拖动 | 平移视口 |
| 滚轮 | 上下平移视口；在目录区滚轮翻页 |
| Shift + 滚轮 / Ctrl + 滚轮 | 以光标为锚点缩放（1x ~ 4x） |
| `W A S D` 或方向键 | 平移视口（按住 Shift 步长从 4 变 12） |
| `+` / `-` | 缩放 |
| `1` ~ `9` | 依次选择 9 个工具 |
| `[` / `]` | 笔刷半径 0 ~ 4 |
| 空格 | 昼夜切换 |
| `N` | 自动昼夜开关 |
| `P` | 城市成长开关 |
| `G` | 显示网格（缩放 < 2x 时不画） |
| `TAB` | 切换建筑分类 |
| `Q` / `E` | 目录翻页 |
| `F1` | 操作说明对话框 |
| `Ctrl+S` / `Ctrl+O` | 存 / 读 localStorage |
| `Ctrl+Z` | 撤销（当前版本未接入快照，只会提示「没有可撤销的操作」，见 `docs/gameplay.md`） |
| `Esc` / `Enter` | 关闭对话框 |

顶部状态栏还可直接点击：`x∈[566,600)` 切昼夜，`x∈[96,100)` 切速度档。

---

## 管线总览

```
  Go 离线（make assets，一次性、可重复）
  ┌──────────────────────────────────────────────────────────────────────────┐
  │ internal/catalog  程序化建模                                              │
  │   Def{Footprint, Height, Variants, Cost/Pop/Jobs/Level, Build}            │
  │      └─ BuildMesh(def, variant) → geom.Mesh（一堆带材质的 Quad）           │
  │                         │                                                 │
  │                         ▼                                                 │
  │ internal/render  等距光栅化（Day profile / Night profile 各跑一遍）        │
  │   Project:  sx=(x-y)*16, sy=(x+y)*8-z*8, depth=x+y+2z                     │
  │   4x 超采样 → 逐三角形 Z-buffer → 程序化图案查色阶 → 平面着色               │
  │   → 阴影扫掠体（画在物体之下）→ 盒式降采样 → 自发光辉光（加法）             │
  │   → 像素化：描边压暗 / 受光侧提亮 → 母板量化 + 8x8 有序抖动                  │
  │   → 裁剪透明边、记录锚点 anchor                                           │
  │                         │                                                 │
  │                         ▼                                                 │
  │ internal/sheet  货架式装箱 → atlas_0.png（2048x2048，1px 间隔）            │
  │ cmd/prerender   夜间档复用日间画布（Bounds+NoTrim），尺寸/锚点逐张断言一致  │
  │                 输出 manifest.json（含日夜两套图集坐标）+ palette.json      │
  └──────────────────────────────────────────────────────────────────────────┘
                                     │
                                     ▼
  浏览器运行时（web/js）
  ┌──────────────────────────────────────────────────────────────────────────┐
  │ assets.js  加载 manifest + palette + 图集；按昼夜档取图集坐标              │
  │ render.js  画家算法合成整图缓存：按 d=x+y 升序铺地形/侧壁/道路             │
  │            单体按「占地东南角」的 d 值插入 → 整图 canvas（仅数据变化时重建） │
  │ view.js    相机 + 整数倍缩放 + 视口 blit + 屏幕→格子拾取 + 光标预览         │
  │            水面动画：只重画视口内的水面瓦片（不进缓存）                     │
  │ ui.js      状态栏 / 工具栏 / 分类页签 / 目录 / 信息框 / 小地图 / 对话框      │
  │ font.js    系统字体二值化成点阵，按色板着色                                │
  │ crt.js     辉光 → 扫描线 → 荫罩 → 暗角 → 97 色量化 + 8x8 抖动              │
  │            → 整数倍最近邻放大到窗口                                        │
  └──────────────────────────────────────────────────────────────────────────┘
```

---

## 设计取舍

**为什么不用 Blender。** 目标观感是 90 年代 DOS/PC-98 的等距游戏：形体简单、色块干净、网点规则、尺寸严格对齐到 32x16 网格。这种美术的约束是**几何和色板**，不是造型精度。用代码建模能直接把这套约束写成断言——每个原型必须落在整数格占地（`Footprint`）、必须对齐 8 像素高度单位、必须只引用母板色阶——而且改一个参数就能整批重渲 83 张图。外部 DCC 工具反而会引入不可复现的手工步骤。

**为什么运行时不做光照。** 等距场景里光照是**每张精灵图一次性确定**的：朝向固定、光源固定、阴影方向固定。运行时若再做一遍光照，既要维护法线/材质/光源数据，又要让实时结果和离线烘焙结果在观感上完全一致——纯属自找麻烦。把明暗、阴影、辉光全部烘焙进精灵图后，运行时的每帧成本退化成一次 `drawImage`，而观感严格可控。代价是「动态光源」这类效果需要靠换精灵图（比如昼夜两档）而非计算来实现。

**为什么用有序抖动而不是误差扩散。** 误差扩散（Floyd–Steinberg 之类）会产生**不规则噪点**，并且把误差横向传播，在动画里会「爬行」——同一块静止画面在逐帧重算时会闪烁。有序抖动（Bayer 矩阵）是位置确定性的：同一像素永远得到同一个阈值，因此静止画面绝对稳定，运动时也只有规则网点在动。这恰好就是那个年代显卡和美术软件的做法：规则的 2x2 网点看起来像「材质」，不像「噪声」。管线里两处都用到它——离线渲染用 4x4 图案打「贴图」，最终量化与 CRT 用 8x8。

**为什么一个像素尺度上只压一次色板。** 精灵图在离线阶段已经量化到 97 色；运行时混色（半透明阴影、UI 抗锯齿图标）会引入色板外颜色，所以 CRT 后处理在整屏层面再做一次量化——这样「整屏有限色板」的最终保证落在唯一的出口上。

更多细节：

- [docs/pre-render-pipeline.md](docs/pre-render-pipeline.md) —— 离线管线：投影约定、Z-buffer、材质系统、图案表、贴花 Bias、超采样与量化、侧壁堆叠、锚点、昼夜一致性、参考场景合成器
- [docs/art-direction.md](docs/art-direction.md) —— 97 色母板、三色调明暗、阴影方向、抖动网点、CRT 参数
- [docs/gameplay.md](docs/gameplay.md) —— 工具、地形与海岸线、城市成长、存档
- [docs/architecture.md](docs/architecture.md) —— Go/Web 模块职责、数据流、缓存失效、命名约定
