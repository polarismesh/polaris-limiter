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

package ratelimitv2

import (
	"sync"
	"sync/atomic"
	"time"

	apiv2 "github.com/polarismesh/polaris-limiter/pkg/api/v2"
	"github.com/polarismesh/polaris-limiter/pkg/log"
	"github.com/polarismesh/polaris-limiter/pkg/utils"
	"github.com/polarismesh/polaris-limiter/plugin"
)

// defaultMode 默认为批量抢占模式
const defaultMode = apiv2.Mode_BATCH_OCCUPY

// InitRequest 初始化请求
type InitRequest struct {
	MaxAmount      uint32
	SlideCount     int
	Sender         Client
	Duration       time.Duration
	ExpireDuration time.Duration
	AmountMode     apiv2.QuotaMode
	PushManager    PushManager
}

// CounterV2 计数器
type CounterV2 interface {
	// CounterKey 获取标识
	CounterKey() uint32
	// Identifier 计数器标识
	Identifier() *CounterIdentifier
	// Reload 刷新配额值
	Reload(InitRequest)
	// AcquireQuota 原子增加
	AcquireQuota(client Client, quotaSum *apiv2.QuotaSum,
		nowMs int64, startMicro int64, collector *plugin.RateLimitStatCollectorV2) *apiv2.QuotaLeft
	// SumQuota 获取当前quota总量
	SumQuota(client Client, timestampMs int64) *apiv2.QuotaLeft
	// IsExpired 是否已过期
	IsExpired() bool
	// PushMessage 推送消息
	PushMessage(*PushValue)
	// ExpireDuration 过期周期
	ExpireDuration() time.Duration
	// LastUpdateTime 最后一次更新时间
	LastUpdateTime() int64
	// Update 更新时间戳
	Update()
	// Mode 配额分配模式
	Mode() apiv2.Mode
	// MaxAmount 最大配额数
	MaxAmount() uint32
	// ClientCount 客户端数量
	ClientCount() uint32
	// DelSender 删除客户端
	DelSender(sender Client, expired bool)
	// CleanupSenders 清理没用的客户端
	CleanupSenders(bool)
	// UpdateClientSendTime 更新对应客户端最近一次发送时间
	UpdateClientSendTime(sender Client, sendTimeMicro int64)
}

// CounterClients 计算器管理的客户端数据结构
type CounterClients struct {
	// 同步锁
	mutex *sync.Mutex
	// 订阅该key的客户端，key是clientKey, value是client对象
	clientSenders *sync.Map
	// 客户端主键，clientId <--> clientKey
	clientIds map[string]map[uint32]bool
	// 客户端数量缓存
	clientCount uint32
}

// Init 初始化
func (cc *CounterClients) Init() {
	cc.mutex = &sync.Mutex{}
	cc.clientSenders = &sync.Map{}
	cc.clientIds = make(map[string]map[uint32]bool)
}

// AddSender 增加调用者
func (cc *CounterClients) AddSender(sender Client, counter *counterV2) {
	cc.mutex.Lock()
	defer cc.mutex.Unlock()
	curTime := utils.CurrentMillisecond()
	// 上报
	timeStr := utils.TimestampMsToUtcIso8601(curTime)
	// 需要更新sender
	cc.clientSenders.Store(sender.ClientKey(), &ClientSendTime{
		curClient: sender,
	})
	clientKeys, ok := cc.clientIds[sender.ClientId()]
	if !ok {
		clientKeys = make(map[uint32]bool, 0)
		clientKeys[sender.ClientKey()] = true
		cc.clientIds[sender.ClientId()] = clientKeys
	}
	clientCount := uint32(len(cc.clientIds))
	if !ok {
		// 记录客户端新增事件
		clientEvent := &CounterClientUpdateEvent{
			TimeNumber:  timeStr,
			EventType:   EventTypeCounterClientUpdate,
			Namespace:   counter.identifier.Namespace,
			Service:     counter.identifier.Service,
			Labels:      counter.identifier.Labels,
			Duration:    counter.identifier.Duration.String(),
			ClientId:    sender.ClientId(),
			IPAddress:   sender.ClientIP().String(),
			Action:      ActionAdd,
			ClientCount: int(clientCount),
		}
		statics.AddEventToLog(clientEvent)
	}
	atomic.StoreUint32(&cc.clientCount, clientCount)
	counter.reloadMaxAmount(timeStr, clientCount, sender.ClientId())
}

// ClientCount 获取客户端数量快照
func (cc *CounterClients) ClientCount() uint32 {
	return atomic.LoadUint32(&cc.clientCount)
}

// DelSender 删除调用者
func (cc *CounterClients) DelSender(sender Client, counter *counterV2, counterExpired bool) {
	cc.mutex.Lock()
	defer cc.mutex.Unlock()
	timeStr := utils.TimestampMsToUtcIso8601(utils.CurrentMillisecond())
	clientKeys, ok := cc.clientIds[sender.ClientId()]
	if !ok {
		// 已经不存在或者已经被置换，则不进行删除，一般不会出现
		log.Warnf("[RateLimit]clientId %s not exist when del sender %s", sender.ClientId(), sender.ClientKey())
		return
	}
	if !counterExpired && !sender.IsDetached() {
		// 未过期的客户端，而且未被解引用，不删除
		return
	}
	cc.clientSenders.Delete(sender.ClientKey())
	delete(clientKeys, sender.ClientKey())
	if len(clientKeys) == 0 {
		delete(cc.clientIds, sender.ClientId())
	}
	clientCount := uint32(len(cc.clientIds))
	lastClientCount := atomic.SwapUint32(&cc.clientCount, clientCount)
	if clientCount != lastClientCount {
		// 上报
		clientEvent := &CounterClientUpdateEvent{
			TimeNumber:  timeStr,
			EventType:   EventTypeCounterClientUpdate,
			Namespace:   counter.identifier.Namespace,
			Service:     counter.identifier.Service,
			Labels:      counter.identifier.Labels,
			Duration:    counter.identifier.Duration.String(),
			ClientId:    sender.ClientId(),
			IPAddress:   sender.ClientIP().String(),
			Action:      ActionDelete,
			ClientCount: int(clientCount),
		}
		statics.AddEventToLog(clientEvent)
		counter.reloadMaxAmount(timeStr, clientCount, sender.ClientId())
	}
}

// 计数器实现
type counterV2 struct {
	// 计数器的唯一标识
	identifier *CounterIdentifier
	// 用于上报解析的字段信息
	subLabels *utils.SubLabels
	// 计数器的整数标识
	counterKey uint32
	// 计数器的度量周期
	duration time.Duration
	// 超时周期
	expireDurationMilli int64
	// 最后一次更新时间
	lastMTimeMilli int64
	// 规则中设置的最大配额
	ruleMaxAmount uint32
	// 实际生效的最大配额
	maxAmount uint32
	// 阈值模式，全局模式还是单机均摊模式
	amountMode int32
	// 配额已经用完
	quotaUsedOff uint32
	// 配额分配器
	allocator QuotaAllocator
	// 计数器客户端集合
	counterClients CounterClients
}

// NewCounterV2 创建计数器
func NewCounterV2(counterKey uint32, identifier *CounterIdentifier, initRequest InitRequest) CounterV2 {
	counter := &counterV2{
		identifier: identifier,
		subLabels:  utils.ParseLabels(identifier.Labels),
		counterKey: counterKey,
		duration:   identifier.Duration,
	}
	counter.counterClients.Init()
	counter.Init(initRequest)
	return counter
}

// Init 初始化Counter
func (c *counterV2) Init(initRequest InitRequest) {
	c.ruleMaxAmount = initRequest.MaxAmount
	c.lastMTimeMilli = utils.CurrentMillisecond()
	c.duration = initRequest.Duration
	c.expireDurationMilli = initRequest.ExpireDuration.Milliseconds()
	c.amountMode = int32(initRequest.AmountMode)
	c.counterClients.AddSender(initRequest.Sender, c)
	c.allocator = NewOccupyAllocator(initRequest.SlideCount, int(c.duration.Milliseconds()), initRequest.PushManager, c)
	// 上报
	counterEvent := &CounterUpdateEvent{
		TimeNumber: utils.TimestampMsToUtcIso8601(c.LastUpdateTime()),
		EventType:  EventTypeCounterUpdate,
		Namespace:  c.Identifier().Namespace,
		Service:    c.Identifier().Service,
		Labels:     c.Identifier().Labels,
		Duration:   c.Identifier().Duration.String(),
		Action:     ActionAdd,
		Amount:     c.MaxAmount(),
	}
	statics.AddEventToLog(counterEvent)
}

// ExpireDuration 过期周期
func (c *counterV2) ExpireDuration() time.Duration {
	return time.Duration(c.expireDurationMilli) * time.Millisecond
}

// LastUpdateTime 最后一次更新时间
func (c *counterV2) LastUpdateTime() int64 {
	return atomic.LoadInt64(&c.lastMTimeMilli)
}

// Mode 配额分配模式
func (c *counterV2) Mode() apiv2.Mode {
	return defaultMode
}

// MaxAmount 最大配额数
func (c *counterV2) MaxAmount() uint32 {
	return atomic.LoadUint32(&c.maxAmount)
}

// Reload 刷新配额值
func (c *counterV2) Reload(initRequest InitRequest) {
	atomic.StoreUint32(&c.ruleMaxAmount, initRequest.MaxAmount)
	atomic.StoreInt64(&c.expireDurationMilli, initRequest.ExpireDuration.Milliseconds())
	atomic.StoreInt32(&c.amountMode, int32(initRequest.AmountMode))
	c.duration = initRequest.Duration
	lastMTimeMilli := utils.CurrentMillisecond()
	atomic.StoreInt64(&c.lastMTimeMilli, lastMTimeMilli)
	c.counterClients.AddSender(initRequest.Sender, c)
}

// Update 更新时间戳
func (c *counterV2) Update() {
	atomic.StoreInt64(&c.lastMTimeMilli, utils.CurrentMillisecond())
}

// DelSender 删除客户端
func (c *counterV2) DelSender(sender Client, counterExpired bool) {
	c.counterClients.DelSender(sender, c, counterExpired)
}

// CleanupSenders counter下线，清空所有的客户端
func (c *counterV2) CleanupSenders(expired bool) {
	c.counterClients.clientSenders.Range(func(key, value interface{}) bool {
		sender := value.(*ClientSendTime).curClient
		c.DelSender(sender, expired)
		return true
	})
}

// UpdateClientSendTime 更新客户端发送时间
func (c *counterV2) UpdateClientSendTime(sender Client, sendTimeMicro int64) {
	value, ok := c.counterClients.clientSenders.Load(sender.ClientKey())
	if !ok {
		return
	}
	clientSendTime := value.(*ClientSendTime)
	// 这个时间只影响推送，所以不保证原子性也影响不大
	clientSendTime.UpdateLastSendTime(sendTimeMicro)
}

// IsExpired 超时
func (c *counterV2) IsExpired() bool {
	timePassed := utils.CurrentMillisecond() - atomic.LoadInt64(&c.lastMTimeMilli)
	return timePassed > atomic.LoadInt64(&c.expireDurationMilli)
}

// AcquireQuota 获取超时配额
func (c *counterV2) AcquireQuota(client Client, quotaSum *apiv2.QuotaSum,
	timestampMs int64, startTimeMicro int64, collector *plugin.RateLimitStatCollectorV2) *apiv2.QuotaLeft {
	sumUsed := quotaSum.GetUsed()
	sumLimit := quotaSum.GetLimited()
	c.doQuotaStatReport(startTimeMicro, sumUsed, sumLimit, client, collector)
	return c.allocator.Allocate(client, quotaSum, timestampMs, startTimeMicro)
}

// SumQuota 汇总超时
func (c *counterV2) SumQuota(client Client, timestampMs int64) *apiv2.QuotaLeft {
	// 更新客户端时间戳
	return c.allocator.Allocate(client, &apiv2.QuotaSum{
		CounterKey: c.counterKey,
		Used:       0,
		Limited:    0,
	}, timestampMs, timestampMs*1e3)
}

// CounterKey 获取标识
func (c *counterV2) CounterKey() uint32 {
	return c.counterKey
}

// Identifier 计数器标识
func (c *counterV2) Identifier() *CounterIdentifier {
	return c.identifier
}

// 重新计算配额值
func (c *counterV2) reloadMaxAmount(timeStr string, clientCount uint32, clientId string) {
	ruleMaxAmount := atomic.LoadUint32(&c.ruleMaxAmount)
	amountMode := atomic.LoadInt32(&c.amountMode)
	var action string
	var curMaxAmount uint32
	if amountMode != int32(apiv2.QuotaMode_DIVIDE) {
		// 全局配额模式
		action = TriggerShareGlobal
		curMaxAmount = ruleMaxAmount
	} else {
		// 单机均摊模式
		action = TriggerShareEqually
		curMaxAmount = clientCount * ruleMaxAmount
	}
	lastMaxAmount := atomic.SwapUint32(&c.maxAmount, curMaxAmount)
	// if lastMaxAmount == curMaxAmount {
	//	//没有发生变化，则不上报日志
	//	return
	// }
	// 上报
	clientEvent := &QuotaChangeEvent{
		TimeNumber:    timeStr,
		EventType:     EventTypeQuotaChange,
		Namespace:     c.identifier.Namespace,
		Service:       c.identifier.Service,
		Labels:        c.identifier.Labels,
		Duration:      c.identifier.Duration.String(),
		Action:        action,
		LatestAmount:  lastMaxAmount,
		CurrentAmount: curMaxAmount,
		ClientId:      clientId,
	}
	statics.AddEventToLog(clientEvent)
}

// PushMessage 推送消息
func (c *counterV2) PushMessage(pushValue *PushValue) {
	c.counterClients.clientSenders.Range(func(key, value interface{}) bool {
		clientSendTime := value.(*ClientSendTime)
		sender := clientSendTime.curClient
		if sender.ClientId() == pushValue.ExcludeClient {
			return true
		}
		var sent bool
		var err error
		var spendTimeMicro = utils.CurrentMicrosecond() - pushValue.StartTimeMicro
		if sent, err = sender.SendAndUpdate(pushValue.Msg, clientSendTime, pushValue.MsgTimeMicro); nil != err {
			log.Errorf("fail to push response to client %d, addr %s, err is %v",
				sender.ClientKey(), sender.ClientIP(), err)
		}
		if !sent {
			return true
		}
		// 执行上报
		apiCallStatValue := plugin.PoolGetAPICallStatValueImpl()
		apiCallStatValue.StatKey.APIKey = plugin.AcquireQuotaV2
		apiCallStatValue.StatKey.Code = apiv2.GetErrorCode(pushValue.Msg)
		apiCallStatValue.StatKey.MsgType = plugin.MsgPush
		apiCallStatValue.StatKey.Duration = c.identifier.Duration
		apiCallStatValue.Latency = spendTimeMicro
		apiCallStatValue.ReqCount = 1
		statics.AddAPICall(apiCallStatValue)
		plugin.PoolPutAPICallStatValueImpl(apiCallStatValue)
		return true
	})
}

// 加入上报队列
func (c *counterV2) doQuotaStatReport(startTimeMicro int64,
	passed uint32, limited uint32, client Client, collector *plugin.RateLimitStatCollectorV2) {
	startTimeMilli := startTimeMicro / 1e3
	if passed > 0 || limited > 0 {
		curveStatValue := plugin.PoolGetRateLimitStatValueV2()
		curveStatValue.StatKey.ClientIP = client.ClientIP()
		curveStatValue.StatKey.CounterKey = c.counterKey
		curveStatValue.Namespace = c.identifier.Namespace
		curveStatValue.Service = c.identifier.Service
		curveStatValue.Method = c.subLabels.Method
		curveStatValue.AppId = c.subLabels.AppId
		curveStatValue.Uin = c.subLabels.Uin
		curveStatValue.Labels = c.subLabels.Labels
		curveStatValue.Duration = c.identifier.Duration
		curveStatValue.GetPrecisionData().InitValues(int64(passed), int64(limited), startTimeMilli)
		curveStatValue.GetCurveData().InitValues(int64(passed), int64(limited), startTimeMilli)
		curveStatValue.Total = int64(c.MaxAmount())
		curveStatValue.LastUpdateTime = startTimeMilli
		curveStatValue.ExpireDuration = c.expireDurationMilli
		collector.AddStatValueV2(curveStatValue)
		plugin.PoolPutRateLimitStatValueV2(curveStatValue)
	}
}

// ClientCount 客户端数量
func (c *counterV2) ClientCount() uint32 {
	return c.counterClients.ClientCount()
}
