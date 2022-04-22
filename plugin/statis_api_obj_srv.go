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
	"sync"
	"sync/atomic"
	"time"

	"github.com/modern-go/reflect2"
)

type MsgType uint32

const (
	// MsgSync 同步消息
	MsgSync MsgType = iota
	// MsgPush 推送消息
	MsgPush
)

// String 文本输出
func (m MsgType) String() string {
	if m == MsgPush {
		return "push"
	}
	return "sync"
}

// APICallStatKey v2版本的服务调用
type APICallStatKey struct {
	APIKey   APIKey
	Code     uint32
	MsgType  MsgType
	Duration time.Duration
}

// APICallStatValueImpl v2版本的服务调用
type APICallStatValueImpl struct {
	StatKey APICallStatKey
	// 接口调用时延，单位：us
	Latency int64
	// 下面是结构复用的预留字段，传入时无需填入
	ReqCount       int32
	MaxLatency     int64
	LastUpdateTime int64
}

// GetAPIName 获取接口名
func (a APICallStatValueImpl) GetAPIName() string {
	return GetAPIKeyPresent(a.StatKey.APIKey)
}

// GetCode 获取返回码
func (a APICallStatValueImpl) GetCode() uint32 {
	return a.StatKey.Code
}

// GetMsgType 获取消息类型
func (a APICallStatValueImpl) GetMsgType() MsgType {
	return a.StatKey.MsgType
}

// GetLatency 获取接口调用时延
func (a *APICallStatValueImpl) GetLatency() int64 {
	return atomic.LoadInt64(&a.Latency)
}

// AddLatency 增加时延总数
func (a *APICallStatValueImpl) AddLatency(delta int64) {
	atomic.AddInt64(&a.Latency, delta)
}

// GetDuration 获取标签值
func (a *APICallStatValueImpl) GetDuration() string {
	return a.StatKey.Duration.String()
}

// GetReqCount 获取请求总数
func (a *APICallStatValueImpl) GetReqCount() int32 {
	return atomic.LoadInt32(&a.ReqCount)
}

// AddReqCount 增加请求总数
func (a *APICallStatValueImpl) AddReqCount(delta int32) {
	atomic.AddInt32(&a.ReqCount, delta)
}

// GetMaxLatency 获取最大时延
func (a *APICallStatValueImpl) GetMaxLatency() int64 {
	return atomic.LoadInt64(&a.MaxLatency)
}

// CasMaxLatency CAS设置最大时延
func (a *APICallStatValueImpl) CasMaxLatency(value int64) {
	for {
		lastValue := atomic.LoadInt64(&a.MaxLatency)
		if lastValue >= value {
			return
		}
		if atomic.CompareAndSwapInt64(&a.MaxLatency, lastValue, value) {
			return
		}
	}
}

// ResetMaxLatency 重置最大时延
func (a *APICallStatValueImpl) ResetMaxLatency(lastValue int64) {
	atomic.CompareAndSwapInt64(&a.MaxLatency, lastValue, 0)
}

// GetLastUpdateTime 获取最近一次更新时间
func (a *APICallStatValueImpl) GetLastUpdateTime() int64 {
	return atomic.LoadInt64(&a.LastUpdateTime)
}

// SetLastUpdateTime 设置最近一次更新时间
func (a *APICallStatValueImpl) SetLastUpdateTime(now int64) {
	atomic.StoreInt64(&a.LastUpdateTime, now)
}

// GetStatKey 获取统计的Key
func (a *APICallStatValueImpl) GetStatKey() APICallStatKey {
	return a.StatKey
}

// Clone 复制对象，并重置变量值
func (a *APICallStatValueImpl) Clone() APICallStatValue {
	savedValue := *a
	savedValue.MaxLatency = savedValue.Latency
	return &savedValue
}

// APICallStatValue 接口调用统计内容
type APICallStatValue interface {
	// GetStatKey 获取统计的Key
	GetStatKey() APICallStatKey
	// GetAPIName 获取接口名
	GetAPIName() string
	// GetCode 获取返回码
	GetCode() uint32
	// GetMsgType 获取消息类型
	GetMsgType() MsgType
	// GetDuration 获取限流周期
	GetDuration() string
	// GetReqCount 获取请求总数
	GetReqCount() int32
	// AddReqCount 增加请求总数
	AddReqCount(int32)
	// GetLatency 获取接口调用时延
	GetLatency() int64
	// AddLatency 增加时延总数
	AddLatency(int64)
	// GetMaxLatency 获取最大时延
	GetMaxLatency() int64
	// CasMaxLatency CAS设置最大时延
	CasMaxLatency(int64)
	// ResetMaxLatency 重置最大时延
	ResetMaxLatency(lastValue int64)
	// GetLastUpdateTime 获取最近一次更新时间
	GetLastUpdateTime() int64
	// SetLastUpdateTime 设置最近一次更新时间
	SetLastUpdateTime(now int64)
	// Clone 复制对象，并重置变量值
	Clone() APICallStatValue
}

var (
	apiCallStatValueImplPool = &sync.Pool{}
)

// PoolGetAPICallStatValueImpl 池子获取RateLimitStatValue
func PoolGetAPICallStatValueImpl() *APICallStatValueImpl {
	value := apiCallStatValueImplPool.Get()
	if !reflect2.IsNil(value) {
		return value.(*APICallStatValueImpl)
	}
	return &APICallStatValueImpl{}
}

// PoolPutAPICallStatValueImpl 池子回收RateLimitStatValue
func PoolPutAPICallStatValueImpl(value *APICallStatValueImpl) {
	apiCallStatValueImplPool.Put(value)
}

type APIKey uint32

const (
	InitQuotaV1 APIKey = iota
	AcquireQuotaV1
	InitQuotaV2
	AcquireQuotaV2
	BatchInitQuotaV2
	BatchAcquireQuotaV2
)

var (
	apiKeyPresent = map[APIKey]string{
		InitQuotaV2:         "v2.RateLimitGRPCV2/Service/Init",
		AcquireQuotaV2:      "v2.RateLimitGRPCV2/Service/Acquire",
		BatchInitQuotaV2:    "v2.RateLimitGRPCV2/Service/BatchInit",
		BatchAcquireQuotaV2: "v2.RateLimitGRPCV2/Service/BatchAcquire",
		InitQuotaV1:         "v1.RateLimitGRPC/InitializeQuota",
		AcquireQuotaV1:      "v1.RateLimitGRPC/AcquireQuota",
	}
)

// GetAPIKeyPresent 获取API文本
func GetAPIKeyPresent(key APIKey) string {
	return apiKeyPresent[key]
}
