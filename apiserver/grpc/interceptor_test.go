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
	"sync/atomic"
	"testing"

	"github.com/polarismesh/specification/source/go/api/v1/traffic_manage/ratelimiter"
	. "github.com/smartystreets/goconvey/convey"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/polarismesh/polaris-limiter/plugin"
)

// countingStatis 只关心 AddProcessTime 被调用的次数，其余方法为空实现。
type countingStatis struct {
	processTimeCalls atomic.Int64
}

func (f *countingStatis) Name() string                         { return "counting" }
func (f *countingStatis) Initialize(*plugin.ConfigEntry) error { return nil }
func (f *countingStatis) Destroy() error                       { return nil }
func (f *countingStatis) CreateRateLimitStatCollectorV1() *plugin.RateLimitStatCollectorV1 {
	return plugin.NewRateLimitStatCollectorV1()
}
func (f *countingStatis) CreateRateLimitStatCollectorV2() *plugin.RateLimitStatCollectorV2 {
	return plugin.NewRateLimitStatCollectorV2()
}
func (f *countingStatis) DropRateLimitStatCollector(plugin.RateLimitStatCollector) {}
func (f *countingStatis) AddAPICall(plugin.APICallStatValue)                       {}
func (f *countingStatis) AddEventToLog(plugin.EventToLog)                          {}
func (f *countingStatis) AddProcessTime(int64)                                     { f.processTimeCalls.Add(1) }

// fakeServerStream 满足 grpc.ServerStream，供 stream 包装类测试使用。
type fakeServerStream struct {
	ctx context.Context
}

func (f *fakeServerStream) SetHeader(metadata.MD) error  { return nil }
func (f *fakeServerStream) SendHeader(metadata.MD) error { return nil }
func (f *fakeServerStream) SetTrailer(metadata.MD)       {}
func (f *fakeServerStream) Context() context.Context     { return f.ctx }
func (f *fakeServerStream) SendMsg(interface{}) error    { return nil }
func (f *fakeServerStream) RecvMsg(interface{}) error    { return nil }

// TestInterceptor_ProcessTimeReporting 验证处理耗时统计只走 unary 路径：
// stream 的 SendMsg（含服务端主动 push）不再上报耗时，从而消除对 startTime 的
// 跨 goroutine 竞争与主动 push 造成的指标污染。
func TestInterceptor_ProcessTimeReporting(t *testing.T) {
	Convey("处理耗时统计的上报路径", t, func() {
		statis := &countingStatis{}
		srv := &Server{rateLimitServiceV2: &RateLimitServiceV2{statics: statis}}

		Convey("unary 路径 reportProcessTime 上报一次耗时", func() {
			tor := newInterceptor(context.Background(), srv, "/test/Unary")
			tor.preProcess()
			tor.reportProcessTime()
			So(statis.processTimeCalls.Load(), ShouldEqual, int64(1))
		})

		Convey("stream 的 SendMsg（postProcess）不再上报耗时", func() {
			tor := newInterceptor(context.Background(), srv, "/test/Stream")
			st := &stream{ServerStream: &fakeServerStream{ctx: context.Background()}, tor: tor}
			// 模拟一次 recv 建立 startTime
			So(st.RecvMsg(&ratelimiter.RateLimitResponse{}), ShouldBeNil)
			// 多次 SendMsg（模拟响应 + 服务端主动 push）都不应触发 AddProcessTime
			So(st.SendMsg(&ratelimiter.RateLimitResponse{}), ShouldBeNil)
			So(st.SendMsg(&ratelimiter.RateLimitResponse{}), ShouldBeNil)
			So(statis.processTimeCalls.Load(), ShouldEqual, int64(0))
		})

		Convey("reportProcessTime 在 statis 为 nil 时安全", func() {
			srvNil := &Server{rateLimitServiceV2: &RateLimitServiceV2{}}
			tor := newInterceptor(context.Background(), srvNil, "/test/Unary")
			tor.preProcess()
			So(func() { tor.reportProcessTime() }, ShouldNotPanic)
			So(statis.processTimeCalls.Load(), ShouldEqual, int64(0))
		})
	})
}

// realUnaryMethods 从 proto 生成的文件描述符推导真实的 unary FullMethod 集合。
// 不复用被测代码里的任何字符串常量，这样 proto package 或方法名变化时测试会独立失败。
func realUnaryMethods() map[string]bool {
	methods := make(map[string]bool)
	services := ratelimiter.File_grpcapi_ratelimiter_proto.Services()
	for i := 0; i < services.Len(); i++ {
		svc := services.Get(i)
		ms := svc.Methods()
		for j := 0; j < ms.Len(); j++ {
			m := ms.Get(j)
			if m.IsStreamingClient() || m.IsStreamingServer() {
				continue
			}
			methods["/"+string(svc.FullName())+"/"+string(m.Name())] = true
		}
	}
	return methods
}

// TestUnaryMethodsNoInterceptor_KeysMatchRealMethods 防止免拦截白名单的 key 与 proto
// 生成的 FullMethod 脱节。历史 bug：key 误写为 polaris.limiter.v2（跟随模块名），而
// proto package 实为 polaris.metric.v2，导致 TimeAdjust 无法命中白名单。
func TestUnaryMethodsNoInterceptor_KeysMatchRealMethods(t *testing.T) {
	Convey("免拦截白名单与 proto 真实方法名保持一致", t, func() {
		realMethods := realUnaryMethods()

		Convey("白名单里每个 key 都是真实存在的 unary 方法", func() {
			So(len(unaryMethodsNoInterceptor), ShouldBeGreaterThan, 0)
			for key := range unaryMethodsNoInterceptor {
				So(realMethods, ShouldContainKey, key)
			}
		})

		Convey("TimeAdjust 必须在白名单中", func() {
			var timeAdjust string
			for key := range realMethods {
				if strings.HasSuffix(key, "/TimeAdjust") {
					timeAdjust = key
				}
			}
			So(timeAdjust, ShouldNotBeEmpty)
			So(unaryMethodsNoInterceptor[timeAdjust], ShouldBeTrue)
		})
	})
}

// TestUnaryInterceptor_TimeAdjustSkipsPostProcess 验证 TimeAdjust 走短路分支：
// 既不打印 "response is invalid"（其响应无 code 字段，一旦进入 postProcess 必然报错），
// 也不把时间对齐的耗时混入限流接口的 process 耗时指标。
func TestUnaryInterceptor_TimeAdjustSkipsPostProcess(t *testing.T) {
	Convey("TimeAdjust 不经过拦截器的后置处理", t, func() {
		statis := &countingStatis{}
		srv := &Server{rateLimitServiceV2: &RateLimitServiceV2{statics: statis}}

		var timeAdjustMethod string
		for key := range realUnaryMethods() {
			if strings.HasSuffix(key, "/TimeAdjust") {
				timeAdjustMethod = key
			}
		}
		So(timeAdjustMethod, ShouldNotBeEmpty)

		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return &ratelimiter.TimeAdjustResponse{ServerTimestamp: 1}, nil
		}

		Convey("TimeAdjust 命中白名单，不上报处理耗时", func() {
			info := &grpc.UnaryServerInfo{FullMethod: timeAdjustMethod}
			rsp, err := srv.unaryInterceptor(context.Background(),
				&ratelimiter.TimeAdjustRequest{}, info, handler)

			So(err, ShouldBeNil)
			So(rsp, ShouldHaveSameTypeAs, &ratelimiter.TimeAdjustResponse{})
			So(statis.processTimeCalls.Load(), ShouldEqual, int64(0))
		})

		Convey("非白名单方法仍然走拦截器并上报耗时", func() {
			info := &grpc.UnaryServerInfo{FullMethod: "/polaris.metric.v2.RateLimitGRPCV2/Other"}
			_, err := srv.unaryInterceptor(context.Background(),
				&ratelimiter.TimeAdjustRequest{}, info,
				func(ctx context.Context, req interface{}) (interface{}, error) {
					return &ratelimiter.RateLimitResponse{}, nil
				})

			So(err, ShouldBeNil)
			So(statis.processTimeCalls.Load(), ShouldEqual, int64(1))
		})
	})
}

// TestTimeAdjustResponse_FailsPostProcessValidation 固化 bug 的成因：
// TimeAdjustResponse 既不满足 validResponse（无 GetCode），也不是 RateLimitResponse，
// 所以一旦它进入 postProcess 就必然打印 "response is invalid"。这解释了白名单为何必要。
func TestTimeAdjustResponse_FailsPostProcessValidation(t *testing.T) {
	Convey("TimeAdjustResponse 无法通过 postProcess 的响应校验", t, func() {
		rsp := &ratelimiter.TimeAdjustResponse{ServerTimestamp: 1}

		_, isValidResponse := interface{}(rsp).(validResponse)
		So(isValidResponse, ShouldBeFalse)

		_, matched := GetV2ResponseCode(rsp)
		So(matched, ShouldBeFalse)
	})
}
