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

package plugin

import (
	"github.com/polarismesh/polaris-limit/pkg/utils"
	"github.com/google/uuid"
	"github.com/modern-go/reflect2"
	"sync"
	"sync/atomic"
	"time"
)

//接口调用统计内容
type RateLimitStatValue interface {
	//获取统计的Key
	GetStatKey(bool) interface{}
	//获取客户端IP字符串
	GetClientIPStr() string
	//获取命名空间
	GetNamespace() string
	//获取服务名
	GetService() string
	//获取接口名
	GetMethod() string
	//获取应用标识
	GetAppId() string
	//获取用户标识
	GetUin() string
	//获取自定义标签
	GetLabels() string
	//获取限流周期
	GetDuration() time.Duration
	//获取请求总数
	GetTotal() int64
	//获取曲线上报数据
	GetCurveData() RateLimitData
	//获取精度上报的数据
	GetPrecisionData() RateLimitData
	//获取超时周期
	GetExpireDuration() int64
	//设置超时周期
	SetExpireDuration(value int64)
	//获取最近一次更新时间，单位毫秒
	GetLastUpdateTime() int64
	//设置最近一次更新时间
	SetLastUpdateTime(now int64)
	//复制对象，并重置变量值
	Clone() RateLimitStatValue
}

//限流统计数据
type RateLimitData interface {
	//获取通过数
	GetPassed() int64
	//增加通过数
	AddPassed(delta int64)
	//获取限流数
	GetLimited() int64
	//增加限流数
	AddLimited(delta int64)
	//增加统计数据
	AddValues(data RateLimitData)
	//初始化数据结构
	InitValues(passed int64, limited int64, lastFetchTime int64)
	//设置最后一次拉取时间
	SetLastFetchTime(timeMs int64)
	//获得最后一次拉取时间
	GetLastFetchTime() int64
}

var (
	rateLimitStatValueV2Pool = &sync.Pool{}
)

//池子获取RateLimitStatValue
func PoolGetRateLimitStatValueV2() *RateLimitStatValueV2 {
	value := rateLimitStatValueV2Pool.Get()
	if !reflect2.IsNil(value) {
		return value.(*RateLimitStatValueV2)
	}
	return &RateLimitStatValueV2{}
}

//池子回收RateLimitStatValue
func PoolPutRateLimitStatValueV2(value *RateLimitStatValueV2) {
	rateLimitStatValueV2Pool.Put(value)
}

//限流key，只带入counter信息
type RateLimitStatCounterKeyV2 struct {
	//新版本上报的API有CounterKey
	CounterKey uint32
}

//限流key
type RateLimitStatKeyV2 struct {
	RateLimitStatCounterKeyV2
	ClientIP utils.IPAddress
}

//用于曲线上报的统计数据
type rateLimitData struct {
	Passed  int64
	Limited int64
	//最近一次拉取时间，单位毫秒
	LastFetchTime int64
}

//获取通过数
func (r *rateLimitData) GetPassed() int64 {
	return atomic.LoadInt64(&r.Passed)
}

//增加通过数
func (r *rateLimitData) AddPassed(delta int64) {
	atomic.AddInt64(&r.Passed, delta)
}

//获取限流数
func (r *rateLimitData) GetLimited() int64 {
	return atomic.LoadInt64(&r.Limited)
}

//获取限流数
func (r *rateLimitData) AddLimited(delta int64) {
	atomic.AddInt64(&r.Limited, delta)
}

//增加统计数据
func (r *rateLimitData) AddValues(data RateLimitData) {
	r.AddPassed(data.GetPassed())
	r.AddLimited(data.GetLimited())
}

//设置最后一次拉取时间
func (r *rateLimitData) SetLastFetchTime(timeMs int64) {
	atomic.StoreInt64(&r.LastFetchTime, timeMs)
}

//获得最后一次拉取时间
func (r *rateLimitData) GetLastFetchTime() int64 {
	return atomic.LoadInt64(&r.LastFetchTime)
}

//设计统计数据
func (r *rateLimitData) InitValues(passed int64, limited int64, lastFetchTime int64) {
	r.Passed = passed
	r.Limited = limited
	r.LastFetchTime = lastFetchTime
}

//限流value
type RateLimitStatValueV2 struct {
	StatKey       RateLimitStatKeyV2
	Total         int64
	CurveData     rateLimitData
	PrecisionData rateLimitData
	//使用CounterKey则需要填以下字段
	Namespace string
	Service   string
	Method    string
	AppId     string
	Uin       string
	Labels    string
	Duration  time.Duration
	//超时间隔，单位毫秒
	ExpireDuration int64
	//下面是结构复用的预留字段，传入时无需填入
	LastUpdateTime int64
}

//获取统计的Key
func (r RateLimitStatValueV2) GetStatKey(counterOnly bool) interface{} {
	if counterOnly {
		return r.StatKey.RateLimitStatCounterKeyV2
	}
	return r.StatKey
}

//获取客户端IP字符串
func (r RateLimitStatValueV2) GetClientIPStr() string {
	return r.StatKey.ClientIP.String()
}

//获取命名空间
func (r RateLimitStatValueV2) GetNamespace() string {
	return r.Namespace
}

//获取服务名
func (r RateLimitStatValueV2) GetService() string {
	return r.Service
}

//获取接口名
func (r RateLimitStatValueV2) GetMethod() string {
	return r.Method
}

//获取应用标识
func (r RateLimitStatValueV2) GetAppId() string {
	return r.AppId
}

//获取用户标识
func (r RateLimitStatValueV2) GetUin() string {
	return r.Uin
}

//获取自定义标签
func (r RateLimitStatValueV2) GetLabels() string {
	return r.Labels
}

//获取限流周期
func (r RateLimitStatValueV2) GetDuration() time.Duration {
	return r.Duration
}

//获取请求总数
func (r *RateLimitStatValueV2) GetTotal() int64 {
	return atomic.LoadInt64(&r.Total)
}

//获取通过数
func (r *RateLimitStatValueV2) GetCurveData() RateLimitData {
	return &r.CurveData
}

//获取限流数
func (r *RateLimitStatValueV2) GetPrecisionData() RateLimitData {
	return &r.PrecisionData
}

//获取超时周期
func (r *RateLimitStatValueV2) GetExpireDuration() int64 {
	return atomic.LoadInt64(&r.ExpireDuration)
}

//设置超时周期
func (r *RateLimitStatValueV2) SetExpireDuration(value int64) {
	atomic.StoreInt64(&r.ExpireDuration, value)
}

//获取最近一次更新时间,单位毫秒
func (r *RateLimitStatValueV2) GetLastUpdateTime() int64 {
	return atomic.LoadInt64(&r.LastUpdateTime)
}

//设置最近一次更新时间
func (r *RateLimitStatValueV2) SetLastUpdateTime(now int64) {
	atomic.StoreInt64(&r.LastUpdateTime, now)
}

//复制对象，并重置变量值
func (r *RateLimitStatValueV2) cloneV2() *RateLimitStatValueV2 {
	savedValue := *r
	return &savedValue
}

//复制对象，并重置变量值
func (r *RateLimitStatValueV2) Clone() RateLimitStatValue {
	return r.cloneV2()
}

var (
	rateLimitStatValueV1Pool = &sync.Pool{}
)

//池子获取RateLimitStatValue
func PoolGetRateLimitStatValueV1() *RateLimitStatValueV1 {
	value := rateLimitStatValueV1Pool.Get()
	if !reflect2.IsNil(value) {
		return value.(*RateLimitStatValueV1)
	}
	return &RateLimitStatValueV1{}
}

//池子回收RateLimitStatValue
func PoolPutRateLimitStatValueV1(value *RateLimitStatValueV1) {
	rateLimitStatValueV1Pool.Put(value)
}

//限流key v1，只带入counter信息
type RateLimitStatCounterKeyV1 struct {
	Namespace string
	Service   string
	Method    string
	AppId     string
	Uin       string
	Labels    string
	Duration  time.Duration
}

//限流key v1
type RateLimitStatKeyV1 struct {
	RateLimitStatCounterKeyV1
	ClientIP utils.IPAddress
}

//限流value v1
type RateLimitStatValueV1 struct {
	StatKey       RateLimitStatKeyV1
	Total         int64
	CurveData     rateLimitData
	PrecisionData rateLimitData
	//下面是结构复用的预留字段，传入时无需填入
	LastUpdateTime int64
	ExpireDuration int64
}

//获取统计的Key
func (r RateLimitStatValueV1) GetStatKey(counterOnly bool) interface{} {
	if counterOnly {
		return r.StatKey.RateLimitStatCounterKeyV1
	}
	return r.StatKey
}

//获取客户端IP字符串
func (r RateLimitStatValueV1) GetClientIPStr() string {
	return r.StatKey.ClientIP.String()
}

//获取命名空间
func (r RateLimitStatValueV1) GetNamespace() string {
	return r.StatKey.Namespace
}

//获取服务名
func (r RateLimitStatValueV1) GetService() string {
	return r.StatKey.Service
}

//获取接口名
func (r RateLimitStatValueV1) GetMethod() string {
	return r.StatKey.Method
}

//获取应用标识
func (r RateLimitStatValueV1) GetAppId() string {
	return r.StatKey.AppId
}

//获取用户标识
func (r RateLimitStatValueV1) GetUin() string {
	return r.StatKey.Uin
}

//获取自定义标签
func (r RateLimitStatValueV1) GetLabels() string {
	return r.StatKey.Labels
}

//获取限流周期
func (r RateLimitStatValueV1) GetDuration() time.Duration {
	return r.StatKey.Duration
}

//获取请求总数
func (r RateLimitStatValueV1) GetTotal() int64 {
	return r.Total
}

//获取通过数
func (r *RateLimitStatValueV1) GetCurveData() RateLimitData {
	return &r.CurveData
}

//获取限流数
func (r *RateLimitStatValueV1) GetPrecisionData() RateLimitData {
	return &r.PrecisionData
}

//获取超时周期
func (r *RateLimitStatValueV1) GetExpireDuration() int64 {
	return atomic.LoadInt64(&r.ExpireDuration)
}

//设置超时周期
func (r *RateLimitStatValueV1) SetExpireDuration(value int64) {
	atomic.StoreInt64(&r.ExpireDuration, value)
}

//获取最近一次更新时间
func (r *RateLimitStatValueV1) GetLastUpdateTime() int64 {
	return atomic.LoadInt64(&r.LastUpdateTime)
}

//设置最近一次更新时间
func (r *RateLimitStatValueV1) SetLastUpdateTime(now int64) {
	atomic.StoreInt64(&r.LastUpdateTime, now)
}

//复制对象，并重置变量值
func (r *RateLimitStatValueV1) cloneV1() *RateLimitStatValueV1 {
	savedValue := *r
	return &savedValue
}

//复制对象，并重置变量值
func (r *RateLimitStatValueV1) Clone() RateLimitStatValue {
	return r.cloneV1()
}

//采集器
type RateLimitStatCollector interface {
	//获取ID信息
	ID() string
	//拷贝并解引用
	DumpAndExpire(valueSlice []RateLimitStatValue, enableExpire bool) ([]RateLimitStatValue, int)
}

//限流上报收集齐
type RateLimitStatCollectorV1 struct {
	//收集器ID，每个stream一个，通过uuid生成
	id      string
	values  map[RateLimitStatKeyV1]*RateLimitStatValueV1
	rwMutex *sync.RWMutex
}

//构造函数
func NewRateLimitStatCollectorV1() *RateLimitStatCollectorV1 {
	return &RateLimitStatCollectorV1{
		id:      uuid.New().String(),
		values:  make(map[RateLimitStatKeyV1]*RateLimitStatValueV1),
		rwMutex: &sync.RWMutex{},
	}
}

//获取ID信息
func (r *RateLimitStatCollectorV1) ID() string {
	return r.id
}

//阻塞新增值对象
func (r *RateLimitStatCollectorV1) createStatValueV1(value *RateLimitStatValueV1) *RateLimitStatValueV1 {
	r.rwMutex.Lock()
	defer r.rwMutex.Unlock()
	existValue, ok := r.values[value.StatKey]
	if !ok {
		createValue := value.cloneV1()
		r.values[createValue.StatKey] = createValue
	}
	return existValue
}

//新增值信息
func (r *RateLimitStatCollectorV1) AddStatValueV1(value *RateLimitStatValueV1) {
	var existValue *RateLimitStatValueV1
	var ok bool
	r.rwMutex.RLock()
	existValue, ok = r.values[value.StatKey]
	r.rwMutex.RUnlock()
	if !ok {
		existValue = r.createStatValueV1(value)
	}
	if nil != existValue {
		existValue.GetPrecisionData().AddValues(value.GetPrecisionData())
		existValue.GetCurveData().AddValues(value.GetCurveData())
		existValue.SetExpireDuration(value.GetExpireDuration())
		existValue.SetLastUpdateTime(value.GetLastUpdateTime())
	}
}

//拷贝并解引用
func (r *RateLimitStatCollectorV1) DumpAndExpire(
	valueSlice []RateLimitStatValue, enableExpire bool) ([]RateLimitStatValue, int) {
	var expiredKeys []RateLimitStatKeyV1
	r.rwMutex.RLock()
	if len(valueSlice) < len(r.values) {
		valueSlice = make([]RateLimitStatValue, len(r.values))
	}
	nowMilli := utils.CurrentMillisecond()
	var idx int
	for key, value := range r.values {
		valueSlice[idx] = value
		idx++
		if enableExpire && nowMilli-value.GetLastUpdateTime() > value.GetExpireDuration() {
			expiredKeys = append(expiredKeys, key)
		}
	}
	r.rwMutex.RUnlock()
	if len(expiredKeys) > 0 {
		r.rwMutex.Lock()
		for _, key := range expiredKeys {
			delete(r.values, key)
		}
		r.rwMutex.Unlock()
	}
	return valueSlice, idx
}

//限流上报收集齐
type RateLimitStatCollectorV2 struct {
	//收集器ID，每个stream一个，通过uuid生成
	id      string
	values  map[RateLimitStatKeyV2]*RateLimitStatValueV2
	rwMutex *sync.RWMutex
}

//构造函数
func NewRateLimitStatCollectorV2() *RateLimitStatCollectorV2 {
	return &RateLimitStatCollectorV2{
		id:      uuid.New().String(),
		values:  make(map[RateLimitStatKeyV2]*RateLimitStatValueV2),
		rwMutex: &sync.RWMutex{},
	}
}

//获取ID信息
func (r *RateLimitStatCollectorV2) ID() string {
	return r.id
}

//阻塞新增值对象
func (r *RateLimitStatCollectorV2) createStatValueV2(value *RateLimitStatValueV2) *RateLimitStatValueV2 {
	r.rwMutex.Lock()
	defer r.rwMutex.Unlock()
	existValue, ok := r.values[value.StatKey]
	if !ok {
		createValue := value.cloneV2()
		r.values[createValue.StatKey] = createValue
		return nil
	}
	return existValue
}

//新增值信息
func (r *RateLimitStatCollectorV2) AddStatValueV2(value *RateLimitStatValueV2) {
	var existValue *RateLimitStatValueV2
	var ok bool
	r.rwMutex.RLock()
	existValue, ok = r.values[value.StatKey]
	r.rwMutex.RUnlock()
	if !ok {
		existValue = r.createStatValueV2(value)
	}
	if nil != existValue {
		existValue.GetPrecisionData().AddValues(value.GetPrecisionData())
		existValue.GetCurveData().AddValues(value.GetCurveData())
		existValue.SetExpireDuration(value.GetExpireDuration())
		existValue.SetLastUpdateTime(value.GetLastUpdateTime())
	}
}

//拷贝并解引用
func (r *RateLimitStatCollectorV2) DumpAndExpire(
	valueSlice []RateLimitStatValue, enableExpire bool) ([]RateLimitStatValue, int) {
	var expiredKeys []RateLimitStatKeyV2
	r.rwMutex.RLock()
	if len(valueSlice) < len(r.values) {
		valueSlice = make([]RateLimitStatValue, len(r.values))
	}
	nowMilli := utils.CurrentMillisecond()
	var idx int
	for key, value := range r.values {
		valueSlice[idx] = value
		idx++
		if enableExpire && nowMilli-value.GetLastUpdateTime() > value.GetExpireDuration() {
			expiredKeys = append(expiredKeys, key)
		}
	}
	r.rwMutex.RUnlock()
	if len(expiredKeys) > 0 {
		r.rwMutex.Lock()
		for _, key := range expiredKeys {
			delete(r.values, key)
		}
		r.rwMutex.Unlock()
	}
	return valueSlice, idx
}
