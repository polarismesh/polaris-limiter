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

func TestSlidingWindow_SlideFive(t *testing.T) {
	var total uint32 = 100
	slidingWindow := NewSlidingWindow(5, 1000)

	var allocated uint32
	var value uint32
	value = slidingWindow.AddAndGetCurrent(CurrentMillisecond(), CurrentMillisecond(), 10)
	allocated += total - value
	fmt.Printf("left is %d\n", total-value)
	time.Sleep(200 * time.Millisecond)
	value = slidingWindow.AddAndGetCurrent(CurrentMillisecond(), CurrentMillisecond(), 40)
	allocated += total - value
	fmt.Printf("left is %d\n", total-value)
	time.Sleep(500 * time.Millisecond)
	value = slidingWindow.AddAndGetCurrent(CurrentMillisecond(), CurrentMillisecond(), 15)
	allocated += total - value
	fmt.Printf("left is %d\n", total-value)
	time.Sleep(500 * time.Millisecond)
	value = slidingWindow.AddAndGetCurrent(CurrentMillisecond(), CurrentMillisecond(), 30)
	allocated += total - value
	fmt.Printf("left is %d\n", total-value)
	if value != 45 {
		t.Fatalf("value is %d, invalid", value)
	}
	time.Sleep(300 * time.Millisecond)
	value = slidingWindow.AddAndGetCurrent(CurrentMillisecond(), CurrentMillisecond(), 20)
	allocated += total - value
	fmt.Printf("left is %d\n", total-value)
	fmt.Printf("allocated is %d\n", allocated)
}

func TestSlidingWindow_SlideOne(t *testing.T) {
	var total uint32 = 100
	slidingWindow := NewSlidingWindow(1, 1000)

	var allocated uint32
	var value uint32
	value = slidingWindow.AddAndGetCurrent(CurrentMillisecond(), CurrentMillisecond(), 10)
	allocated += total - value
	fmt.Printf("left is %d\n", total-value)
	time.Sleep(200 * time.Millisecond)
	value = slidingWindow.AddAndGetCurrent(CurrentMillisecond(), CurrentMillisecond(), 40)
	allocated += total - value
	fmt.Printf("left is %d\n", total-value)
	time.Sleep(600 * time.Millisecond)
	value = slidingWindow.AddAndGetCurrent(CurrentMillisecond(), CurrentMillisecond(), 15)
	allocated += total - value
	fmt.Printf("left is %d\n", total-value)
	time.Sleep(200 * time.Millisecond)
	value = slidingWindow.AddAndGetCurrent(CurrentMillisecond(), CurrentMillisecond(), 30)
	allocated += total - value
	fmt.Printf("left is %d\n", total-value)
	if value != 45 {
		t.Fatalf("value is %d, invalid", value)
	}
	time.Sleep(300 * time.Millisecond)
	value = slidingWindow.AddAndGetCurrent(CurrentMillisecond(), CurrentMillisecond(), 20)
	allocated += total - value
	fmt.Printf("left is %d\n", total-value)
	fmt.Printf("allocated is %d\n", allocated)
}
