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

	"github.com/polarismesh/polaris-limit/pkg/log"
	"github.com/polarismesh/polaris-limit/pkg/utils"
	"github.com/polarismesh/polaris-limit/plugin"
)

//限流曲线上报
type ServerCurveReporter struct {
	mutex *sync.Mutex
	//收集的数据
	collections *sync.Map
	//曲线上报的时间间隔
	interval time.Duration
	//上报监控的应用名
	appName string
}

//创建曲线上报系统
func NewServerCurveReporter(config *ReportConfig) *ServerCurveReporter {
	reporter := &ServerCurveReporter{}
	reporter.mutex = &sync.Mutex{}
	reporter.interval = time.Duration(config.LogInterval) * time.Second
	reporter.appName = config.ServerAppName
	reporter.collections = &sync.Map{}
	log.Infof("succeed to init serverCurveReporter, interval=%v", reporter.interval)
	return reporter
}

//获取统计窗口
func (s *ServerCurveReporter) getAndCreateStoreValue(
	curValue plugin.APICallStatValue) (plugin.APICallStatValue, bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	storeValue, exists := s.collections.Load(curValue.GetStatKey())
	if exists {
		return storeValue.(plugin.APICallStatValue), true
	}
	savedValuePtr := curValue.Clone()
	s.collections.Store(savedValuePtr.GetStatKey(), savedValuePtr)
	return savedValuePtr, false
}

//添加增量数据
func (s *ServerCurveReporter) AddIncrement(apiCallStatValue plugin.APICallStatValue) {
	var storeValue plugin.APICallStatValue
	now := time.Now().UnixNano()
	storeValueIntf, exists := s.collections.Load(apiCallStatValue.GetStatKey())
	if exists {
		storeValue = storeValueIntf.(plugin.APICallStatValue)
	} else {
		storeValue, exists = s.getAndCreateStoreValue(apiCallStatValue)
	}
	if exists {
		storeValue.AddReqCount(1)
		storeValue.AddLatency(apiCallStatValue.GetLatency())
		storeValue.CasMaxLatency(apiCallStatValue.GetLatency())
	}
	storeValue.SetLastUpdateTime(now)
}

//构建上报记录
func (s *ServerCurveReporter) BuildReportRecord() *ReportRecord {
	record := &ReportRecord{
		AppName: s.appName,
	}
	startTime := time.Now()
	nowNano := startTime.UnixNano()
	s.collections.Range(func(key, value interface{}) bool {
		storeValue := value.(plugin.APICallStatValue)
		tagStr := s.GetTagStr(storeValue)
		if storeValue.GetReqCount() == 0 {
			if nowNano-storeValue.GetLastUpdateTime() >= 2*s.interval.Nanoseconds() {
				log.Infof("server report item %s expired", tagStr)
				s.collections.Delete(storeValue.GetStatKey())
			}
			return true
		}
		valueStr := s.GetValueStr(storeValue)
		record.Tags = append(record.Tags, &ReportItem{
			TagStr:   tagStr,
			ValueStr: valueStr,
		})
		return true
	})
	return record
}

const (
	ServerTagStrPattern  = "inf=%s&err_code=%d&duration=%s&msg_type=%s&limit_service=%s"
	ServerTagValuePatter = "latency.max.interface=%d&latency.avg.interface=%d&count.network_err=%d" +
		"&count.system_err=%d&count.user_err=%d&count.success=%d&count.total=%d"
)

//上报的Tag字符串
func (s *ServerCurveReporter) GetTagStr(value plugin.APICallStatValue) string {
	tagBuilder := strings.Builder{}
	tagBuilder.WriteString(fmt.Sprintf(
		ServerTagStrPattern, value.GetAPIName(), value.GetCode(), value.GetDuration(), value.GetMsgType(),
		utils.LimitServiceName))
	return tagBuilder.String()
}

//上报的数据值
func (s *ServerCurveReporter) GetValueStr(value plugin.APICallStatValue) string {
	reqCount := value.GetReqCount()
	value.AddReqCount(0 - reqCount)
	latencyTotal := value.GetLatency()
	value.AddLatency(0 - latencyTotal)
	maxLatency := value.GetMaxLatency()
	value.ResetMaxLatency(maxLatency)
	latencyAvg := latencyTotal / int64(reqCount)
	var reqCountNetworkErr int32 = 0
	var reqCountUserErr int32 = 0
	var reqCountSysErr int32 = 0
	var reqCountSuccess int32 = 0
	code := value.GetCode()
	if utils.IsSuccess(code) {
		reqCountSuccess = reqCount
	} else if utils.IsSysErr(code) {
		reqCountSysErr = reqCount
	} else if utils.IsUserErr(code) {
		reqCountUserErr = reqCount
	}
	return fmt.Sprintf(ServerTagValuePatter, maxLatency, latencyAvg, reqCountNetworkErr,
		reqCountSysErr, reqCountUserErr, reqCountSuccess, reqCount)
}
