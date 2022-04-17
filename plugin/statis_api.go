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

package plugin

import (
	"sync"
)

var (
	statisOnce = &sync.Once{}
)

/**
 * @brief 统计插件接口
 */
type Statis interface {
	Plugin
	//创建采集器V1，每个stream上来后获取一次
	CreateRateLimitStatCollectorV1() *RateLimitStatCollectorV1
	//创建采集器V2，每个stream上来后获取一次
	CreateRateLimitStatCollectorV2() *RateLimitStatCollectorV2
	//归还采集器
	DropRateLimitStatCollector(RateLimitStatCollector)
	//服务方法调用结果反馈，含有规则的计算周期
	AddAPICall(value APICallStatValue)
	//添加日志时间
	AddEventToLog(value EventToLog)
}

//可输出的事件
type EventToLog interface {
	//获取事件类型
	GetEventType() string
	//变成Json输出
	ToJson() string
}

/**
 * @brief 获取统计插件
 */
func GetStatis() (Statis, error) {
	plugin, err := subInitialize("statis", config.Statis, statisOnce)
	if err != nil || plugin == nil {
		return nil, err
	}

	return plugin.(Statis), nil
}
