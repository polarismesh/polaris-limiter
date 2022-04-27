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

package utils

import (
	"encoding/json"
	"strings"
	"time"
)

var (
	// ServerAddress 统一的服务器地址
	ServerAddress = "127.0.0.1"
	// LimitServiceName 限流集群服务名
	LimitServiceName = ""
)

// CounterStat 计数器的状态
type CounterStat struct {
	Key              string
	Namespace        string
	Service          string
	Duration         Duration
	TotalAmount      uint32 // 总体的amount
	SumAmount        uint32 // 累加amount
	StartTime        int64
	LastMtime        int64
	NeedUpdateRemote bool
	RemoteRecordTime int64
}

// LimiterStat 限制器的状态
type LimiterStat struct {
	Duration  Duration
	Amount    uint32
	SumAmount uint32
	Cycle     uint64
}

// RemoteCounterStat 远端的counterStat
type RemoteCounterStat struct {
	Key          string
	StartTime    int64
	RecordTime   int64
	RecordServer string
	SumAmount    uint32
}

// Duration 自定义Duration
type Duration time.Duration

// UnmarshalJSON 实现Unmarshaler
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}

	duration, err := time.ParseDuration(s)
	if err != nil {
		return err
	}

	*d = Duration(duration)
	return nil
}

// MarshalJSON 实现Marshaler
func (d Duration) MarshalJSON() ([]byte, error) {
	s := time.Duration(d).String()
	return json.Marshal(s)
}

// SubLabels 复合标签的子标签
type SubLabels struct {
	Method string
	AppId  string
	Uin    string
	Labels string
}

const (
	splitItems   = "|"
	methodPrefix = "method:"
	appIdPrefix  = "appid:"
	uinPrefix    = "uin:"
)

// ParseLabels 把客户端上报的符合字段，解析成多维度的值
func ParseLabels(composedLabels string) *SubLabels {
	subLabels := &SubLabels{}
	items := strings.Split(composedLabels, splitItems)
	excludeItems := make(map[int]bool)
	for i, item := range items {
		if strings.HasPrefix(item, methodPrefix) {
			subLabels.Method = item[len(methodPrefix):]
			excludeItems[i] = true
			continue
		}
		if strings.HasPrefix(item, appIdPrefix) {
			subLabels.AppId = item[len(appIdPrefix):]
			excludeItems[i] = true
			continue
		}
		if strings.HasPrefix(item, uinPrefix) {
			subLabels.Uin = item[len(uinPrefix):]
			excludeItems[i] = true
			continue
		}
	}
	if len(excludeItems) == 0 {
		subLabels.Labels = composedLabels
		return subLabels
	}
	if len(excludeItems) == len(items) {
		return subLabels
	}
	restItems := make([]string, 0)
	for i, item := range items {
		if _, ok := excludeItems[i]; ok {
			continue
		}
		restItems = append(restItems, item)
	}
	subLabels.Labels = strings.Join(restItems, splitItems)
	return subLabels
}
