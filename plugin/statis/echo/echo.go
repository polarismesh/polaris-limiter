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

package echo

import (
	"github.com/polarismesh/polaris-limiter/plugin"
)

// 插件注册
func init() {
	s := &StaticsWorker{}
	plugin.RegisterPlugin(s.Name(), s)
}

// StaticsWorker 智研上报处理器
type StaticsWorker struct {
}

// Name 获取统计插件名称
func (s *StaticsWorker) Name() string {
	return "echo"
}

/**
 * @brief 初始化统计插件
 */
func (s *StaticsWorker) Initialize(conf *plugin.ConfigEntry) error {
	return nil
}

/**
 * @brief 销毁统计插件
 */
func (s *StaticsWorker) Destroy() error {
	return nil
}

// 创建采集器V1，每个stream上来后获取一次
func (s *StaticsWorker) CreateRateLimitStatCollectorV1() *plugin.RateLimitStatCollectorV1 {
	return plugin.NewRateLimitStatCollectorV1()
}

// 创建采集器V2，每个stream上来后获取一次
func (s *StaticsWorker) CreateRateLimitStatCollectorV2() *plugin.RateLimitStatCollectorV2 {
	return plugin.NewRateLimitStatCollectorV2()
}

// 归还采集器
func (s *StaticsWorker) DropRateLimitStatCollector(collector plugin.RateLimitStatCollector) {
	return
}

// 服务方法调用结果反馈，含有规则的计算周期
func (s *StaticsWorker) AddAPICall(value plugin.APICallStatValue) {
}

// 添加日志时间
func (s *StaticsWorker) AddEventToLog(value plugin.EventToLog) {

}
