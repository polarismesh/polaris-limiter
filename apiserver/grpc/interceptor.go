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
	"strings"
	"time"

	"github.com/golang/protobuf/ptypes/wrappers"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	"github.com/polarismesh/polaris-limiter/pkg/api/base"
	apiv2 "github.com/polarismesh/polaris-limiter/pkg/api/v2"
	"github.com/polarismesh/polaris-limiter/pkg/log"
	"github.com/polarismesh/polaris-limiter/pkg/utils"
)

// 不需要走拦截器的同步方法
var unaryMethodsNoInterceptor = map[string]bool{
	"/polaris.limiter.v2.RateLimitGRPCV2/TimeAdjust": true,
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
	if log.DebugEnabled() {
		i.startTime = time.Unix(0, utils.CurrentNanosecond())
		log.Debugf("receive request", utils.ZapRequestID(i.ctx), utils.ZapClientAddr(i.ctx),
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
	if v2Resp, ok := rsp.(*apiv2.RateLimitResponse); ok {
		return apiv2.GetErrorCode(v2Resp), true
	}
	return 0, false
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
