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

	"github.com/google/uuid"

	apiv2 "github.com/polarismesh/polaris-limiter/pkg/api/v2"
	"github.com/polarismesh/polaris-limiter/pkg/utils"
)

// Client 客户端统计数据
type Client interface {
	// ClientKey 客户端标识
	ClientKey() uint32
	// ClientIP 获取客户端IP
	ClientIP() utils.IPAddress
	// ClientId 获取客户端ID
	ClientId() string
	// SendAndUpdate 发送
	SendAndUpdate(*apiv2.RateLimitResponse, *ClientSendTime, int64) (bool, error)
	// UpdateStreamContext 更新流上下文，返回该stream是否已经更新成功
	UpdateStreamContext(streamCtx *StreamContext) bool
	// Cleanup 清理所有上下文信息
	Cleanup()
	// Detach 原子操作解引用客户端，返回是否解引用成功
	Detach(clientId string, streamCtxId string) bool
	// IsDetached 检查是否已经被解引用
	IsDetached() bool
}

// StreamContext 连接上下文
type StreamContext struct {
	ctxId  string
	stream Stream
	mutex  *sync.Mutex
}

// NewStreamContext 创建连接上下文
func NewStreamContext(stream Stream) *StreamContext {
	return &StreamContext{
		ctxId:  uuid.New().String(),
		stream: stream,
		mutex:  &sync.Mutex{},
	}
}

// ContextId 上下文唯一标识
func (s *StreamContext) ContextId() string {
	return s.ctxId
}

// Send 发送消息
func (s *StreamContext) Send(resp *apiv2.RateLimitResponse) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.stream.Send(resp)
}

// Stream 应答发送Stream
type Stream interface {
	// Send 推送应答
	Send(*apiv2.RateLimitResponse) error
}

// NewClient 新建客户端
func NewClient(
	clientKey uint32, clientIP *utils.IPAddress, clientId string, streamCtx *StreamContext) Client {
	client := &client{
		clientKey: clientKey,
		clientIP:  clientIP,
		clientId:  clientId,
		streamCtx: streamCtx,
		mutex:     &sync.RWMutex{},
	}
	statics.AddEventToLog(NewClientUpdateEvent(clientId, clientIP, ActionAdd))
	statics.AddEventToLog(NewClientStreamUpdateEvent("", streamCtx.ctxId, client, ActionAdd))
	return client
}

// client 客户端实现类
type client struct {
	// clientKey 客户端int主键，server唯一
	clientKey uint32
	// clientIP 客户端IP地址
	clientIP *utils.IPAddress
	// clientId 客户端唯一ID，重启后会改变
	clientId string
	// streamCtx 流上下文信息，一个client一次只与一个流绑定
	streamCtx *StreamContext
	// mutex map的锁
	mutex *sync.RWMutex
	// detached 客户端是否已经解引用
	detached bool
}

// CounterSendTime 记录计数器最后一次发送时间
type CounterSendTime struct {
	counter CounterV2
	// lastSentMicro 最后一次消息发送时间
	lastSentMicro int64
}

// UpdateLastSendTime 更新最后一次发送时间
func (c *CounterSendTime) UpdateLastSendTime(value int64) bool {
	for {
		curValue := atomic.LoadInt64(&c.lastSentMicro)
		if curValue >= value {
			return false
		}
		if atomic.CompareAndSwapInt64(&c.lastSentMicro, curValue, value) {
			return true
		}
	}
}

// ClientSendTime 记录客户端上次发送时间
type ClientSendTime struct {
	curClient Client
	// 最后一次消息发送时间
	lastSentMicro int64
}

// UpdateLastSendTime 更新最后一次发送时间
func (c *ClientSendTime) UpdateLastSendTime(value int64) bool {
	for {
		curValue := atomic.LoadInt64(&c.lastSentMicro)
		if curValue >= value {
			return false
		}
		if atomic.CompareAndSwapInt64(&c.lastSentMicro, curValue, value) {
			return true
		}
	}
}

// ClientKey 客户端标识
func (c *client) ClientKey() uint32 {
	return c.clientKey
}

// ClientId 客户端标识
func (c *client) ClientId() string {
	return c.clientId
}

// ClientIP 客户端标识
func (c *client) ClientIP() utils.IPAddress {
	return *c.clientIP
}

// SameContext 是否基于同一个流上下文
func (c *client) SameContext(ctxId string) bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.streamCtx.ctxId == ctxId
}

// SendAndUpdate 发送及更新
func (c *client) SendAndUpdate(
	resp *apiv2.RateLimitResponse, clientSendTime *ClientSendTime, msgTimeMicro int64) (bool, error) {
	var streamCtx *StreamContext
	c.mutex.RLock()
	streamCtx = c.streamCtx
	c.mutex.RUnlock()
	if resp.Cmd == apiv2.RateLimitCmd_ACQUIRE {
		if nil == clientSendTime || nil == streamCtx {
			return false, nil
		}
		// 只有发送消息才需要处理倒序情况
		updateSuccess := clientSendTime.UpdateLastSendTime(msgTimeMicro)
		if !updateSuccess {
			return false, nil
		}
		return true, streamCtx.Send(resp)
	}
	return true, streamCtx.Send(resp)
}

// streamContextId 获取上下文ID
func (c *client) streamContextId() string {
	if nil != c.streamCtx {
		return c.streamCtx.ctxId
	}
	return ""
}

// Detach 对客户端进行解引用，解引用后不再复用
func (c *client) Detach(clientId string, streamCtxId string) bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.clientId == clientId && c.streamContextId() == streamCtxId {
		c.detached = true
	}
	return c.detached
}

// Cleanup 清理计数器
func (c *client) Cleanup() {
	lastCtx := c.clearStreamContext()
	var ctxId string
	if nil != lastCtx {
		ctxId = lastCtx.ctxId
	}
	statics.AddEventToLog(NewClientStreamUpdateEvent(ctxId, "", c, ActionDelete))
	statics.AddEventToLog(NewClientUpdateEvent(c.clientId, c.clientIP, ActionDelete))

}

// UpdateStreamContext 更新流上下文
func (c *client) UpdateStreamContext(streamCtx *StreamContext) bool {
	var lastStreamId string
	var curStreamId string
	lastStreamCtx, updated, detach := c.setStreamContext(streamCtx)
	if detach {
		return false
	}
	if !updated {
		return true
	}
	if nil != streamCtx {
		curStreamId = streamCtx.ctxId
	}
	if nil != lastStreamCtx {
		lastStreamId = lastStreamCtx.ctxId
	}
	statics.AddEventToLog(NewClientStreamUpdateEvent(lastStreamId, curStreamId, c, ActionReplace))
	return true
}

// setStreamContext 设置流式上下文
func (c *client) setStreamContext(streamCtx *StreamContext) (*StreamContext, bool, bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.detached {
		return nil, false, true
	}
	lastStreamCtx := c.streamCtx
	if lastStreamCtx.ctxId == streamCtx.ctxId {
		return lastStreamCtx, false, false
	}
	c.streamCtx = streamCtx
	return lastStreamCtx, true, false
}

// clearStreamContext 设置流式上下文
func (c *client) clearStreamContext() *StreamContext {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	lastStreamCtx := c.streamCtx
	c.streamCtx = nil
	return lastStreamCtx
}

// IsDetached 返回是否已经解引用
func (c *client) IsDetached() bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.detached
}
