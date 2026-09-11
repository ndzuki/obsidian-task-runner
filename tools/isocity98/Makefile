# ISO-CITY '98 —— 离线预渲染管线 + 网页运行时
#
# 沙箱/CI 里 ~/.cache 可能只读，所以把 Go 缓存固定在项目内（可用环境变量覆盖）。

GOCACHE ?= $(CURDIR)/.cache/go-build
GOMODCACHE ?= $(CURDIR)/.cache/gomod
export GOCACHE
export GOMODCACHE

ASSETS  ?= web/assets
BUILD   ?= build
ADDR    ?= 127.0.0.1:8098
SS      ?= 4
# 像素密度：1 = 1 格 32x16 像素（640x400 内部帧缓冲），2 = 64x32（1280x800）。
# 几何与色板完全不变，只改像素密度，因此可以随时切换。
SCALE   ?= 3

.PHONY: help
help:
	@echo "make assets     重新渲染全部精灵图与图集到 $(ASSETS)（默认像素密度 $(SCALE)x）"
	@echo "make sheet      输出接触印相图到 $(BUILD)/contact.png（美术审阅）"
	@echo "make scene      输出参考城市场景到 $(BUILD)/scene.png（离线审阅整体观感）"
	@echo "make serve      启动静态服务器（默认 $(ADDR)）"
	@echo "make check      构建 + 测试 + 重渲染资产 + 校验 manifest"
	@echo "make shot       无头 Chrome 截图到 $(BUILD)/shot.png"
	@echo "make e2e        端到端交互冒烟（真实鼠标事件 + 20 项断言）"
	@echo "make agenttown  Agent Town 等距重设计探针（图集 + 小镇预览 + 季节对比图）"
	@echo "make shots      重新截取两张成品图（白天/夜间）到 $(BUILD)/"
	@echo "make clean      清理产物"
	@echo ""
	@echo "像素密度：SCALE=1（640x400）/ SCALE=2（1280x800）/ SCALE=3（1920x1200，默认）"

.PHONY: assets
assets:
	go run ./cmd/prerender -ss $(SS) -scale $(SCALE) -out $(ASSETS) -build $(BUILD)

.PHONY: sheet
sheet:
	go run ./cmd/prerender -scale 1 -out $(BUILD)/probe -build $(BUILD) \
		-sheet $(BUILD)/contact.png -cols 10 -zoom 2 -no-night

.PHONY: scene
scene:
	go run ./cmd/prerender -scale 1 -out $(BUILD)/probe -build $(BUILD) \
		-scene $(BUILD)/scene.png -zoom 2 -no-night

.PHONY: serve
serve:
	go run ./cmd/serve -addr $(ADDR) -dir web

.PHONY: build
build:
	go build ./...

.PHONY: test
test:
	go test ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: fmt
fmt:
	gofmt -l -w cmd internal

.PHONY: shot
shot: assets
	@mkdir -p $(BUILD)
	@go run ./cmd/serve -addr $(ADDR) -dir web >/dev/null 2>&1 & echo $$! > $(BUILD)/serve.pid; \
	sleep 2; \
	node scripts/shot.mjs --url http://$(ADDR)/ --out $(BUILD)/shot.png --wait 3500 \
		--eval "(()=>{ISO.newMap(7);ISO.demo();return 'ok';})()"; \
	status=$$?; kill $$(cat $(BUILD)/serve.pid) 2>/dev/null; rm -f $(BUILD)/serve.pid; exit $$status

.PHONY: e2e
e2e: assets
	@mkdir -p $(BUILD)
	@go run ./cmd/serve -addr $(ADDR) -dir web >/dev/null 2>&1 & echo $$! > $(BUILD)/serve.pid; \
	sleep 2; \
	node scripts/e2e.mjs --url http://$(ADDR)/ --out $(BUILD)/e2e.png; \
	status=$$?; kill $$(cat $(BUILD)/serve.pid) 2>/dev/null; rm -f $(BUILD)/serve.pid; exit $$status

.PHONY: check
check: build vet test assets verify

.PHONY: fmtcheck
fmtcheck:
	@gofmt -l cmd internal | tee /dev/stderr | wc -l | grep -q '^0$$' || (echo 'gofmt 未通过'; exit 1)

.PHONY: verify
verify:
	@test -s $(ASSETS)/manifest.json || (echo "manifest.json 缺失"; exit 1)
	@test -s $(ASSETS)/palette.json || (echo "palette.json 缺失"; exit 1)
	@test -s $(ASSETS)/atlas_0.png || (echo "atlas_0.png 缺失"; exit 1)
	@echo "资产校验通过：$$(ls $(ASSETS) | tr '\n' ' ')"

.PHONY: clean
clean:
	rm -rf $(BUILD) .cache

.PHONY: agenttown
agenttown:
	@mkdir -p $(BUILD)
	go run ./cmd/prerender -set agenttown -scale 1 -out $(BUILD)/at-assets -build $(BUILD) \
		-scene $(BUILD)/at_town_1x.png -zoom 2 -no-night
	go run ./cmd/prerender -set agenttown -scale 1 -out $(BUILD)/at-assets -build $(BUILD) \
		-tint $(BUILD)/at_tint.png -seasons $(BUILD)/at_seasons.png -no-night
	go run ./cmd/prerender -set agenttown -scale 1 -out $(BUILD)/at-assets -build $(BUILD) \
		-weathers $(BUILD)/at_weather.png -tods $(BUILD)/at_tods.png -season summer -tod day -no-night
	@# 面板运行时需要的资产包：图集 + manifest + 布局 + 调色板
	rm -rf $(BUILD)/panel && mkdir -p $(BUILD)/panel
	go run ./cmd/prerender -set agenttown -scale 1 -out $(BUILD)/panel -build $(BUILD) -town $(BUILD)/panel/town.json
	@ls -la $(BUILD)/panel/
	# 注意：-only 会同时过滤场景与印相图，所以这里必须拆成两条命令
	go run ./cmd/prerender -set agenttown -scale 1 -out $(BUILD)/at-assets -build $(BUILD) \
		-scene $(BUILD)/at_xray.png -zoom 2 -no-night
	go run ./cmd/prerender -set agenttown -scale 1 -out $(BUILD)/at-assets -build $(BUILD) \
		-sheet $(BUILD)/at_open.png -only '\.open$$' -cols 6 -zoom 2 -no-night
	go run ./cmd/prerender -set agenttown -scale 2 -out $(BUILD)/at-assets2 -build $(BUILD) \
		-scene $(BUILD)/at_town_2x.png -zoom 2 -no-night
	@echo "Agent Town 探针产物：$(BUILD)/at_town_1x.png  $(BUILD)/at_town_2x.png  $(BUILD)/at_tint.png"

.PHONY: shots
shots: assets
	@mkdir -p $(BUILD)
	@go run ./cmd/serve -addr $(ADDR) -dir web >/dev/null 2>&1 & echo $$! > $(BUILD)/serve.pid; \
	sleep 2; \
	node scripts/shot.mjs --out $(BUILD)/hd3_day.png --wait 4000 --w 1920 --h 1200 \
		--eval "(()=>{ISO.newMap(7);ISO.demo();return 'ok';})()"; \
	node scripts/shot.mjs --out $(BUILD)/hd3_night.png --wait 4000 --w 1920 --h 1200 \
		--eval "(()=>{ISO.newMap(7);ISO.demo();ISO.state.autoNight=false;ISO.state.night=true;ISO.assets.night=true;ISO.world.version=-1;return 'ok';})()"; \
	status=$$?; kill $$(cat $(BUILD)/serve.pid) 2>/dev/null; rm -f $(BUILD)/serve.pid; exit $$status
