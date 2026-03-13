/*
 * Tencent is pleased to support the open source community by making polaris-limiter available.
 *
 * Copyright (C) 2021 Tencent. All rights reserved.
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
	"fmt"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"github.com/polarismesh/specification/source/go/api/v1/traffic_manage/ratelimiter"

	apiv2 "github.com/polarismesh/polaris-limiter/pkg/api/v2"
	"github.com/polarismesh/polaris-limiter/pkg/utils"
	"github.com/polarismesh/polaris-limiter/plugin/statis/echo"
)

// mockStream 实现 Stream 接口，用于测试
type mockStream struct{}

func (m *mockStream) Send(_ *ratelimiter.RateLimitResponse) error { return nil }

// mockStatis 确保 statics 已初始化
func init() {
	SetStatics(&echo.StaticsWorker{})
}

// newTestClientManager 创建一个用于测试的 ClientManager
func newTestClientManager(maxSize uint32) *ClientManager {
	return NewClientManager(maxSize)
}

// addTestClient 向 ClientManager 添加一个测试客户端
func addTestClient(cm *ClientManager, clientID string, ip string) (apiv2.Code, Client) {
	ipAddr := utils.NewIPAddress(ip)
	streamCtx := NewStreamContext(&mockStream{})
	return cm.AddClient(clientID, ipAddr, streamCtx)
}

// ---- ClientManager 测试 ----

// TestClientManager_ClientCount 测试 ClientCount 方法
func TestClientManager_ClientCount(t *testing.T) {
	Convey("测试 ClientManager.ClientCount", t, func() {
		Convey("空管理器应返回 0", func() {
			cm := newTestClientManager(10)
			So(cm.ClientCount(), ShouldEqual, 0)
		})

		Convey("添加一个客户端后应返回 1", func() {
			cm := newTestClientManager(10)
			code, _ := addTestClient(cm, "client-1", "127.0.0.1")
			So(code, ShouldEqual, apiv2.ExecuteSuccess)
			So(cm.ClientCount(), ShouldEqual, 1)
		})

		Convey("添加多个客户端后应返回正确数量", func() {
			cm := newTestClientManager(10)
			addTestClient(cm, "client-1", "127.0.0.1")
			addTestClient(cm, "client-2", "127.0.0.2")
			addTestClient(cm, "client-3", "127.0.0.3")
			So(cm.ClientCount(), ShouldEqual, 3)
		})

		Convey("删除客户端后数量应减少", func() {
			cm := newTestClientManager(10)
			_, c1 := addTestClient(cm, "client-1", "127.0.0.1")
			addTestClient(cm, "client-2", "127.0.0.2")
			So(cm.ClientCount(), ShouldEqual, 2)

			streamCtxID := c1.(*client).streamContextId()
			cm.DelClient(c1, streamCtxID)
			So(cm.ClientCount(), ShouldEqual, 1)
		})

		Convey("超出最大容量时应返回错误码，数量不变", func() {
			cm := newTestClientManager(2)
			addTestClient(cm, "client-1", "127.0.0.1")
			addTestClient(cm, "client-2", "127.0.0.2")
			code, _ := addTestClient(cm, "client-3", "127.0.0.3")
			So(code, ShouldEqual, apiv2.ExceedMaxClient)
			So(cm.ClientCount(), ShouldEqual, 2)
		})
	})
}

// TestClientManager_ListClients 测试 ListClients 方法
func TestClientManager_ListClients(t *testing.T) {
	Convey("测试 ClientManager.ListClients", t, func() {
		Convey("空管理器应返回空切片", func() {
			cm := newTestClientManager(10)
			clients := cm.ListClients(0, 0)
			So(len(clients), ShouldEqual, 0)
		})

		Convey("添加客户端后应能列出", func() {
			cm := newTestClientManager(10)
			addTestClient(cm, "client-1", "127.0.0.1")
			addTestClient(cm, "client-2", "127.0.0.2")

			clients := cm.ListClients(0, 0)
			So(len(clients), ShouldEqual, 2)

			ids := make(map[string]bool)
			for _, c := range clients {
				ids[c.ClientId()] = true
			}
			So(ids["client-1"], ShouldBeTrue)
			So(ids["client-2"], ShouldBeTrue)
		})

		Convey("列出的客户端包含正确的 IP 信息", func() {
			cm := newTestClientManager(10)
			addTestClient(cm, "client-1", "192.168.1.100")

			clients := cm.ListClients(0, 0)
			So(len(clients), ShouldEqual, 1)
			So(clients[0].ClientId(), ShouldEqual, "client-1")
			So(clients[0].ClientIP().String(), ShouldContainSubstring, "192.168.1.100")
		})

		Convey("删除客户端后不应出现在列表中", func() {
			cm := newTestClientManager(10)
			_, c1 := addTestClient(cm, "client-1", "127.0.0.1")
			addTestClient(cm, "client-2", "127.0.0.2")

			streamCtxID := c1.(*client).streamContextId()
			cm.DelClient(c1, streamCtxID)

			clients := cm.ListClients(0, 0)
			So(len(clients), ShouldEqual, 1)
			So(clients[0].ClientId(), ShouldEqual, "client-2")
		})
	})
}

// TestClientManager_GetClient 测试 GetClient 方法
func TestClientManager_GetClient(t *testing.T) {
	Convey("测试 ClientManager.GetClient", t, func() {
		Convey("key 为 0 时应返回 NotFoundLimiter", func() {
			cm := newTestClientManager(10)
			code, c := cm.GetClient(0)
			So(code, ShouldEqual, apiv2.NotFoundLimiter)
			So(c, ShouldBeNil)
		})

		Convey("key 超出 maxSize 时应返回 NotFoundLimiter", func() {
			cm := newTestClientManager(10)
			code, c := cm.GetClient(11)
			So(code, ShouldEqual, apiv2.NotFoundLimiter)
			So(c, ShouldBeNil)
		})

		Convey("key 不存在时应返回 NotFoundLimiter", func() {
			cm := newTestClientManager(10)
			code, c := cm.GetClient(1)
			So(code, ShouldEqual, apiv2.NotFoundLimiter)
			So(c, ShouldBeNil)
		})

		Convey("key 存在时应返回对应客户端", func() {
			cm := newTestClientManager(10)
			code, added := addTestClient(cm, "client-1", "127.0.0.1")
			So(code, ShouldEqual, apiv2.ExecuteSuccess)

			getCode, got := cm.GetClient(added.ClientKey())
			So(getCode, ShouldEqual, apiv2.ExecuteSuccess)
			So(got, ShouldNotBeNil)
			So(got.ClientId(), ShouldEqual, "client-1")
		})

		Convey("删除后用原 key 查询应返回 NotFoundLimiter", func() {
			cm := newTestClientManager(10)
			_, c1 := addTestClient(cm, "client-1", "127.0.0.1")
			key := c1.ClientKey()

			streamCtxID := c1.(*client).streamContextId()
			cm.DelClient(c1, streamCtxID)

			code, got := cm.GetClient(key)
			So(code, ShouldEqual, apiv2.NotFoundLimiter)
			So(got, ShouldBeNil)
		})
	})
}

// mockClient 实现 Client 接口，用于 counter 测试
type mockClient struct {
	key uint32
	id  string
	ip  utils.IPAddress
}

func (m *mockClient) ClientKey() uint32                                      { return m.key }
func (m *mockClient) ClientIP() utils.IPAddress                              { return m.ip }
func (m *mockClient) ClientId() string                                       { return m.id }
func (m *mockClient) SendAndUpdate(_ *ratelimiter.RateLimitResponse, _ *ClientSendTime, _ int64) (bool, error) {
	return true, nil
}
func (m *mockClient) UpdateStreamContext(_ *StreamContext) bool { return true }
func (m *mockClient) Cleanup()                                  {}
func (m *mockClient) Detach(_, _ string) bool                   { return true }
func (m *mockClient) IsDetached() bool                          { return false }

// ---- CounterManagerV2 测试 ----

// newTestCounterManager 创建用于测试的 CounterManagerV2
func newTestCounterManager(maxSize uint32) *CounterManagerV2 {
	pushMgr, _ := NewPushManager(1, 100)
	return NewCounterManagerV2(maxSize, 30*time.Second, pushMgr)
}

// addTestCounter 向 CounterManagerV2 添加一个测试 counter
func addTestCounter(cm *CounterManagerV2, namespace, service, labels string, durationSec int64) (apiv2.Code, CounterV2) {
	identifier := &CounterIdentifier{
		Namespace: namespace,
		Service:   service,
		Labels:    labels,
		Duration:  time.Duration(durationSec) * time.Second,
	}
	sender := &mockClient{key: 1, id: "test-client", ip: *utils.NewIPAddress("127.0.0.1")}
	initReq := InitRequest{
		MaxAmount:      100,
		SlideCount:     1,
		Sender:         sender,
		Duration:       time.Duration(durationSec) * time.Second,
		ExpireDuration: 60 * time.Second,
	}
	code, counter, _ := cm.allocateCounterKey(identifier, initReq)
	return code, counter
}

// TestCounterManagerV2_CounterCount 测试 CounterCount 方法
func TestCounterManagerV2_CounterCount(t *testing.T) {
	Convey("测试 CounterManagerV2.CounterCount", t, func() {
		Convey("空管理器应返回 0", func() {
			cm := newTestCounterManager(100)
			So(cm.CounterCount(), ShouldEqual, 0)
		})

		Convey("分配一个 counter 后应返回 1", func() {
			cm := newTestCounterManager(100)
			code, _ := addTestCounter(cm, "default", "svc-a", "", 1)
			So(code, ShouldEqual, apiv2.ExecuteSuccess)
			So(cm.CounterCount(), ShouldEqual, 1)
		})

		Convey("分配多个不同 counter 后应返回正确数量", func() {
			cm := newTestCounterManager(100)
			addTestCounter(cm, "default", "svc-a", "", 1)
			addTestCounter(cm, "default", "svc-b", "", 1)
			addTestCounter(cm, "default", "svc-c", "key=val", 1)
			So(cm.CounterCount(), ShouldEqual, 3)
		})

		Convey("相同 identifier 重复分配不应增加数量", func() {
			cm := newTestCounterManager(100)
			addTestCounter(cm, "default", "svc-a", "", 1)
			addTestCounter(cm, "default", "svc-a", "", 1)
			So(cm.CounterCount(), ShouldEqual, 1)
		})

		Convey("超出最大容量时应返回错误码，数量不变", func() {
			cm := newTestCounterManager(2)
			addTestCounter(cm, "default", "svc-a", "", 1)
			addTestCounter(cm, "default", "svc-b", "", 1)
			code, _ := addTestCounter(cm, "default", "svc-c", "", 1)
			So(code, ShouldEqual, apiv2.ExceedMaxCounter)
			So(cm.CounterCount(), ShouldEqual, 2)
		})
	})
}

// TestCounterManagerV2_ListCounterIdentifiers 测试 ListCounterIdentifiers 方法
func TestCounterManagerV2_ListCounterIdentifiers(t *testing.T) {
	Convey("测试 CounterManagerV2.ListCounterIdentifiers", t, func() {
		Convey("空管理器应返回空切片", func() {
			cm := newTestCounterManager(100)
			ids := cm.ListCounterIdentifiers(0, 0)
			So(len(ids), ShouldEqual, 0)
		})

		Convey("分配 counter 后应能列出对应标识", func() {
			cm := newTestCounterManager(100)
			_, c1 := addTestCounter(cm, "ns-a", "svc-a", "k=v", 1)
			_, c2 := addTestCounter(cm, "ns-b", "svc-b", "", 2)

			ids := cm.ListCounterIdentifiers(0, 0)
			So(len(ids), ShouldEqual, 2)

			type key struct{ ns, svc, labels string }
			found := make(map[key]uint32)
			for _, item := range ids {
				found[key{item.Identifier.Namespace, item.Identifier.Service, item.Identifier.Labels}] = item.Key
			}
			So(found[key{"ns-a", "svc-a", "k=v"}], ShouldEqual, c1.CounterKey())
			So(found[key{"ns-b", "svc-b", ""}], ShouldEqual, c2.CounterKey())
		})

		Convey("标识中的 Duration 应与分配时一致", func() {
			cm := newTestCounterManager(100)
			addTestCounter(cm, "default", "svc-a", "", 5)

			ids := cm.ListCounterIdentifiers(0, 0)
			So(len(ids), ShouldEqual, 1)
			So(ids[0].Identifier.Duration, ShouldEqual, 5*time.Second)
		})

		Convey("Key 应为非零值", func() {
			cm := newTestCounterManager(100)
			addTestCounter(cm, "default", "svc-a", "", 1)

			ids := cm.ListCounterIdentifiers(0, 0)
			So(len(ids), ShouldEqual, 1)
			So(ids[0].Key, ShouldBeGreaterThan, uint32(0))
		})
	})
}

// TestClientManager_ListClients_Pagination 测试 ListClients 分页
func TestClientManager_ListClients_Pagination(t *testing.T) {
	Convey("测试 ClientManager.ListClients 分页", t, func() {
		cm := newTestClientManager(20)
		for i := 0; i < 10; i++ {
			addTestClient(cm, fmt.Sprintf("client-%d", i), "127.0.0.1")
		}

		Convey("limit=3 应只返回 3 条", func() {
			clients := cm.ListClients(0, 3)
			So(len(clients), ShouldEqual, 3)
		})

		Convey("offset 超出总数应返回空切片", func() {
			clients := cm.ListClients(100, 10)
			So(len(clients), ShouldEqual, 0)
		})

		Convey("offset+limit 超出总数时应返回剩余数据", func() {
			clients := cm.ListClients(8, 5)
			So(len(clients), ShouldEqual, 2)
		})

		Convey("limit=0 表示不限制，应返回从 offset 开始的全部数据", func() {
			clients := cm.ListClients(3, 0)
			So(len(clients), ShouldEqual, 7)
		})
	})
}

// TestCounterManagerV2_ListCounterIdentifiers_Pagination 测试 ListCounterIdentifiers 分页
func TestCounterManagerV2_ListCounterIdentifiers_Pagination(t *testing.T) {
	Convey("测试 CounterManagerV2.ListCounterIdentifiers 分页", t, func() {
		cm := newTestCounterManager(100)
		for i := 0; i < 10; i++ {
			addTestCounter(cm, "default", fmt.Sprintf("svc-%d", i), "", 1)
		}

		Convey("limit=3 应只返回 3 条", func() {
			ids := cm.ListCounterIdentifiers(0, 3)
			So(len(ids), ShouldEqual, 3)
		})

		Convey("offset 超出总数应返回空切片", func() {
			ids := cm.ListCounterIdentifiers(100, 10)
			So(len(ids), ShouldEqual, 0)
		})

		Convey("offset+limit 超出总数时应返回剩余数据", func() {
			ids := cm.ListCounterIdentifiers(8, 5)
			So(len(ids), ShouldEqual, 2)
		})

		Convey("limit=0 表示不限制，应返回从 offset 开始的全部数据", func() {
			ids := cm.ListCounterIdentifiers(3, 0)
			So(len(ids), ShouldEqual, 7)
		})
	})
}
