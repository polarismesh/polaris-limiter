/*
 * Tencent is pleased to support the open source community by making polaris-limiter available.
 *
 * Copyright (C) 2021 Tencent. All rights reserved.
 *
 * Licensed under the BSD 3-Clause License (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * https://opensource.org/licenses/BSD-3-Clause
 *
 * Unless required by applicable law or agreed to in writing, software distributed
 * under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR
 * CONDITIONS OF ANY KIND, either express or implied. See the License for the
 * specific language governing permissions and limitations under the License.
 */

// Package prometheus 实现 polaris-limiter 的 prometheus statis 插件。
//
// 插件做两件事：
//  1. 维护 Prometheus 指标注册表（rq_total / rq_pass / rq_limit /
//     process_avg_us / process_max_us / active_streams / counter_count），
//     由 HTTP server 通过 promhttp.Handler() 暴露 /metrics 文本。
//  2. 启动定时刷新 goroutine（对齐分钟边界，每 60s 触发），从所有
//     RateLimitStatCollector 中通过 DumpAndExpire 拿到本周期增量并累加到
//     Counter 指标；从 AddProcessTime 累计的耗时数据计算 avg/max 并写入 Gauge；
//     通过 plugin.GetServerStats 查询活跃 stream / counter 数量。
package prometheus

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/polarismesh/polaris-limiter/pkg/log"
	"github.com/polarismesh/polaris-limiter/plugin"
	"github.com/polarismesh/polaris-limiter/plugin/statis/file"
)

const (
	// PluginName 插件名，配置文件中 plugin.statis.name 设为该值时启用。
	PluginName = "prometheus"

	// flushInterval flush goroutine 触发周期，与 monitor 采集周期一致（60 秒）。
	flushInterval = time.Minute

	// staleFlushCycles series 淘汰阈值：某规则维度连续这么多轮 flush 无增量，
	// 则从 CounterVec 中删除其 series，避免规则 churn 导致基数无界增长。
	// 30 轮 × 60s = 30min 内无流量才淘汰，兼顾 monitor 侧 delta 计算的连续性。
	staleFlushCycles = 30
)

// 插件注册
func init() {
	plugin.RegisterPlugin(PluginName, NewStaticsWorker())
}

// StaticsWorker prometheus statis 插件实现
type StaticsWorker struct {
	// registry 独立的 prometheus 注册表，避免污染默认全局注册表
	registry *prometheus.Registry

	// Counter 类指标（带 namespace/service/method label）
	rqTotal *prometheus.CounterVec
	rqPass  *prometheus.CounterVec
	rqLimit *prometheus.CounterVec

	// Gauge 类指标（实例级，无 label）
	processAvgUs  prometheus.Gauge
	processMaxUs  prometheus.Gauge
	activeStreams prometheus.Gauge
	counterCount  prometheus.Gauge

	// 处理耗时累计（每个 flush 周期重置）。
	// 用 atomic 而非 mutex：AddProcessTime 在每条 gRPC 消息的 postProcess 热路径上调用，
	// 单锁会成为高 QPS 下的争用点；total/count 用原子加，max 用 CAS 循环。
	processTotal atomic.Int64
	processMax   atomic.Int64
	processCount atomic.Int64

	// 活跃 collector 列表
	collectorsMu sync.RWMutex
	collectors   map[string]plugin.RateLimitStatCollector

	// 已被 drop 但尚未结算的 collector，flush 后清空
	droppedMu sync.Mutex
	dropped   map[string]plugin.RateLimitStatCollector

	// 生命周期
	ctx    context.Context
	cancel context.CancelFunc

	// fileLogger 可选的 file 插件日志组件（配置了 file_log 才初始化）
	// 用于同时输出 event_log / ratelimit_report / precision_log / server_report 分类日志
	// collector 共享 prometheus 创建的实例，fileLogger 只读不清零 CurveData（由 flushOnce 清零）
	fileLogger *file.FileLogger

	// flushCycle 单调递增的 flush 轮次序号，配合 lastSeen 做 series TTL 淘汰。
	// 仅在 flushOnce（单 goroutine）内读写，无需加锁。
	flushCycle int64
	// lastSeen 记录每个规则维度最近一次有增量的 flush 轮次。
	// Counter series 一旦创建便常驻内存，规则频繁变更（churn）时会单调膨胀；
	// 连续 staleFlushCycles 轮无增量则从 3 个 CounterVec 中 Delete，收敛基数与内存。
	// 仅在 flushOnce 内访问，无需加锁。
	lastSeen map[dimensionKey]int64
}

// NewStaticsWorker 构造一个 prometheus 插件实例（init 与单元测试共用）。
func NewStaticsWorker() *StaticsWorker {
	reg := prometheus.NewRegistry()
	// 三个 counter 共享的细粒度规则维度：与 RateLimitStatCounterKeyV1 对齐
	// （client_ip 维度变化大，且文档要求不进入 monitor 上报，故不暴露 label，仅保留实例级聚合）。
	rqLabels := []string{"namespace", "service", "method", "appid", "uin", "labels", "duration"}
	w := &StaticsWorker{
		registry: reg,
		rqTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ratelimit_rq_total",
			Help: "Total number of ratelimit acquire requests aggregated per minute, labeled by rule key.",
		}, rqLabels),
		rqPass: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ratelimit_rq_pass",
			Help: "Number of passed ratelimit requests aggregated per minute, labeled by rule key.",
		}, rqLabels),
		rqLimit: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ratelimit_rq_limit",
			Help: "Number of limited ratelimit requests aggregated per minute, labeled by rule key.",
		}, rqLabels),
		processAvgUs: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ratelimit_process_avg_us",
			Help: "Average gRPC message processing latency in microseconds in the last flush period.",
		}),
		processMaxUs: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ratelimit_process_max_us",
			Help: "Maximum gRPC message processing latency in microseconds in the last flush period.",
		}),
		activeStreams: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ratelimit_active_streams",
			Help: "Number of currently active gRPC streams.",
		}),
		counterCount: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ratelimit_counter_count",
			Help: "Number of currently active rate limit counters.",
		}),
		collectors: make(map[string]plugin.RateLimitStatCollector),
		dropped:    make(map[string]plugin.RateLimitStatCollector),
		lastSeen:   make(map[dimensionKey]int64),
	}
	reg.MustRegister(w.rqTotal, w.rqPass, w.rqLimit,
		w.processAvgUs, w.processMaxUs, w.activeStreams, w.counterCount)
	return w
}

// Name 返回插件名
func (s *StaticsWorker) Name() string {
	return PluginName
}

// Registry 返回 prometheus 注册表（供 HTTP server 挂载 /metrics handler）
func (s *StaticsWorker) Registry() *prometheus.Registry {
	return s.registry
}

// Initialize 初始化插件并启动 flush goroutine。
// 如果配置了 option.file_log，会同时初始化 file 插件的分类日志组件，
// 写出 event_log / ratelimit_report / precision_log / server_report 4 个日志文件。
// file_log 属可选增强：解析/校验失败时仅告警降级（禁用分类日志），不影响 /metrics 主路径，
// 避免一处日志配置错误拖垮整个指标暴露（遵循 config.md：可选字段缺失/非法应安全降级）。
func (s *StaticsWorker) Initialize(conf *plugin.ConfigEntry) error {
	s.ctx, s.cancel = context.WithCancel(context.Background())
	go s.flushLoop(s.ctx)

	// 可选：初始化 file 分类日志
	if conf != nil && len(conf.Option) > 0 {
		if rawFileLog, ok := conf.Option["file_log"]; ok {
			if err := s.initFileLogger(rawFileLog); err != nil {
				log.Warn("prometheus statis plugin: init file_log failed, classified logs disabled",
					zap.Error(err))
			}
		}
	}

	log.Info("prometheus statis plugin initialized",
		zap.Bool("fileLoggerEnabled", s.fileLogger != nil))
	return nil
}

// initFileLogger 从配置解析 file_log 字段，初始化 FileLogger 并启动日志 goroutine。
// file_log 配置格式与 file 插件的 option 一致（ratelimit_report_log_path 等）。
func (s *StaticsWorker) initFileLogger(rawFileLog interface{}) error {
	// YAML 解析嵌套 map 会产生 map[interface{}]interface{}，json.Marshal 不支持，
	// 需要先递归转成 map[string]interface{}
	normalized := normalizeYAMLMap(rawFileLog)
	text, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	reportConf := &file.ReportConfig{}
	if err := json.Unmarshal(text, reportConf); err != nil {
		return err
	}
	// sharedCollector=true：fileLogger 读取 collector 的 CurveData 时不清零
	// （清零由 prometheus flushOnce 负责），避免两者互相吃掉增量
	fl, err := file.NewFileLogger(reportConf, true)
	if err != nil {
		return err
	}
	fl.Start()
	s.fileLogger = fl
	return nil
}

// normalizeYAMLMap 递归把 YAML 解析产生的 map[interface{}]interface{} 转成
// map[string]interface{}，让 json.Marshal 能处理。数组和嵌套 map 递归处理。
func normalizeYAMLMap(v interface{}) interface{} {
	switch x := v.(type) {
	case map[interface{}]interface{}:
		m := make(map[string]interface{}, len(x))
		for k, val := range x {
			ks, ok := k.(string)
			if !ok {
				continue
			}
			m[ks] = normalizeYAMLMap(val)
		}
		return m
	case map[string]interface{}:
		m := make(map[string]interface{}, len(x))
		for k, val := range x {
			m[k] = normalizeYAMLMap(val)
		}
		return m
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, val := range x {
			out[i] = normalizeYAMLMap(val)
		}
		return out
	default:
		return v
	}
}

// Destroy 停止 flush goroutine 和 fileLogger goroutine
func (s *StaticsWorker) Destroy() error {
	if s.fileLogger != nil {
		s.fileLogger.Stop()
	}
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

// CreateRateLimitStatCollectorV1 创建 V1 采集器
func (s *StaticsWorker) CreateRateLimitStatCollectorV1() *plugin.RateLimitStatCollectorV1 {
	c := plugin.NewRateLimitStatCollectorV1()
	s.addCollector(c)
	if s.fileLogger != nil {
		s.fileLogger.RegisterCollector(c)
	}
	return c
}

// CreateRateLimitStatCollectorV2 创建 V2 采集器
func (s *StaticsWorker) CreateRateLimitStatCollectorV2() *plugin.RateLimitStatCollectorV2 {
	c := plugin.NewRateLimitStatCollectorV2()
	s.addCollector(c)
	if s.fileLogger != nil {
		s.fileLogger.RegisterCollector(c)
	}
	return c
}

// DropRateLimitStatCollector 归还采集器，移到 dropped 列表，下个周期 flush 后彻底丢弃
func (s *StaticsWorker) DropRateLimitStatCollector(c plugin.RateLimitStatCollector) {
	if c == nil {
		return
	}
	s.collectorsMu.Lock()
	delete(s.collectors, c.ID())
	s.collectorsMu.Unlock()

	s.droppedMu.Lock()
	s.dropped[c.ID()] = c
	s.droppedMu.Unlock()

	if s.fileLogger != nil {
		s.fileLogger.DropCollector(c)
	}
}

// AddAPICall 转发给 fileLogger 写 server_report 日志（prometheus 自身不消费此数据）
func (s *StaticsWorker) AddAPICall(value plugin.APICallStatValue) {
	if s.fileLogger != nil {
		s.fileLogger.AddAPICall(value)
	}
}

// AddEventToLog 转发给 fileLogger 写 event_log 日志（prometheus 自身不消费此数据）
func (s *StaticsWorker) AddEventToLog(value plugin.EventToLog) {
	if s.fileLogger != nil {
		s.fileLogger.AddEventToLog(value)
	}
}

// AddProcessTime 累计单次 gRPC 消息处理耗时
func (s *StaticsWorker) AddProcessTime(us int64) {
	if us < 0 {
		return
	}
	s.processTotal.Add(us)
	s.processCount.Add(1)
	// CAS 更新 max：仅当本次样本更大时循环重试写入
	for {
		old := s.processMax.Load()
		if us <= old {
			break
		}
		if s.processMax.CompareAndSwap(old, us) {
			break
		}
	}
}

// addCollector 注册新采集器
func (s *StaticsWorker) addCollector(c plugin.RateLimitStatCollector) {
	s.collectorsMu.Lock()
	s.collectors[c.ID()] = c
	s.collectorsMu.Unlock()
}

// snapshotCollectors 拷贝当前活跃 collector 列表，避免 flush 时长时间持锁
func (s *StaticsWorker) snapshotCollectors() []plugin.RateLimitStatCollector {
	s.collectorsMu.RLock()
	out := make([]plugin.RateLimitStatCollector, 0, len(s.collectors))
	for _, c := range s.collectors {
		out = append(out, c)
	}
	s.collectorsMu.RUnlock()
	return out
}

// drainDropped 取出并清空 dropped 列表
func (s *StaticsWorker) drainDropped() []plugin.RateLimitStatCollector {
	s.droppedMu.Lock()
	out := make([]plugin.RateLimitStatCollector, 0, len(s.dropped))
	for _, c := range s.dropped {
		out = append(out, c)
	}
	s.dropped = make(map[string]plugin.RateLimitStatCollector)
	s.droppedMu.Unlock()
	return out
}

// resetProcessStats 读取并重置周期累计耗时数据。
// 单一 flush goroutine 调用，用 Swap(0) 原子取值并清零；与 AddProcessTime 的原子写并发安全。
// 三个字段分别 Swap 之间可能有极小的样本跨窗口偏移（一次采样落到下一周期），对 avg/max 统计无实质影响。
func (s *StaticsWorker) resetProcessStats() (total, max, count int64) {
	total = s.processTotal.Swap(0)
	max = s.processMax.Swap(0)
	count = s.processCount.Swap(0)
	return
}

// flushLoop 对齐到分钟边界后每 60s 触发一次 flush
func (s *StaticsWorker) flushLoop(ctx context.Context) {
	// 对齐到下一个分钟整点
	now := time.Now()
	next := now.Truncate(flushInterval).Add(flushInterval)
	timer := time.NewTimer(next.Sub(now))
	defer timer.Stop()

	select {
	case <-ctx.Done():
		log.Info("prometheus flush loop stopped before first tick")
		return
	case <-timer.C:
	}
	s.flushOnce()

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("prometheus flush loop stopped")
			return
		case <-ticker.C:
			s.flushOnce()
		}
	}
}

// dimensionKey 限流规则维度（与 RateLimitStatCounterKeyV1 对齐）。
// client_ip 维度变化大，不暴露 label，仅用于实例级聚合。
type dimensionKey struct {
	Namespace string
	Service   string
	Method    string
	AppId     string
	Uin       string
	Labels    string
	Duration  string // 用 string 形态承载 time.Duration，避免 prometheus label 类型不一致
}

type dimensionStats struct {
	Passed  int64
	Limited int64
}

// toLabels 把维度转成 prometheus label map（Add / Delete series 共用）。
func (k dimensionKey) toLabels() prometheus.Labels {
	return prometheus.Labels{
		"namespace": k.Namespace,
		"service":   k.Service,
		"method":    k.Method,
		"appid":     k.AppId,
		"uin":       k.Uin,
		"labels":    k.Labels,
		"duration":  k.Duration,
	}
}

// evictStaleSeries 删除连续 staleFlushCycles 轮无增量的规则维度对应的 Counter series。
// 仅在 flushOnce（单 goroutine）内调用，直接操作 lastSeen 无需加锁。
func (s *StaticsWorker) evictStaleSeries() {
	for k, seen := range s.lastSeen {
		if s.flushCycle-seen < staleFlushCycles {
			continue
		}
		labels := k.toLabels()
		s.rqTotal.Delete(labels)
		s.rqPass.Delete(labels)
		s.rqLimit.Delete(labels)
		delete(s.lastSeen, k)
	}
}

// flushOnce 完成一次 prometheus 指标刷新
func (s *StaticsWorker) flushOnce() {
	defer func() {
		if r := recover(); r != nil {
			log.Error("prometheus flush panic", zap.Any("panic", r))
		}
	}()

	// Step A：聚合所有 collector 增量
	s.flushCycle++
	agg := make(map[dimensionKey]*dimensionStats)
	// curveDeltas 收集本次 dump 的曲线增量，用于共享模式下驱动 file 曲线日志，
	// 保证 /metrics 与 ratelimit_report.log 基于同一次 dump/清零（nil 时不分配）。
	var curveDeltas []file.CurveDelta
	collectors := append(s.snapshotCollectors(), s.drainDropped()...)
	var valueBuf []plugin.RateLimitStatValue
	for _, collector := range collectors {
		var count int
		valueBuf, count = collector.DumpAndExpire(valueBuf, true /* isCurve, 同时清零 */)
		for i := 0; i < count; i++ {
			v := valueBuf[i]
			curve := v.GetCurveData()
			passed := curve.GetPassed()
			limited := curve.GetLimited()
			// 清零 collector 内的累计值，确保下一周期为增量
			if passed != 0 {
				curve.AddPassed(-passed)
			}
			if limited != 0 {
				curve.AddLimited(-limited)
			}
			if passed == 0 && limited == 0 {
				continue
			}
			// v 持有 collector 内活跃对象的指针（非 valueBuf 槽位），后续复用 valueBuf 不影响；
			// tag 维度创建后不可变，passed/limited 已按值取出，共享模式下转发给 file 曲线日志。
			if s.fileLogger != nil {
				curveDeltas = append(curveDeltas, file.CurveDelta{
					StatValue: v, Passed: passed, Limited: limited,
				})
			}
			key := dimensionKey{
				Namespace: v.GetNamespace(),
				Service:   v.GetService(),
				Method:    v.GetMethod(),
				AppId:     v.GetAppId(),
				Uin:       v.GetUin(),
				Labels:    v.GetLabels(),
				Duration:  v.GetDuration().String(),
			}
			st, ok := agg[key]
			if !ok {
				st = &dimensionStats{}
				agg[key] = st
			}
			st.Passed += passed
			st.Limited += limited
		}
	}
	for k, st := range agg {
		labels := k.toLabels()
		if st.Passed > 0 {
			s.rqPass.With(labels).Add(float64(st.Passed))
		}
		if st.Limited > 0 {
			s.rqLimit.With(labels).Add(float64(st.Limited))
		}
		total := st.Passed + st.Limited
		if total > 0 {
			s.rqTotal.With(labels).Add(float64(total))
		}
		// 本轮有增量，刷新 last-active 轮次
		s.lastSeen[k] = s.flushCycle
	}

	// Step A2：淘汰陈旧 series，收敛基数。连续 staleFlushCycles 轮无增量的维度，
	// 从三个 CounterVec 中删除其 series 并移出 lastSeen，避免规则 churn 下内存单调增长。
	s.evictStaleSeries()

	// Step A3：共享模式下，用本次 dump 的同一份曲线增量驱动 file 曲线日志，
	// 避免 file 自身 ticker 只读不清零导致的相位错位少报。
	if s.fileLogger != nil {
		s.fileLogger.ReportCurveDeltas(curveDeltas)
	}

	// Step B：处理耗时
	total, max, count := s.resetProcessStats()
	if count > 0 {
		s.processAvgUs.Set(float64(total) / float64(count))
		s.processMaxUs.Set(float64(max))
	} else {
		s.processAvgUs.Set(0)
		s.processMaxUs.Set(0)
	}

	// Step C：实例级状态。
	// active_streams 直接取本插件持有的活跃 collector 数：每个 gRPC Service() stream 建立时
	// CreateRateLimitStatCollectorV2 恰好创建一个 collector、stream 结束时 DropRateLimitStatCollector 归还，
	// 故活跃 collector 数 == 真实活跃 stream 数（不像 ClientCount 会把单 client 多路复用低报）。
	// counter_count 仍取 server 侧注入的活跃计数器数。
	_, counters := plugin.GetServerStats()
	s.activeStreams.Set(float64(s.activeStreamCount()))
	s.counterCount.Set(float64(counters))
}

// activeStreamCount 返回当前活跃 collector 数，等价于活跃 gRPC stream 数。
func (s *StaticsWorker) activeStreamCount() int {
	s.collectorsMu.RLock()
	n := len(s.collectors)
	s.collectorsMu.RUnlock()
	return n
}
