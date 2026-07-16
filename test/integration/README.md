# polaris-limiter 集成测试

本目录用于对 **polaris-limiter 服务端** 做端到端验证：分布式限流行为 + `/metrics` 监控指标输出。包含两套测试场景——**本地单机**与**云端多节点分布式**——共用同一套基于 polaris-go 的 consumer / provider demo。

## 目录结构

```
test/integration/
├── README.md                     # 本文件：目录总览与功能说明
├── test.sh                       # 本地单机 E2E 主脚本
├── cleanup.sh                    # 本地残留进程 / 产物清理
├── polaris-limiter-test.yaml     # 本地 limiter 启动配置（statis=prometheus）
├── local-standalone-test.md      # 本地单机 test.sh 详细说明
├── cloud-distributed-test.md     # 云端多节点分布式测试详细说明
├── metrics.md                    # polaris-limiter /metrics 指标说明
├── consumer/                     # 消费者 demo（独立 go.mod）
│   ├── main.go                   # HTTP server：服务发现选实例 + 透传请求（含 429）
│   ├── polaris.yaml              # SDK 配置（push 模式 prometheus 上报）
│   ├── Makefile                  # build / build-linux / run / clean
│   ├── go.mod / go.sum
│   ├── consumer.sh               # 云端 consumer 节点 E2E 编排（多服务串行验证）
│   └── clean-c.sh                # 云端 consumer 节点清理脚本
├── provider-qps/                 # QPS 限流 provider demo（独立 go.mod）
│   ├── main.go                   # HTTP server：LimitAPI.GetQuota 限流判定 + 注册/注销
│   ├── polaris.yaml              # SDK 配置（pull prometheus + eventReporter + limiter 占位）
│   ├── Makefile
│   ├── go.mod / go.sum
│   ├── provider.sh               # 云端 provider 节点部署 + 自检脚本
│   └── clean-p.sh                # 云端 provider 节点清理脚本
├── .build/                       # 编译产物与 SDK 运行目录（gitignore）
├── .logs/                        # 运行日志 / /metrics 快照 / monitor_sim 输出（gitignore）
└── .tmp/                         # sed 生成的临时配置（gitignore）
```

## 文件与脚本功能

| 文件 / 目录 | 类型 | 功能 |
|---|---|---|
| `test.sh` | 本地脚本 | 本地单机端到端主脚本：编译并启动 polaris-limiter + provider + consumer，验证 `/metrics` 指标 + 6.x 分布式 GLOBAL 用例 + monitor sidecar 抓取模拟 |
| `cleanup.sh` | 本地脚本 | 清理 `test.sh` 残留的 polaris-limiter / provider / consumer 进程及 `.build` / `.logs` / `.tmp` 产物 |
| `polaris-limiter-test.yaml` | 配置 | 本地 limiter 启动配置：`statis=prometheus`（暴露 `/metrics`）、`registry.name=polaris.limiter-local`、端口 8100/8101 |
| `consumer/` | demo | polaris-go ConsumerAPI 示例，独立 go.mod |
| `provider-qps/` | demo | polaris-go ProviderAPI + LimitAPI 示例，独立 go.mod |
| `consumer/consumer.sh`、`consumer/clean-c.sh` | 云端脚本 | 云端 consumer 节点的 E2E 编排与清理 |
| `provider-qps/provider.sh`、`provider-qps/clean-p.sh` | 云端脚本 | 云端 provider 节点的部署自检与清理 |

## 两套测试场景

### 场景一：本地单机 E2E（`test.sh`）

在本地编译并启动整套链路（polaris-limiter + provider + consumer），验证 polaris-limiter 服务端行为：

- **`/metrics` 指标验证**：7 个 `ratelimit_*` 指标齐备、7 维 label 完整、`total == pass + limit` 等（服务 `MetricsRatelimitEchoServer`）
- **6.x 分布式 GLOBAL 用例**：多窗口聚合限流、多实例共享配额、regex_combine 多 path 共享配额、远端 limiter 不可用降级等
- **monitor sidecar 模拟**：后台按每分钟 `:15` 抓取 `/metrics`，模拟 polaris-monitor sidecar 的 delta 上报契约

调用链路：

```
curl → consumer:18201 → provider:18200 → polaris.limiter-local:8101 (gRPC)
                                                  ↓
                                              /metrics:8100 (HTTP, prometheus 插件)
```

前置依赖：本地已运行 polaris-server（默认 `127.0.0.1:8090` HTTP / `:8091` gRPC）。

> 详细分步说明（目标、链路、10 步验证过程、配置说明、常见问题）见 [local-standalone-test.md](local-standalone-test.md)。

```bash
./test/integration/test.sh                              # 默认连本地 polaris-server
./test/integration/test.sh --polaris-server 1.2.3.4     # 指定远程 polaris-server
./test/integration/test.sh --keep                       # 保留进程与日志便于排查
```

### 场景二：云端多节点分布式（`provider.sh` / `consumer.sh`）

polaris-server 与 polaris-limiter（2 节点）已云端部署时，在三台机器（eee1 / eee2 / eee3）上验证多服务 × 多 limiter 节点的分布式限流拓扑：

- **provider 节点（eee2 / eee3）**：`provider.sh` 启动 provider 实例注册到 polaris（不创建规则）
- **consumer 节点（eee1）**：`consumer.sh` 串行验证多服务，创建/刷新 GLOBAL 规则，跑 Case A/B/C，并打印 limiter 双节点命中与累加核对提示
- 验证范围：多服务分布式限流 + 每服务跨节点共享配额 + 驱动 2 个 limiter 节点（不验证 limiter `/metrics`，云端跨节点网络不通）

> 详细拓扑、用例与人工核对步骤见 [cloud-distributed-test.md](cloud-distributed-test.md)。

## demo 程序

`consumer/` 与 `provider-qps/` 是两个**独立 go.mod** 的 polaris-go 示例程序，供两套场景共用：

| 程序 | 角色 | 关键行为 | 主要 flag |
|---|---|---|---|
| `consumer/main.go` | 消费者 | HTTP server（catch-all），用 polaris 服务发现选 provider 实例并透传请求（含 429），上报服务调用结果 | `--service` `--port` `--namespace` `--caller-service` `--caller-ip` `--caller-metadata` `--debug` |
| `provider-qps/main.go` | 限流 provider | HTTP server（catch-all），调用 `LimitAPI.GetQuota` 做限流判定，被限返回 429（优先用规则 CustomResponse），并把 method / header / query / caller 等维度塞进 quota 请求；启动注册、退出注销 | `--service` `--port` `--namespace` `--token` `--debug` |

两个 demo 的 `polaris.yaml` 通过 `${POLARIS_SERVER}` / `${POLARIS_TOKEN}` / `${POLARIS_LIMITER_NS}` / `${POLARIS_LIMITER_SVC}` / `${POLARIS_METRICS_PORT}` 等占位符由环境变量注入，`Makefile` 提供 `build` / `build-linux`（交叉编译 `x86-bin`）/ `run` / `clean` 目标。

## /metrics 指标

polaris-limiter 启用 prometheus 插件后，HTTP `/metrics` 暴露 7 个指标：4 个实例级 gauge（`ratelimit_active_streams` / `ratelimit_counter_count` / `ratelimit_process_avg_us` / `ratelimit_process_max_us`）+ 3 个带 7 维 label 的服务级 counter（`ratelimit_rq_total` / `ratelimit_rq_pass` / `ratelimit_rq_limit`），每 60s 对齐分钟整点 flush。

> 指标定义、数据流、flush 时序与完整输出示例见 [metrics.md](metrics.md)。

## 前置依赖

| 依赖 | 说明 |
|---|---|
| polaris-server | 注册中心 + 服务发现 + 限流规则下发。本地场景需自行启动；云端场景已部署在 `172.16.0.5` |
| Go 工具链 | 1.20+（demo 程序 go.mod 声明 go 1.24）。本地 `test.sh` 会从源码编译 polaris-limiter 与两个 demo |

## 运行时产物

`test.sh` / `consumer.sh` / `provider.sh` 运行产生的 `.build/` / `.logs/` / `.tmp/` 及各 demo 下的 `polaris/`（SDK 日志目录）均已加入 `.gitignore`，不纳入版本管理。云端部署用的预编译 `x86-bin` 由各 demo 的 `make build-linux` 生成。

## 文档索引

| 文档 | 内容 |
|---|---|
| [local-standalone-test.md](local-standalone-test.md) | 本地单机 `test.sh` 的目标、链路、10 步验证过程、配置说明与常见问题 |
| [cloud-distributed-test.md](cloud-distributed-test.md) | 云端多节点拓扑、`provider.sh` / `consumer.sh` / 清理脚本用法、Case A/B/C 与人工核对步骤 |
| [metrics.md](metrics.md) | polaris-limiter `/metrics` 的 7 个指标定义、label、数据流、flush 时序与输出示例 |
