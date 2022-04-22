/**
 * Tencent is pleased to support the open source community by making Polaris available.
 *
 * Copyright (C) 2019 THL A29 Limited, a Tencent company. All rights reserved.
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
	statValueSlice, count = collector.DumpAndExpire(statValueSlice, isCurve)
	if count == 0 {
		return statValueSlice
	}
	var keyCounterOnly = !isCurve
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
			statValueLimitData.AddPassed(0 - passed)
			statValueLimitData.AddLimited(0 - limited)
			existsRateLimitData := fetchRateLimitData(existsStatValue, isCurve)
			existsRateLimitData.AddPassed(passed)
			existsRateLimitData.AddLimited(limited)
		} else {
			existsStatValue := statValue.Clone()
			statValues[statKey] = existsStatValue
			existsRateLimitData := fetchRateLimitData(existsStatValue, isCurve)
			passed := existsRateLimitData.GetPassed()
			limited := existsRateLimitData.GetLimited()
			statValueLimitData.AddPassed(0 - passed)
			statValueLimitData.AddLimited(0 - limited)
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
		if isCurve {
			// 处理完就彻底删除
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
