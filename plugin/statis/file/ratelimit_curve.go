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

package file

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/polarismesh/polaris-limiter/pkg/log"
	"github.com/polarismesh/polaris-limiter/pkg/utils"
	"github.com/polarismesh/polaris-limiter/plugin"
)

// RateLimitCurveReporter 限流曲线上报
type RateLimitCurveReporter struct {
	// appName 上报监控的应用名
	appName string
	// collectors 采集器列表
	collectors *sync.Map
	// droppedCollectors 被丢弃，没有来得及上报的
	droppedCollectors *sync.Map
	// sharedCollector 共享 collector 模式：只读不清零 CurveData（清零由外部调用方负责）
	// 用于 prometheus 插件共享 collector 场景，避免两者互相吃掉增量
	// PrecisionData 仍由本 reporter 清零（prometheus 不读 PrecisionData，无冲突）
	sharedCollector bool
}

// NewRateLimitCurveReporter 创建曲线上报系统
func NewRateLimitCurveReporter(config *ReportConfig) *RateLimitCurveReporter {
	reporter := &RateLimitCurveReporter{}
	reporter.appName = config.RateLimitAppName
	reporter.collectors = &sync.Map{}
	reporter.droppedCollectors = &sync.Map{}
	log.Infof("succeed to init rateLimitCurveReporter, appName %s", reporter.appName)
	return reporter
}

// fetchRateLimitData 获取限流数据
func fetchRateLimitData(statValue plugin.RateLimitStatValue, isCurve bool) plugin.RateLimitData {
	if isCurve {
		return statValue.GetCurveData()
	}
	return statValue.GetPrecisionData()
}

// 判断本次是否需要处理该统计项
func needProcess(isCurve bool, rateLimitData plugin.RateLimitData, curTimeMs int64, duration time.Duration) bool {
	if isCurve {
		return true
	}
	// 对于精度统计，必须是一个完整的统计项，否则精度会存在问题
	lastFetchTimeMs := rateLimitData.GetLastFetchTime()
	timePassed := curTimeMs - lastFetchTimeMs
	return timePassed >= duration.Milliseconds()
}

// 处理采集器的数据
func (s *RateLimitCurveReporter) processCollector(statValues map[interface{}]plugin.RateLimitStatValue,
	collector plugin.RateLimitStatCollector, statValueSlice []plugin.RateLimitStatValue,
	isCurve bool) []plugin.RateLimitStatValue {
	var count int
	// sharedCollector 模式下，collector 的值过期（删除）也交由外部调用方（prometheus flushOnce）负责；
	// 否则 fileLogger 的 60s report 会先把未上报给 /metrics 的值 expire 掉，与 prometheus flush 竞争，
	// 导致部分流量永远不进 /metrics（实测 GlobalRatelimitEchoServer 首批流量被 fileLogger 抢先 expire）。
	// 非共享模式（独立 file 插件）行为不变：report 路径仍按 isCurve 过期。
	statValueSlice, count = collector.DumpAndExpire(statValueSlice, isCurve && !s.sharedCollector)
	if count == 0 {
		return statValueSlice
	}
	var keyCounterOnly = !isCurve
	// sharedCollector + isCurve 模式下，CurveData 的清零由外部调用方（prometheus flushOnce）负责，
	// 这里只读不清零，避免互相吃掉增量。PrecisionData（isCurve=false）仍由本方法清零。
	clearCurve := !s.sharedCollector || !isCurve
	for i := 0; i < count; i++ {
		statValue := statValueSlice[i]
		statValueLimitData := fetchRateLimitData(statValue, isCurve)
		curTimeMs := utils.CurrentMillisecond()
		if !needProcess(isCurve, statValueLimitData, curTimeMs, statValue.GetDuration()) {
			continue
		}
		statKey := statValue.GetStatKey(keyCounterOnly)
		statValueLimitData.SetLastFetchTime(curTimeMs)
		if existsStatValue, ok := statValues[statKey]; ok {
			passed := statValueLimitData.GetPassed()
			limited := statValueLimitData.GetLimited()
			if clearCurve {
				statValueLimitData.AddPassed(0 - passed)
				statValueLimitData.AddLimited(0 - limited)
			}
			existsRateLimitData := fetchRateLimitData(existsStatValue, isCurve)
			existsRateLimitData.AddPassed(passed)
			existsRateLimitData.AddLimited(limited)
		} else {
			existsStatValue := statValue.Clone()
			statValues[statKey] = existsStatValue
			existsRateLimitData := fetchRateLimitData(existsStatValue, isCurve)
			passed := existsRateLimitData.GetPassed()
			limited := existsRateLimitData.GetLimited()
			if clearCurve {
				statValueLimitData.AddPassed(0 - passed)
				statValueLimitData.AddLimited(0 - limited)
			}
		}
	}
	return statValueSlice
}

// BuildReportRecord 构建上报记录
func (s *RateLimitCurveReporter) BuildReportRecord() *ReportRecord {
	record := &ReportRecord{
		AppName: s.appName,
	}
	statValues := s.MergeAllStatValues(true)
	for _, v := range statValues {
		record.Tags = append(record.Tags, &ReportItem{
			TagStr:   s.GetTagStr(v),
			ValueStr: s.GetValueStr(v),
		})
	}
	return record
}

// MergeAllStatValues 汇总所有的统计数据
func (s *RateLimitCurveReporter) MergeAllStatValues(isCurve bool) map[interface{}]plugin.RateLimitStatValue {
	var statValuesSlice []plugin.RateLimitStatValue
	var statValues = make(map[interface{}]plugin.RateLimitStatValue)
	s.collectors.Range(func(key, value interface{}) bool {
		collector := value.(plugin.RateLimitStatCollector)
		statValuesSlice = s.processCollector(statValues, collector, statValuesSlice, isCurve)
		return true
	})
	s.droppedCollectors.Range(func(key, value interface{}) bool {
		collector := value.(plugin.RateLimitStatCollector)
		statValuesSlice = s.processCollector(statValues, collector, statValuesSlice, isCurve)
		// 非共享模式：曲线路径（isCurve=true）读完曲线数据后彻底删除。
		// 共享模式：曲线增量由外部（prometheus flushOnce）用其自身的 dropped 列表结算，
		//   本 reporter 的曲线路径（BuildReportRecord）不会被调用，故改由每秒的 precision
		//   路径（isCurve=false）读一次精度数据后删除，否则 dropped collector 永不清理导致
		//   内存泄漏。删除本 map 的 entry 不影响 prometheus.dropped 中同一 collector 指针，
		//   曲线增量仍能被后续 flushOnce 正确结算。
		if isCurve || s.sharedCollector {
			s.droppedCollectors.Delete(key)
		}
		return true
	})
	return statValues
}

const (
	rateLimitTagStrPattern = "namespace=%s&service=%s&method=%s" +
		"&appid=%s&uin=%s&labels=%s&client_ip=%s&duration=%s&limit_service=%s"
	rateLimitValueStrPattern = "limit_count=%d&quota_count=%d"
)

// GetTagStr 上报的Tag字符串
func (s *RateLimitCurveReporter) GetTagStr(value plugin.RateLimitStatValue) string {
	tagBuilder := strings.Builder{}
	tagBuilder.WriteString(fmt.Sprintf(rateLimitTagStrPattern, value.GetNamespace(), value.GetService(),
		value.GetMethod(), value.GetAppId(), value.GetUin(), value.GetLabels(),
		value.GetClientIPStr(), value.GetDuration(), utils.LimitServiceName))
	return tagBuilder.String()
}

// GetValueStr 上报的数据值
func (s *RateLimitCurveReporter) GetValueStr(value plugin.RateLimitStatValue) string {
	reportLimitedValue := value.GetCurveData().GetLimited()
	value.GetCurveData().AddLimited(0 - reportLimitedValue)
	reportPassedValue := value.GetCurveData().GetPassed()
	value.GetCurveData().AddPassed(0 - reportPassedValue)
	return fmt.Sprintf(rateLimitValueStrPattern, reportLimitedValue, reportPassedValue)
}

// buildValueStr 按给定的 limited/passed 增量格式化上报值（供 delta 驱动路径复用，不触碰 CurveData）。
func buildValueStr(limited, passed int64) string {
	return fmt.Sprintf(rateLimitValueStrPattern, limited, passed)
}

// BuildRecordFromDeltas 用外部传入的曲线增量构建上报记录（共享 collector 模式）。
//
// 与 BuildReportRecord 的区别：数据来自 prometheus flushOnce 同一次 dump 的增量，
// 而非本 reporter 读取 collector。按 statKey（含 client_ip，与本地曲线日志维度一致）聚合，
// 保证同一 (counterKey, client_ip) 的多次增量合并为一行。
func (s *RateLimitCurveReporter) BuildRecordFromDeltas(deltas []CurveDelta) *ReportRecord {
	record := &ReportRecord{AppName: s.appName}
	// 按 statKey 聚合同维度增量（含 client_ip），并保留一个样本用于取 tag。
	type agg struct {
		sample  plugin.RateLimitStatValue
		passed  int64
		limited int64
	}
	merged := make(map[interface{}]*agg, len(deltas))
	order := make([]interface{}, 0, len(deltas))
	for i := range deltas {
		d := deltas[i]
		if d.StatValue == nil || (d.Passed == 0 && d.Limited == 0) {
			continue
		}
		key := d.StatValue.GetStatKey(false)
		a, ok := merged[key]
		if !ok {
			a = &agg{sample: d.StatValue}
			merged[key] = a
			order = append(order, key)
		}
		a.passed += d.Passed
		a.limited += d.Limited
	}
	for _, key := range order {
		a := merged[key]
		record.Tags = append(record.Tags, &ReportItem{
			TagStr:   s.GetTagStr(a.sample),
			ValueStr: buildValueStr(a.limited, a.passed),
		})
	}
	return record
}

// CreateCollectorV2 创建采集器V2
func (s *RateLimitCurveReporter) CreateCollectorV2() *plugin.RateLimitStatCollectorV2 {
	collectorV2 := plugin.NewRateLimitStatCollectorV2()
	s.collectors.Store(collectorV2.ID(), collectorV2)
	return collectorV2
}

// CreateCollectorV1 创建采集器V2
func (s *RateLimitCurveReporter) CreateCollectorV1() *plugin.RateLimitStatCollectorV1 {
	collectorV1 := plugin.NewRateLimitStatCollectorV1()
	s.collectors.Store(collectorV1.ID(), collectorV1)
	return collectorV1
}

// DropCollector 创建采集器V2
func (s *RateLimitCurveReporter) DropCollector(collector plugin.RateLimitStatCollector) {
	s.collectors.Delete(collector.ID())
	s.droppedCollectors.Store(collector.ID(), collector)
}
