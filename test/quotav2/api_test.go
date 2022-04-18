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

package quotav2

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	. "github.com/smartystreets/goconvey/convey"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	slog "log"
	"net/http"
	"sync"
	"testing"
	"time"

	_ "github.com/polarismesh/polaris-limiter/apiserver/grpc"
	_ "github.com/polarismesh/polaris-limiter/apiserver/http"
	"github.com/polarismesh/polaris-limiter/bootstrap"
	"github.com/polarismesh/polaris-limiter/pkg/api/base"
	apiv2 "github.com/polarismesh/polaris-limiter/pkg/api/v2"
	"github.com/polarismesh/polaris-limiter/pkg/log"
	_ "github.com/polarismesh/polaris-limiter/plugin/statis/file"
)

// 初始化
func init() {
	go func() {
		log.Debugf("pprof listen result %v", http.ListenAndServe("localhost:6060", nil))
	}()
	options := log.DefaultOptions()
	_ = options.SetOutputLevel(log.DefaultScopeName, "debug")
	options.SetStackTraceLevel(log.DefaultScopeName, "none")
	options.SetLogCallers(log.DefaultScopeName, true)
	_ = log.Configure(options)
	bootstrap.NonBlockingStart("testdata/polaris-limiter.yaml", false)
}

const (
	mockServerAddr = "127.0.0.1:8081"
	timeout        = 1 * time.Second
)

var (
	singleSvcName            = "singleTestSvc"
	singleNamespace          = "singleTestNamespace"
	singleMethodName         = "payment"
	singleClientId           = uuid.New().String()
	singleTotal       uint32 = 200
	singleLongSvcName        = "singleLongTestSvc"
	singleLongTotal   uint32 = 1000
)

var (
	multiSvcName1           = "multiTestSvc1"
	multiSvcName2           = "multiTestSvc2"
	multiSvcName3           = "multiTestSvc3"
	multiSvcName4           = "multiTestSvc4"
	multiNamespace          = "multiTestNamespace"
	multiMethodName         = "payToCard"
	multiWholeTotal  uint32 = 200
	multiWholeDivide uint32 = 100
)

//构造初始化请求
func buildInitRequestWitDuration(svcName string, namespace string, methodName string,
	clientId string, totals map[time.Duration]uint32, quotaMode apiv2.QuotaMode) *apiv2.RateLimitRequest {
	req := &apiv2.RateLimitRequest{
		Cmd: apiv2.RateLimitCmd_INIT,
		RateLimitInitRequest: &apiv2.RateLimitInitRequest{
			Target: &apiv2.LimitTarget{
				Namespace: namespace,
				Service:   svcName,
				Labels:    fmt.Sprintf("method:%s", methodName),
			},
			ClientId: clientId},
	}
	for timeDuration, total := range totals {
		req.RateLimitInitRequest.Totals = append(req.RateLimitInitRequest.Totals, &apiv2.QuotaTotal{
			Duration:  uint32(timeDuration.Nanoseconds() / time.Second.Nanoseconds()),
			MaxAmount: total,
			Mode:      quotaMode,
		})
	}
	return req
}

//构造初始化请求
func buildAcquireRequestWitDuration(initResp *apiv2.RateLimitInitResponse,
	usedAmounts map[time.Duration]uint32, limitAmounts map[time.Duration]uint32) *apiv2.RateLimitRequest {
	req := &apiv2.RateLimitRequest{
		Cmd: apiv2.RateLimitCmd_ACQUIRE,
		RateLimitReportRequest: &apiv2.RateLimitReportRequest{
			ClientKey: initResp.GetClientKey(),
			Timestamp: time.Now().UnixNano() / 1e6,
		},
	}
	var idx int
	for duration, used := range usedAmounts {
		req.RateLimitReportRequest.QuotaUses = append(req.RateLimitReportRequest.QuotaUses, &apiv2.QuotaSum{
			CounterKey: initResp.GetCounters()[idx].GetCounterKey(),
			Used:       used,
			Limited:    limitAmounts[duration],
		})
		idx++
	}
	return req
}

//进行初始化，第一个调用init方法，期望看到初始化成功
//配额上报汇总，并发上报成功，期望看到按时间滑窗汇总结果
//配额汇总结果查询，对于确切的key查询，期望返回当前的key的汇总查询结果
func TestSingleThreadInitAcquireQuery(t *testing.T) {
	Convey("测试单线程初始化上报查询", t, func() {
		var opts []grpc.DialOption
		opts = append(opts, grpc.WithInsecure())
		opts = append(opts, grpc.WithBlock())
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		conn, err := grpc.DialContext(ctx, mockServerAddr, opts...)
		So(err, ShouldBeNil)
		defer func() {
			slog.Printf("finallize\n")
			conn.Close()
		}()
		//初始化上报数据
		client := apiv2.NewRateLimitGRPCV2Client(conn)
		initReq := buildInitRequestWitDuration(singleSvcName, singleNamespace, singleMethodName,
			singleClientId, map[time.Duration]uint32{
				time.Second: singleTotal,
			}, apiv2.QuotaMode_WHOLE)
		stream, err := client.Service(context.Background())
		So(err, ShouldBeNil)
		slog.Printf("v2 init request sent\n")
		err = stream.Send(initReq)
		So(err, ShouldBeNil)
		initResp, err := stream.Recv()
		So(err, ShouldBeNil)
		So(initResp.GetRateLimitInitResponse().Code, ShouldEqual, apiv2.ExecuteSuccess)
		slog.Printf("v2 init resp recved, %+v\n", initResp)
		So(initResp.Cmd, ShouldEqual, apiv2.RateLimitCmd_INIT)
		So(initResp.RateLimitInitResponse.Counters[0].Left, ShouldEqual, singleTotal)
		//处理测试逻辑
		var total uint32
		var limited uint32
		for i := 0; i < 2000; i++ {
			var quotaUsed uint32
			if limited > 0 {
				quotaUsed = 0
			} else {
				quotaUsed = 1
			}
			reportReq := buildAcquireRequestWitDuration(initResp.GetRateLimitInitResponse(), map[time.Duration]uint32{
				time.Second: quotaUsed,
			}, map[time.Duration]uint32{
				time.Second: limited,
			})
			limited = 0
			total++
			err = stream.Send(reportReq)
			So(err, ShouldBeNil)
			resp, err := stream.Recv()
			So(err, ShouldBeNil)
			So(resp.Cmd, ShouldEqual, apiv2.RateLimitCmd_ACQUIRE)
			reportResp := resp.GetRateLimitReportResponse()
			So(reportResp.Code, ShouldEqual, apiv2.ExecuteSuccess)
			slog.Printf("v2 report resp recved, %+v\n", reportResp)
			left := reportResp.GetQuotaLefts()[0].Left
			if left <= 0 {
				slog.Printf("start quota limited\n")
				limited++
			} else {
				slog.Printf("end quota limited\n")
				limited = 0
			}
			time.Sleep(4 * time.Millisecond)
		}
	})
}

//进行初始化，第一个调用init方法，期望看到初始化成功
//配额上报汇总，并发上报成功，期望看到按时间滑窗汇总结果
//配额汇总结果查询，对于确切的key查询，期望返回当前的key的汇总查询结果
func TestSingleThreadStreamingFail(t *testing.T) {
	Convey("测试单线程初始化上报查询", t, func() {
		var opts []grpc.DialOption
		opts = append(opts, grpc.WithInsecure())
		opts = append(opts, grpc.WithBlock())
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		conn, err := grpc.DialContext(ctx, mockServerAddr, opts...)
		So(err, ShouldBeNil)
		defer func() {
			slog.Printf("finallize\n")
			conn.Close()
		}()
		//初始化上报数据
		client := apiv2.NewRateLimitGRPCV2Client(conn)
		var streams []apiv2.RateLimitGRPCV2_ServiceClient
		for i := 0; i < 2; i++ {
			initReq := buildInitRequestWitDuration(singleSvcName, singleNamespace, singleMethodName,
				singleClientId, map[time.Duration]uint32{
					time.Second: singleTotal,
				}, apiv2.QuotaMode_WHOLE)
			stream, err := client.Service(context.Background())
			So(err, ShouldBeNil)
			streams = append(streams, stream)
			slog.Printf("v2 init request sent\n")
			err = stream.Send(initReq)
			So(err, ShouldBeNil)
			initResp, err := stream.Recv()
			So(err, ShouldBeNil)
			So(initResp.GetRateLimitInitResponse().Code, ShouldEqual, apiv2.ExecuteSuccess)
			slog.Printf("v2 init resp recved, %+v\n", initResp)
			So(initResp.Cmd, ShouldEqual, apiv2.RateLimitCmd_INIT)
			//处理测试逻辑
			var total uint32
			var limited uint32
			var quotaUsed uint32
			if limited > 0 {
				quotaUsed = 0
			} else {
				quotaUsed = 1
			}
			reportReq := buildAcquireRequestWitDuration(initResp.GetRateLimitInitResponse(), map[time.Duration]uint32{
				time.Second: quotaUsed,
			}, map[time.Duration]uint32{
				time.Second: limited,
			})
			limited = 0
			total++
			err = stream.Send(reportReq)
			So(err, ShouldBeNil)
			resp, err := stream.Recv()
			So(err, ShouldBeNil)
			So(resp.Cmd, ShouldEqual, apiv2.RateLimitCmd_ACQUIRE)
			reportResp := resp.GetRateLimitReportResponse()
			So(reportResp.Code, ShouldEqual, apiv2.ExecuteSuccess)
			slog.Printf("v2 report resp recved, %+v\n", reportResp)
			left := reportResp.GetQuotaLefts()[0].Left
			if left <= 0 {
				slog.Printf("start quota limited\n")
				limited++
			} else {
				slog.Printf("end quota limited\n")
				limited = 0
			}
			time.Sleep(4 * time.Millisecond)
		}
		for _, stream := range streams {
			stream.CloseSend()
		}
	})
	time.Sleep(2 * time.Second)
}

//进行初始化，第一个调用init方法，期望看到初始化成功
//配额上报汇总，并发上报成功，期望看到按时间滑窗汇总结果
//配额汇总结果查询，对于确切的key查询，期望返回当前的key的汇总查询结果
func TestSingleThreadStreamingQuery(t *testing.T) {
	Convey("测试单线程初始化上报查询", t, func() {
		var opts []grpc.DialOption
		opts = append(opts, grpc.WithInsecure())
		opts = append(opts, grpc.WithBlock())
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		conn, err := grpc.DialContext(ctx, mockServerAddr, opts...)
		So(err, ShouldBeNil)
		defer func() {
			slog.Printf("finallize\n")
			conn.Close()
		}()
		//初始化上报数据
		client := apiv2.NewRateLimitGRPCV2Client(conn)
		for i := 0; i < 200; i++ {
			initReq := buildInitRequestWitDuration(singleSvcName, singleNamespace, singleMethodName,
				singleClientId, map[time.Duration]uint32{
					time.Second: singleTotal,
				}, apiv2.QuotaMode_WHOLE)
			stream, err := client.Service(context.Background())
			So(err, ShouldBeNil)
			slog.Printf("v2 init request sent\n")
			err = stream.Send(initReq)
			So(err, ShouldBeNil)
			initResp, err := stream.Recv()
			So(err, ShouldBeNil)
			So(initResp.GetRateLimitInitResponse().Code, ShouldEqual, apiv2.ExecuteSuccess)
			slog.Printf("v2 init resp recved, %+v\n", initResp)
			So(initResp.Cmd, ShouldEqual, apiv2.RateLimitCmd_INIT)
			//处理测试逻辑
			var total uint32
			var limited uint32
			var quotaUsed uint32
			if limited > 0 {
				quotaUsed = 0
			} else {
				quotaUsed = 1
			}
			reportReq := buildAcquireRequestWitDuration(initResp.GetRateLimitInitResponse(), map[time.Duration]uint32{
				time.Second: quotaUsed,
			}, map[time.Duration]uint32{
				time.Second: limited,
			})
			limited = 0
			total++
			err = stream.Send(reportReq)
			So(err, ShouldBeNil)
			resp, err := stream.Recv()
			So(err, ShouldBeNil)
			So(resp.Cmd, ShouldEqual, apiv2.RateLimitCmd_ACQUIRE)
			reportResp := resp.GetRateLimitReportResponse()
			So(reportResp.Code, ShouldEqual, apiv2.ExecuteSuccess)
			slog.Printf("v2 report resp recved, %+v\n", reportResp)
			left := reportResp.GetQuotaLefts()[0].Left
			if left <= 0 {
				slog.Printf("start quota limited\n")
				limited++
			} else {
				slog.Printf("end quota limited\n")
				limited = 0
			}
			stream.CloseSend()
			time.Sleep(4 * time.Millisecond)
		}
	})
}

//单机均摊阈值的测试
func TestMultiThreadDivideInitAcquireQuery(t *testing.T) {
	testMultiThreadInitAcquireQuery(t, multiSvcName1, multiWholeDivide, apiv2.QuotaMode_DIVIDE)
}

//单机均摊阈值的测试
func TestMultiThreadWholeInitAcquireQuery(t *testing.T) {
	testMultiThreadInitAcquireQuery(t, multiSvcName2, multiWholeTotal, apiv2.QuotaMode_WHOLE)
}

//返回期待的总量
func expectTotal(count int, total uint32, mode apiv2.QuotaMode) uint32 {
	if mode == apiv2.QuotaMode_WHOLE {
		return total
	}
	return total * uint32(count)
}

const IpPattern = "127.0.0.%d"

//单个线程内测试限流
func testRateLimitInOneThread(idx int, wg *sync.WaitGroup, t *testing.T,
	conn *grpc.ClientConn, svcName string, total uint32, mode apiv2.QuotaMode) {
	defer wg.Done()
	Convey(fmt.Sprintf("测试客户端%d上报查询", idx), t, func() {
		client := apiv2.NewRateLimitGRPCV2Client(conn)
		md := metadata.New(map[string]string{base.HeaderKeyClientIP: fmt.Sprintf(IpPattern, 11)})
		stream, err := client.Service(metadata.NewOutgoingContext(context.Background(), md))
		So(err, ShouldBeNil)
		initReq := buildInitRequestWitDuration(svcName, multiNamespace, multiMethodName,
			uuid.New().String(), map[time.Duration]uint32{
				time.Second: total,
			}, mode)
		slog.Printf("v2 init request %d sent\n", idx)
		err = stream.Send(initReq)
		So(err, ShouldBeNil)
		initResp, err := stream.Recv()
		So(err, ShouldBeNil)
		So(initResp.GetRateLimitInitResponse().Code, ShouldEqual, apiv2.ExecuteSuccess)
		slog.Printf("v2 init resp %d recved, %+v\n", idx, initResp)
		So(initResp.Cmd, ShouldEqual, apiv2.RateLimitCmd_INIT)
		err = stream.Send(initReq)
		So(err, ShouldBeNil)
		initResp, err = stream.Recv()
		So(err, ShouldBeNil)
		//处理测试逻辑
		var total uint32
		var limited uint32
		for i := 0; i < 20000; i++ {
			var quotaUsed uint32
			if limited > 0 {
				quotaUsed = 0
			} else {
				quotaUsed = 1
			}
			reportReq := buildAcquireRequestWitDuration(initResp.GetRateLimitInitResponse(),
				map[time.Duration]uint32{
					time.Second: quotaUsed,
				}, map[time.Duration]uint32{
					time.Second: limited,
				})
			total++
			err = stream.Send(reportReq)
			So(err, ShouldBeNil)
			resp, err := stream.Recv()
			So(err, ShouldBeNil)
			So(resp.Cmd, ShouldEqual, apiv2.RateLimitCmd_ACQUIRE)
			reportResp := resp.GetRateLimitReportResponse()
			So(reportResp.Code, ShouldEqual, apiv2.ExecuteSuccess)
			slog.Printf("v2 report %d resp recved, %+v\n", idx, reportResp)
			left := reportResp.GetQuotaLefts()[0].Left
			if left <= 0 {
				slog.Printf("start quota limited\n")
				limited++
			} else {
				slog.Printf("end quota limited\n")
				limited = 0
			}
			time.Sleep(4 * time.Millisecond)
		}
	})
}

//进行初始化，第一个调用init方法，期望看到初始化成功
//配额上报汇总，并发上报成功，期望看到按时间滑窗汇总结果
//配额汇总结果查询，对于确切的key查询，期望返回当前的key的汇总查询结果
func testMultiThreadInitAcquireQuery(t *testing.T, svcName string, total uint32, mode apiv2.QuotaMode) {
	var opts []grpc.DialOption
	opts = append(opts, grpc.WithInsecure())
	opts = append(opts, grpc.WithBlock())
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := grpc.DialContext(ctx, mockServerAddr, opts...)
	if nil != err {
		panic(err)
	}
	defer func() {
		slog.Printf("finallize\n")
		conn.Close()
	}()
	//初始化上报数据
	wg := &sync.WaitGroup{}
	clientCount := 4
	wg.Add(clientCount)

	for i := 0; i < clientCount; i++ {
		go testRateLimitInOneThread(i, wg, t, conn, svcName, total, mode)
	}
	wg.Wait()
	time.Sleep(10 * time.Second)
}

//测试超过1s周期的限流表现
func TestSingleThreadLongAcquireQuery(t *testing.T) {
	Convey("测试单线程初始化上报查询", t, func() {
		var opts []grpc.DialOption
		opts = append(opts, grpc.WithInsecure())
		opts = append(opts, grpc.WithBlock())
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		conn, err := grpc.DialContext(ctx, mockServerAddr, opts...)
		So(err, ShouldBeNil)
		defer func() {
			slog.Printf("finallize\n")
			conn.Close()
		}()
		//初始化上报数据
		client := apiv2.NewRateLimitGRPCV2Client(conn)
		initReq := buildInitRequestWitDuration(singleLongSvcName, singleNamespace, singleMethodName,
			singleClientId, map[time.Duration]uint32{
				10 * time.Second: singleLongTotal,
			}, apiv2.QuotaMode_WHOLE)
		stream, err := client.Service(context.Background())
		So(err, ShouldBeNil)
		slog.Printf("v2 init request sent\n")
		err = stream.Send(initReq)
		So(err, ShouldBeNil)
		initResp, err := stream.Recv()
		So(err, ShouldBeNil)
		So(initResp.GetRateLimitInitResponse().Code, ShouldEqual, apiv2.ExecuteSuccess)
		slog.Printf("v2 init resp recved, %+v\n", initResp)
		So(initResp.Cmd, ShouldEqual, apiv2.RateLimitCmd_INIT)
		So(initResp.RateLimitInitResponse.Counters[0].Left, ShouldEqual, singleLongTotal)
		//处理测试逻辑
		var total uint32
		var limited uint32
		timeoutCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	L1:
		for {
			select {
			case <-timeoutCtx.Done():
				break L1
			default:
				var quotaUsed uint32
				if limited > 0 {
					quotaUsed = 0
				} else {
					quotaUsed = 1
				}
				reportReq := buildAcquireRequestWitDuration(initResp.GetRateLimitInitResponse(), map[time.Duration]uint32{
					10 * time.Second: quotaUsed,
				}, map[time.Duration]uint32{
					10 * time.Second: limited,
				})
				limited = 0
				total++
				err = stream.Send(reportReq)
				So(err, ShouldBeNil)
				resp, err := stream.Recv()
				So(err, ShouldBeNil)
				So(resp.Cmd, ShouldEqual, apiv2.RateLimitCmd_ACQUIRE)
				reportResp := resp.GetRateLimitReportResponse()
				So(reportResp.Code, ShouldEqual, apiv2.ExecuteSuccess)
				slog.Printf("v2 report resp recved, %+v\n", reportResp)
				left := reportResp.GetQuotaLefts()[0].Left
				if left <= 0 {
					slog.Printf("start quota limited\n")
					limited++
				} else {
					slog.Printf("end quota limited\n")
					limited = 0
				}
				time.Sleep(5 * time.Millisecond)
			}

		}
	})
}

//测试时间对齐功能
func TestTimeAdjust(t *testing.T) {
	Convey("测试单线程初始化上报查询", t, func() {
		var opts []grpc.DialOption
		opts = append(opts, grpc.WithInsecure())
		opts = append(opts, grpc.WithBlock())
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		conn, err := grpc.DialContext(ctx, mockServerAddr, opts...)
		So(err, ShouldBeNil)
		defer func() {
			slog.Printf("finallize\n")
			conn.Close()
		}()
		//初始化上报数据
		client := apiv2.NewRateLimitGRPCV2Client(conn)
		resp, err := client.TimeAdjust(context.Background(), &apiv2.TimeAdjustRequest{})
		So(err, ShouldBeNil)
		_ = resp
		timeStamp := time.Unix(0, resp.ServerTimestamp*1e6)
		slog.Printf("server timestamp is %s\n", timeStamp)
	})
}

//测试客户端上下线
func TestClientUpAndDown(t *testing.T) {
	var opts []grpc.DialOption
	opts = append(opts, grpc.WithInsecure())
	opts = append(opts, grpc.WithBlock())
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := grpc.DialContext(ctx, mockServerAddr, opts...)
	if nil != err {
		panic(err)
	}
	defer func() {
		slog.Printf("finallize\n")
		conn.Close()
	}()
	//初始化上报数据
	wg := &sync.WaitGroup{}
	clientCount := 2
	wg.Add(clientCount)
	var total uint32 = 100
	for i := 0; i < clientCount; i++ {
		go func(idx int) {
			defer wg.Done()
			Convey(fmt.Sprintf("测试客户端%d上报查询", idx), t, func() {
				client := apiv2.NewRateLimitGRPCV2Client(conn)
				for i := 0; i < 4; i++ {
					func() {
						stream, err := client.Service(context.Background())
						So(err, ShouldBeNil)
						defer stream.CloseSend()
						clientId := uuid.New().String()
						initReq := buildInitRequestWitDuration(multiSvcName3, multiNamespace, multiMethodName,
							clientId, map[time.Duration]uint32{
								time.Second: total,
							}, apiv2.QuotaMode_DIVIDE)
						slog.Printf("v2 init request %d sent\n", idx)
						err = stream.Send(initReq)
						So(err, ShouldBeNil)
						initResp, err := stream.Recv()
						So(err, ShouldBeNil)
						So(initResp.GetRateLimitInitResponse().Code, ShouldEqual, apiv2.ExecuteSuccess)
						slog.Printf("v2 init resp %d recved, %+v\n", idx, initResp)
						time.Sleep(1 * time.Second)
					}()
				}
			})
		}(i)
	}
	wg.Wait()
	slog.Printf("start the last report\n")
	Convey(fmt.Sprintf("测试最后一次上报查询"), t, func() {
		time.Sleep(4 * time.Second)
		client := apiv2.NewRateLimitGRPCV2Client(conn)
		stream, err := client.Service(context.Background())
		So(err, ShouldBeNil)
		defer stream.CloseSend()
		clientId := uuid.New().String()
		slog.Printf("clientId is %s, time is %s\n", clientId, time.Now())
		initReq := buildInitRequestWitDuration(multiSvcName3, multiNamespace, multiMethodName,
			clientId, map[time.Duration]uint32{
				time.Second: total,
			}, apiv2.QuotaMode_DIVIDE)
		slog.Printf("v2 init request over sent\n")
		err = stream.Send(initReq)
		So(err, ShouldBeNil)
		initResp, err := stream.Recv()
		So(err, ShouldBeNil)
		So(initResp.GetRateLimitInitResponse().Code, ShouldEqual, apiv2.ExecuteSuccess)
		slog.Printf("v2 init resp over recved, %+v\n", initResp)
		So(initResp.RateLimitInitResponse.Counters[0].Left, ShouldEqual, total)
	})
	time.Sleep(5 * time.Second)

}

//测试客户端上下线
func TestMultiClientReconnect(t *testing.T) {
	var opts []grpc.DialOption
	opts = append(opts, grpc.WithInsecure())
	opts = append(opts, grpc.WithBlock())
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := grpc.DialContext(ctx, mockServerAddr, opts...)
	if nil != err {
		panic(err)
	}
	defer func() {
		slog.Printf("finallize\n")
		conn.Close()
	}()
	//初始化上报数据
	wg := &sync.WaitGroup{}
	clientCount := 1
	wg.Add(clientCount)
	var total uint32 = 1000
	for i := 0; i < clientCount; i++ {
		go func(idx int) {
			defer wg.Done()
			Convey(fmt.Sprintf("测试客户端%d上报查询", idx), t, func() {
				client := apiv2.NewRateLimitGRPCV2Client(conn)
				clientId := uuid.New().String()
				for i := 0; i < 2; i++ {
					func(idx int) {
						stream, err := client.Service(context.Background())
						So(err, ShouldBeNil)
						defer func(theStream apiv2.RateLimitGRPCV2_ServiceClient) {
							go func() {
								if idx == 0 {
									fmt.Printf("start to wait 2s\n")
									time.Sleep(1 * time.Second)
								}
								theStream.CloseSend()
							}()
						}(stream)
						initReq := buildInitRequestWitDuration(multiSvcName4, multiNamespace,
							fmt.Sprintf("%s_%d", multiMethodName, i),
							clientId, map[time.Duration]uint32{
								time.Second: total,
							}, apiv2.QuotaMode_WHOLE)
						slog.Printf("v2 init request %d sent\n", idx)
						err = stream.Send(initReq)
						So(err, ShouldBeNil)
						initResp, err := stream.Recv()
						So(err, ShouldBeNil)
						So(initResp.GetRateLimitInitResponse().Code, ShouldEqual, apiv2.ExecuteSuccess)
						slog.Printf("v2 init resp %d recved, %+v\n", idx, initResp)
					}(i)
				}
			})
		}(i)
	}
	wg.Wait()

	time.Sleep(5 * time.Second)
}
