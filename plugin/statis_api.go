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

package plugin

import (
	"sync"
)

var (
	statisOnce = &sync.Once{}

	// serverStatsProvider 由上层注入，用于 statis 插件在 flush 时查询活跃 stream / 计数器数量。
	// 避免 plugin 包反向依赖 ratelimitv2 包。
	serverStatsProvider     func() (activeStreams int, counterCount int)
	serverStatsProviderLock sync.RWMutex
)

// SetServerStatsProvider 注入实例级状态查询函数（活跃 stream 数、活跃计数器数）。
// 由 bootstrap 在 ratelimitv2.Initialize 完成后调用。允许传 nil 解除注入。
func SetServerStatsProvider(p func() (activeStreams int, counterCount int)) {
	serverStatsProviderLock.Lock()
	defer serverStatsProviderLock.Unlock()
	serverStatsProvider = p
}

// GetServerStats 查询当前 server 的活跃 stream / 计数器数量，未注入或注入函数返回错误时返回 (0, 0)。
func GetServerStats() (activeStreams int, counterCount int) {
	serverStatsProviderLock.RLock()
	p := serverStatsProvider
	serverStatsProviderLock.RUnlock()
	if p == nil {
		return 0, 0
	}
	return p()
}

// Statis 统计插件接口
type Statis interface {
	Plugin
	// CreateRateLimitStatCollectorV1 创建采集器V1，每个stream上来后获取一次
	CreateRateLimitStatCollectorV1() *RateLimitStatCollectorV1
	// CreateRateLimitStatCollectorV2 创建采集器V2，每个stream上来后获取一次
	CreateRateLimitStatCollectorV2() *RateLimitStatCollectorV2
	// DropRateLimitStatCollector 归还采集器
	DropRateLimitStatCollector(RateLimitStatCollector)
	// AddAPICall 服务方法调用结果反馈，含有规则的计算周期
	AddAPICall(value APICallStatValue)
	// AddEventToLog 添加日志时间
	AddEventToLog(value EventToLog)
	// AddProcessTime 上报单次 gRPC 消息处理耗时（微秒），由 prometheus 等插件用于计算 avg/max
	AddProcessTime(us int64)
}

// EventToLog 可输出的事件
type EventToLog interface {
	// GetEventType 获取事件类型
	GetEventType() string
	// ToJson 变成Json输出
	ToJson() string
}

// GetStatis 获取统计插件
func GetStatis() (Statis, error) {
	plugin, err := subInitialize("statis", config.Statis, statisOnce)
	if err != nil || plugin == nil {
		return nil, err
	}

	return plugin.(Statis), nil
}
