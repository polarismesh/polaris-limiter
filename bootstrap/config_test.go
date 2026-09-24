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
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"gopkg.in/yaml.v2"
)

func TestConfigYAML_Health(t *testing.T) {
	Convey("YAML 解析 health.iohang", t, func() {
		Convey("未配置 health 段时应走默认值", func() {
			var config Config
			err := yaml.Unmarshal([]byte("registry:\n  enable: true\n"), &config)
			So(err, ShouldBeNil)
			got := config.Health.IOHang.WithDefaults()
			So(got.Enabled(), ShouldBeTrue)
			So(got.HeartbeatAffected(), ShouldBeTrue)
			So(got.Interval, ShouldEqual, 5*time.Second)
			So(got.Path, ShouldEqual, "log/polaris-limiter-probe.log")
			So(got.RotationMaxSize, ShouldEqual, 10)
		})

		Convey("配置零值时长与空 path 时应回退默认值", func() {
			yamlContent := `
health:
  iohang:
    enable: true
    interval: 0s
    timeout: 0s
    staleness: 0s
    path: ""
    rotation-max-size: 0
`
			var config Config
			err := yaml.Unmarshal([]byte(yamlContent), &config)
			So(err, ShouldBeNil)
			got := config.Health.IOHang.WithDefaults()
			So(got.Interval, ShouldEqual, 5*time.Second)
			So(got.Timeout, ShouldEqual, 3*time.Second)
			So(got.Staleness, ShouldEqual, 15*time.Second)
			So(got.Path, ShouldEqual, "log/polaris-limiter-probe.log")
			So(got.RotationMaxSize, ShouldEqual, 10)
		})

		Convey("配置正常值时应保留", func() {
			yamlContent := `
health:
  iohang:
    enable: false
    interval: 2s
    timeout: 1s
    staleness: 8s
    affect-heartbeat: false
    path: /tmp/probe.log
    rotation-max-size: 2
    rotation-max-age: 1
    rotation-max-backups: 1
`
			var config Config
			err := yaml.Unmarshal([]byte(yamlContent), &config)
			So(err, ShouldBeNil)
			raw := config.Health.IOHang
			So(raw.Enabled(), ShouldBeFalse)
			So(raw.HeartbeatAffected(), ShouldBeFalse)
			got := raw.WithDefaults()
			So(got.Interval, ShouldEqual, 2*time.Second)
			So(got.Timeout, ShouldEqual, time.Second)
			So(got.Staleness, ShouldEqual, 8*time.Second)
			So(got.Path, ShouldEqual, "/tmp/probe.log")
			So(got.RotationMaxSize, ShouldEqual, 2)
			So(got.RotationMaxAge, ShouldEqual, 1)
			So(got.RotationMaxBackups, ShouldEqual, 1)
		})

		Convey("显式 enable true 且自定义滚动参数", func() {
			yamlContent := `
health:
  iohang:
    enable: true
    rotation-max-size: 20
    rotation-max-age: 3
    rotation-max-backups: 5
`
			var config Config
			err := yaml.Unmarshal([]byte(yamlContent), &config)
			So(err, ShouldBeNil)
			got := config.Health.IOHang.WithDefaults()
			So(got.Enabled(), ShouldBeTrue)
			So(got.RotationMaxSize, ShouldEqual, 20)
			So(got.RotationMaxAge, ShouldEqual, 3)
			So(got.RotationMaxBackups, ShouldEqual, 5)
		})
	})
}
