# polaris-limiter `/metrics` 指标说明

> 来源：`plugin/statis/prometheus` 插件，HTTP `/metrics` 接口暴露。
>
> 自 `polaris-limiter.yaml` 默认 `plugin.statis.name` 改为 `prometheus` 后，limiter 启动即开箱暴露 `/metrics`（HTTP :8100）；同时可通过 `option.file_log` 保留 4 个分类日志。
>
> 真实输出由 `plugin/statis/prometheus/dump_sample_test.go::TestDumpSampleMetrics` 生成（合成数据，仅演示格式，见下方 3.1 说明）。
> 复现命令：`go test -run TestDumpSampleMetrics -v ./plugin/statis/prometheus/...`

---

## 1. 概览

| 指标名 | 类型 | 维度 / Label | 周期语义 |
|--------|------|--------------|----------|
| `ratelimit_active_streams` | gauge | 无 | 瞬时快照 |
| `ratelimit_counter_count` | gauge | 无 | 瞬时快照 |
| `ratelimit_process_avg_us` | gauge | 无 | 1 分钟周期内均值，flush 后清零重算 |
| `ratelimit_process_max_us` | gauge | 无 | 1 分钟周期内最大值，flush 后清零重算 |
| `ratelimit_rq_total` | counter | `namespace, service, method, appid, uin, labels, duration` | 单调递增累计值（每次 flush `Add(delta)`） |
| `ratelimit_rq_pass` | counter | 同上 7 维 | 单调递增累计值 |
| `ratelimit_rq_limit` | counter | 同上 7 维 | 单调递增累计值 |

flush goroutine 每 60 秒触发一次，对齐到分钟整点边界。

### 1.1 数据流与 flush 时序

```
SDK Acquire (gRPC)
    │  ratelimitv2.counterV2.AcquireQuota → doQuotaStatReport
    ▼
RateLimitStatCollectorV2 (内存 CurveData 累加 passed/limited)
    │  prometheus flushOnce 每 60s（对齐分钟整点）DumpAndExpire
    ▼
prometheus Counter/Gauge  →  HTTP /metrics
```

- **只有 Acquire 路径写 statis**：`doQuotaStatReport`（`ratelimitv2/counter.go`）仅由 `AcquireQuota` 调用；`InitializeQuota`/`SumQuota` 不写 CurveData。因此 `ratelimit_rq_*` 三个 counter 只反映 Acquire 请求，Init 请求不计入。
- **流量到 /metrics 的延迟**：SDK 通过 gRPC 异步上报到 limiter，limiter 收到后存入 collector 的 CurveData，下一次分钟整点 flush 才写入 Counter。即一条流量最快在下一个 :00 flush 后可见（≤60s）。
- **共享 collector 模式**（prometheus 插件 + `option.file_log`）：prometheus 与 file 分类日志组件共享同一个 collector。**CurveData 的 dump 与清零统一由 prometheus `flushOnce` 负责**；`ratelimit-report` 曲线日志改由 `flushOnce` 用同一次 dump 的增量驱动写出（`FileLogger.ReportCurveDeltas`），与 `/metrics` 完全同源，不会因 file 自身 ticker 与 prometheus flush 的相位错位而少报（参见 `plugin/statis/prometheus/statis.go` 的 `flushOnce` 与 `plugin/statis/file/file_logger.go` 的 `ReportCurveDeltas`）。其余 3 个分类日志（`event` / `stat` / `server-report`）数据源独立，仍由 file 组件按自身周期写出。

---

## 2. 实例级 Gauge（无 label，每实例 1 行）

> 这 4 个指标在 polaris-limiter 进程范围内是**单值**，与具体规则/服务/调用方无关。`/metrics` 文本里没有 `{}` label 块。

### 2.1 `ratelimit_active_streams`

```
# HELP ratelimit_active_streams Number of currently active gRPC streams.
# TYPE ratelimit_active_streams gauge
ratelimit_active_streams 7
```

| 字段 | 值 |
|---|---|
| 类型 | `gauge` |
| 数据源 | `ratelimitv2.Server.ClientMng().ClientCount()` |
| 取数路径 | `bootstrap.SetServerStatsProvider` 注入的回调，flush 时实时查询 |
| 行数 | 永远 **1 行** |
| 含义 | 当前活跃 gRPC stream 数量 |

### 2.2 `ratelimit_counter_count`

```
# HELP ratelimit_counter_count Number of currently active rate limit counters.
# TYPE ratelimit_counter_count gauge
ratelimit_counter_count 33
```

| 字段 | 值 |
|---|---|
| 类型 | `gauge` |
| 数据源 | `ratelimitv2.Server.CounterMng().CounterCount()` |
| 行数 | 永远 **1 行** |
| 含义 | 当前活跃的限流规则计数器数量 |

### 2.3 `ratelimit_process_avg_us`

```
# HELP ratelimit_process_avg_us Average gRPC message processing latency in microseconds in the last flush period.
# TYPE ratelimit_process_avg_us gauge
ratelimit_process_avg_us 287.25
```

| 字段 | 值 |
|---|---|
| 类型 | `gauge`，**支持小数**（`float64`） |
| 单位 | μs（微秒） |
| 计算 | `processTotal / processCount`，每次 flush 后清零重算 |
| 数据源 | grpc interceptor `postProcess` 调用 `statis.AddProcessTime` 累计 |
| 行数 | 永远 **1 行** |
| count=0 时 | 写 `0`（不省略） |

**示例验证**：上面的 `287.25 = (20+50+80+999) / 4`。

### 2.4 `ratelimit_process_max_us`

```
# HELP ratelimit_process_max_us Maximum gRPC message processing latency in microseconds in the last flush period.
# TYPE ratelimit_process_max_us gauge
ratelimit_process_max_us 999
```

| 字段 | 值 |
|---|---|
| 类型 | `gauge`，整数（`int64` 转 `float64`） |
| 单位 | μs（微秒） |
| 计算 | 周期内 `max(elapsed_us)`，每次 flush 后清零 |
| 行数 | 永远 **1 行** |

---

## 3. 服务级 / 规则级 Counter（带 7 维 label，N 行）

> 三个 counter 共享同一组 label：`namespace, service, method, appid, uin, labels, duration`，
> 与 `plugin.RateLimitStatCounterKeyV1` 的字段集完全对齐。
>
> 每个 (namespace, service, method, appid, uin, labels, duration) 7 元组占一行；
> Counter 是**单调递增**：每次 flush 把本周期增量 `Counter.Add(delta)` 上去，**不会清零**。
>
> Label 输出顺序由 prometheus 客户端按**字母序**自动排列：
> `appid, duration, labels, method, namespace, service, uin`。

### 3.1 Label 字段定义

| Label | 类型 | 来源 | 含义 | 示例 |
|-------|------|------|------|------|
| `namespace` | string | `RateLimitStatValue.GetNamespace()` | 命名空间 | `default` / `prod` |
| `service` | string | `GetService()` | 服务名 | `svc-a` / `order-svc` |
| `method` | string | `GetMethod()`（来自 `subLabels.Method`，由 `utils.ParseLabels` 解析规则 labels 得到） | 接口/方法名；实际 GLOBAL/QPS 规则下通常为**空串**（命中的 path 被解析进 `labels` 字段） | `""` / `Acquire` |
| `appid` | string | `GetAppId()` | 应用标识 | `appOrder` |
| `uin` | string | `GetUin()` | 用户标识 | `uin3` |
| `labels` | string | `GetLabels()` | 自定义标签字串（业务侧可能为空） | `region=sh` / `""` |
| `duration` | string | `GetDuration().String()` | 限流周期，按 `time.Duration` 格式化 | `1s` / `30s` / `1m0s` |

> ⚠️ `client_ip` 维度**未加入** Counter label —— 文档明确不上报到 monitor，避免维度爆炸；
> 实例级监控由 `process_*` gauge 覆盖。

> ⚠️ **关于下方样本中的 `method="Acquire"` / `method="Init"` 行**：样本由 `TestDumpSampleMetrics` 构造的**合成数据**生成，仅用于演示 `/metrics` 文本格式。真实运行时：
> - `method` label 通常为空串（QPS/GLOBAL 规则的 path 进 `labels`，如 `labels="/echo|"`、`method=""`）；
> - `ratelimit_rq_*` 只由 Acquire 路径写入（见 1.1），**不会**出现 Init 维度的行——Init 请求不调用 `doQuotaStatReport`，不计入 counter。
>
> 即真实 `/metrics` 中同一 (namespace, service, labels, duration) 7 元组一般只有一行（Acquire），而非样本里 Acquire + Init 两行。

### 3.2 `ratelimit_rq_total`

```
# HELP ratelimit_rq_total Total number of ratelimit acquire requests aggregated per minute, labeled by rule key.
# TYPE ratelimit_rq_total counter
ratelimit_rq_total{appid="appA",duration="1s",labels="tag=user",method="Acquire",namespace="default",service="svc-a",uin="uin1"} 100
ratelimit_rq_total{appid="appA",duration="1s",labels="tag=user",method="Init",namespace="default",service="svc-a",uin="uin1"} 23
ratelimit_rq_total{appid="appB",duration="5s",labels="tag=order",method="Acquire",namespace="default",service="svc-b",uin="uin2"} 7
ratelimit_rq_total{appid="appOrder",duration="1m0s",labels="region=sh",method="Acquire",namespace="prod",service="order-svc",uin="uin3"} 1290
ratelimit_rq_total{appid="appOrder",duration="1m0s",labels="region=sh",method="Init",namespace="prod",service="order-svc",uin="uin3"} 12
ratelimit_rq_total{appid="appPay",duration="30s",labels="",method="Acquire",namespace="prod",service="payment-svc",uin="uin4"} 1000
```

| 字段 | 值 |
|---|---|
| 类型 | `counter`（单调递增，进程生命周期内不清零） |
| 计算 | `passed + limited`，按 7 维度分别累加 |
| 关系 | `total = pass + limit` |

**示例验证**（svc-a/Acquire 一行）：测试构造 passed=80, limited=20 → `100 = 80 + 20` ✅

### 3.3 `ratelimit_rq_pass`

```
# HELP ratelimit_rq_pass Number of passed ratelimit requests aggregated per minute, labeled by rule key.
# TYPE ratelimit_rq_pass counter
ratelimit_rq_pass{appid="appA",duration="1s",labels="tag=user",method="Acquire",namespace="default",service="svc-a",uin="uin1"} 80
ratelimit_rq_pass{appid="appA",duration="1s",labels="tag=user",method="Init",namespace="default",service="svc-a",uin="uin1"} 23
ratelimit_rq_pass{appid="appB",duration="5s",labels="tag=order",method="Acquire",namespace="default",service="svc-b",uin="uin2"} 5
ratelimit_rq_pass{appid="appOrder",duration="1m0s",labels="region=sh",method="Acquire",namespace="prod",service="order-svc",uin="uin3"} 1234
ratelimit_rq_pass{appid="appOrder",duration="1m0s",labels="region=sh",method="Init",namespace="prod",service="order-svc",uin="uin3"} 12
ratelimit_rq_pass{appid="appPay",duration="30s",labels="",method="Acquire",namespace="prod",service="payment-svc",uin="uin4"} 999
```

| 字段 | 值 |
|---|---|
| 类型 | `counter` |
| 计算 | 单维度的 `passed` 累加值 |
| 输出特点 | 当某周期 `passed=0 && limited=0` 时整个维度被丢弃；只 limited>0 也不会输出 pass=0 行 |

### 3.4 `ratelimit_rq_limit`

```
# HELP ratelimit_rq_limit Number of limited ratelimit requests aggregated per minute, labeled by rule key.
# TYPE ratelimit_rq_limit counter
ratelimit_rq_limit{appid="appA",duration="1s",labels="tag=user",method="Acquire",namespace="default",service="svc-a",uin="uin1"} 20
ratelimit_rq_limit{appid="appB",duration="5s",labels="tag=order",method="Acquire",namespace="default",service="svc-b",uin="uin2"} 2
ratelimit_rq_limit{appid="appOrder",duration="1m0s",labels="region=sh",method="Acquire",namespace="prod",service="order-svc",uin="uin3"} 56
ratelimit_rq_limit{appid="appPay",duration="30s",labels="",method="Acquire",namespace="prod",service="payment-svc",uin="uin4"} 1
```

| 字段 | 值 |
|---|---|
| 类型 | `counter` |
| 计算 | 单维度的 `limited` 累加值 |
| 输出特点 | 本周期 `limited=0` 的维度**不输出 limit 行**（注意不是不输出整个维度——pass/total 还会输出） |

#### 3.5 不对称样本对照表

下面的对照表帮助理解某些维度只有 pass 没有 limit 的情况：

| 维度 | passed | limited | total 行 | pass 行 | limit 行 |
|------|--------|---------|----------|---------|----------|
| `default/svc-a/Acquire/appA/uin1/tag=user/1s` | 80 | 20 | ✅ 100 | ✅ 80 | ✅ 20 |
| `default/svc-a/Init/appA/uin1/tag=user/1s` | 23 | 0 | ✅ 23 | ✅ 23 | ❌ 不输出 |
| `default/svc-b/Acquire/appB/uin2/tag=order/5s` | 5 | 2 | ✅ 7 | ✅ 5 | ✅ 2 |
| `prod/order-svc/Acquire/appOrder/uin3/region=sh/1m0s` | 1234 | 56 | ✅ 1290 | ✅ 1234 | ✅ 56 |
| `prod/order-svc/Init/appOrder/uin3/region=sh/1m0s` | 12 | 0 | ✅ 12 | ✅ 12 | ❌ 不输出 |
| `prod/payment-svc/Acquire/appPay/uin4//30s` | 999 | 1 | ✅ 1000 | ✅ 999 | ✅ 1 |

> monitor 端 `parseLimiterMetrics` 用 (ns, svc) 聚合时，缺失的 limit 行天然 += 0，**不会出错**。

---

## 4. 完整 `/metrics` 输出示例（一次 flush 后）

下面是一次完整的 `/metrics` 响应（按字母序排列，prometheus 客户端默认行为）：

```
# HELP ratelimit_active_streams Number of currently active gRPC streams.
# TYPE ratelimit_active_streams gauge
ratelimit_active_streams 7
# HELP ratelimit_counter_count Number of currently active rate limit counters.
# TYPE ratelimit_counter_count gauge
ratelimit_counter_count 33
# HELP ratelimit_process_avg_us Average gRPC message processing latency in microseconds in the last flush period.
# TYPE ratelimit_process_avg_us gauge
ratelimit_process_avg_us 287.25
# HELP ratelimit_process_max_us Maximum gRPC message processing latency in microseconds in the last flush period.
# TYPE ratelimit_process_max_us gauge
ratelimit_process_max_us 999
# HELP ratelimit_rq_limit Number of limited ratelimit requests aggregated per minute, labeled by rule key.
# TYPE ratelimit_rq_limit counter
ratelimit_rq_limit{appid="appA",duration="1s",labels="tag=user",method="Acquire",namespace="default",service="svc-a",uin="uin1"} 20
ratelimit_rq_limit{appid="appB",duration="5s",labels="tag=order",method="Acquire",namespace="default",service="svc-b",uin="uin2"} 2
ratelimit_rq_limit{appid="appOrder",duration="1m0s",labels="region=sh",method="Acquire",namespace="prod",service="order-svc",uin="uin3"} 56
ratelimit_rq_limit{appid="appPay",duration="30s",labels="",method="Acquire",namespace="prod",service="payment-svc",uin="uin4"} 1
# HELP ratelimit_rq_pass Number of passed ratelimit requests aggregated per minute, labeled by rule key.
# TYPE ratelimit_rq_pass counter
ratelimit_rq_pass{appid="appA",duration="1s",labels="tag=user",method="Acquire",namespace="default",service="svc-a",uin="uin1"} 80
ratelimit_rq_pass{appid="appA",duration="1s",labels="tag=user",method="Init",namespace="default",service="svc-a",uin="uin1"} 23
ratelimit_rq_pass{appid="appB",duration="5s",labels="tag=order",method="Acquire",namespace="default",service="svc-b",uin="uin2"} 5
ratelimit_rq_pass{appid="appOrder",duration="1m0s",labels="region=sh",method="Acquire",namespace="prod",service="order-svc",uin="uin3"} 1234
ratelimit_rq_pass{appid="appOrder",duration="1m0s",labels="region=sh",method="Init",namespace="prod",service="order-svc",uin="uin3"} 12
ratelimit_rq_pass{appid="appPay",duration="30s",labels="",method="Acquire",namespace="prod",service="payment-svc",uin="uin4"} 999
# HELP ratelimit_rq_total Total number of ratelimit acquire requests aggregated per minute, labeled by rule key.
# TYPE ratelimit_rq_total counter
ratelimit_rq_total{appid="appA",duration="1s",labels="tag=user",method="Acquire",namespace="default",service="svc-a",uin="uin1"} 100
ratelimit_rq_total{appid="appA",duration="1s",labels="tag=user",method="Init",namespace="default",service="svc-a",uin="uin1"} 23
ratelimit_rq_total{appid="appB",duration="5s",labels="tag=order",method="Acquire",namespace="default",service="svc-b",uin="uin2"} 7
ratelimit_rq_total{appid="appOrder",duration="1m0s",labels="region=sh",method="Acquire",namespace="prod",service="order-svc",uin="uin3"} 1290
ratelimit_rq_total{appid="appOrder",duration="1m0s",labels="region=sh",method="Init",namespace="prod",service="order-svc",uin="uin3"} 12
ratelimit_rq_total{appid="appPay",duration="30s",labels="",method="Acquire",namespace="prod",service="payment-svc",uin="uin4"} 1000
```

---

## 5. monitor 对接要点

| 项 | limiter 实际行为 | monitor 端处理 |
|---|---|---|
| 输出顺序 | 按 metric 名字母序 | 流式逐行解析，不依赖输出顺序 ✅ |
| label 顺序 | 按 label 名字母序 | `parsePromLabels` map 解析，顺序无关 ✅ |
| 数值类型 | `process_avg_us` 可能是 float（287.25），其他为整数 | `strconv.ParseFloat` 兼容 ✅ |
| 注释行 | `# HELP` / `# TYPE` 前缀 | `strings.HasPrefix("#")` 跳过 ✅ |
| Counter 单调递增 | 是（每次 `Counter.Add(delta)` 不重置） | ⚠️ monitor 直接读取 = 累加值，需做 delta 计算或确认 Barad 是 rate 模型 |
| 多余 label | counter 有 7 维 | monitor 端只取 `namespace, service`，其他自动忽略 ✅ |
| 实例级 gauge 上报 | `process_avg/max` 是无 label gauge | ⚠️ 当前 monitor 把它复制到带 ns/svc 维度的服务级 batch，会重复上报 N 次（P0 待修） |

---

## 6. 复现命令

```bash
# 看真实 /metrics 文本格式：
go test -run TestDumpSampleMetrics -v ./plugin/statis/prometheus/...

# 跑全部 prometheus 插件单测（含 race）：
go test -race -count=1 ./plugin/statis/prometheus/...

# 真实环境查看（启动 limiter 后）：
# 默认 polaris-limiter.yaml 已启用 prometheus 插件，/metrics 在 HTTP :8100 暴露
curl -s http://127.0.0.1:8100/metrics | grep ^ratelimit_
```
