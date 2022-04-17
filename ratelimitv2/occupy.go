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
	apiv2 "github.com/polarismesh/polaris-limit/api/v2"
	"github.com/polarismesh/polaris-limit/pkg/utils"
	"sync/atomic"
)

//抢占式分配器
type OccupyAllocator struct {
	pushManager   PushManager
	slidingWindow *utils.SlidingWindow
	//配额已经用完
	quotaUsedOff uint32
	mode         apiv2.Mode
	counter      CounterV2
}

//创建抢占式分配器
func NewOccupyAllocator(slideCount int, intervalMs int, pushManager PushManager, counter CounterV2) QuotaAllocator {
	return &OccupyAllocator{
		pushManager:   pushManager,
		slidingWindow: utils.NewSlidingWindow(slideCount, intervalMs),
		mode:          apiv2.Mode_BATCH_OCCUPY,
		counter:       counter,
	}
}

//返回分配器所属的模式
func (o *OccupyAllocator) Mode() apiv2.Mode {
	return o.mode
}

//分配配额
func (o *OccupyAllocator) Allocate(
	client Client, quotaSum *apiv2.QuotaSum, timestampMs int64, startTimeMicro int64) *apiv2.QuotaLeft {
	sumUsed := quotaSum.GetUsed()
	sumLimit := quotaSum.GetLimited()
	serverTimeMs := startTimeMicro/1e3
	totalUsed := o.slidingWindow.AddAndGetCurrent(timestampMs, serverTimeMs, sumUsed)
	quotaLeft := int64(o.counter.MaxAmount()) - int64(totalUsed)
	quotaLeftRet := &apiv2.QuotaLeft{
		CounterKey:  quotaSum.GetCounterKey(),
		Mode:        o.mode,
		Left:        quotaLeft,
		ClientCount: o.counter.ClientCount(),
	}
	//无状态变更，不推送
	if sumUsed == 0 && sumLimit == 0 {
		quotaLeftRet.Left = quotaLeft
		return quotaLeftRet
	}
	var push bool
	if quotaLeft <= 0 && atomic.CompareAndSwapUint32(&o.quotaUsedOff, 0, 1) {
		push = true
	} else if quotaLeft > 0 {
		atomic.CompareAndSwapUint32(&o.quotaUsedOff, 1, 0)
	}
	if push {
		o.doPush(quotaLeftRet, client, startTimeMicro)
	}
	return quotaLeftRet
}

//启动推送
func (o *OccupyAllocator) doPush(quotaLeft *apiv2.QuotaLeft, client Client, startTimeMicro int64) {
	resp := apiv2.NewRateLimitReportResponse(apiv2.ExecuteSuccess)
	resp.QuotaLefts = append(resp.QuotaLefts, quotaLeft)
	pushValue := &PushValue{
		Counter: o.counter,
		Msg: &apiv2.RateLimitResponse{
			Cmd:                     apiv2.RateLimitCmd_ACQUIRE,
			RateLimitReportResponse: resp.ToRateLimitReportResponse(),
		},
		ExcludeClient:  client.ClientId(),
		StartTimeMicro: startTimeMicro,
		MsgTimeMicro:   resp.CreateTimeMicro(),
	}
	o.pushManager.Schedule(pushValue)
}
