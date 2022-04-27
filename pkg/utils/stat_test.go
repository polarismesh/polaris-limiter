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

package utils

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// 测试自定义Duration的序列化与反序列化
func TestMarshal(t *testing.T) {
	Convey("正常序列化与反序列化", t, func() {
		limiter := &LimiterStat{
			Duration: Duration(time.Hour + time.Minute*20 + time.Second*30), // 1h20m30s
			Amount:   100,
		}

		buf, err := json.Marshal(limiter)
		So(err, ShouldBeNil)
		t.Logf("%s", string(buf))
		So(strings.Contains(string(buf), "1h20m30s"), ShouldBeTrue)

		var out LimiterStat
		So(json.Unmarshal(buf, &out), ShouldBeNil)
		So(out.Duration, ShouldEqual, limiter.Duration)
	})
}

// 测试标签解析
func TestParseLabels(t *testing.T) {
	var composedLabels string
	var subLabels *SubLabels
	composedLabels = "method:test1"
	subLabels = ParseLabels(composedLabels)
	fmt.Printf("sublabels is method %s, appid %s, uin %s, labels %s\n",
		subLabels.Method, subLabels.AppId, subLabels.Uin, subLabels.Labels)
	composedLabels = "appid:tencent_video.doki.heartbeat|interface:Incr"
	subLabels = ParseLabels(composedLabels)
	fmt.Printf("sublabels is method %s, appid %s, uin %s, labels %s\n",
		subLabels.Method, subLabels.AppId, subLabels.Uin, subLabels.Labels)
	composedLabels = "appid:3|method:GetRecords"
	subLabels = ParseLabels(composedLabels)
	fmt.Printf("sublabels is method %s, appid %s, uin %s, labels %s\n",
		subLabels.Method, subLabels.AppId, subLabels.Uin, subLabels.Labels)
	composedLabels = "appid:4|method:GetRecords|user:xxxx"
	subLabels = ParseLabels(composedLabels)
	fmt.Printf("sublabels is method %s, appid %s, uin %s, labels %s\n",
		subLabels.Method, subLabels.AppId, subLabels.Uin, subLabels.Labels)
	composedLabels = "bizid:1047|cmd:getplayinfo"
	subLabels = ParseLabels(composedLabels)
	fmt.Printf("sublabels is method %s, appid %s, uin %s, labels %s\n",
		subLabels.Method, subLabels.AppId, subLabels.Uin, subLabels.Labels)
}
