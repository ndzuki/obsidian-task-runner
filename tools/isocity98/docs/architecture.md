# 代码结构

工程分成两半：**Go 离线预渲染**（`cmd/` + `internal/`）与**浏览器运行时**（`web/`）。两者之间唯一的接口是 `web/assets/manifest.json` + `palette.json` + `atlas_*.png`。运行时不知道任何几何或光照，管线不知道任何 UI 或交互。

---

## 1. 仓库布局

```
cmd/prerender/main.go   管线入口：渲染循环、图集装箱、manifest/palette 输出、参数按种类分派
cmd/prerender/scene.go  参考场景合成器（与运行时同序）+ DemoScene + 缩放合成
cmd/serve/main.go       静态服务器（零依赖，Cache-Control: no-store）
internal/art/           色板、抖动、图案、材质、画布
internal/geom/geom.go   几何词汇表（Mesh / Quad / 构件方法）
internal/render/        投影、Z-buffer 光栅化、阴影、辉光、裁剪、锚点
internal/catalog/       程序化建模目录（唯一出现建筑造型的地方）
internal/sheet/         图集装箱、接触印相图、渐变天空、Set.Draw
web/                    运行时
scripts/shot.mjs        无头 Chrome 截图 + console 错误收集
```

---

## 2. Go 包职责

| 包 | 文件 | 职责 | 关键导出 |
| --- | --- | --- | --- |
| `internal/art` | `color.go` | RGBA / Hex 解析 / Lerp / Clamp | `RGBA`, `Hex`, `Lerp`, `Scale` |
| | `palette.go` | 21 条色阶 = 97 色母板；硬边取档 `Snap`；连续插值 `LerpRamp`；6-6-6 最近色 LUT（`LUTBits=6`，并钉住每个母板色自身所在的格） | `Pal`, `Palette.Len/Ramp/Color/Snap/LerpRamp/Nearest/Quantize/QuantizeAmp/ToJSON`, `LUTBits` |
| | `dither.go` | Bayer8 矩阵、确定性 `Hash`/`Hashf`、xorshift32 `Rand` | `Bayer8`, `Bayer`, `Hash`, `NewRand` |
| | `patterns.go` | `Face` 枚举 + `FaceUV` 面内参数化 + 18 个 `Pattern` 函数 | `FaceTop/PosX/PosY/NegX/NegY/Bottom`, `FaceUV`, `Pat`, `PatternAmp` |
| | `material.go` | `Material`（色阶 + 四朝向位置 + 图案 + 自发光）、`Profile`（昼夜档）、`baseMaterials` / `litMaterials` / `dayExtra`、`validateProfiles` | `Day`, `Night`, `Shade*`, `Material`, `Profile` |
| | `buffer.go` | 预乘浮点画布 `Buf`（Blend/Add/Composite/Sub/Downsample/Blur/AlphaBounds/Opacity）、像素化 `ToNRGBA`、`SavePNG` | `Buf`, `SpriteOpts`, `DefaultSpriteOpts`, `SavePNG` |
| `internal/geom` | `geom.go` | `Vec3` / `Quad` / `Mesh`；构件：`Box` `SlabTop` `Frustum` `Parapet` `Cylinder` `Cone` `Ellipsoid` `DecalX/Y/Top` `WindowGrid` `Stairs` `Translate` `RotateZ90` `Merge` `Clone` `Bounds` | `Mesh`, `Quad`, `Vec3`, `V` |
| `internal/render` | `render.go` | 等距常量与 `Project`、`Renderer`（持色板 + profile）、画布范围推导、超采样光栅化、阴影扫掠、辉光、裁剪、锚点；`resolve` 是「美术定向光照」的落地点 | `TileW/TileH/ZUnit`, `Project`, `Renderer`, `Render`, `Options`, `Sprite`, `Rect`, `ShadowOpts`, `ShadowSrc` |
| `internal/catalog` | `builder.go` | `Def` / `B` 定义、`BuildMesh`（按名称 + 变体号播种 PRNG）、全部建模辅助方法（`Walls` `Solid` `FloorBand` `Windows` `FlatRoof` `GableRoof` `HipRoof` `Eave` `Door` `Awning` `Sign` `Chimney` `Antenna` `WaterTank` `Stoop` `ACUnits` `Trees` `Pipe` `Ledges` `Sawtooth` …） | `Def`, `B`, `BuildMesh`, `Kind*` |
| | `catalog.go` | `All()` 汇总 + 重名/变体/`Build` 断言；道路瓦片 16 连通掩码 + 铁路 | `All` |
| | `terrain.go` | 地形瓦片 + 侧壁瓦片 + 共用画布 `TileBounds` / `CliffBounds` | `TileBounds`, `CliffBounds` |
| | `buildings_*.go` | 住宅 6 / 商业 4 / 工业 2 / 市政 2 / 公园 1 原型，各自的 `*Defs()` 汇总 | — |
| | `props.go` | 阔叶树 5 变体、针叶树 3 变体 | — |
| | `common.go` | `Pad` / `Lot` 场地、8 套住宅配色 `wallSchemes`、`altWall` | — |
| | `vehicles.go` | 车辆目录，**当前 `vehicleDefs()` 返回 `nil`**（占位：四方向靠 `RotateZ90` 复用同一模型） | — |
| `internal/sheet` | `sheet.go` | 货架式 `Packer`；`Contact` 接触印相图；`Sky` 渐变天空（连续位置 + 抖动幅度 46）；`Set.Draw`（Go 端锚点贴图） | `Packer`, `Contact`, `Sky`, `Set` |

---

## 3. Web 模块职责

| 模块 | 职责 | 关键导出 |
| --- | --- | --- |
| `js/main.js` | 入口：启动流程、全局 `state`、输入绑定、工具/目录选择、存档与选项、轻量模拟（时钟 / 自动昼夜 / 城市成长）、主循环 `frame → compose → crt.present`、示例城镇 `buildDemoTown` | `window.ISO` |
| `js/assets.js` | 加载 `palette.json` / `manifest.json` / 图集；名称 → 精灵条目索引；按昼夜档取图集坐标；原型名 / 变体 / 动画帧查询；建筑目录聚合 | `Assets` |
| `js/palette.js` | 与 Go 端同构的色板：Bayer4/Bayer8、`bayer()`、`dot4()`、6-6-6 LUT、`quantize` / `snap` / `lerpRamp` | `Palette`, `BAYER8`, `bayer` |
| `js/render.js` | 等距世界合成器：`project`、整图缓存 `WorldRenderer`（地形 / 侧壁 / 道路 / 单体）、命名回退 `pickVariant` / `variantOf`、水面动画相位 | `WorldRenderer`, `project`, `TILE_W/TILE_H/Z_UNIT` |
| `js/view.js` | 相机（整数倍缩放、以光标锚点缩放、clamp）、视口 blit、屏幕→格子拾取 `pickCache`、水面逐帧覆盖、光标与占地预览 | `View`, `VIEW`, `pickCache` |
| `js/city.js` | 数据模型：高度场 / 地表 / 道路 / 单体占用；编辑操作（改高度、改地表、铺路、放置、移除）；统计；JSON 序列化；岛屿生成与海岸平滑、地表分类 | `City`, `TERRAIN`, `ROADS`, `MAX_HEIGHT`, `generateIsland` |
| `js/tools.js` | 工具状态机：点/拖拽 → `City` 编辑；笔刷；整平参考高度；Bresenham 拖拽补齐 | `ToolRunner` |
| `js/ui.js` | 界面层：布局 `LAYOUT`、`TOOLS`、分类表、DOS 双线镶边 `bevel`、2x2 网点 `dither`、状态栏、工具栏、页签、目录、信息框、小地图（1 像素 = 1 格）、对话框、命中测试 | `UI`, `LAYOUT`, `TOOLS`, `hitTest`, `itemsForTool` |
| `js/font.js` | 位图字体：系统字体渲染 → alpha 阈值 0.42 二值化 → 按色板着色缓存（12px，PC-98 风格） | `Font` |
| `js/crt.js` | CRT 后处理：三档模式、辉光（1/4 分辨率阈值 + 两次盒式模糊）、扫描线、荫罩、暗角、97 色量化 + 8x8 抖动、整数倍放大输出 | `CRT` |

---

## 4. 数据流

```
assets/*.json + atlas_0.png
        │  Assets.load()
        ▼
Assets { byName:Map, pal:Palette, sheets:[Image], night:bool }
        │
        ├─► UI / Font / CRT 取色（pal.snap / pal.rgb）
        │
        ▼
City { w,h, height:Int8Array, terrain:Uint8Array, road:Uint8Array,
       objects:[], occ:Int32Array, version:int }
        │  version++ 于任何一次真实变更（reset / setHeight / setTerrain / setRoad
        │  / place / removeIndex；fromJSON 也 ++）
        │
        ├─► WorldRenderer.ensure(city)
        │      if (cache && version === city.version && night === assets.night) 复用
        │      else build(city) 重建整图 canvas
        │
        ▼
View.draw(ctx, city, world, opts)
    clip 视口 → 底色 → drawImage(整图缓存, camX, camY, w/zoom, h/zoom)
    → drawWater(逐帧覆盖视口内的水面瓦片) → drawCursor(悬停预览)
        │
        ▼
compose(): 网格 → 消息 → 状态栏 + 面板 + 小地图 → 对话框 → 启动画面
        │
        ▼
CRT.present(scene, displayCtx, 640, 400)
    逐像素：辉光 → 扫描线 → 荫罩 → 暗角 → 量化 → 整数倍最近邻放大到窗口
```

单帧主循环（`main.js` 的 `frame`）：

```
dt = min(0.1, now - lastTime)
state.intro -= dt
simulate(dt)                 // 时钟、自动昼夜、城市成长（speed === 0 时整体跳过）
world.animateWater(city, dt*1000*speed)
消息寿命衰减
compose()
crt.present()
每 ~4 秒 saveOptions()
requestAnimationFrame(frame)
```

---

## 5. 世界渲染缓存与失效策略

**整图缓存**（`WorldRenderer`）是性能策略的核心：整张地图一次性合成到一张离屏 canvas，只有缓存失效时才重建；平移、缩放、每帧动画都只在视口层做 blit。

失效条件是**两个值的组合**：

```js
ensure(city) {
  if (this.cache && this.version === city.version && this.night === this.assets.night) return this.cache;
  this.version = city.version;
  this.night = this.assets.night;
  this.cache = this.build(city);
}
```

| 触发 | 机制 | 代码位置 |
| --- | --- | --- |
| 任何地形/地表/道路/建筑变更 | `City.version++`（每个编辑方法只在**真的改变**数据时自增） | `city.js` |
| 撤销 / 读档 / 新地图 | 显式 `world.version = -1` 强制失配 | `main.js` 的 `undo` / `loadSave` / `ISO.newMap` |
| 昼夜切换 | `assets.night` 变化（自动昼夜、空格、状态栏点击） | `main.js` |
| 装载新的资产对象 | `world.assets` 重新赋值（会与当前 `assets.night` 比较） | `main.js` 启动流程 |

缓存画布尺寸与原点（`build`）：

```
originX = MARGIN + (h-1) * 16           // MARGIN = 200
originY = MARGIN + MAX_HEIGHT * 8        // MAX_HEIGHT = 6 → 200 + 48
cw      = (w + h - 1) * 16 + MARGIN*2    // 64x64 → 2032 + 400 = 2432
ch      = (w + h - 1) * 8  + MAX_HEIGHT*8 + MARGIN*2   // 1016 + 48 + 400 = 1464
```

`MARGIN = 200` 预留高楼向上溢出（最高 11.5 格 = 92 像素）与烘焙阴影向东南的延伸。缓存内所有贴图都写在这套「世界原点」坐标系里，视口只需要一次 `drawImage(cache, camX, camY, …)`。相机在同一个坐标系里移动，`View.originX/originY` 每帧从 `world` 同步，用于 `pickCache` 与光标换算。

**缓存内部的两条次级缓存：**

- **水面动画不进缓存**。`terrain.water` 有 4 帧（`terrain.water_f0` ~ `_f3`），若每帧 260ms 重建整图缓存，代价不可接受。因此缓存里放的是第 0 帧，`View.drawWater` 每帧只重画**视口内**的水面瓦片（先用屏幕范围粗筛，再逐格判断 `terrain === 9 && height === 0`）。水面动画因此是纯视口开销。
- **小地图离屏画布**（`UI.buildMap`）按 `(city.version, night)` 缓存一张 1 像素 = 1 格的 `ImageData`，与整图缓存同一套失效语义。

**没有做的缓存**：`assets.catalog()`（建筑目录聚合）每次 `refreshItems()` 都重新遍历 manifest；目录规模只有几十条，代价可忽略，换来的是零失效逻辑。`Assets.variants()` 同样是线性探测（最多探 12 个后缀）。

---

## 6. 命名约定：原型名（base）vs 具体精灵名（base_N）

这是整套代码里**最容易踩坑**的地方，Go 端与 Web 端各有一套推导规则，必须严格对齐。

### 规则

| 概念 | 形态 | 例子 |
| --- | --- | --- |
| **原型名 base** | 无后缀 | `house.small`、`shop.row`、`terrain.grass`、`cliff.soil.e` |
| **具体精灵名** | `base_N`（N 从 0 开始，十进制） | `house.small_0` … `house.small_3` |
| **单变体原型** | 只有 `base`，**没有** `base_0` | `townhall`、`terrain.water`？→ 见下 |
| **动画帧** | `base_fN`（N 从 0 开始） | `terrain.water_f0` … `terrain.water_f3` |

生成规则在 `cmd/prerender/main.go` 的 `variantLabel`：

```go
if d.Anim        { return base + "_f" + v }   // 动画帧
if d.Variants==1 { return base }              // 单形态：无后缀
return base + "_" + v                         // 多形态：base_0..base_{n-1}
```

关键点：**`Variants == 1` 的原型名下没有 `_0`**。manifest 里 `townhall` 就是 `townhall`，不存在 `townhall_0`。

### 三种「base → 实际精灵名」的解析

| 端 | 函数 | 行为 |
| --- | --- | --- |
| Go | `catalog.BuildMesh(d, variant)` | 直接用 `d.Build` 建网格；变体号只影响 PRNG 种子与 `b.Frame`（图案相位）。名称由 `variantLabel` 决定 |
| Go | `sheet.Set.Draw(dst, name, …)` | 按 `name` 精确查表，找不到就返回 false（**不做回退**） |
| Web | `Assets.baseOf(name)` | 若 `name` 在表里且匹配 `^(.*)_(\d+)$` 且 `$1_0` 也在表里 → 返回 `$1`；否则原样返回 |
| Web | `Assets.variants(base)` | 探测 `base_0..base_11`，命中则返回列表；否则返回 `[base]` 或 `[name]` |
| Web | `Assets.pickVariant(base)` | 从 `variants(base)` 里随机取一个（落地时用） |
| Web | `WorldRenderer.variantOf(base, v)` | 依次试 `base_v`、`base`、`base_0`，取第一个存在的 |
| Web | `WorldRenderer.pickVariant(base, x, y, n)` | `k = (x*7 + y*13) mod n`，再按同样的回退链试 `base_k`、`base`、`base_0` |

也就是说 **Web 端所有的名称解析都带三级回退**（精确变体 → 无后缀原型 → `_0`），而 Go 端是精确匹配。这个不对称是刻意的：Go 端在预渲染时手里就有 `Def`，精确性更重要；运行时手里只有地图里存的字符串，必须容忍历史上/手工写入的各种命名。

### 踩过的坑与对应约束

| 坑 | 表现 | 约束 |
| --- | --- | --- |
| 把 `Variants == 1` 的原型当成 `base_0` | 目录里能显示，落地后贴图丢失（`assets.get` 抛「未知精灵图」） | 单变体原型写 `base`，不要写 `base_0`；解析一律走 `variantOf` / `pickVariant` 而不是拼字符串 |
| 用 `baseOf` 处理动画瓦片 | `terrain.water_f0` 会被 `^(.*)_(\d+)$` 匹配失败（`f0` 不是数字），原样返回；若误把 `terrain.water` 传给 `variants()`，探测 `terrain.water_0` 失败、`terrain.water` 也不存在，最终返回输入本身，`assets.get` 抛错 | **动画瓦片的"原型名"必须直接写具体帧名**：`city.js` 里 `TERRAIN` 的水面 sprite 就是 `'terrain.water_f0'`，`itemsForTool` 对地形一律拼 `${t.sprite}_0`。`waterFrameName()` 负责让它在 `_f0.._f3` 之间轮转；判空用 `A.has('terrain.water_f0')` |
| 侧壁瓦片忘了走变体回退 | `cliff.*` 是 `base_0/base_1` 双变体，早期固定拼 `base` 会全部丢图 | `render.js` 的侧壁分支显式走 `this.pickVariant(base, x + k*3, y - k, 2)`，并额外 `if (!has(cn)) continue` |
| 变体号越界 | 存档里 `variant` 大于实际变体数（手工编辑存档） | `variantOf` 的回退链会落到 `base`；`pickVariant` 用 `k mod n` 兜住 |
| `Anim` 与 `Variants` 混用 | `Anim: true` 的原型名是 `_fN`，与 `_N` 混在一张表里 | `Assets.catalog()` 显式 `if (s.anim) continue`，动画帧不进建筑目录 |

### 变体号是怎么选出来的

| 场景 | 选择方式 | 结果 |
| --- | --- | --- |
| 预渲染 | `for v := 0; v < d.Variants; v++`，`BuildMesh` 的 PRNG 种子 = `0x51ed270b ^ hashName(name) ^ (v*7919+1)` | 同一原型的不同变体是**确定性**的（同名同号永远同一造型） |
| 地形 / 侧壁 / 道路 | `pickVariant(base, x, y, n)`：`(x*7 + y*13) mod n` | 空间上规则但不相邻重复的肌理；同一格每次渲染结果一致，因此缓存可复用 |
| 建筑 / 道具落地 | `Assets.pickVariant(base)`：`Math.random()` | 每次放置随机；结果存进对象的 `sprite` 字段，随存档固定下来 |
| 城市成长升档 | 同上 | 每次升档会换一个外观变体 |

「确定性变体」这条约束不能破：地形与侧壁的变体必须由 `(x, y)` 唯一决定，否则每次重建整图缓存（编辑一格、切昼夜）都会让全图纹理跳动。

---

## 7. 两端的同步点（改代码时必须成对修改）

| 约定 | Go 端 | Web 端 |
| --- | --- | --- |
| 图集坐标 / 锚点字段名 | `cmd/prerender/main.go` 的 `clipSprite` json tag | `assets.js` / `render.js` 读取同名字段 |
| 瓦片常量 | `render.TileW/TileH/ZUnit` → manifest 的 `tile` | `render.js` 的 `TILE_W/TILE_H/Z_UNIT`（**硬编码，不读 manifest.tile**） |
| 屏幕映射公式 | `render.Project` | `render.project` |
| 绘制顺序（画家算法） | `cmd/prerender/scene.go` 的 `RenderScene` | `render.js` 的 `build` |
| 侧壁材质映射 | `catalog/terrain.go` 的 `cliffDefs`（`cliff.soil/rock/sand/pave/farm`） | `render.js` 的 `CLIFF_OF` 表 |
| 道路连通掩码 | `catalog/catalog.go` 的 `dirVec` + `roadDefs`（bit0=+X） | `city.js` 的 `roadMask` + `ROADS[].sprite` |
| 色阶 / 量化语义 | `art.Palette.Snap/LerpRamp/Nearest` | `palette.js` 的同名方法 |
| Bayer 矩阵 | `art.Bayer8` | `palette.js` 的 `BAYER8` 与 `crt.js` 内联的同一张表 |
| 地表 id | 无（Go 只管渲染瓦片） | `city.js` 的 `TERRAIN`（id 只能追加） |

`render.js` 硬编码 `TILE_W = 32 / TILE_H = 16 / Z_UNIT = 8`（而不是从 `manifest.tile` 读）是一个有意为之的简化：这几个数在投影公式、拾取、光标绘制里到处出现，读 manifest 只会推迟错误发生的时间。改动投影常量时必须同时改 `internal/render/render.go`、`web/js/render.js`，以及 `render.js` 里散落的 `/ 2`、`/ 16`、`/ 8` 字面量。

---

## 8. 开发与验证入口

| 目的 | 命令 |
| --- | --- |
| 全量门禁 | `make check`（build + vet + test + 重渲资产 + verify） |
| 只重渲一类单体 | `go run ./cmd/prerender -out build/probe -only '^house\.'` |
| 美术审阅 | `make sheet` → `build/contact.png` + `build/contact.png.txt` |
| 整体观感审阅（不开浏览器） | `make scene` → `build/scene.png` |
| 浏览器端验证 | `make serve` 后 `node scripts/shot.mjs --url http://127.0.0.1:8098/ --out build/shot.png --wait 4000 --eval "ISO.demo()"` |
| 控制台调试 | `window.ISO.state / city / view / assets / crt`、`ISO.newMap(seed)`、`ISO.demo()`、`ISO.setTool(i)` |

`scripts/shot.mjs` 用零依赖方式（Node 内置 `WebSocket` + `fetch`）驱动无头 Chrome：收集 `Runtime.consoleAPICalled` / `Runtime.exceptionThrown` / `Log.entryAdded`，执行可选的注入脚本，截图，最后若有 `[exception]` / `[error]` / `[eval]` 开头的日志则以退出码 1 结束——可以直接放进 CI 断言「零 console 错误」。
