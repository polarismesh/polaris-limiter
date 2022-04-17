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

package utils

import (
	"encoding/binary"
	"net"
)

//IP地址
type IPAddress struct {
	//低位, 0-7
	LowValue uint64
	//高位, 8-15
	HighValue uint64
}

//创建IP地址数据
func NewIPAddress(addr string) *IPAddress {
	ipAddr := &IPAddress{}
	ip := net.ParseIP(addr)
	if nil == ip {
		return ipAddr
	}
	mid := net.IPv6len / 2
	ipAddr.HighValue = binary.BigEndian.Uint64(ip[0:mid])
	ipAddr.LowValue = binary.BigEndian.Uint64(ip[mid:net.IPv6len])
	return ipAddr
}

//转成字符串输出
func (addr IPAddress) String() string {
	ip := make(net.IP, net.IPv6len)
	mid := net.IPv6len / 2
	binary.BigEndian.PutUint64(ip[0:mid], addr.HighValue)
	binary.BigEndian.PutUint64(ip[mid:net.IPv6len], addr.LowValue)
	return ip.String()
}
