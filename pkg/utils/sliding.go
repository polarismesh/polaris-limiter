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
	"sync"
	"sync/atomic"
)

//窗口淘汰函数
type WindowHandlerFunc func(windowStart int64, counterValue int64)

//创建滑窗
func NewSlidingWindow(slideCount int, intervalMs int) *SlidingWindow {
	slidingWindow := &SlidingWindow{}
	slidingWindow.intervalMs = intervalMs
	slidingWindow.slideCount = slideCount
	slidingWindow.mutex = &sync.Mutex{}
	slidingWindow.windowLengthMs = intervalMs / slideCount
	slidingWindow.windowArray = make([]*Window, slideCount)
	slidingWindow.stableValue = &StableSlideValue{window: slidingWindow}
	for i := 0; i < slideCount; i++ {
		slidingWindow.windowArray[i] = &Window{}
	}
	return slidingWindow
}

//计算起始滑窗
func (s *SlidingWindow) calculateWindowStart(curTimeMs int64) int64 {
	return curTimeMs - curTimeMs%int64(s.windowLengthMs)
}

//计算时间下标
func (s *SlidingWindow) calculateTimeIdx(curTimeMs int64) int {
	timeId := curTimeMs / int64(s.windowLengthMs)
	return int(timeId % int64(s.slideCount))
}

//当前窗口,返回是否出现了时间倒退
func (s *SlidingWindow) currentWindow(curTimeMs int64, reset bool) *Window {
	idx := s.calculateTimeIdx(curTimeMs)
	windowStart := s.calculateWindowStart(curTimeMs)
	oldWindow := s.windowArray[idx]
	oldWindowStart := atomic.LoadInt64(&oldWindow.windowStart)
	if oldWindowStart == windowStart {
		return oldWindow
	} else if !reset {
		return nil
	} else {
		s.mutex.Lock()
		expiredCount := oldWindow.reset(oldWindowStart, windowStart)
		s.stableValue.Reload(windowStart, oldWindowStart, expiredCount)
		s.mutex.Unlock()
		return oldWindow
	}
}

//原子增加，并返回当前bucket
func (s *SlidingWindow) AddAndGetCurrent(clientTimeMs int64, serverTimeMs int64, counter uint32) uint32 {
	window := s.currentWindow(serverTimeMs, true)
	//判断客户端上报时间是否与当前服务端时间一致，不一致则不使用该配额
	clientStartTimeMs := s.calculateWindowStart(clientTimeMs)
	var delta uint32
	if atomic.LoadInt64(&window.windowStart) == clientStartTimeMs {
		delta = window.addAndGet(counter)
	} else {
		delta = window.addAndGet(0)
	}
	stableCount := s.stableValue.Value()
	return stableCount + delta
}

//非当前时间点的静态滑窗值
type StableSlideValue struct {
	curStartTimeMs int64
	//不包含当前时间的值对象
	value int64
	//当前滑窗对象
	window *SlidingWindow
}

//获取值
func (s *StableSlideValue) Value() uint32 {
	if s.value < 0 {
		return 0
	}
	return uint32(s.value)
}

//重载静态值
func (s *StableSlideValue) Reload(nextStartMs int64, expiredStartMs int64, expireValue uint32) {
	if len(s.window.windowArray) == 1 {
		return
	}
	timePassed := nextStartMs - s.curStartTimeMs
	if timePassed <= 0 || timePassed >= int64(s.window.intervalMs) {
		//时间倒退或者超时未上报，重新开始计算
		s.curStartTimeMs = nextStartMs
		s.value = 0
		return
	}
	expireTimePassed := s.curStartTimeMs - expiredStartMs
	if timePassed == int64(s.window.windowLengthMs) {
		if expireTimePassed > 0 && expireTimePassed < int64(s.window.intervalMs) {
			s.value -= int64(expireValue)
		}
		//只相隔一个窗口
		//加上之前的值
		curBucket := s.window.currentWindow(s.curStartTimeMs, false)
		if nil != curBucket {
			s.value += int64(atomic.LoadUint32(&curBucket.counterValue))
			s.curStartTimeMs = nextStartMs
			return
		}
	}
	//兜底，全遍历
	s.value = 0
	for _, bucket := range s.window.windowArray {
		diffTime := nextStartMs - bucket.windowStart
		if diffTime > 0 && diffTime < int64(s.window.intervalMs) {
			s.value += int64(atomic.LoadUint32(&bucket.counterValue))
		}
	}
	s.curStartTimeMs = nextStartMs
}

//滑窗通用实现
type SlidingWindow struct {
	//单个窗口长度
	windowLengthMs int
	//所有窗口总长度
	intervalMs int
	//更新锁
	mutex *sync.Mutex
	//滑窗列表
	windowArray []*Window
	//滑窗数
	slideCount int
	//静态滑窗总数
	stableValue *StableSlideValue
}

//重置窗口
func (w *Window) reset(oldWindowStart int64, windowStart int64) uint32 {
	if atomic.CompareAndSwapInt64(&w.windowStart, oldWindowStart, windowStart) {
		latestValue := atomic.LoadUint32(&w.counterValue)
		atomic.StoreUint32(&w.counterValue, 0)
		return latestValue
	}
	return 0
}

//单个窗口
type Window struct {
	//起始时间
	windowStart int64
	//计数器
	counterValue uint32
}

//原子增加
func (w *Window) addAndGet(counter uint32) uint32 {
	return atomic.AddUint32(&w.counterValue, counter)
}
