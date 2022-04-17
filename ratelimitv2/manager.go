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
	"container/list"
	"context"
	apiv2 "github.com/polarismesh/polaris-limit/api/v2"
	"github.com/polarismesh/polaris-limit/pkg/log"
	"github.com/polarismesh/polaris-limit/pkg/utils"
	"github.com/modern-go/reflect2"
	"sync"
	"sync/atomic"
	"time"
)

//计数器标识
type CounterIdentifier struct {
	Service   string
	Namespace string
	Labels    string
	Duration  time.Duration
}

//创建限流计数器标识
func NewCounterIdentifier(initReq *apiv2.RateLimitInitRequest, ruleIdx int) *CounterIdentifier {
	target := initReq.GetTarget()
	durationTime := time.Duration(initReq.GetTotals()[ruleIdx].GetDuration()) * time.Second
	return &CounterIdentifier{
		Service:   target.GetService(),
		Namespace: target.GetNamespace(),
		Labels:    target.GetLabels(),
		Duration:  durationTime,
	}
}

//计数器管理类V2
type CounterManagerV2 struct {
	//同步锁
	mutex *sync.Mutex
	//计数器列表
	counters []CounterV2
	//已经分配的counterKey
	allocatedKeys map[uint32]bool
	//去重map
	counterMap *sync.Map
	//最大计数器数量
	maxSize uint32
	//当前游标
	counterIdx uint32
	//过期扫描周期
	cleanupInterval time.Duration
	//推送管理器
	pushManager PushManager
}

//创建计数器管理类
func NewCounterManagerV2(maxSize uint32, cleanupInterval time.Duration, pushManager PushManager) *CounterManagerV2 {
	manager := &CounterManagerV2{
		mutex:           &sync.Mutex{},
		maxSize:         maxSize,
		counters:        make([]CounterV2, maxSize),
		allocatedKeys:   make(map[uint32]bool),
		counterMap:      &sync.Map{},
		cleanupInterval: cleanupInterval,
		pushManager:     pushManager,
	}
	return manager
}

func (cm *CounterManagerV2) cleanupCounter(identifier CounterIdentifier, counterKey uint32) (CounterV2, bool) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	idx := toArrayIndex(counterKey)
	counter := cm.counters[idx]
	if counter.IsExpired() {
		cm.counters[idx] = nil
		cm.counterMap.Delete(identifier)
		delete(cm.allocatedKeys, counterKey)
		log.Infof("counter %+v expired, lastUpdateTime %s, expireDuration %s, recycle counter key is %d",
			identifier, utils.TimestampMsToUtcIso8601(counter.LastUpdateTime()), counter.ExpireDuration(), counterKey)
		//上报
		counterEvent := &CounterUpdateEvent{
			TimeNumber: utils.TimestampMsToUtcIso8601(utils.CurrentMillisecond()),
			EventType:  EventTypeCounterUpdate,
			Namespace:  counter.Identifier().Namespace,
			Service:    counter.Identifier().Service,
			Labels:     counter.Identifier().Labels,
			Duration:   counter.Identifier().Duration.String(),
			Action:     ActionDelete,
			Amount:     counter.MaxAmount(),
		}
		statics.AddEventToLog(counterEvent)
		return counter, true
	}
	return counter, false
}

//启动过期清理
func (cm *CounterManagerV2) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(cm.cleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				log.Infof("counter manager stopped")
				ticker.Stop()
				return
			case <-ticker.C:
				var all int
				var expired int
				cm.counterMap.Range(func(key, value interface{}) bool {
					all++
					counter, isExpired := cm.cleanupCounter(key.(CounterIdentifier), value.(uint32))
					if isExpired {
						expired++
					}
					counter.CleanupSenders(isExpired)
					return true
				})
				log.Infof("finished cleanup counter, all(%d), expired(%d)", all, expired)
			}
		}
	}()
}

func (cm *CounterManagerV2) lookupCounterKey() (apiv2.Code, uint32) {
	for i := 0; i < int(cm.maxSize); i++ {
		nextCounterKey := atomic.AddUint32(&cm.counterIdx, 1)
		if nextCounterKey == cm.maxSize {
			atomic.StoreUint32(&cm.counterIdx, 0)
		}
		_, ok := cm.allocatedKeys[nextCounterKey]
		if !ok {
			cm.allocatedKeys[nextCounterKey] = true
			return apiv2.ExecuteSuccess, nextCounterKey
		}
	}
	return apiv2.ExceedMaxCounter, 0
}

//分配计数器
func (cm *CounterManagerV2) allocateCounterKey(
	identifier *CounterIdentifier, initRequest InitRequest) (apiv2.Code, CounterV2, bool) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	if idx, ok := cm.counterMap.Load(*identifier); ok {
		counter := cm.counters[toArrayIndex(idx.(uint32))]
		counter.Update()
		return apiv2.ExecuteSuccess, counter, true
	}
	if len(cm.allocatedKeys) == int(cm.maxSize) {
		return apiv2.ExceedMaxCounter, nil, false
	}
	var targetIndexKey uint32
	var code apiv2.Code
	code, targetIndexKey = cm.lookupCounterKey()
	if code != apiv2.ExecuteSuccess {
		return code, nil, false
	}
	cm.counterMap.Store(*identifier, targetIndexKey)
	counter := NewCounterV2(targetIndexKey, identifier, initRequest)
	cm.counters[toArrayIndex(targetIndexKey)] = counter
	return apiv2.ExecuteSuccess, counter, false
}

//转换成数组下标
func toArrayIndex(key uint32) int {
	return int(key) - 1
}

//增加计数器
func (cm *CounterManagerV2) AddCounter(initReq *apiv2.RateLimitInitRequest, ruleIdx int,
	sender Client, expireDuration time.Duration) (apiv2.Code, CounterV2) {
	identifier := NewCounterIdentifier(initReq, ruleIdx)
	quotaTotalRule := initReq.GetTotals()[ruleIdx]
	maxAmount := quotaTotalRule.GetMaxAmount()
	duration := time.Duration(initReq.GetTotals()[ruleIdx].GetDuration()) * time.Second
	cInitReq := InitRequest{
		MaxAmount:      maxAmount,
		SlideCount:     int(initReq.GetSlideCount()),
		Sender:         sender,
		Duration:       duration,
		ExpireDuration: expireDuration,
		AmountMode:     quotaTotalRule.GetMode(),
		PushManager:    cm.pushManager,
	}
	code, counter, exists := cm.allocateCounterKey(identifier, cInitReq)
	if code != apiv2.ExecuteSuccess {
		return code, nil
	}
	if exists {
		counter.Reload(cInitReq)
	}
	return code, counter
}

//获取计数器
func (cm *CounterManagerV2) GetCounter(counterKey uint32) (apiv2.Code, CounterV2) {
	if counterKey > cm.maxSize {
		return apiv2.NotFoundLimiter, nil
	}
	counter := cm.counters[toArrayIndex(counterKey)]
	if reflect2.IsNil(counter) {
		return apiv2.NotFoundLimiter, nil
	}
	return apiv2.ExecuteSuccess, counter
}

//客户端管理器
type ClientManager struct {
	mutex *sync.Mutex
	//被淘汰的序号
	freeClientKeys *list.List
	//客户端列表
	clients []Client
	//去重map
	clientMap map[string]uint32
	//最大客户端数量
	maxSize uint32
	//当前游标
	clientIdx uint32
}

//创建客户端管理器
func NewClientManager(maxSize uint32) *ClientManager {
	manager := &ClientManager{
		mutex:          &sync.Mutex{},
		freeClientKeys: &list.List{},
		maxSize:        maxSize,
		clients:        make([]Client, maxSize),
		clientMap:      make(map[string]uint32),
	}
	manager.freeClientKeys.Init()
	return manager
}

//增加客户端
func (c *ClientManager) AddClient(
	clientId string, clientIP *utils.IPAddress, streamContext *StreamContext) (apiv2.Code, Client) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if clientKey, ok := c.clientMap[clientId]; ok {
		client := c.clients[toArrayIndex(clientKey)]
		if client.UpdateStreamContext(streamContext) {
			//没有被解引用，有效更新
			return apiv2.ExecuteSuccess, client
		}
	}
	var targetIndexKey uint32
	if c.freeClientKeys.Len() > 0 {
		targetIndexKey = c.freeClientKeys.Remove(c.freeClientKeys.Front()).(uint32)
	} else {
		nextCounterKey := atomic.AddUint32(&c.clientIdx, 1)
		if nextCounterKey > c.maxSize {
			return apiv2.ExceedMaxClient, nil
		}
		targetIndexKey = nextCounterKey
	}
	c.clientMap[clientId] = targetIndexKey
	client := NewClient(targetIndexKey, clientIP, clientId, streamContext)
	c.clients[toArrayIndex(targetIndexKey)] = client
	return apiv2.ExecuteSuccess, client
}

//删除客户端
func (c *ClientManager) DelClient(client Client, streamCtxId string) bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	idx := toArrayIndex(client.ClientKey())
	curClient := c.clients[idx]
	if !reflect2.IsNil(curClient) && curClient.Detach(client.ClientId(), streamCtxId) {
		delete(c.clientMap, client.ClientId())
		//同一连接的客户端，进行清理
		c.clients[idx] = nil
		c.freeClientKeys.PushBack(client.ClientKey())
		return true
	}
	return false
}
