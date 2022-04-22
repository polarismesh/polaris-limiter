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

package config

import (
	"fmt"
	"math"
	"time"

	"github.com/polarismesh/polaris-limiter/pkg/log"
)

// Config ratelimit的相关配置
type Config struct {
	// 当前限流server节点标识
	Myid uint `yaml:"myid"`

	// 计数器分组个数
	CounterGroup uint32 `yaml:"counter-group"`

	// 最大计数器个数
	MaxCounter uint32 `yaml:"max-counter"`

	// 最大客户端个数
	MaxClient uint32 `yaml:"max-client"`

	// 推送协程数
	PushWorker uint32 `yaml:"push-worker"`

	// 默认滑窗数量
	SlideCount uint32 `yaml:"slide-count"`

	// 回收本地计数器的扫描时间
	PurgeCounterInterval time.Duration `yaml:"purge-counter-interval"`

	// 与远端存储同步的时间间隔
	SyncRemoteStorageInterval time.Duration `yaml:"sync-remote-storage-interval"`

	// 与远端存储异步交互交互的超时时间
	AsyncRemoteWaitTimeout time.Duration `yaml:"async-remote-wait-timeout"`

	// 更新远端存储的limiter阈值，即多大的duration才需要上报到远端
	UpdateRemoteStorageThreshold time.Duration `yaml:"update-remote-storage-threshold"`

	// 本地缓存需要刷新的阈值，当达到该阈值的key无更新，则需要从远端加载最新的数据到本地
	FlushLocalStorageThreshold time.Duration `yaml:"flush-local-storage-threshold"`

	// 最大查询等待时间
	MaxQueryWait time.Duration `yaml:"max_query_wait"`

	// 最小查询等待时间
	MinQueryWait time.Duration `yaml:"min_query_wait"`
}

const (
	defaultMinQueryWait = 1 * time.Second
	defaultMaxQueryWait = 1 * time.Minute
	MaxSlideCount       = 10
	defaultSlideCount   = 1
	defaultMaxIndex     = math.MaxUint8
	maxCounter          = math.MaxUint32 ^ (math.MaxUint8 << 24) // 16,777,215
)

// DefaultConfig 返回一份默认的配置信息
func DefaultConfig() *Config {
	cfg := &Config{
		Myid:                         1,
		CounterGroup:                 64,
		MaxCounter:                   maxCounter, // 默认最大1千万的key
		MaxClient:                    20 * 1000,
		SlideCount:                   defaultSlideCount,
		PushWorker:                   4,
		PurgeCounterInterval:         time.Second * 60,
		SyncRemoteStorageInterval:    time.Second,
		AsyncRemoteWaitTimeout:       time.Millisecond * 200,
		UpdateRemoteStorageThreshold: time.Minute,
		FlushLocalStorageThreshold:   time.Second * 3,
		MinQueryWait:                 defaultMinQueryWait,
		MaxQueryWait:                 defaultMaxQueryWait,
	}
	return cfg
}

// ParseConfig 解析配置
func ParseConfig(config *Config) (*Config, error) {
	if config == nil {
		return DefaultConfig(), nil
	}

	d := DefaultConfig()
	// myid不能为0，避免与线上存量的出现冲突
	if config.Myid == 0 || config.Myid > defaultMaxIndex {
		return nil, fmt.Errorf("myid should be less than %d and greater than 0", defaultMaxIndex)
	}
	if config.Myid > 0 {
		d.Myid = config.Myid
	}
	if config.MaxCounter > maxCounter {
		return nil, fmt.Errorf("maxCounter should be less than %d", maxCounter)
	}
	if config.MaxCounter > 0 {
		d.MaxCounter = config.MaxCounter
	}
	if config.PushWorker > 0 {
		d.PushWorker = config.PushWorker
	}
	if config.MaxClient > 0 {
		d.MaxClient = config.MaxClient
	}
	if config.CounterGroup > 0 {
		d.CounterGroup = config.CounterGroup
	}
	if config.PurgeCounterInterval > 0 {
		d.PurgeCounterInterval = config.PurgeCounterInterval
	}
	if config.SyncRemoteStorageInterval > 0 {
		d.SyncRemoteStorageInterval = config.SyncRemoteStorageInterval
	}
	if config.AsyncRemoteWaitTimeout > 0 {
		d.AsyncRemoteWaitTimeout = config.AsyncRemoteWaitTimeout
	}
	if config.UpdateRemoteStorageThreshold > 0 {
		d.UpdateRemoteStorageThreshold = config.UpdateRemoteStorageThreshold
	}
	if config.FlushLocalStorageThreshold > 0 {
		d.FlushLocalStorageThreshold = config.FlushLocalStorageThreshold
	}
	if config.SlideCount > 0 && config.SlideCount <= MaxSlideCount && 1000%config.SlideCount == 0 {
		d.SlideCount = config.SlideCount
	}
	log.Infof("[Limit] load ratelimit config: %+v", d)
	return d, nil
}
