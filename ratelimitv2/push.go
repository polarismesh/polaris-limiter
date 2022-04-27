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

package ratelimitv2

import (
	"context"
	"errors"

	apiv2 "github.com/polarismesh/polaris-limiter/pkg/api/v2"
)

// PushManager 推送管理器
type PushManager interface {
	// Run 启动push线程
	Run(ctx context.Context)
	// Schedule 准备推送
	Schedule(value *PushValue)
}

// PushValue 推送的值
type PushValue struct {
	Counter       CounterV2
	Msg           *apiv2.RateLimitResponse
	ExcludeClient string
	// StartTimeMicro 请求进入时间
	StartTimeMicro int64
	// MsgTimeMicro 应答生成时间
	MsgTimeMicro int64
}

// 推送管理器实现
type pushManager struct {
	workerCount  int
	pushChannels []chan PushValue
}

// NewPushManager 创建推送管理器
func NewPushManager(workerCount int, chanSize int) (PushManager, error) {
	if workerCount == 0 {
		return nil, errors.New("workerCount should greater than zero")
	}
	pm := &pushManager{
		workerCount:  workerCount,
		pushChannels: make([]chan PushValue, 0, workerCount),
	}
	for i := 0; i < workerCount; i++ {
		pm.pushChannels = append(pm.pushChannels, make(chan PushValue, chanSize))
	}
	return pm, nil
}

// Run 启动push线程
func (p *pushManager) Run(ctx context.Context) {
	for i := 0; i < p.workerCount; i++ {
		go func(idx int) {
			for {
				select {
				case <-ctx.Done():
					return
				case value := <-p.pushChannels[idx]:
					value.Counter.PushMessage(&value)
				}
			}
		}(i)
	}
}

// Schedule 准备推送
func (p *pushManager) Schedule(pushValue *PushValue) {
	startIdx := int(pushValue.Counter.CounterKey()) % p.workerCount
	for i := 0; i < p.workerCount; i++ {
		// 轮询并获取所有的推送管道
		pushChannel := p.pushChannels[(startIdx+i)%p.workerCount]
		select {
		case pushChannel <- *pushValue:
			return
		default:
			continue
		}
	}
}
