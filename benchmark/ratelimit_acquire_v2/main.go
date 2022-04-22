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
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"

	apiv2 "github.com/polarismesh/polaris-limiter/pkg/api/v2"
)

var (
	serverAddress string
	concurrency   int
	timeout       time.Duration
)

func initArgs() {
	flag.StringVar(&serverAddress, "server_address", "", "limit server address")
	flag.IntVar(&concurrency, "concurrency", 1, "test concurrency")
	flag.DurationVar(&timeout, "timeout", 20*time.Second, "test timeout")
}

var (
	sentCount uint32
	recvCount uint32
)

var (
	benchSvcName           = "benchTestSvc"
	benchNamespace         = "Test"
	benchMethodName        = "payment"
	benchTotal      uint32 = 20000
)

// 构造初始化请求
func buildInitRequest() *apiv2.RateLimitRequest {
	req := &apiv2.RateLimitRequest{
		Cmd: apiv2.RateLimitCmd_INIT,
		RateLimitInitRequest: &apiv2.RateLimitInitRequest{
			Target: &apiv2.LimitTarget{
				Namespace: benchNamespace,
				Service:   benchSvcName,
				Labels:    fmt.Sprintf("method:%s", benchMethodName),
			},
			ClientId: uuid.New().String(),
			Totals: []*apiv2.QuotaTotal{
				{
					Duration:  1,
					MaxAmount: benchTotal,
				},
			},
		},
	}
	return req
}

// 构造初始化请求
func buildReportRequest(resp *apiv2.RateLimitInitResponse, baseTimeMilli int64) *apiv2.RateLimitRequest {
	req := &apiv2.RateLimitRequest{
		Cmd: apiv2.RateLimitCmd_ACQUIRE,
		RateLimitReportRequest: &apiv2.RateLimitReportRequest{
			ClientKey: resp.ClientKey,
			QuotaUses: []*apiv2.QuotaSum{
				{
					CounterKey: resp.Counters[0].CounterKey,
					Used:       1,
				},
			},
			Timestamp: resp.Timestamp + (time.Now().UnixNano()/1e6 - baseTimeMilli),
		},
	}
	return req
}

// 执行初始化操作
func doRateLimitInits(concurrency int) (
	[]*apiv2.RateLimitInitResponse, []int64, []apiv2.RateLimitGRPCV2_ServiceClient, []*grpc.ClientConn) {
	conns := make([]*grpc.ClientConn, 0, concurrency)
	conn, err := grpc.Dial(serverAddress, grpc.WithInsecure())
	if err != nil {
		log.Fatalln(err)
	}
	conns = append(conns, conn)
	client := apiv2.NewRateLimitGRPCV2Client(conn)
	initResponses := make([]*apiv2.RateLimitInitResponse, 0, concurrency)
	baseTimes := make([]int64, 0, concurrency)
	streams := make([]apiv2.RateLimitGRPCV2_ServiceClient, 0, concurrency)
	for i := 0; i < concurrency; i++ {
		stream, err := client.Service(context.Background())
		if err != nil {
			log.Fatalln(err)
		}
		// 执行初始化
		initReq := buildInitRequest()
		log.Printf("start to init %d, uid %s", i, initReq.GetRateLimitInitRequest().GetClientId())
		err = stream.Send(initReq)
		if err != nil {
			log.Fatalln(err)
		}
		resp, err := stream.Recv()
		if err != nil {
			log.Fatalln(err)
		}
		if resp.RateLimitInitResponse.Code != uint32(apiv2.ExecuteSuccess) {
			log.Fatalln(fmt.Sprintf("fail to init, response is %+v", resp))
		}
		streams = append(streams, stream)
		initResponses = append(initResponses, resp.RateLimitInitResponse)
		baseTimes = append(baseTimes, time.Now().UnixNano()/1e6)
	}
	return initResponses, baseTimes, streams, conns
}

// 性能测试流程
func main() {
	initArgs()
	flag.Parse()
	log.Println(fmt.Sprintf("Client running ..., server address is %s", serverAddress))

	initResponses, baseTimes, streams, conns := doRateLimitInits(concurrency)
	defer func() {
		for _, stream := range streams {
			if nil != stream {
				stream.CloseSend()
			}
		}
		for _, conn := range conns {
			if nil != conn {
				conn.Close()
			}
		}
	}()
	wg := &sync.WaitGroup{}
	wg.Add(concurrency)
	var err error
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
					reportReq := buildReportRequest(initResponses[idx], baseTimes[idx])
					err = streams[idx].Send(reportReq)
					if err != nil {
						log.Fatalln(err)
					}
					sentCount++
					// time.Sleep(100 * time.Microsecond)
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
					_, err = streams[idx].Recv()
					if err != nil {
						if err == io.EOF {
							log.Printf("ctx %d recv eof", idx)
							return
						}
						log.Fatalln(err)
					}
					recvCount++
				}
			}
		}(i)
	}

	log.Printf("start to wait until finish\n")
	wg.Wait()
	log.Printf("result: sent %d, recv %d\n", sentCount, recvCount)
}
