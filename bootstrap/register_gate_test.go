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

package bootstrap

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/polarismesh/polaris-limiter/pkg/health"
)

type errWriter struct{ err error }

func (w errWriter) WriteAndSync([]byte) error { return w.err }

func waitUntil(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
	return true
}

func TestSelfRegisterWhenReady(t *testing.T) {
	Convey("启动期注册门禁", t, func() {
		origPoll := registerGatePoll
		registerGatePoll = 10 * time.Millisecond
		resetRegistrar()
		ctx, cancel := context.WithCancel(context.Background())
		defer func() {
			cancel()
			registerGatePoll = origPoll
			health.SetDefault(nil)
			resetRegistrar()
		}()

		var calls atomic.Int32
		registerFn = func(*registrar) error {
			calls.Add(1)
			return nil
		}
		cfg := &Registry{Name: "polaris.limiter"}

		Convey("未注入 Checker 时同步注册", func() {
			So(selfRegisterWhenReady(ctx, cfg, nil, nil, "10.0.0.1"), ShouldBeNil)
			So(calls.Load(), ShouldEqual, 1)
		})

		Convey("同步注册失败时返回错误，由 bootstrap 退出", func() {
			registerFn = func(*registrar) error { return errors.New("polaris unavailable") }
			So(selfRegisterWhenReady(ctx, cfg, nil, nil, "10.0.0.1"), ShouldNotBeNil)
		})

		Convey("首次探测写入报错不算 IOHang，照常注册", func() {
			hcfg := health.Config{Enable: boolPtr(true)}.WithDefaults()
			d := health.NewIOHangDetector(hcfg, errWriter{err: errors.New("no space left on device")})
			_ = d.ProbeOnce()
			health.SetDefault(health.NewChecker(hcfg, d))

			So(selfRegisterWhenReady(ctx, cfg, nil, nil, "10.0.0.1"), ShouldBeNil)
			So(calls.Load(), ShouldEqual, 1)
		})

		Convey("启动时 IOHang：不注册、不报错，恢复后再注册", func() {
			c, release := hangingChecker(true)
			health.SetDefault(c)

			So(selfRegisterWhenReady(ctx, cfg, nil, nil, "10.0.0.1"), ShouldBeNil)
			So(reg.ctx.Load(), ShouldNotBeNil)
			time.Sleep(50 * time.Millisecond)
			So(calls.Load(), ShouldEqual, 0)

			release()
			So(waitUntil(func() bool { return calls.Load() == 1 }, 2*time.Second), ShouldBeTrue)
		})

		Convey("启动时 IOHang 且 ctx 取消：放弃注册", func() {
			c, release := hangingChecker(true)
			defer release()
			health.SetDefault(c)

			So(selfRegisterWhenReady(ctx, cfg, nil, nil, "10.0.0.1"), ShouldBeNil)
			cancel()
			time.Sleep(50 * time.Millisecond)
			So(calls.Load(), ShouldEqual, 0)
		})

		Convey("affect-heartbeat=false 时 IOHang 也同步注册", func() {
			c, release := hangingChecker(false)
			defer release()
			health.SetDefault(c)

			So(selfRegisterWhenReady(ctx, cfg, nil, nil, "10.0.0.1"), ShouldBeNil)
			So(calls.Load(), ShouldEqual, 1)
		})
	})
}
