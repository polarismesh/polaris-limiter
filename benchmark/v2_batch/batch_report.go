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

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"

	apiv2 "github.com/polarismesh/polaris-limiter/pkg/api/v2"
)

var (
	serverAddress  string
	concurrency    int
	timeout        time.Duration
	labelCount     int // 同时生效的label
	expireInterval int // label过期间隔
	batchCount     int
)

var (
	sentCount int
	recvCount int
)

var (
	benchNamespace        = "Test"
	benchSvcName          = "benchTestSvc"
	benchTotal     uint32 = 200000
)

type Client struct {
	idx          int
	conn         *grpc.ClientConn
	stream       apiv2.RateLimitGRPCV2_ServiceClient
	clientId     string
	lastUid      int
	clientKey    uint32
	mutex        *sync.Mutex
	counterKeys  map[int]uint32
	lastInitTime int
}

func (client *Client) Send() {
	if client.lastUid <= labelCount || client.lastInitTime+expireInterval < time.Now().Second() { // 先初始化
		client.lastUid++
		req := &apiv2.RateLimitRequest{
			Cmd: apiv2.RateLimitCmd_INIT,
			RateLimitInitRequest: &apiv2.RateLimitInitRequest{
				Target: &apiv2.LimitTarget{Namespace: benchNamespace, Service: benchSvcName,
					Labels: fmt.Sprintf("uin:abcde_%d", client.lastUid)},
				ClientId: client.clientId,
				Totals:   []*apiv2.QuotaTotal{{Duration: 1, MaxAmount: benchTotal}}},
		}
		err := client.stream.Send(req)
		if err != nil {
			log.Fatalln(err)
		}
		sentCount++
		client.lastInitTime = time.Now().Second()
		return
	}

	clientKey := atomic.LoadUint32(&client.clientKey)
	if clientKey > 0 { // 检查是否有初始化请求完成
		client.mutex.Lock()
		req := &apiv2.RateLimitRequest{
			Cmd: apiv2.RateLimitCmd_ACQUIRE,
			RateLimitReportRequest: &apiv2.RateLimitReportRequest{
				ClientKey: client.clientKey,
				QuotaUses: []*apiv2.QuotaSum{{
					CounterKey: client.counterKeys[sentCount%len(client.counterKeys)],
					Used:       1}},
				Timestamp: time.Now().UnixNano() / 1e6,
			},
		}
		client.mutex.Unlock()

		err := client.stream.Send(req)
		if err != nil {
			log.Fatalln(err)
		}
		sentCount++
		return
	}

	time.Sleep(10 * time.Microsecond) // 等待初始化完成
}

func (client *Client) Recv() {
	resp, err := client.stream.Recv()
	if err != nil {
		if err == io.EOF {
			log.Printf("ctx %d recv eof", client.idx)
			return
		}
		log.Fatalln(err)
	}
	if resp.Cmd == apiv2.RateLimitCmd_INIT {
		counterKey := resp.RateLimitInitResponse.GetCounters()[0].CounterKey
		client.mutex.Lock()
		mapLen := len(client.counterKeys)
		if mapLen < labelCount {
			client.counterKeys[mapLen] = counterKey
		} else {
			client.counterKeys[client.lastUid%labelCount] = counterKey
		}
		client.mutex.Unlock()
		if resp.RateLimitInitResponse.ClientKey > 0 {
			atomic.StoreUint32(&client.clientKey, resp.RateLimitInitResponse.ClientKey)
		}
	}
	recvCount++
}

func (client *Client) BatchSend() {
	if client.lastUid == 0 { // 首次批量初始化
		labels := make([]string, 0, labelCount)
		for i := 0; i < labelCount; i++ {
			labels = append(labels, fmt.Sprintf("uin:abcde_%d", i))
		}
		client.lastUid = labelCount

		initRequests := make([]*apiv2.RateLimitInitRequest, 0, 1)
		initRequests = append(initRequests, &apiv2.RateLimitInitRequest{
			Target: &apiv2.LimitTarget{Namespace: benchNamespace, Service: benchSvcName, LabelsList: labels},
			Totals: []*apiv2.QuotaTotal{{Duration: 1, MaxAmount: benchTotal}}})
		req := &apiv2.RateLimitRequest{
			Cmd: apiv2.RateLimitCmd_BATCH_INIT,
			RateLimitBatchInitRequest: &apiv2.RateLimitBatchInitRequest{
				ClientId: client.clientId,
				Request:  initRequests,
			},
		}
		err := client.stream.Send(req)
		if err != nil {
			log.Fatalln(err)
		}
		sentCount++
		client.lastInitTime = time.Now().Second()
		return
	}

	clientKey := atomic.LoadUint32(&client.clientKey)
	if clientKey == 0 { // 检查是否有初始化请求完成
		time.Sleep(10 * time.Microsecond) // 等待初始化完成
		return
	}

	if client.lastInitTime+expireInterval < time.Now().Second() { // 初始化新的label
		client.lastUid++
		client.lastInitTime = time.Now().Second()

		req := &apiv2.RateLimitRequest{
			Cmd: apiv2.RateLimitCmd_INIT,
			RateLimitInitRequest: &apiv2.RateLimitInitRequest{
				Target: &apiv2.LimitTarget{Namespace: benchNamespace, Service: benchSvcName,
					Labels: fmt.Sprintf("uin:abcde_%d", client.lastUid)},
				ClientId: client.clientId,
				Totals:   []*apiv2.QuotaTotal{{Duration: 1, MaxAmount: benchTotal}}},
		}
		err := client.stream.Send(req)
		if err != nil {
			log.Fatalln(err)
		}
		sentCount++
		return
	}

	client.mutex.Lock()
	quotaUses := make([]*apiv2.QuotaSum, 0, len(client.counterKeys))
	for _, value := range client.counterKeys {
		quotaUses = append(quotaUses, &apiv2.QuotaSum{CounterKey: value, Used: 1})
	}
	client.mutex.Unlock()

	req := &apiv2.RateLimitRequest{
		Cmd: apiv2.RateLimitCmd_BATCH_ACQUIRE,
		RateLimitReportRequest: &apiv2.RateLimitReportRequest{
			ClientKey: client.clientKey,
			QuotaUses: quotaUses,
			Timestamp: time.Now().UnixNano() / 1e6,
		},
	}

	err := client.stream.Send(req)
	if err != nil {
		log.Fatalln(err)
	}
	sentCount++
}

func (client *Client) BatchRecv() {
	resp, err := client.stream.Recv()
	if err != nil {
		if err == io.EOF {
			log.Printf("ctx %d recv eof", client.idx)
			return
		}
		log.Fatalln(err)
	}
	if resp.Cmd == apiv2.RateLimitCmd_BATCH_INIT && resp.GetRateLimitBatchInitResponse() != nil {
		counter := resp.RateLimitBatchInitResponse.Result[0].Counters
		client.mutex.Lock()
		counterLen := len(counter)
		for i := 0; i < counterLen; i++ {
			mapLen := len(client.counterKeys)
			if mapLen < labelCount {
				client.counterKeys[mapLen] = counter[i].Counters[0].CounterKey
			} else {
				client.counterKeys[client.lastUid%labelCount] = counter[i].Counters[0].CounterKey
			}
		}
		client.mutex.Unlock()
		if resp.RateLimitBatchInitResponse.ClientKey > 0 {
			atomic.StoreUint32(&client.clientKey, resp.RateLimitBatchInitResponse.ClientKey)
		}
	} else if resp.Cmd == apiv2.RateLimitCmd_INIT {
		counterKey := resp.RateLimitInitResponse.GetCounters()[0].CounterKey
		client.mutex.Lock()
		mapLen := len(client.counterKeys)
		if mapLen < labelCount {
			client.counterKeys[mapLen] = counterKey
		} else {
			client.counterKeys[client.lastUid%labelCount] = counterKey
		}
		client.mutex.Unlock()
		if resp.RateLimitInitResponse.ClientKey > 0 {
			atomic.StoreUint32(&client.clientKey, resp.RateLimitInitResponse.ClientKey)
		}
	}
	recvCount++
}

// 关闭客户端
func (client *Client) Close() {
	client.stream.CloseSend()
	client.conn.Close()
}

// 建立连接
func createStream(concurrency int) []*Client {
	clients := make([]*Client, 0, concurrency)
	for i := 0; i < concurrency; i++ {
		conn, err := grpc.Dial(serverAddress, grpc.WithInsecure())
		if err != nil {
			log.Fatalln(err)
		}
		stream, err := apiv2.NewRateLimitGRPCV2Client(conn).Service(context.Background())
		if err != nil {
			log.Fatalln(err)
		}
		client := &Client{idx: i, conn: conn, stream: stream, clientId: uuid.New().String(), lastUid: 0,
			clientKey: 0, mutex: &sync.Mutex{}, counterKeys: make(map[int]uint32), lastInitTime: time.Now().Second()}
		clients = append(clients, client)
	}
	return clients
}

// 运行测试
func runBench() {
	clients := createStream(concurrency)
	defer func() { // 关闭连接
		for _, client := range clients {
			client.Close()
		}
	}()

	wg := &sync.WaitGroup{}
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		timeoutCtx, _ := context.WithTimeout(context.Background(), timeout)
		go func(idx int) {
			for {
				select {
				case <-timeoutCtx.Done():
					wg.Done()
					log.Printf("ctx %d sent exits", idx)
					return
				default:
					clients[idx].Send()
				}
			}
		}(i)
		go func(idx int) {
			for {
				select {
				case <-timeoutCtx.Done():
					log.Printf("ctx %d recv exits", idx)
					return
				default:
					clients[idx].Recv()
				}
			}
		}(i)
	}

	log.Printf("start to wait until finish\n")
	wg.Wait()
}

// 运行批量测试
func runBatchBench() {
	clients := createStream(concurrency)
	defer func() { // 关闭连接
		for _, client := range clients {
			client.Close()
		}
	}()

	wg := &sync.WaitGroup{}
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		timeoutCtx, _ := context.WithTimeout(context.Background(), timeout)
		go func(idx int) {
			for {
				select {
				case <-timeoutCtx.Done():
					wg.Done()
					log.Printf("ctx %d sent exits", idx)
					return
				default:
					clients[idx].BatchSend()
				}
			}
		}(i)
		go func(idx int) {
			for {
				select {
				case <-timeoutCtx.Done():
					log.Printf("ctx %d recv exits", idx)
					return
				default:
					clients[idx].BatchRecv()
				}
			}
		}(i)
	}

	log.Printf("start to wait until finish\n")
	wg.Wait()
}

// 性能测试流程
func main() {
	flag.StringVar(&serverAddress, "server_address", "", "limit server address")
	flag.DurationVar(&timeout, "timeout", 180*time.Second, "test timeout")
	flag.IntVar(&concurrency, "concurrency", 8, "test concurrency")
	flag.IntVar(&labelCount, "label", 100, "label count")
	flag.IntVar(&expireInterval, "expire", 10, "expire interval")
	flag.IntVar(&batchCount, "batch", 1, "batch count ")
	flag.Parse()
	log.Println(fmt.Sprintf("Client running ..., server address is %s", serverAddress))

	if batchCount > 1 {
		runBatchBench()
	} else {
		runBench()
	}

	log.Printf("result: sent %d, recv %d\n", sentCount, recvCount)
}
