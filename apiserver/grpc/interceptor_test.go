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
	"sync/atomic"
	"testing"

	"github.com/polarismesh/specification/source/go/api/v1/traffic_manage/ratelimiter"
	. "github.com/smartystreets/goconvey/convey"
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
