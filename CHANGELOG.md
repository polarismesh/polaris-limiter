# polaris limit change log

## [Unreleased]

### 修改的特性

- 默认 `plugin.statis` 插件由 `file` 改为 `prometheus`：limiter 启动后开箱即在 HTTP `/metrics` 暴露 `ratelimit_rq_total`/`ratelimit_rq_pass`/`ratelimit_rq_limit`/`ratelimit_process_avg_us`/`ratelimit_process_max_us`/`ratelimit_active_streams`/`ratelimit_counter_count` 7 个指标；同时通过 `option.file_log` 复用 file 插件分类日志组件，仍写出 ratelimit-report/event/stat/server-report 4 个日志。原纯 `file` 配置改为注释示例。升级注意：若旧配置未显式指定 `plugin.statis.name`，升级后 HTTP `:8100/metrics` 会变为可访问端点（原仅写分类日志）。

### 修复的BUG

- 修复 prometheus statis 插件共享 collector 模式下，file 插件 60s report 路径抢先 expire 共享 collector 的过期值，导致部分限流流量被写进 ratelimit-report 日志后从 `/metrics` 消失的竞态。`plugin/statis/file/ratelimit_curve.go` 在 `sharedCollector` 模式下不再 expire 值，值过期与 CurveData 清零统一交由 prometheus 60s flush 负责。

## [0.4.8] - 2021-12-2

### 添加的特性

- 支持批量初始化接口

## [0.4.7] - 2021-09-26

### 修改的特性

- 添加健康检查开启开关支持开启自注册时关闭健康检查
- 修改GRPC日志配置默认值为关闭

### 修复的BUG

- 修复智研上报日志配置校验时将重置日志配置的问题
