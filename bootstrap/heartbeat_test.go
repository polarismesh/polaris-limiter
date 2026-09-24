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
	"testing"
	"time"

	"github.com/golang/protobuf/ptypes/wrappers"
	. "github.com/smartystreets/goconvey/convey"

	polaris "github.com/polarismesh/polaris-limiter/pkg/api/polaris/v1"
	"github.com/polarismesh/polaris-limiter/pkg/health"
)

type hangWriter struct {
	started chan struct{}
	release chan struct{}
}

func (w *hangWriter) WriteAndSync([]byte) error {
	close(w.started)
	<-w.release
	return nil
}

// hangingChecker 返回一个探测写已 hang 超过 timeout 的 Checker 及其释放函数。
func hangingChecker(affectHeartbeat bool) (*health.Checker, func()) {
	w := &hangWriter{started: make(chan struct{}), release: make(chan struct{})}
	cfg := health.Config{
		Enable:          boolPtr(true),
		AffectHeartbeat: boolPtr(affectHeartbeat),
		Timeout:         time.Millisecond,
	}.WithDefaults()
	d := health.NewIOHangDetector(cfg, w)
	go func() { _ = d.ProbeOnce() }()
	<-w.started
	time.Sleep(20 * time.Millisecond)
	return health.NewChecker(cfg, d), func() { close(w.release) }
}

func TestHeartbeatRound_SkipWhenReadinessDown(t *testing.T) {
	Convey("readiness 与心跳联动", t, func() {
		origSend := sendHeartbeatsFn
		defer func() {
			sendHeartbeatsFn = origSend
			health.SetDefault(nil)
		}()

		var sent int
		sendHeartbeatsFn = func(context.Context, *registrar, []*polaris.Instance) { sent++ }

		r := &registrar{}
		instances := []*polaris.Instance{{Host: &wrappers.StringValue{Value: "10.0.0.1"}}}
		r.instances.Store(&instances)

		Convey("未注入 Checker 时照常上报", func() {
			r.heartbeatRound(context.Background())
			So(sent, ShouldEqual, 1)
		})

		Convey("IOHang DOWN 时跳过上报并计数，恢复后续上报并清零", func() {
			c, release := hangingChecker(true)
			defer release()
			health.SetDefault(c)

			r.heartbeatRound(context.Background())
			r.heartbeatRound(context.Background())
			So(sent, ShouldEqual, 0)
			So(r.skippedBeats.Load(), ShouldEqual, 2)

			health.SetDefault(nil)
			r.heartbeatRound(context.Background())
			So(sent, ShouldEqual, 1)
			So(r.skippedBeats.Load(), ShouldEqual, 0)
		})

		Convey("affect-heartbeat=false 时 DOWN 也照常上报", func() {
			c, release := hangingChecker(false)
			defer release()
			health.SetDefault(c)

			r.heartbeatRound(context.Background())
			So(sent, ShouldEqual, 1)
			So(r.skippedBeats.Load(), ShouldEqual, 0)
		})

		Convey("无已注册实例时不上报", func() {
			empty := &registrar{}
			empty.heartbeatRound(context.Background())
			So(sent, ShouldEqual, 0)
		})
	})
}
