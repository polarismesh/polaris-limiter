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
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

func TestConfigWithDefaults(t *testing.T) {
	Convey("IOHang 配置缺省与覆盖", t, func() {
		Convey("未配置 / 零值应填默认值，enable 与 affect-heartbeat 默认为 true", func() {
			got := Config{}.WithDefaults()
			So(got.Interval, ShouldEqual, defaultInterval)
			So(got.Timeout, ShouldEqual, defaultTimeout)
			So(got.Staleness, ShouldEqual, defaultStaleness)
			So(got.Path, ShouldEqual, defaultProbePath)
			So(got.RotationMaxSize, ShouldEqual, defaultRotationMaxSize)
			So(got.RotationMaxAge, ShouldEqual, defaultRotationMaxAge)
			So(got.RotationMaxBackups, ShouldEqual, defaultRotationMaxBackups)
			So(got.Enabled(), ShouldBeTrue)
			So(got.HeartbeatAffected(), ShouldBeTrue)
			So(got.Enable, ShouldNotBeNil)
			So(got.AffectHeartbeat, ShouldNotBeNil)
		})

		Convey("显式 false 不被默认值覆盖", func() {
			got := Config{Enable: boolPtr(false), AffectHeartbeat: boolPtr(false)}.WithDefaults()
			So(got.Enabled(), ShouldBeFalse)
			So(got.HeartbeatAffected(), ShouldBeFalse)
		})

		Convey("配置正常值时应保留", func() {
			got := Config{
				Enable:             boolPtr(false),
				Interval:           time.Second,
				Timeout:            2 * time.Second,
				Staleness:          4 * time.Second,
				AffectHeartbeat:    boolPtr(false),
				Path:               "tmp/probe.log",
				RotationMaxSize:    2,
				RotationMaxAge:     1,
				RotationMaxBackups: 1,
			}.WithDefaults()
			So(got.Enabled(), ShouldBeFalse)
			So(got.HeartbeatAffected(), ShouldBeFalse)
			So(got.Interval, ShouldEqual, time.Second)
			So(got.Timeout, ShouldEqual, 2*time.Second)
			So(got.Staleness, ShouldEqual, 4*time.Second)
			So(got.Path, ShouldEqual, "tmp/probe.log")
			So(got.RotationMaxSize, ShouldEqual, 2)
			So(got.RotationMaxAge, ShouldEqual, 1)
			So(got.RotationMaxBackups, ShouldEqual, 1)
		})

		Convey("负值/异常值应按默认值回退", func() {
			got := Config{
				Interval:           -1,
				Timeout:            -1,
				Staleness:          -1,
				RotationMaxSize:    -8,
				RotationMaxAge:     -8,
				RotationMaxBackups: -8,
			}.WithDefaults()
			So(got.Interval, ShouldEqual, defaultInterval)
			So(got.Timeout, ShouldEqual, defaultTimeout)
			So(got.Staleness, ShouldEqual, defaultStaleness)
			So(got.RotationMaxSize, ShouldEqual, defaultRotationMaxSize)
			So(got.RotationMaxAge, ShouldEqual, defaultRotationMaxAge)
			So(got.RotationMaxBackups, ShouldEqual, defaultRotationMaxBackups)
		})

		Convey("staleness 小于 2*interval+timeout 时应自动抬高，避免误判抖动", func() {
			got := Config{Interval: 30 * time.Second}.WithDefaults()
			So(got.Staleness, ShouldEqual, 2*30*time.Second+defaultTimeout)

			got = Config{Interval: time.Second, Timeout: time.Second, Staleness: time.Second}.WithDefaults()
			So(got.Staleness, ShouldEqual, 3*time.Second)
		})
	})
}
