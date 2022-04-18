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
	"google.golang.org/grpc"
	"log"
	"math"
	"time"

	apiv2 "github.com/polarismesh/polaris-limiter/pkg/api/v2"
	"github.com/polarismesh/polaris-limiter/pkg/utils"
)

var (
	serverAddress string
	times int

)

//初始化
func initArgs() {
	flag.StringVar(&serverAddress, "server_address", "", "limit server address")
	flag.IntVar(&times, "times", 10, "test times")
}

//测试时间同步
func main() {
	initArgs()
	flag.Parse()
	var opts []grpc.DialOption
	opts = append(opts, grpc.WithInsecure())
	opts = append(opts, grpc.WithBlock())
	ctx, cancel := context.WithTimeout(context.Background(), 1 *time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, serverAddress, opts...)
	if nil != err {
		log.Fatal(err)
	}
	defer func() {
		conn.Close()
	}()
	client := apiv2.NewRateLimitGRPCV2Client(conn)
	var totalDiffs []float64
	var allTotal float64
	for i := 0; i < times; i++ {
		startTime := utils.CurrentMillisecond()
		servTime, err := client.TimeAdjust(context.Background(), &apiv2.TimeAdjustRequest{})
		if nil != err {
			log.Fatal(err)
		}
		endTime := utils.CurrentMillisecond()
		curDiffRound := math.Abs(float64(servTime.ServerTimestamp - startTime + (endTime - servTime.ServerTimestamp)))
		curDiff := curDiffRound/2
		fmt.Printf("diff is %.2f for round %d\n", curDiff, i)
		totalDiffs = append(totalDiffs, curDiff)
		allTotal += curDiff
	}
	fmt.Printf("avg diff is %.2f\n", allTotal/float64(times))
}

