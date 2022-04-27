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

package file

import (
	"sync"

	"go.uber.org/zap"

	"github.com/polarismesh/polaris-limiter/pkg/log"
	"github.com/polarismesh/polaris-limiter/plugin"
)

// EventLogReporter 事件日志上报器
type EventLogReporter struct {
	collection *sync.Map
	logger     *zap.Logger
}

// NewEventLogReporter 创建对象
func NewEventLogReporter(cfg *ReportConfig) *EventLogReporter {
	return &EventLogReporter{collection: &sync.Map{},
		logger: initLogger(cfg.RateLimitEventLogPath, cfg.LogSize, cfg.LogBackups, cfg.LogMaxAge)}
}

// EventQueue 事件收集器
type EventQueue struct {
	mutex sync.Mutex
	queue []plugin.EventToLog
}

// AddEvent 写入事件
func (e *EventLogReporter) AddEvent(event plugin.EventToLog) {
	var eventQueue *EventQueue
	value, ok := e.collection.Load(event.GetEventType())
	if ok {
		eventQueue = value.(*EventQueue)
	} else {
		value, _ = e.collection.LoadOrStore(event.GetEventType(), &EventQueue{})
		eventQueue = value.(*EventQueue)
	}
	eventQueue.mutex.Lock()
	defer eventQueue.mutex.Unlock()
	eventQueue.queue = append(eventQueue.queue, event)
}

// LogAllEvents 获取所有的事件日志
func (e *EventLogReporter) LogAllEvents() int {
	eventToLogs := make(map[string][]plugin.EventToLog)
	e.collection.Range(func(key, value interface{}) bool {
		eventQueue := value.(*EventQueue)
		eventQueue.mutex.Lock()
		defer eventQueue.mutex.Unlock()
		if len(eventQueue.queue) == 0 {
			return true
		}
		eventToLogs[key.(string)] = eventQueue.queue
		eventQueue.queue = nil
		return true
	})
	if len(eventToLogs) == 0 {
		return 0
	}
	var total int
	for eventType, events := range eventToLogs {
		for _, event := range events {
			e.logger.Info(event.ToJson())
		}
		total += len(events)
		log.Infof("finish log events, eventType %s, event count %d", eventType, len(events))
	}
	return total
}
