/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-10 17:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-10 17:00:00
 * @FilePath: \go-wsc\client\servers.go
 * @Description: LB 直连地址管理 —— 多地址列表清理与 headless Service 域名解析
 *
 * 长连接场景的 LB 分布不均根因：连接建立后不迁移，初始分布由转发层决定，
 * 且断线重连集中打回原节点。直连模式将选择权下放到客户端 SDK：
 * 每次建连随机挑选地址（连接天然均匀分布），重连风暴自动再平衡
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package client

import (
	"context"
	"net"
)

// sanitizeServers 过滤空地址并去重（LB 直连地址列表清理，保序）
func sanitizeServers(urls []string) []string {
	if len(urls) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(urls))
	servers := make([]string, 0, len(urls))
	for _, u := range urls {
		if u == "" {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		servers = append(servers, u)
	}
	if len(servers) == 0 {
		return nil
	}
	return servers
}

// ResolveServers 解析 headless Service 域名到全部 Pod 直连地址（集群内 LB 直连入口）
//
// host 为 headless Service 域名（如 apex-notify-service-headless.business.svc.cluster.local），
// port/path 为直连端口与 WS 路径；返回的 ws:// 地址列表直接喂给 WithServers。
// Pod 扩缩容/滚动更新会改变解析结果——调用方应周期性重解析刷新列表
// （建议重连窗口前刷新；列表过期仅导致轮转到已下线地址，失败转下一下一自愈）。
// 注意：产出为明文 ws://，集群内直连通常无 TLS；跨网段需 TLS 时由调用方自行包装 wss:// 地址
func ResolveServers(ctx context.Context, host, port, path string) ([]string, error) {
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return nil, err
	}
	servers := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		servers = append(servers, "ws://"+net.JoinHostPort(addr, port)+path)
	}
	return servers, nil
}
