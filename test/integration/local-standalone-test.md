# polaris-limiter 集成测试

本目录下的 `test.sh` 用于端到端验证 **polaris-limiter 服务端** 的分布式限流功能与 `/metrics` 监控指标输出。

## 1. 测试目标

参考 `git.woa.com/polaris-go-examples/ratelimit/verify_ratelimit.sh`，本地启动一个 polaris-limiter 实例，让 provider 通过 gRPC 接入它做 **分布式限流（GLOBAL）**，然后验证 **polaris-limiter 进程 /metrics 端点**输出的监控数据是否符合预期。

> **不验证** provider 自身 polaris-go SDK 的 `/metrics`（那套指标 label 是 `callee_namespace/callee_service/...`，与 polaris-limiter 服务端的 `namespace/service/method/appid/uin/labels/duration` 七维 label 是两码事）。

## 2. 调用链路

```
curl → consumer:18201 → provider:18200 → polaris.limiter-local:8101 (gRPC)
                                                 ↓
                                             /metrics:8100 (HTTP, prometheus 插件)
```

- `consumer` 用 polaris 服务发现选 `MetricsRatelimitEchoServer` 的 provider 实例并转发请求
- `provider` 调用 `LimitAPI.GetQuota`，SDK 通过 gRPC stream 接入 `polaris.limiter-local:8101`
- `polaris-limiter` 服务端做配额判定，结果由 provider 透传回 consumer，最终 curl 看到 200 或 429
- 限流结果聚合到 polaris-limiter 内部 collector，由 prometheus 插件每 60s flush 到 `/metrics`

## 3. 前置依赖

| 依赖 | 默认地址 | 说明 |
|---|---|---|
| polaris-server | `127.0.0.1:8090` (HTTP API) / `:8091` (gRPC) | 必须先启动，limiter/provider/consumer 都依赖它做注册与服务发现 |
| Go 工具链 | `go` 1.20+ | 用于编译 polaris-limiter / provider / consumer |

**无需** 预先安装 polaris-limiter 二进制——`test.sh` 会从源码编译。

polaris-server 不可达时脚本会立即报错退出，例如：

```
[ERROR] 无法连接 polaris-server: http://192.0.2.1:8090
[ERROR] 请确认 polaris-server 已启动（默认监听 8090/8091），或用 --polaris-server <addr> 指定远程地址
```

## 4. 用法

```bash
# 默认连接本地 polaris-server (127.0.0.1)
cd /path/to/polaris-limiter
./test/integration/test.sh

# 指定远程 polaris-server 地址
./test/integration/test.sh --polaris-server 1.2.3.4

# 开启 polaris 鉴权时传 token
./test/integration/test.sh --polaris-token <TOKEN>

# 保留 polaris-limiter / provider / consumer 进程和日志（便于排查）
./test/integration/test.sh --keep

# 避开 8100 端口冲突（同时改 yaml 或直接改 --limiter-http-port）
./test/integration/test.sh --limiter-http-port 9100
```

完整参数：

```
./test/integration/test.sh --help
```

退出码：`0` = 通过，`1` = 失败。

## 5. 验证过程（10 个步骤）

`test.sh` 按以下步骤串行执行，每步都有 `log_step` 高亮输出：

### Step 1 — polaris-server 存活探测

`curl http://<polaris-server>:8090/health` 和 `/naming/v1/ratelimits?limit=1` 两次探测，任一返回 `000` 即判定不可达并退出。

### Step 2 — 端口冲突检测

检查 `8100` (limiter HTTP) / `8101` (limiter gRPC) / `18200` (provider) / `18201` (consumer) / `28200` (provider SDK metrics) 是否被占用。冲突时报错并提示用 `--limiter-http-port` 覆盖。

### Step 3 — 编译

- polaris-limiter：在仓库根目录 `go build -o .build/polaris-limiter .`
- provider-qps：在 `test/integration/provider-qps/` 下 `go build`（独立 go.mod）
- consumer：在 `test/integration/consumer/` 下 `go build`（独立 go.mod）

### Step 4 — 启动 polaris-limiter

用 `polaris-limiter-test.yaml` 启动（见下文配置文件说明），关键点：

- `plugin.statis.name: prometheus` —— `/metrics` 才会暴露 `ratelimit_rq_*` 等 7 个指标
- `registry.name: polaris.limiter-local` —— 用独立服务名，避免与集群中可能存在的 `polaris.limiter` 实例冲突
- `registry.host: 127.0.0.1` —— 注册到 polaris-server 的 IP 固定为本地回环，provider 服务发现拿到 `127.0.0.1:8101` 即可直连
- 端口固定 `8100` (HTTP) / `8101` (gRPC)

启动后轮询 `http://127.0.0.1:8100/metrics` 可达（最长 20s），再轮询 polaris-server 的 `/naming/v1/instances?service=polaris.limiter-local` 确认 limiter 已注册（最长 30s）。

### Step 5 — 创建 GLOBAL 限流规则

通过 polaris HTTP API `POST /naming/v1/ratelimits` 创建规则，关键字段：

```json
{
  "name": "ratelimit-e2e-metrics-rule",
  "service": "MetricsRatelimitEchoServer",
  "namespace": "default",
  "resource": "QPS",
  "type": "GLOBAL",           ← 关键：必须 GLOBAL，否则不经过 limiter 服务端
  "method": {"type": "EXACT", "value": "/echo"},
  "amounts": [{"maxAmount": 2, "validDuration": "1s"}],
  "action": "REJECT",
  "disable": false
}
```

已存在同名规则则跳过创建（与参考脚本一致，规则不删除，下次复用）。

### Step 6 — 启动 provider + consumer

- **provider-qps**：`--service MetricsRatelimitEchoServer --port 18200`
  - 通过 `POLARIS_LIMITER_NS=Polaris` / `POLARIS_LIMITER_SVC=polaris.limiter-local` 让 SDK 接入本地 limiter
- **consumer**：`--service MetricsRatelimitEchoServer --port 18201`

轮询 polaris-server 确认 provider 已注册、consumer 端口可达（最长 30s）。

### Step 7 — 链路验证（产生限流数据）

串行发 6 次 `curl http://127.0.0.1:18201/echo`，每次间隔 100ms：

```
curl → consumer:18201 → provider:18200 → polaris.limiter-local:8101 (gRPC RateLimitReport)
```

统计 HTTP 200 / 429 比例。规则是 `maxAmount=2 / 1s`，1s 窗口内限到 2，最坏跨 2 窗口仍能限到 ≥2，因此期望看到 `429 ≥ 2`。这一步的核心目的是让 provider 调用 limiter 的 gRPC 接口，在 limiter 服务端产生 `ratelimit_rq_*` 监控数据。

### Step 8 — 等待 polaris-limiter flush（最长 70s）

polaris-limiter 的 prometheus 插件 **每 60s flush 一次，对齐到分钟整点**（见 `plugin/statis/prometheus/statis.go:248`）。

flush 前的 `/metrics` 只有 gauge 类指标（`ratelimit_active_streams` / `ratelimit_counter_count` / `ratelimit_process_*`），没有 `ratelimit_rq_*` counter。

脚本每 5s 轮询一次 `/metrics`，直到出现 `^ratelimit_rq_total` 行，最长等 70s。日志会打印当前时间和轮询次数，避免误以为卡死。

### Step 9 — /metrics 指标断言

拉取 `http://127.0.0.1:8100/metrics`，做 6 组断言：

| # | 断言 | 失败处理 |
|---|---|---|
| 1 | 7 个 `ratelimit_*` 指标全部存在 | 列出缺失指标 |
| 2 | `ratelimit_active_streams ≥ 1`（provider 已接入 limiter） | 提示 provider 未接入 |
| 3 | `ratelimit_counter_count ≥ 1`（至少一个限流桶） | 提示无活跃桶 |
| 4 | `ratelimit_rq_total{service="MetricsRatelimitEchoServer"}` 行存在，且 7 维 label 齐备（`namespace/service/method/appid/uin/labels/duration`） | 列出实际 label |
| 5 | `total == pass + limit` 且 `pass > 0`（用 awk 做浮点比较） | 打印实际值 |
| 6 | `ratelimit_process_avg_us / max_us ≥ 0` | 打印实际值 |

**7 个指标**（来自 `plugin/statis/prometheus/statis.go:91-132`）：

| 指标 | 类型 | Label | 含义 |
|---|---|---|---|
| `ratelimit_rq_total` | counter | namespace, service, method, appid, uin, labels, duration | 总请求数 = pass + limit |
| `ratelimit_rq_pass` | counter | 同上 | 放行请求数 |
| `ratelimit_rq_limit` | counter | 同上 | 限流请求数 |
| `ratelimit_active_streams` | gauge | 无 | 当前活跃 gRPC stream 数 |
| `ratelimit_counter_count` | gauge | 无 | 当前活跃限流桶数量 |
| `ratelimit_process_avg_us` | gauge | 无 | 上个 flush 周期内 gRPC 平均处理耗时（μs） |
| `ratelimit_process_max_us` | gauge | 无 | 上个 flush 周期内 gRPC 最大处理耗时（μs） |

label 顺序按 prometheus 字母序：`appid, duration, labels, method, namespace, service, uin`。

完整 `/metrics` 快照保存到 `.logs/metrics_snapshot.txt` 供审计。

### Step 10 — 输出结论

```
[INFO] 验证结论: ✅ PASS — /metrics 限流监控指标验证通过
[INFO]   - 7 个 ratelimit_* 指标全部存在
[INFO]   - ratelimit_active_streams=1, ratelimit_counter_count=1
[INFO]   - ratelimit_rq_total{service="MetricsRatelimitEchoServer"} label 7 维齐备
[INFO]   - total(6) == pass(2) + limit(4)
```

任一断言失败则输出 `❌ FAIL — N 项断言失败` 并退出码 1。

## 6. 目录结构

```
test/integration/
├── test.sh                          # 主脚本
├── cleanup.sh                       # 残留进程/目录清理脚本
├── polaris-limiter-test.yaml        # polaris-limiter 启动配置（statis=prometheus）
├── README.md                        # 本文件
├── consumer/                        # 消费者 demo（独立 go.mod）
│   ├── main.go
│   ├── polaris.yaml                 # 通过 ${POLARIS_SERVER} 占位符注入 polaris 地址
│   └── Makefile
├── provider-qps/                    # QPS 限流 provider demo（独立 go.mod）
│   ├── main.go                      # 支持 --service / --port 参数
│   ├── polaris.yaml                 # 配置 limiterNamespace/limiterService 占位符
│   └── Makefile
├── .build/                          # 编译产物（gitignore）
├── .logs/                           # 运行日志（gitignore）
│   ├── polaris-limiter.log
│   ├── provider.log
│   ├── consumer.log
│   ├── test-YYYYMMDD_HHMMSS.log     # test.sh 自身输出
│   └── metrics_snapshot.txt          # 最后一次 /metrics 快照
└── .tmp/                            # sed 生成的临时配置（gitignore）
    └── polaris-limiter-run.yaml
```

## 7. 配置文件说明

### `polaris-limiter-test.yaml`

与仓库根目录 `polaris-limiter.yaml` 的关键差异：

| 字段 | 原值 | 测试值 | 原因 |
|---|---|---|---|
| `plugin.statis.name` | `file` | `prometheus` | file 插件下 `/metrics` 输出为空；prometheus 才有 7 个 `ratelimit_*` 指标 |
| `registry.polaris-server-address` | `127.0.0.1:8091` | `${POLARIS_SERVER}:8091` | 启动时由环境变量注入，支持 `--polaris-server` 指定远程 |
| `registry.name` | `polaris.limiter` | `polaris.limiter-local` | 避免与集群中已存在的 `polaris.limiter` 实例冲突 |
| `registry.host` | (未设) | `127.0.0.1` | 固定注册 IP，provider 服务发现拿到本地回环地址 |
| `logger.RotateOutputPath` | `log/polaris-limiter.log` | `.logs/polaris-limiter.log` | 避免污染仓库根目录 |

`test.sh` 启动前用 `sed` 把 `${POLARIS_SERVER}` 替换为实际值，生成 `.tmp/polaris-limiter-run.yaml`。

### provider-qps/polaris.yaml

通过 `${POLARIS_SERVER}` / `${POLARIS_TOKEN}` / `${POLARIS_LIMITER_NS}` / `${POLARIS_LIMITER_SVC}` / `${POLARIS_METRICS_PORT}` 占位符由 polaris-go SDK 自带的 env 替换功能注入。`test.sh` 启动 provider 时 export 这些环境变量。

## 8. 与参考脚本的关键差异

| 维度 | 参考脚本 `verify_ratelimit.sh` | 本 `test.sh` |
|---|---|---|
| 验证对象 | provider SDK 的 `/metrics`（pull 28200） | **polaris-limiter 服务端的 `/metrics`（8100）** |
| 限流模式 | LOCAL 为主，6.x 用例为 GLOBAL | **必须 GLOBAL**（否则不经过 limiter 服务端） |
| 依赖 polaris-limiter 进程 | 仅 6.x 用例依赖 | **全程依赖** |
| 用例数 | 10+ 个（QPS / unirate / 并发 / 自定义匹配 / regex / GLOBAL / custom-response / metrics） | 1 个 /metrics 验证主流程（另含 6.x 分布式 GLOBAL 用例，见 `test.sh`） |
| flush 等待 | 不需要（SDK 直接暴露） | **必须等 60s flush**（polaris-limiter 服务端聚合） |
| 指标 label | `callee_namespace/callee_service/callee_method/caller_labels/rule_name` | `namespace/service/method/appid/uin/labels/duration` |

## 9. 常见问题

### Q1: `/metrics` 里只有 gauge，没有 `ratelimit_rq_*`？

prometheus 插件每 60s flush 一次，对齐到分钟整点。Step 8 会轮询最长 70s 等待 flush。如果 70s 后仍无 `ratelimit_rq_total`，检查：

1. provider 是否真的调用了 limiter（看 `provider.log` 里 `GetQuota` 调用日志）
2. limiter 进程是否还在（`ps -p $LIMITER_PID`）
3. polaris-server 是否有 `polaris.limiter-local` 的健康实例（provider 服务发现依赖）

### Q2: `ratelimit_active_streams=0`？

provider 未成功接入 limiter。检查：

1. `polaris.limiter-local` 是否注册到 polaris-server（Step 4 会探测）
2. provider 的 `POLARIS_LIMITER_SVC` 环境变量是否等于 `polaris.limiter-local`
3. provider 日志是否有 limiter 连接错误

### Q3: 端口 8100/8101 被占用？

用 `--limiter-http-port 9100 --limiter-grpc-port 9101` 覆盖。注意：覆盖后需要同步修改 `polaris-limiter-test.yaml` 中的 `port` 字段（当前端口固定在 yaml 里，未通过占位符注入）。

### Q4: 规则创建失败 HTTP 401？

polaris-server 开启了鉴���，用 `--polaris-token <TOKEN>` 传 token。

### Q5: 限流没触发（429=0）？

分布式限流有"先消费后结算"语义，首次请求可能本地预消费多放几个。Step 7 发 6 次请求串行打，1s 窗口内 maxAmount=2 应该能触发 ≥2 次 429。如果仍未触发，看 `/metrics` 里 `ratelimit_counter_count` 是否 ≥1（桶是否创建）。

## 10. 清理

### 自动清理

脚本 `EXIT` trap 会自动 kill polaris-limiter / provider / consumer 三个子进程。

- 限流规则 **不删除**（与参考脚本一致，下次复用）
- `--keep` 参数可保留进程和日志便于排查

### 手动清理（cleanup.sh）

如果脚本异常退出（如被 Ctrl+C 中断、进程脱离 trap），可以用 `cleanup.sh` 清理残留：

```bash
# 默认模式：先展示匹配的进程和目录，确认后清理
./test/integration/cleanup.sh

# 强制模式：直接清理，不需要确认
./test/integration/cleanup.sh -f

# 仅展示，不执行清理
./test/integration/cleanup.sh --dry-run
```

`cleanup.sh` 清理内容：

1. **残留进程**（按角色分组，逐组确认）：
   - `polaris-limiter`（`.build/polaris-limiter`）
   - `provider-qps`（`.build/provider-qps` 或 `provider-qps/bin`，兜底手动 `make run`）
   - `consumer`（`.build/consumer` 或 `consumer/bin`，兜底手动 `make run`）
2. **构建/日志目录**：`.build/` / `.logs/` / `.tmp/`
3. **SDK 残留**：`consumer/polaris/` / `provider-qps/polaris/`（兜底手动 `go run .` 产生的）

进程清理流程：先 SIGTERM 等 1s，未响应再 SIGKILL 强杀。

**不清理**：polaris-server 上的限流规则（`MetricsRatelimitEchoServer` 下的 `ratelimit-e2e-metrics-rule`），下次 `test.sh` 直接复用。
