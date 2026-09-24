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

package health

import (
	"context"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

func TestCheckerReports(t *testing.T) {
	Convey("健康报告聚合", t, func() {
		defer SetDefault(nil)

		Convey("liveness 始终仅含 basic UP", func() {
			c := NewChecker(Config{Enable: boolPtr(true)}.WithDefaults(), downDetector())
			report := c.Liveness()
			So(report.Status, ShouldEqual, StatusUp)
			So(report.Components["basic"].Status, ShouldEqual, StatusUp)
			_, hasIO := report.Components["ioHang"]
			So(hasIO, ShouldBeFalse)
		})

		Convey("enable=false 时 readiness 仅 basic 恒 UP，且不影响心跳", func() {
			c := NewChecker(Config{Enable: boolPtr(false)}.WithDefaults(), downDetector())
			report := c.Readiness()
			So(report.Status, ShouldEqual, StatusUp)
			_, hasIO := report.Components["ioHang"]
			So(hasIO, ShouldBeFalse)
			So(c.AllowHeartbeat(), ShouldBeTrue)
		})

		Convey("nil Checker 的 readiness 仅 basic UP", func() {
			var c *Checker
			So(c.Readiness().Status, ShouldEqual, StatusUp)
			So(c.AllowHeartbeat(), ShouldBeTrue)
		})

		Convey("ioHang DOWN 时整体为 DOWN", func() {
			c := NewChecker(Config{Enable: boolPtr(true)}.WithDefaults(), downDetector())
			report := c.Readiness()
			So(report.Status, ShouldEqual, StatusDown)
			So(report.Components["ioHang"].Status, ShouldEqual, StatusDown)
		})

		Convey("affect-heartbeat=false 时即使 DOWN 也允许心跳", func() {
			c := NewChecker(Config{
				Enable:          boolPtr(true),
				AffectHeartbeat: boolPtr(false),
			}.WithDefaults(), downDetector())
			So(c.AllowHeartbeat(), ShouldBeTrue)
		})

		Convey("默认 affect-heartbeat 在 DOWN 时跳过心跳", func() {
			SetDefault(NewChecker(Config{Enable: boolPtr(true)}.WithDefaults(), downDetector()))
			So(AllowHeartbeat(), ShouldBeFalse)
		})

		Convey("北极星联动未开启时 WaitFirstResult 立即返回", func() {
			idle := NewIOHangDetector(testDetectorCfg(), nopSink{})
			for _, c := range []*Checker{
				nil,
				NewChecker(Config{Enable: boolPtr(false)}.WithDefaults(), idle),
				NewChecker(Config{Enable: boolPtr(true), AffectHeartbeat: boolPtr(false)}.WithDefaults(), idle),
			} {
				begin := time.Now()
				c.WaitFirstResult(context.Background())
				So(time.Since(begin), ShouldBeLessThan, idle.timeout)
			}
			SetDefault(nil)
			WaitFirstResult(context.Background())
		})
	})
}
