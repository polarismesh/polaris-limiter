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

package utils

import (
	"fmt"
	"testing"
)

// TestSlidingWindow_SlideFive 使用手动传入时间值，避免 time.Sleep 精度问题导致的 flaky test
func TestSlidingWindow_SlideFive(t *testing.T) {
	var total uint32 = 100
	slidingWindow := NewSlidingWindow(5, 1000)

	// 使用固定基准时间，手动控制时间推进
	baseTime := int64(1000000)

	var allocated uint32
	var value uint32

	// +0ms
	now := baseTime
	value = slidingWindow.AddAndGetCurrent(now, now, 10)
	allocated += total - value
	fmt.Printf("left is %d\n", total-value)

	// +200ms
	now = baseTime + 200
	value = slidingWindow.AddAndGetCurrent(now, now, 40)
	allocated += total - value
	fmt.Printf("left is %d\n", total-value)

	// +700ms
	now = baseTime + 700
	value = slidingWindow.AddAndGetCurrent(now, now, 15)
	allocated += total - value
	fmt.Printf("left is %d\n", total-value)

	// +1200ms：步骤1(+0)和步骤2(+200)的桶已超过1000ms窗口，应过期
	now = baseTime + 1200
	value = slidingWindow.AddAndGetCurrent(now, now, 30)
	allocated += total - value
	fmt.Printf("left is %d\n", total-value)
	if value != 45 {
		t.Fatalf("value is %d, invalid", value)
	}

	// +1500ms
	now = baseTime + 1500
	value = slidingWindow.AddAndGetCurrent(now, now, 20)
	allocated += total - value
	fmt.Printf("left is %d\n", total-value)
	fmt.Printf("allocated is %d\n", allocated)
}

// TestSlidingWindow_SlideOne 使用手动传入时间值，避免 time.Sleep 精度问题导致的 flaky test
func TestSlidingWindow_SlideOne(t *testing.T) {
	var total uint32 = 100
	slidingWindow := NewSlidingWindow(1, 1000)

	// 使用固定基准时间，手动控制时间推进
	baseTime := int64(1000000)

	var allocated uint32
	var value uint32

	// +0ms
	now := baseTime
	value = slidingWindow.AddAndGetCurrent(now, now, 10)
	allocated += total - value
	fmt.Printf("left is %d\n", total-value)

	// +200ms（与步骤1在同一个1000ms桶内）
	now = baseTime + 200
	value = slidingWindow.AddAndGetCurrent(now, now, 40)
	allocated += total - value
	fmt.Printf("left is %d\n", total-value)

	// +1050ms（跨到新的1000ms桶，旧桶的10+40被清除，重新开始计数15）
	now = baseTime + 1050
	value = slidingWindow.AddAndGetCurrent(now, now, 15)
	allocated += total - value
	fmt.Printf("left is %d\n", total-value)

	// +1100ms（与步骤3在同一个1000ms桶内，counter=15+30=45）
	now = baseTime + 1100
	value = slidingWindow.AddAndGetCurrent(now, now, 30)
	allocated += total - value
	fmt.Printf("left is %d\n", total-value)
	if value != 45 {
		t.Fatalf("value is %d, invalid", value)
	}

	// +1400ms
	now = baseTime + 1400
	value = slidingWindow.AddAndGetCurrent(now, now, 20)
	allocated += total - value
	fmt.Printf("left is %d\n", total-value)
	fmt.Printf("allocated is %d\n", allocated)
}
