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

package grpc

import (
	"context"
	"io"

	"github.com/modern-go/reflect2"

	apiv2 "github.com/polarismesh/polaris-limiter/pkg/api/v2"
	"github.com/polarismesh/polaris-limiter/pkg/utils"
	"github.com/polarismesh/polaris-limiter/plugin"
	"github.com/polarismesh/polaris-limiter/ratelimitv2"
)

// RateLimitServiceV2 限流Server：v2接口
type RateLimitServiceV2 struct {
	coreServer *ratelimitv2.Server
	statics    plugin.Statis
}

// 处理Stream结束
func (s *RateLimitServiceV2) postService(streamCtx *ratelimitv2.StreamContext, ipAddr *utils.IPAddress,
	collector *plugin.RateLimitStatCollectorV2, wrapper *clientWrapper) {
	s.statics.DropRateLimitStatCollector(collector)
	client := wrapper.client
	if reflect2.IsNil(client) {
		return
	}
	s.coreServer.CleanupClient(client, streamCtx.ContextId())
	s.statics.AddEventToLog(ratelimitv2.NewStreamUpdateEvent(streamCtx.ContextId(), ipAddr, ratelimitv2.ActionDelete))
}

// 客户端包装类
type clientWrapper struct {
	client ratelimitv2.Client
}

// Service 限流KEY初始化
func (s *RateLimitServiceV2) Service(stream apiv2.RateLimitGRPCV2_ServiceServer) error {
	ctx := parseContext(stream.Context())
	clientIP := utils.ParseStructClientIP(ctx)
	collector := s.statics.CreateRateLimitStatCollectorV2()
	streamCtx := ratelimitv2.NewStreamContext(stream)
	s.statics.AddEventToLog(ratelimitv2.NewStreamUpdateEvent(streamCtx.ContextId(), clientIP, ratelimitv2.ActionAdd))
	var clientWrapper = &clientWrapper{}
	defer s.postService(streamCtx, clientIP, collector, clientWrapper)
	for {
		req, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		var startTimeMicro = utils.CurrentMicrosecond()
		var counter ratelimitv2.CounterV2
		var allResp = &apiv2.RateLimitResponse{Cmd: req.Cmd}
		var timedReportResp *apiv2.TimedRateLimitReportResponse
		var code = apiv2.ExecuteSuccess
		switch req.Cmd {
		case apiv2.RateLimitCmd_ACQUIRE:
			fallthrough // 批量上报和单独上报处理逻辑一致
		case apiv2.RateLimitCmd_BATCH_ACQUIRE:
			timedReportResp, counter = s.coreServer.AcquireQuota(clientWrapper.client,
				startTimeMicro, req.GetRateLimitReportRequest(), collector)
			allResp.RateLimitReportResponse = timedReportResp.ToRateLimitReportResponse()
			code = apiv2.Code(timedReportResp.RateLimitReportResponse.Code)
		case apiv2.RateLimitCmd_INIT:
			allResp.RateLimitInitResponse, clientWrapper.client = s.coreServer.InitializeClient(
				req.GetRateLimitInitRequest(), clientWrapper.client, clientIP, streamCtx)
			if nil == allResp.RateLimitInitResponse {
				allResp.RateLimitInitResponse, counter = s.coreServer.InitializeQuota(
					ctx, clientWrapper.client, req.GetRateLimitInitRequest())
			}
		case apiv2.RateLimitCmd_BATCH_INIT:
			allResp.RateLimitBatchInitResponse, clientWrapper.client = s.coreServer.InitializeClientBatch(
				req.GetRateLimitBatchInitRequest(), clientWrapper.client, clientIP, streamCtx)
			if nil == allResp.RateLimitBatchInitResponse { // 客户端初始化OK
				allResp.RateLimitBatchInitResponse, counter = s.coreServer.BatchInitializeQuota(
					ctx, clientWrapper.client, req.GetRateLimitBatchInitRequest())
			}
		}
		err = stream.Send(allResp)
		endTimeMicro := utils.CurrentMicrosecond()
		if nil != clientWrapper.client && nil != counter {
			counter.UpdateClientSendTime(clientWrapper.client, endTimeMicro)
		}
		// 进行API调用上报
		apiCallStatValue := plugin.PoolGetAPICallStatValueImpl()
		apiCallStatValue.StatKey.APIKey = apiv2.GetAPIKey(allResp)
		apiCallStatValue.StatKey.Code = apiv2.GetErrorCode(allResp)
		apiCallStatValue.StatKey.MsgType = plugin.MsgSync
		if !reflect2.IsNil(counter) {
			identifier := counter.Identifier()
			apiCallStatValue.StatKey.Duration = identifier.Duration
		} else {
			apiCallStatValue.StatKey.Duration = 0
		}
		apiCallStatValue.Latency = endTimeMicro - startTimeMicro
		apiCallStatValue.ReqCount = 1
		s.statics.AddAPICall(apiCallStatValue)
		plugin.PoolPutAPICallStatValueImpl(apiCallStatValue)
		if nil != err || code == apiv2.InvalidCounterKey {
			return err
		}
	}
}

// TimeAdjust 获取时间戳
func (s *RateLimitServiceV2) TimeAdjust(
	ctx context.Context, req *apiv2.TimeAdjustRequest) (*apiv2.TimeAdjustResponse, error) {
	return &apiv2.TimeAdjustResponse{ServerTimestamp: utils.CurrentMillisecond()}, nil
}
