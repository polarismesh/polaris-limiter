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
	"fmt"

	"github.com/modern-go/reflect2"

	apiv2 "github.com/polarismesh/polaris-limiter/pkg/api/v2"
	"github.com/polarismesh/polaris-limiter/pkg/log"
	"github.com/polarismesh/polaris-limiter/pkg/utils"
	"github.com/polarismesh/polaris-limiter/plugin"
)

// InitializeClient 初始化客户端
func (s *Server) InitializeClient(request *apiv2.RateLimitInitRequest,
	client Client, clientIP *utils.IPAddress, streamContext *StreamContext) (*apiv2.RateLimitInitResponse, Client) {
	if !reflect2.IsNil(client) {
		if client.ClientId() != request.ClientId {
			return apiv2.NewRateLimitInitResponse(apiv2.ExceedMaxClientOneStream, request.GetTarget()), client
		}
		return nil, client
	}
	code, newClient := s.clientMng.AddClient(request.ClientId, clientIP, streamContext)
	if code != apiv2.ExecuteSuccess {
		return apiv2.NewRateLimitInitResponse(code, request.GetTarget()), client
	}
	return nil, newClient
}

// InitializeClientBatch 初始化客户端
func (s *Server) InitializeClientBatch(request *apiv2.RateLimitBatchInitRequest, client Client,
	clientIP *utils.IPAddress, streamContext *StreamContext) (*apiv2.RateLimitBatchInitResponse, Client) {
	if !reflect2.IsNil(client) {
		if client.ClientId() != request.ClientId {
			return apiv2.NewRateLimitBatchInitResponse(apiv2.ExceedMaxClientOneStream), client
		}
		return nil, client
	}
	code, newClient := s.clientMng.AddClient(request.ClientId, clientIP, streamContext)
	if code != apiv2.ExecuteSuccess {
		return apiv2.NewRateLimitBatchInitResponse(code), client
	}
	return nil, newClient
}

// InitializeQuota 限流KEY初始化
func (s *Server) InitializeQuota(ctx context.Context, client Client,
	request *apiv2.RateLimitInitRequest) (*apiv2.RateLimitInitResponse, CounterV2) {
	log.Info(fmt.Sprintf("get v2 init request: %+v", request), utils.ZapRequestID(ctx))
	resp, maxDuration := CheckRateLimitInitRequest(request, s.cfg.SlideCount)
	if nil != resp { // 请求不合法：缺失字段
		return resp, nil
	}
	var code = apiv2.ExecuteSuccess
	// 然后加入counter
	counters := make([]*apiv2.QuotaCounter, 0, len(request.GetTotals()))
	expireDuration := 2 * maxDuration
	nowMs := utils.CurrentMillisecond()
	var cCounter CounterV2
	for idx, total := range request.GetTotals() {
		cCode, counter := s.counterMng.AddCounter(request, idx, client, expireDuration)
		if cCode != apiv2.ExecuteSuccess {
			code = cCode
			break
		}
		cCounter = counter
		left := counter.SumQuota(client, nowMs)
		counters = append(counters, &apiv2.QuotaCounter{
			Duration:    total.GetDuration(),
			CounterKey:  s.boxCounterKey(left.GetCounterKey()),
			Left:        left.GetLeft(),
			Mode:        left.GetMode(),
			ClientCount: counter.ClientCount(),
		})
	}
	resp = apiv2.NewRateLimitInitResponse(code, request.GetTarget())
	if code != apiv2.ExecuteSuccess {
		return resp, cCounter
	}
	resp.ClientKey = client.ClientKey()
	resp.Counters = counters
	resp.SlideCount = request.GetSlideCount()
	return resp, cCounter
}

// BatchInitializeQuota 限流KEY初始化
func (s *Server) BatchInitializeQuota(ctx context.Context, client Client,
	request *apiv2.RateLimitBatchInitRequest) (*apiv2.RateLimitBatchInitResponse, CounterV2) {
	log.Info(fmt.Sprintf("get v2 batch init request: %+v", request), utils.ZapRequestID(ctx))
	if len(request.GetRequest()) == 0 { // 请求不合法：缺失字段
		return apiv2.NewRateLimitBatchInitResponse(apiv2.InvalidBatchInitReq), nil
	}
	if len(request.ClientId) == 0 { // 请求不合法：缺失字段
		return apiv2.NewRateLimitBatchInitResponse(apiv2.InvalidClientId), nil
	}

	var cCounter CounterV2
	resp := apiv2.NewRateLimitBatchInitResponse(apiv2.ExecuteSuccess)
	resp.ClientKey = client.ClientKey()
	resp.Result = make([]*apiv2.BatchInitResult, 0, len(request.GetRequest()))
	for _, initReq := range request.GetRequest() {
		initResult := &apiv2.BatchInitResult{
			Code:       uint32(apiv2.ExecuteSuccess),
			SlideCount: initReq.GetSlideCount()}

		initResp, maxDuration := CheckRateLimitBatchInitRequest(initReq, s.cfg.SlideCount)
		if nil != initResp { // 请求不合法：缺失字段
			initResult.Code = initResp.Code
			initResult.Target = initReq.GetTarget()
			resp.Result = append(resp.Result, initResult)
			continue
		}
		initResult.Target = &apiv2.LimitTarget{
			Namespace: initReq.GetTarget().GetNamespace(),
			Service:   initReq.GetTarget().GetService()}

		labelsList := initReq.GetTarget().GetLabelsList()
		// 加入counter，总数为labels数量*规则里配置的配额数
		initResult.Counters = make([]*apiv2.LabeledQuotaCounter, 0, len(labelsList)*len(initReq.GetTotals()))
		expireDuration := 2 * maxDuration
		nowMs := utils.CurrentMillisecond()

		for _, label := range labelsList {
			counters := make([]*apiv2.QuotaCounter, 0, len(initReq.GetTotals()))
			for idx, total := range initReq.GetTotals() {
				initReq.GetTarget().Labels = label
				cCode, counter := s.counterMng.AddCounter(initReq, idx, client, expireDuration)
				if cCode != apiv2.ExecuteSuccess {
					initResult.Code = uint32(cCode)
					break
				}
				cCounter = counter
				left := counter.SumQuota(client, nowMs)
				counters = append(counters, &apiv2.QuotaCounter{
					Duration:    total.GetDuration(),
					CounterKey:  s.boxCounterKey(left.GetCounterKey()),
					Left:        left.GetLeft(),
					Mode:        left.GetMode(),
					ClientCount: counter.ClientCount(),
				})
			}
			labeledCounter := &apiv2.LabeledQuotaCounter{Labels: label, Counters: counters}
			initResult.Counters = append(initResult.Counters, labeledCounter)
		}
		resp.Result = append(resp.Result, initResult)
	}
	return resp, cCounter
}

func (s *Server) boxCounterKey(counterKey uint32) uint32 {
	return uint32(s.cfg.Myid)<<24 | counterKey
}

func (s *Server) unboxCounterKey(counterKey uint32) (apiv2.Code, uint32) {
	myid := uint8(counterKey >> 24)
	if myid != uint8(s.cfg.Myid) {
		return apiv2.InvalidCounterKey, 0
	}
	realCounterKey := counterKey & uint32(0x00FFFFFF)
	if realCounterKey == 0 || realCounterKey > s.cfg.MaxCounter {
		return apiv2.InvalidCounterKey, 0
	}
	return apiv2.ExecuteSuccess, realCounterKey
}

// AcquireQuota 获取限流配额
func (s *Server) AcquireQuota(client Client, startTimeMicro int64, request *apiv2.RateLimitReportRequest,
	collector *plugin.RateLimitStatCollectorV2) (*apiv2.TimedRateLimitReportResponse, CounterV2) {
	resp := CheckRateLimitReportRequest(request)
	if nil != resp {
		return resp, nil
	}

	var code = apiv2.ExecuteSuccess
	var counter CounterV2
	var quotaLefts = make([]*apiv2.QuotaLeft, 0, len(request.GetQuotaUses()))
	for idx, used := range request.GetQuotaUses() {
		var counterKey uint32
		code, counterKey = s.unboxCounterKey(used.GetCounterKey())
		if code != apiv2.ExecuteSuccess {
			break
		}
		code, counter = s.counterMng.GetCounter(counterKey)
		if code != apiv2.ExecuteSuccess {
			break
		}
		sum := request.QuotaUses[idx]
		counter.Update()
		quotaLeft := counter.AcquireQuota(client, sum, request.GetTimestamp(), startTimeMicro, collector)
		quotaLeft.CounterKey = s.boxCounterKey(quotaLeft.CounterKey)
		quotaLefts = append(quotaLefts, quotaLeft)
	}
	resp = apiv2.NewRateLimitReportResponse(code)
	resp.QuotaLefts = quotaLefts
	return resp, counter
}
