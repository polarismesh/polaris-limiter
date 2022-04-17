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
	"github.com/polarismesh/polaris-limit/pkg/utils"
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

//客户端更新事件
type StreamUpdateEvent struct {
	//时间点
	TimeNumber string `json:"time_number"`
	//事件类型
	EventType string `json:"event"`
	//客户端ID
	StreamId string `json:"stream_id"`
	//IP地址
	IPAddress string `json:"ip_address"`
	//动作，上线还是下线
	Action string `json:"action"`
}

//更新客户端事件
func NewStreamUpdateEvent(streamId string, ipAddr *utils.IPAddress, action string) *StreamUpdateEvent {
	return &StreamUpdateEvent{
		TimeNumber: utils.TimestampMsToUtcIso8601(utils.CurrentMillisecond()),
		EventType:  EventTypeStreamUpdate,
		StreamId:   streamId,
		IPAddress:  ipAddr.String(),
		Action:     action,
	}
}

//获取事件类型
func (c *StreamUpdateEvent) GetEventType() string {
	return c.EventType
}

//变成Json输出
func (c *StreamUpdateEvent) ToJson() string {
	return toJsonStr(c)
}

//客户端更新事件
type ClientUpdateEvent struct {
	//时间点
	TimeNumber string `json:"time_number"`
	//事件类型
	EventType string `json:"event"`
	//客户端ID
	ClientId string `json:"client_id"`
	//IP地址
	IPAddress string `json:"ip_address"`
	//动作，上线还是下线
	Action string `json:"action"`
}

//获取事件类型
func (c *ClientUpdateEvent) GetEventType() string {
	return c.EventType
}

//变成Json输出
func (c *ClientUpdateEvent) ToJson() string {
	return toJsonStr(c)
}

//更新客户端事件
func NewClientUpdateEvent(clientId string, ipAddr *utils.IPAddress, action string) *ClientUpdateEvent {
	return &ClientUpdateEvent{
		TimeNumber: utils.TimestampMsToUtcIso8601(utils.CurrentMillisecond()),
		EventType:  EventTypeClientUpdate,
		ClientId:   clientId,
		IPAddress:  ipAddr.String(),
		Action:     action,
	}
}

//客户端更新事件
type ClientStreamUpdateEvent struct {
	//时间点
	TimeNumber string `json:"time_number"`
	//事件类型
	EventType string `json:"event"`
	//最后的流ID
	LastStreamId string `json:"last_stream_id"`
	//流ID
	StreamId string `json:"stream_id"`
	//客户端ID
	ClientId string `json:"client_id"`
	//IP地址
	IPAddress string `json:"ip_address"`
	//动作，上线还是下线
	Action string `json:"action"`
}

//获取事件类型
func (c *ClientStreamUpdateEvent) GetEventType() string {
	return c.EventType
}

//变成Json输出
func (c *ClientStreamUpdateEvent) ToJson() string {
	return toJsonStr(c)
}

//更新客户端事件
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

//客户端更新事件
type CounterClientUpdateEvent struct {
	//时间点
	TimeNumber string `json:"time_number"`
	//事件类型
	EventType string `json:"event"`
	//命名空间
	Namespace string `json:"namespace"`
	//服务名
	Service string `json:"service"`
	//标签
	Labels string `json:"labels"`
	//时间段
	Duration string `json:"duration"`
	//客户端ID
	ClientId string `json:"client_id"`
	//IP地址
	IPAddress string `json:"ip_address"`
	//动作，上线还是下线
	Action string `json:"action"`
	//客户端总数
	ClientCount int `json:"client_count"`
}

//获取事件类型
func (c *CounterClientUpdateEvent) GetEventType() string {
	return c.EventType
}

//变成Json输出
func (c *CounterClientUpdateEvent) ToJson() string {
	return toJsonStr(c)
}

//变成Json输出
func toJsonStr(value interface{}) string {
	str, _ := json.Marshal(value)
	return string(str)
}

//配额变更时间
type QuotaChangeEvent struct {
	//时间点
	TimeNumber string `json:"time_number"`
	//事件类型
	EventType string `json:"event"`
	//命名空间
	Namespace string `json:"namespace"`
	//服务名
	Service string `json:"service"`
	//标签
	Labels string `json:"labels"`
	//时间段
	Duration string `json:"duration"`
	//触发配额调整的动作
	Action string `json:"action"`
	//客户端ID
	ClientId string `json:"client_id"`
	//原来的配额值
	LatestAmount uint32 `json:"latest_amount"`
	//当前配额值
	CurrentAmount uint32 `json:"current_amount"`
}

//获取事件类型
func (q *QuotaChangeEvent) GetEventType() string {
	return q.EventType
}

//变成Json输出
func (q *QuotaChangeEvent) ToJson() string {
	return toJsonStr(q)
}

//配额变更时间
type CounterUpdateEvent struct {
	//时间点
	TimeNumber string `json:"time_number"`
	//事件类型
	EventType string `json:"event"`
	//命名空间
	Namespace string `json:"namespace"`
	//服务名
	Service string `json:"service"`
	//标签
	Labels string `json:"labels"`
	//时间段
	Duration string `json:"duration"`
	//触发配额调整的动作
	Action string `json:"action"`
	//当前配额值
	Amount uint32 `json:"amount"`
}

//获取事件类型
func (q *CounterUpdateEvent) GetEventType() string {
	return q.EventType
}

//变成Json输出
func (q *CounterUpdateEvent) ToJson() string {
	return toJsonStr(q)
}

