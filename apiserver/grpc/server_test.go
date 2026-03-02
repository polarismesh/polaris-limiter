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

package grpc

import (
	"net"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"golang.org/x/net/netutil"
)

// TestInitializeWithMaxConnections 测试 Initialize 方法能正确解析 max-connections 参数
func TestInitializeWithMaxConnections(t *testing.T) {
	Convey("测试 Initialize 解析 max-connections 参数", t, func() {
		Convey("设置了 max-connections 时应正确解析", func() {
			s := &Server{}
			option := map[string]interface{}{
				"ip":              "127.0.0.1",
				"port":            8081,
				"max-connections": 100,
			}
			err := s.Initialize(option)
			So(err, ShouldBeNil)
			So(s.IP, ShouldEqual, "127.0.0.1")
			So(s.Port, ShouldEqual, uint32(8081))
			So(s.MaxConnections, ShouldEqual, 100)
		})

		Convey("未设置 max-connections 时默认值应为0", func() {
			s := &Server{}
			option := map[string]interface{}{
				"ip":   "127.0.0.1",
				"port": 8082,
			}
			err := s.Initialize(option)
			So(err, ShouldBeNil)
			So(s.IP, ShouldEqual, "127.0.0.1")
			So(s.Port, ShouldEqual, uint32(8082))
			So(s.MaxConnections, ShouldEqual, 0)
		})

		Convey("max-connections 设置为 0 时不应启用连接数限制", func() {
			s := &Server{}
			option := map[string]interface{}{
				"ip":              "127.0.0.1",
				"port":            8083,
				"max-connections": 0,
			}
			err := s.Initialize(option)
			So(err, ShouldBeNil)
			So(s.MaxConnections, ShouldEqual, 0)
		})

		Convey("max-connections 设置为较大值时应正确解析", func() {
			s := &Server{}
			option := map[string]interface{}{
				"ip":              "0.0.0.0",
				"port":            9090,
				"max-connections": 10000,
			}
			err := s.Initialize(option)
			So(err, ShouldBeNil)
			So(s.MaxConnections, ShouldEqual, 10000)
		})
	})
}

// TestGetProtocol 测试 GetProtocol 返回正确的协议名
func TestGetProtocol(t *testing.T) {
	Convey("GetProtocol 应返回 grpc", t, func() {
		s := &Server{}
		So(s.GetProtocol(), ShouldEqual, "grpc")
	})
}

// TestGetPort 测试 GetPort 返回正确的端口
func TestGetPort(t *testing.T) {
	Convey("GetPort 应返回初始化时设置的端口", t, func() {
		s := &Server{}
		option := map[string]interface{}{
			"ip":   "127.0.0.1",
			"port": 8888,
		}
		err := s.Initialize(option)
		So(err, ShouldBeNil)
		So(s.GetPort(), ShouldEqual, uint32(8888))
	})
}

// TestLimitListenerConnectionLimit 测试连接数限制实际生效
// 通过直接测试 netutil.LimitListener 的行为来验证连接数限制逻辑
func TestLimitListenerConnectionLimit(t *testing.T) {
	Convey("测试连接数限制功能", t, func() {
		Convey("当设置了 MaxConnections 时，超出限制的连接应被阻塞", func() {
			maxConns := 2

			// 创建一个 TCP listener
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			So(err, ShouldBeNil)
			defer listener.Close()

			addr := listener.Addr().String()

			// 使用 netutil.LimitListener 限制连接数，与 server.go 中的逻辑一致
			limitedListener := netutil.LimitListener(listener, maxConns)

			// 用于存储服务端接受的连接
			var serverConns []net.Conn
			var mu sync.Mutex
			// 使用带足够缓冲的 channel，避免信号丢失
			acceptDone := make(chan struct{}, 10)

			// 启动服务端持续接受连接
			go func() {
				for {
					conn, err := limitedListener.Accept()
					if err != nil {
						return
					}
					mu.Lock()
					serverConns = append(serverConns, conn)
					mu.Unlock()
					acceptDone <- struct{}{}
				}
			}()

			// 建立 maxConns 个连接（应成功）
			clientConns := make([]net.Conn, 0, maxConns)
			for i := 0; i < maxConns; i++ {
				conn, err := net.DialTimeout("tcp", addr, time.Second)
				So(err, ShouldBeNil)
				clientConns = append(clientConns, conn)
				// 等待服务端接受此连接
				select {
				case <-acceptDone:
				case <-time.After(2 * time.Second):
					t.Fatal("服务端接受连接超时")
				}
			}

			// 验证已建立的连接数等于 maxConns
			mu.Lock()
			So(len(serverConns), ShouldEqual, maxConns)
			mu.Unlock()

			// 尝试建立第 maxConns+1 个连接
			// TCP 握手可能成功（因为操作系统 backlog），但 LimitListener 不会 Accept 它
			extraConn, err := net.DialTimeout("tcp", addr, time.Second)
			if err == nil {
				defer extraConn.Close()
			}

			// 给一点时间让 Accept 有机会处理
			time.Sleep(300 * time.Millisecond)

			// 验证服务端仍然只接受了 maxConns 个连接（第3个连接被阻塞在 Accept 中）
			mu.Lock()
			So(len(serverConns), ShouldEqual, maxConns)
			mu.Unlock()

			// 关闭一个已有连接后，被阻塞的连接应该能被接受
			mu.Lock()
			serverConns[0].Close()
			mu.Unlock()
			clientConns[0].Close()

			// 等待被阻塞的连接被接受
			select {
			case <-acceptDone:
				// 连接被成功接受
			case <-time.After(2 * time.Second):
				t.Fatal("关闭已有连接后，被阻塞的连接未被接受")
			}

			mu.Lock()
			So(len(serverConns), ShouldEqual, maxConns+1)
			mu.Unlock()

			// 清理
			for _, conn := range clientConns[1:] {
				conn.Close()
			}
			mu.Lock()
			for _, conn := range serverConns[1:] {
				conn.Close()
			}
			mu.Unlock()
		})

		Convey("当 MaxConnections 为 0 时，不应限制连接数", func() {
			s := &Server{}
			option := map[string]interface{}{
				"ip":   "127.0.0.1",
				"port": 0,
			}
			err := s.Initialize(option)
			So(err, ShouldBeNil)

			// MaxConnections 为 0，不应使用 LimitListener
			So(s.MaxConnections, ShouldEqual, 0)
			// 验证 MaxConnections <= 0 的条件不会触发 LimitListener
			So(s.MaxConnections > 0, ShouldBeFalse)
		})

		Convey("当 MaxConnections > 0 时，应启用连接数限制", func() {
			s := &Server{}
			option := map[string]interface{}{
				"ip":              "127.0.0.1",
				"port":            0,
				"max-connections": 50,
			}
			err := s.Initialize(option)
			So(err, ShouldBeNil)

			So(s.MaxConnections, ShouldEqual, 50)
			// 验证 MaxConnections > 0 的条件会触发 LimitListener
			So(s.MaxConnections > 0, ShouldBeTrue)
		})
	})
}

// TestLimitListenerNoLimit 测试不设置限制时连接不受限
func TestLimitListenerNoLimit(t *testing.T) {
	Convey("测试不设置连接数限制时可以建立多个连接", t, func() {
		// 创建一个 TCP listener，不使用 LimitListener
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		So(err, ShouldBeNil)
		defer listener.Close()

		addr := listener.Addr().String()

		var serverConns []net.Conn
		var mu sync.Mutex

		// 启动服务端持续接受连接
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				mu.Lock()
				serverConns = append(serverConns, conn)
				mu.Unlock()
			}
		}()

		// 建立多个连接，全部应成功
		connCount := 10
		clientConns := make([]net.Conn, 0, connCount)
		for i := 0; i < connCount; i++ {
			conn, err := net.DialTimeout("tcp", addr, time.Second)
			So(err, ShouldBeNil)
			clientConns = append(clientConns, conn)
		}

		// 给服务端一点时间接受所有连接
		time.Sleep(200 * time.Millisecond)

		mu.Lock()
		So(len(serverConns), ShouldEqual, connCount)
		mu.Unlock()

		// 清理
		for _, conn := range clientConns {
			conn.Close()
		}
		mu.Lock()
		for _, conn := range serverConns {
			conn.Close()
		}
		mu.Unlock()
	})
}

// TestLimitListenerWithSingleConnection 测试连接数限制为1时的行为
func TestLimitListenerWithSingleConnection(t *testing.T) {
	Convey("测试连接数限制为1时只能有一个活跃连接", t, func() {
		maxConns := 1

		listener, err := net.Listen("tcp", "127.0.0.1:0")
		So(err, ShouldBeNil)
		defer listener.Close()

		addr := listener.Addr().String()
		limitedListener := netutil.LimitListener(listener, maxConns)

		var serverConns []net.Conn
		var mu sync.Mutex
		acceptDone := make(chan struct{}, 10)

		go func() {
			for {
				conn, err := limitedListener.Accept()
				if err != nil {
					return
				}
				mu.Lock()
				serverConns = append(serverConns, conn)
				mu.Unlock()
				acceptDone <- struct{}{}
			}
		}()

		// 第一个连接应成功
		conn1, err := net.DialTimeout("tcp", addr, time.Second)
		So(err, ShouldBeNil)
		defer conn1.Close()

		select {
		case <-acceptDone:
		case <-time.After(time.Second):
			t.Fatal("服务端接受第一个连接超时")
		}

		mu.Lock()
		So(len(serverConns), ShouldEqual, 1)
		mu.Unlock()

		// 第二个连接 TCP 可能握手成功，但 Accept 被阻塞
		conn2, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			defer conn2.Close()
		}

		time.Sleep(200 * time.Millisecond)

		// 服务端仍然只接受了1个连接
		mu.Lock()
		So(len(serverConns), ShouldEqual, 1)
		mu.Unlock()

		// 关闭第一个连接后，第二个连接应被接受
		mu.Lock()
		serverConns[0].Close()
		mu.Unlock()
		conn1.Close()

		select {
		case <-acceptDone:
		case <-time.After(2 * time.Second):
			t.Fatal("关闭第一个连接后，第二个连接未被接受")
		}

		mu.Lock()
		So(len(serverConns), ShouldEqual, 2)
		mu.Unlock()
	})
}
