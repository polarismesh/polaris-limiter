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
	"fmt"
	"testing"
	"time"
)

func TestTimestampMsToUtcIso8601(t *testing.T) {
	now := CurrentMillisecond()
	str := TimestampMsToUtcIso8601(now)
	fmt.Printf("now is %s\n", str)
}

// 性能测试
func BenchmarkCurrentMicrosecond(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		_ = CurrentMillisecond()
	}
}

// 测试调用now函数性能
func BenchmarkTimeNow(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = time.Now().Unix() / 1e3
	}
}

/**
linux下的性能测试结果
[sam@VM_15_118_centos ~/polaris-limiter/pkg/utils]$ go test -mod=vendor -bench=.
now is 2021-03-26T16:34:03.220+0800
goos: linux
goarch: amd64
pkg: github.com/polarismesh/polaris-limiter/pkg/utils
BenchmarkCurrentMicrosecond-8           16930384                71.1 ns/op             0 B/op          0 allocs/op
BenchmarkTimeNow-8                       8023588               149 ns/op               0 B/op          0 allocs/op
PASS
ok      github.com/polarismesh/polaris-limiter/pkg/utils        2.630s
*/
