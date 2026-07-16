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
 * Unless required by applicable law or agreed to in writing, software distributed under the License
 * is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and limitations under the License.
 */

package file

import (
	"context"
	"time"

	"github.com/polarismesh/polaris-limiter/pkg/log"
	"github.com/polarismesh/polaris-limiter/plugin"
)

// FileLogger 封装 file 插件的日志组件，供其他 statis 插件（如 prometheus）复用。
//
// 当 polaris-limiter 配置 statis=prometheus 时，仍可以同时启用 file 插件的分类日志
// （event_log / ratelimit_report / precision_log / server_report），通过 prometheus 插件
// 内部持有 FileLogger 实现日志输出，避免 statis 插件只能配一个的限制。
//
// 关键设计：collector 共享
//   - prometheus 创建 collector 后，通过 RegisterCollector 注册到 FileLogger 的 RateLimitCurveReporter
//   - FileLogger 读取 collector 数据时，sharedCollector=true 模式下只读不清零 CurveData
//     （清零由 prometheus flushOnce 负责），避免两者互相吃掉增量
//   - PrecisionData 由 FileLogger 负责清零（prometheus 不读 PrecisionData，无冲突）
type FileLogger struct {
	rateLimitCurveReporter *RateLimitCurveReporter
	serverCurveReporter    *ServerCurveReporter
	eventLogReporter       *EventLogReporter
	logStatHandler         *LogStatHandler
	reportHandler          ReportHandler

	ctx    context.Context
	cancel context.CancelFunc

	// interval 曲线上报的时间间隔（写 ratelimit_report + server_report）
	interval time.Duration
	// precisionInterval 精度上报的时间间隔（写 precision_log）
	precisionInterval time.Duration
}

// NewFileLogger 创建一个 FileLogger 实例。
// sharedCollector=true 时，RateLimitCurveReporter 读取 collector 数据不清零 CurveData
// （由调用方负责清零），用于 prometheus 共享 collector 场景。
func NewFileLogger(cfg *ReportConfig, sharedCollector bool) (*FileLogger, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	fl := &FileLogger{
		rateLimitCurveReporter: NewRateLimitCurveReporter(cfg),
		serverCurveReporter:    NewServerCurveReporter(cfg),
		eventLogReporter:       NewEventLogReporter(cfg),
		reportHandler:          NewReportHandler(cfg),
		logStatHandler:         NewLogStatHandler(cfg),
		interval:               time.Duration(cfg.LogInterval) * time.Second,
		precisionInterval:      time.Duration(cfg.PrecisionLogInterval) * time.Second,
	}
	fl.rateLimitCurveReporter.sharedCollector = sharedCollector
	fl.ctx, fl.cancel = context.WithCancel(context.Background())
	return fl, nil
}

// Start 启动日志 goroutine（precision_log 每秒，ratelimit_report/event_log/server_report 每 interval）
func (f *FileLogger) Start() {
	go func() {
		ticker := time.NewTicker(f.interval)
		precisionTicker := time.NewTicker(f.precisionInterval)
		defer func() {
			ticker.Stop()
			precisionTicker.Stop()
		}()
		for {
			select {
			case <-f.ctx.Done():
				log.Infof("file logger loop stopped")
				return
			case <-precisionTicker.C:
				startTime := time.Now()
				statValues := f.rateLimitCurveReporter.MergeAllStatValues(false)
				total := f.logStatHandler.LogPrecisionRecord(statValues)
				totalTime := time.Since(startTime)
				if total > 0 && totalTime >= 800*time.Millisecond {
					log.Infof("time consume for log precision is %v, item count is %d", totalTime, total)
				}
			case <-ticker.C:
				startTime := time.Now()
				srvRecord := f.serverCurveReporter.BuildReportRecord()
				if srvRecord.HasTags() {
					f.reportHandler.Report(srvRecord)
				}
				totalItemCount := len(srvRecord.Tags)
				// 共享 collector 模式下，限流曲线日志改由外部（prometheus flushOnce）经
				// ReportCurveDeltas 驱动，这里不再自行读取曲线数据——否则与 prometheus 的
				// dump/清零形成双 ticker 竞争，导致相位错位少报。非共享模式仍走本地读取+清零。
				if !f.rateLimitCurveReporter.sharedCollector {
					rateLimitRecord := f.rateLimitCurveReporter.BuildReportRecord()
					if rateLimitRecord.HasTags() {
						f.reportHandler.Report(rateLimitRecord)
					}
					totalItemCount += len(rateLimitRecord.Tags)
				}
				totalTime := time.Since(startTime)
				log.Infof("time consume for report is %v, item count is %d", totalTime, totalItemCount)

				startTime = time.Now()
				total := f.eventLogReporter.LogAllEvents()
				totalTime = time.Since(startTime)
				log.Infof("time consume for log event is %v, item count is %d", totalTime, total)
			}
		}
	}()
	log.Infof("file logger has started (sharedCollector=%v)", f.rateLimitCurveReporter.sharedCollector)
}

// Stop 停止日志 goroutine
func (f *FileLogger) Stop() {
	if f.cancel != nil {
		f.cancel()
	}
}

// RegisterCollector 注册一个 rate limit collector，让 FileLogger 能读取它的数据写日志。
// 用于 prometheus 插件共享 collector 场景。
func (f *FileLogger) RegisterCollector(c plugin.RateLimitStatCollector) {
	f.rateLimitCurveReporter.collectors.Store(c.ID(), c)
}

// DropCollector 归还 collector，移到 droppedCollectors，下个周期 flush 后彻底丢弃。
func (f *FileLogger) DropCollector(c plugin.RateLimitStatCollector) {
	if c == nil {
		return
	}
	// 复用 RateLimitCurveReporter.DropCollector：从 collectors 删除 + 移入 droppedCollectors。
	// 此前只 Store 到 droppedCollectors 而漏了 collectors.Delete，导致已关闭 stream 的
	// collector 永久留在 collectors 中被 precision 路径反复处理且无法 GC。
	f.rateLimitCurveReporter.DropCollector(c)
}

// AddEventToLog 添加事件日志（写 polaris-limiter-event.log）
func (f *FileLogger) AddEventToLog(value plugin.EventToLog) {
	f.eventLogReporter.AddEvent(value)
}

// AddAPICall 添加服务端 API 调用统计（写 polaris-limiter-server-report.log）
func (f *FileLogger) AddAPICall(value plugin.APICallStatValue) {
	f.serverCurveReporter.AddIncrement(value)
}

// CurveDelta 一条限流曲线增量，由外部调用方（prometheus flushOnce）在 dump/清零时同步产出。
// StatValue 仅用于读取不可变的 tag 维度（namespace/service/client_ip/... ），
// Passed/Limited 是本周期已按值取出的增量，不受后续清零影响。
type CurveDelta struct {
	StatValue plugin.RateLimitStatValue
	Passed    int64
	Limited   int64
}

// ReportCurveDeltas 用外部传入的曲线增量写出 ratelimit_report 日志。
//
// 共享 collector 模式下，CurveData 的 dump 与清零统一由 prometheus flushOnce 负责，
// FileLogger 自身的 ticker 不再读取曲线数据（见 Start）。改由本方法接收同一次 dump 的增量，
// 保证 /metrics 与 ratelimit_report.log 基于完全相同的一次读取与清零，不会因双 ticker 相位错位而少报。
func (f *FileLogger) ReportCurveDeltas(deltas []CurveDelta) {
	if len(deltas) == 0 {
		return
	}
	record := f.rateLimitCurveReporter.BuildRecordFromDeltas(deltas)
	if record.HasTags() {
		f.reportHandler.Report(record)
	}
}
