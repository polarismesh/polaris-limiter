# polaris-limiter Makefile
#
# 入口约定：
#   make / make help        显示所有 target
#   make build              本地构建二进制
#   make test               跑单测（不开 race）
#   make race               跑单测（开 -race）
#   make vet / lint         静态检查
#   make run / stop / ps    本地启动 / 停止 / 查看
#   make release [VERSION=] 打包发版 zip（同 build.sh）
#   make docker IMAGE_TAG=  构建多架构 docker 镜像
#   make clean              清理构建产物
#
# 风格：所有 recipe 都通过 .PHONY 声明，避免与同名文件冲突。

# ============================================================
# 变量
# ============================================================

SHELL              := /bin/bash
GO                 ?= go
PKG                := github.com/polarismesh/polaris-limiter
BIN                := polaris-limiter

# 构建版本：未传则以当前时间戳为准（兼容 build.sh 行为）
VERSION            ?= $(shell date +%s)000
BUILD_DATE         := $(shell date "+%Y%m%d.%H%M%S")
GOOS               ?= $(shell $(GO) env GOOS)
GOARCH             ?= $(shell $(GO) env GOARCH)

VERSION_PKG        := $(PKG)/pkg/version
LDFLAGS            := -X $(VERSION_PKG).Version=$(VERSION) -X $(VERSION_PKG).BuildDate=$(BUILD_DATE)

# 测试目录范围（默认全仓，集成测试需要外部依赖故排除）
PKGS               ?= $$($(GO) list ./... | grep -v /test/)

# release 包命名：与 build.sh 完全保持一致
RELEASE_DIR        := polaris-limiter-release_$(VERSION).$(GOOS).$(GOARCH)
RELEASE_ZIP        := $(RELEASE_DIR).zip

# Docker 镜像
IMAGE_REPO         ?= polarismesh/polaris-limiter
IMAGE_TAG          ?= $(VERSION)
PLATFORMS          ?= linux/amd64,linux/arm64

# CGO 默认关闭，与 build.sh 一致
export CGO_ENABLED ?= 0

# ============================================================
# 默认 target
# ============================================================

.DEFAULT_GOAL := help

.PHONY: help
help: ## 显示所有可用 target
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "} {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' \
		| sort

# ============================================================
# 构建
# ============================================================

.PHONY: build
build: ## 本地构建二进制（输出到当前目录的 polaris-limiter）
	@echo ">> building $(BIN) ($(GOOS)/$(GOARCH))"
	GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build -o $(BIN) -ldflags="$(LDFLAGS)" .

.PHONY: build-linux
build-linux: ## 交叉编译 Linux amd64 二进制
	@$(MAKE) build GOOS=linux GOARCH=amd64

.PHONY: build-linux-arm64
build-linux-arm64: ## 交叉编译 Linux arm64 二进制
	@$(MAKE) build GOOS=linux GOARCH=arm64

# ============================================================
# 测试 / 静态检查
# ============================================================

.PHONY: test
test: ## 跑全部单测（不含集成测试）
	$(GO) test -count=1 -timeout=120s $(PKGS)

.PHONY: race
race: ## 跑全部单测，开启 -race 检测
	$(GO) test -race -count=1 -timeout=180s $(PKGS)

.PHONY: cover
cover: ## 生成覆盖率报告（cover.out + cover.html）
	$(GO) test -count=1 -coverprofile=cover.out $(PKGS)
	$(GO) tool cover -html=cover.out -o cover.html
	@echo ">> coverage report: cover.html"

.PHONY: bench
bench: ## 运行 benchmark/ 下的性能基准
	$(GO) test -bench=. -benchmem -count=3 ./benchmark/...

.PHONY: vet
vet: ## go vet 静态检查
	$(GO) vet ./...

.PHONY: fmt
fmt: ## gofmt 格式化全部代码
	$(GO) fmt ./...

.PHONY: tidy
tidy: ## go mod tidy 整理依赖
	$(GO) mod tidy

.PHONY: lint
lint: vet ## 静态检查（vet + 可选 golangci-lint）
	@if command -v golangci-lint >/dev/null 2>&1; then \
		echo ">> running golangci-lint"; \
		golangci-lint run ./...; \
	else \
		echo ">> golangci-lint not found, skip (install: brew install golangci-lint)"; \
	fi

.PHONY: ci
ci: vet test ## CI 入口：vet + test，等价于 GitHub Actions 的检查
	@echo ">> ci passed"

# ============================================================
# 运行 / 停止
# ============================================================

.PHONY: run
run: build ## 本地启动 polaris-limiter（同 ./tool/start.sh）
	@./tool/start.sh

.PHONY: stop
stop: ## 停止本地 polaris-limiter（同 ./tool/stop.sh）
	@./tool/stop.sh

.PHONY: ps
ps: ## 查看 polaris-limiter 进程（同 ./tool/p.sh）
	@./tool/p.sh

# 本地快速验证 /metrics 接口（需要先 make run）
.PHONY: metrics
metrics: ## curl 查看本地 /metrics 输出（需先启动）
	@curl -s http://127.0.0.1:8100/metrics | grep '^ratelimit_' || echo "not running, try: make run"

# ============================================================
# 发版打包
# ============================================================

.PHONY: release
release: ## 打包发版 zip（与 build.sh 行为一致）
	@bash ./build.sh $(VERSION)

# ============================================================
# Docker
# ============================================================

.PHONY: docker
docker: build-linux build-linux-arm64 ## 构建多架构 Docker 镜像（buildx）
	@echo ">> building docker $(IMAGE_REPO):$(IMAGE_TAG)"
	@cp $(BIN) polaris-limiter-amd64 || true
	@$(MAKE) build GOOS=linux GOARCH=amd64 BIN=polaris-limiter-amd64
	@$(MAKE) build GOOS=linux GOARCH=arm64 BIN=polaris-limiter-arm64
	docker buildx build \
		--platform $(PLATFORMS) \
		-t $(IMAGE_REPO):$(IMAGE_TAG) \
		-t $(IMAGE_REPO):latest \
		--load \
		.
	@rm -f polaris-limiter-amd64 polaris-limiter-arm64

# ============================================================
# 清理
# ============================================================

.PHONY: clean
clean: ## 清理构建产物（二进制 / release 目录 / 覆盖率文件）
	@echo ">> cleaning build artifacts"
	@rm -f $(BIN) polaris-limiter-amd64 polaris-limiter-arm64
	@rm -rf polaris-limiter-release_*
	@rm -f polaris-limiter-release_*.zip polaris-limiter-release_*.zip.md5sum
	@rm -f cover.out cover.html
