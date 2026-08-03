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

package grpc

import (
	"context"
	"strings"
	"time"

	"github.com/golang/protobuf/ptypes/wrappers"
	"github.com/polarismesh/specification/source/go/api/v1/traffic_manage/ratelimiter"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	"github.com/polarismesh/polaris-limiter/pkg/api/base"
	apiv2 "github.com/polarismesh/polaris-limiter/pkg/api/v2"
	"github.com/polarismesh/polaris-limiter/pkg/log"
	"github.com/polarismesh/polaris-limiter/pkg/utils"
)

// 不需要走拦截器的同步方法。
// key 必须与 proto 生成的 FullMethod 完全一致：proto package 是 polaris.metric.v2，
// 与本模块名（polaris-limiter）无关。历史上此处误写为 polaris.limiter.v2，导致 TimeAdjust
// 无法命中白名单，每次调用都走进 postProcess 打印 "response is invalid"（TimeAdjustResponse
// 没有 code 字段，必然无法通过类型校验），并把耗时混入 process_avg_us / process_max_us。
// 变更时由 TestUnaryMethodsNoInterceptor_KeysMatchRealMethods 兜底校验。
var unaryMethodsNoInterceptor = map[string]bool{
	"/polaris.metric.v2.RateLimitGRPCV2/TimeAdjust": true,
}

// grpc unary拦截器函数
func (g *Server) unaryInterceptor(ctx context.Context, req interface{},
	info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	if _, ok := unaryMethodsNoInterceptor[info.FullMethod]; ok {
		// 直接转发，无需走拦截器
		return handler(ctx, req)
	}
	tor := newInterceptor(ctx, g, info.FullMethod)
	tor.preProcess()
	rsp, err := handler(tor.ctx, req)
	// unary 的 preProcess 与本调用在同一 goroutine 顺序执行，读 startTime 无竞争。
	// stream 的处理耗时不走此路径，改由 Service 在 recv→send 之间同步测量后上报
	// （见 api_v2.go），以避免服务端主动 push 的 SendMsg 读到无配对的 startTime
	// （污染 process_max_us）并与 recv 写 startTime 形成数据竞争。
	tor.reportProcessTime()
	tor.postProcess(rsp)
	return rsp, err
}

// grpc stream 拦截器处理函数
func (g *Server) streamInterceptor(srv interface{}, ss grpc.ServerStream,
	info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	tor := newInterceptor(ss.Context(), g, info.FullMethod)
	// pre/post操作放到handler中的recv和send执行
	st := &stream{ss, tor}
	if err := handler(srv, st); err != nil {
		log.Error("grpc stream handler err", zap.String("err", err.Error()),
			utils.ZapRequestID(tor.ctx),
			utils.ZapClientAddr(tor.ctx),
			utils.ZapUserAgent(tor.ctx),
			utils.ZapMethod(tor.fullMethod))
		return err
	}

	return nil
}

// grpc拦截器
type interceptor struct {
	ctx        context.Context
	startTime  time.Time
	fullMethod string
	server     *Server
}

// 新建一个拦截器
func newInterceptor(ctx context.Context, server *Server, fullMethod string) *interceptor {
	nCtx := parseContext(ctx)
	return &interceptor{
		ctx:        nCtx,
		fullMethod: fullMethod,
		server:     server,
	}
}

// 拦截器前置处理
func (i *interceptor) preProcess() {
	// 记录起始时间，供 unary 拦截器的 reportProcessTime 计算处理耗时（prometheus 等插件消费）。
	// 必须用 time.Now()：它携带单调时钟读数，reportProcessTime 的 time.Since 才能得到真实耗时；
	// 若用 time.Unix(0, ...) 构造墙钟时间，NTP 跳变会污染 process_max_us（虚高）或丢样本（负值）。
	// 注：stream 的 RecvMsg 也会调用本方法（用于 debug 日志），但 stream 的耗时统计不依赖
	// startTime——由 Service 在 recv→send 之间同步测量后上报，避免主动 push 读到过期 startTime。
	i.startTime = time.Now()
	if log.DebugEnabled() {
		log.Debug("receive request", utils.ZapRequestID(i.ctx), utils.ZapClientAddr(i.ctx),
			utils.ZapUserAgent(i.ctx), utils.ZapMethod(i.fullMethod))
	}
}

// 校验是否正确的应答类型
type validResponse interface {
	GetCode() *wrappers.UInt32Value
}

// 提供消息ID
type msgIdProvider interface {
	GetMsgId() *wrappers.Int64Value
}

// GetV2ResponseCode 获取返回码
func GetV2ResponseCode(rsp interface{}) (uint32, bool) {
	if v2Resp, ok := rsp.(*ratelimiter.RateLimitResponse); ok {
		return apiv2.GetErrorCode(v2Resp), true
	}
	return 0, false
}

// reportProcessTime 上报单次处理耗时（微秒），仅供 unary 拦截器调用。
// preProcess 与本方法在同一 goroutine 顺序执行，读 startTime 无数据竞争。
// stream 场景严禁调用本方法：其 SendMsg 既服务于响应也服务于服务端主动 push，
// push 无配对的 recv，会读到上一次请求残留的 startTime（污染指标），且与 recv 写
// startTime 分处不同 goroutine（竞争）。stream 的耗时由 Service 同步测量后上报。
func (i *interceptor) reportProcessTime() {
	if i.startTime.IsZero() || i.server == nil || i.server.rateLimitServiceV2 == nil {
		return
	}
	statis := i.server.rateLimitServiceV2.statics
	if statis == nil {
		return
	}
	elapsed := time.Since(i.startTime).Microseconds()
	if elapsed >= 0 {
		statis.AddProcessTime(elapsed)
	}
}

// 拦截器后置处理
func (i *interceptor) postProcess(rsp interface{}) {
	var rspCode uint32
	var match bool
	if obj, ok := rsp.(validResponse); ok {
		rspCode = obj.GetCode().GetValue()
	} else if rspCode, match = GetV2ResponseCode(rsp); !match {
		log.Errorf("[interceptor] response is invalid")
		return
	}
	if utils.IsSuccess(rspCode) {
		// 成功无需打印日志
		return
	}
	var msgId int64
	if mProvider, ok := rsp.(msgIdProvider); ok {
		msgId = mProvider.GetMsgId().GetValue()
	}
	log.Info("send error resp", utils.ZapRequestID(i.ctx), utils.ZapClientAddr(i.ctx),
		utils.ZapUserAgent(i.ctx), utils.ZapMethod(i.fullMethod), utils.ZapCode(rspCode), utils.ZapMsgId(msgId))
}

// 封装一下grpc.ServerStream
type stream struct {
	grpc.ServerStream
	tor *interceptor
}

// 重写RecvMsg
func (s *stream) RecvMsg(m interface{}) error {
	if err := s.ServerStream.RecvMsg(m); err != nil {
		return err
	}

	s.tor.preProcess()
	return nil
}

// 重写SendMsg
func (s *stream) SendMsg(m interface{}) error {
	s.tor.postProcess(m)
	if err := s.ServerStream.SendMsg(m); err != nil {
		return err
	}

	return nil
}

// 解析grpc的context
func parseContext(ctx context.Context) context.Context {
	nCtx := context.Background()
	requestID := ""
	userAgent := ""
	clientIP := ""
	meta, exist := metadata.FromIncomingContext(ctx)
	if exist {
		agents := meta["user-agent"]
		if len(agents) > 0 {
			userAgent = agents[0]
		}

		ids := meta["request-id"]
		if len(ids) > 0 {
			requestID = ids[0]
		}
		ips := meta[base.HeaderKeyClientIP]
		if len(ips) > 0 {
			clientIP = ips[0]
		}
	}
	nCtx = utils.WithRequestID(nCtx, requestID)
	nCtx = utils.WithUserAgent(nCtx, userAgent)

	if pr, ok := peer.FromContext(ctx); ok && pr.Addr != nil {
		address := pr.Addr.String()
		nCtx = utils.WithClientAddr(nCtx, address)
		if len(clientIP) == 0 {
			addrSlice := strings.Split(address, ":")
			if len(addrSlice) == 2 {
				clientIP = addrSlice[0]
			}
		}
	}

	nCtx = utils.WithClientIP(nCtx, clientIP)

	ipAddr := utils.NewIPAddress(clientIP)
	nCtx = utils.WithStructClientIP(nCtx, ipAddr)
	return nCtx
}
