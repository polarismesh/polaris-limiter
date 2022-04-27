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
	"encoding/json"

	"github.com/polarismesh/polaris-limiter/pkg/utils"
)

const (
	EventTypeStreamUpdate        = "streamUpdate"
	EventTypeClientUpdate        = "clientUpdate"
	EventTypeClientStreamUpdate  = "clientStreamUpdate"
	EventTypeCounterClientUpdate = "counterClientUpdate"
	EventTypeCounterUpdate       = "counterUpdate"
	EventTypeQuotaChange         = "quotaChange"
	ActionAdd                    = "add"
	ActionReplace                = "replace"
	ActionDelete                 = "delete"
	TriggerShareEqually          = "shareEqually"
	TriggerShareGlobal           = "shareGlobal"
)

// StreamUpdateEvent 客户端更新事件
type StreamUpdateEvent struct {
	// 时间点
	TimeNumber string `json:"time_number"`
	// 事件类型
	EventType string `json:"event"`
	// 客户端ID
	StreamId string `json:"stream_id"`
	// IP地址
	IPAddress string `json:"ip_address"`
	// 动作，上线还是下线
	Action string `json:"action"`
}

// NewStreamUpdateEvent 更新客户端事件
func NewStreamUpdateEvent(streamId string, ipAddr *utils.IPAddress, action string) *StreamUpdateEvent {
	return &StreamUpdateEvent{
		TimeNumber: utils.TimestampMsToUtcIso8601(utils.CurrentMillisecond()),
		EventType:  EventTypeStreamUpdate,
		StreamId:   streamId,
		IPAddress:  ipAddr.String(),
		Action:     action,
	}
}

// GetEventType 获取事件类型
func (c *StreamUpdateEvent) GetEventType() string {
	return c.EventType
}

// ToJson 变成Json输出
func (c *StreamUpdateEvent) ToJson() string {
	return toJsonStr(c)
}

// ClientUpdateEvent 客户端更新事件
type ClientUpdateEvent struct {
	// 时间点
	TimeNumber string `json:"time_number"`
	// 事件类型
	EventType string `json:"event"`
	// 客户端ID
	ClientId string `json:"client_id"`
	// IP地址
	IPAddress string `json:"ip_address"`
	// 动作，上线还是下线
	Action string `json:"action"`
}

// GetEventType 获取事件类型
func (c *ClientUpdateEvent) GetEventType() string {
	return c.EventType
}

// ToJson 变成Json输出
func (c *ClientUpdateEvent) ToJson() string {
	return toJsonStr(c)
}

// NewClientUpdateEvent 更新客户端事件
func NewClientUpdateEvent(clientId string, ipAddr *utils.IPAddress, action string) *ClientUpdateEvent {
	return &ClientUpdateEvent{
		TimeNumber: utils.TimestampMsToUtcIso8601(utils.CurrentMillisecond()),
		EventType:  EventTypeClientUpdate,
		ClientId:   clientId,
		IPAddress:  ipAddr.String(),
		Action:     action,
	}
}

// ClientStreamUpdateEvent 客户端更新事件
type ClientStreamUpdateEvent struct {
	// TimeNumber 时间点
	TimeNumber string `json:"time_number"`
	// EventType 事件类型
	EventType string `json:"event"`
	// LastStreamId 最后的流ID
	LastStreamId string `json:"last_stream_id"`
	// StreamId 流ID
	StreamId string `json:"stream_id"`
	// ClientId 客户端ID
	ClientId string `json:"client_id"`
	// IPAddress IP地址
	IPAddress string `json:"ip_address"`
	// Action 动作，上线还是下线
	Action string `json:"action"`
}

// GetEventType 获取事件类型
func (c *ClientStreamUpdateEvent) GetEventType() string {
	return c.EventType
}

// ToJson 变成Json输出
func (c *ClientStreamUpdateEvent) ToJson() string {
	return toJsonStr(c)
}

// NewClientStreamUpdateEvent 更新客户端事件
func NewClientStreamUpdateEvent(
	lastStreamId string, streamId string, client Client, action string) *ClientStreamUpdateEvent {
	return &ClientStreamUpdateEvent{
		TimeNumber:   utils.TimestampMsToUtcIso8601(utils.CurrentMillisecond()),
		EventType:    EventTypeClientStreamUpdate,
		LastStreamId: lastStreamId,
		StreamId:     streamId,
		ClientId:     client.ClientId(),
		IPAddress:    client.ClientIP().String(),
		Action:       action,
	}
}

// CounterClientUpdateEvent 客户端更新事件
type CounterClientUpdateEvent struct {
	// TimeNumber 时间点
	TimeNumber string `json:"time_number"`
	// EventType 事件类型
	EventType string `json:"event"`
	// Namespace 命名空间
	Namespace string `json:"namespace"`
	// Service 服务名
	Service string `json:"service"`
	// Labels 标签
	Labels string `json:"labels"`
	// Duration 时间段
	Duration string `json:"duration"`
	// ClientId 客户端ID
	ClientId string `json:"client_id"`
	// IPAddress IP地址
	IPAddress string `json:"ip_address"`
	// Action 动作，上线还是下线
	Action string `json:"action"`
	// ClientCount 客户端总数
	ClientCount int `json:"client_count"`
}

// GetEventType 获取事件类型
func (c *CounterClientUpdateEvent) GetEventType() string {
	return c.EventType
}

// ToJson 变成Json输出
func (c *CounterClientUpdateEvent) ToJson() string {
	return toJsonStr(c)
}

// 变成Json输出
func toJsonStr(value interface{}) string {
	str, _ := json.Marshal(value)
	return string(str)
}

// QuotaChangeEvent 配额变更时间
type QuotaChangeEvent struct {
	// TimeNumber 时间点
	TimeNumber string `json:"time_number"`
	// EventType 事件类型
	EventType string `json:"event"`
	// Namespace 命名空间
	Namespace string `json:"namespace"`
	// Service 服务名
	Service string `json:"service"`
	// Labels 标签
	Labels string `json:"labels"`
	// Duration 时间段
	Duration string `json:"duration"`
	// Action 触发配额调整的动作
	Action string `json:"action"`
	// ClientId 客户端ID
	ClientId string `json:"client_id"`
	// LatestAmount 原来的配额值
	LatestAmount uint32 `json:"latest_amount"`
	// CurrentAmount 当前配额值
	CurrentAmount uint32 `json:"current_amount"`
}

// GetEventType 获取事件类型
func (q *QuotaChangeEvent) GetEventType() string {
	return q.EventType
}

// ToJson 变成Json输出
func (q *QuotaChangeEvent) ToJson() string {
	return toJsonStr(q)
}

// CounterUpdateEvent 配额变更时间
type CounterUpdateEvent struct {
	// TimeNumber 时间点
	TimeNumber string `json:"time_number"`
	// EventType 事件类型
	EventType string `json:"event"`
	// Namespace 命名空间
	Namespace string `json:"namespace"`
	// Service 服务名
	Service string `json:"service"`
	// Labels 标签
	Labels string `json:"labels"`
	// Duration 时间段
	Duration string `json:"duration"`
	// Action 触发配额调整的动作
	Action string `json:"action"`
	// Amount 当前配额值
	Amount uint32 `json:"amount"`
}

// GetEventType 获取事件类型
func (q *CounterUpdateEvent) GetEventType() string {
	return q.EventType
}

// ToJson 变成Json输出
func (q *CounterUpdateEvent) ToJson() string {
	return toJsonStr(q)
}
