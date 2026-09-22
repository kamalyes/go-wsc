/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 15:04:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 15:04:00
 * @FilePath: \go-wsc\group\observer.go
 * @Description: 观察者查询与统计 —— 从 hub/observer.go 抽出
 *
 * 观察者三级模型（类似 k8s namespace 隔离）：
 *  1. 全局观察者（Namespace=""）：接收所有命名空间的消息
 *  2. 命名空间观察者（Namespace="ns1"）：接收指定命名空间的所有消息
 *  3. 群组观察者（Namespace="ns1", GroupID="g1"）：仅接收指定命名空间+群组的消息
 *
 * 本文件只承接「查谁在观察」与「观察者统计」——两者都只读本地分片注册表的
 * 三级索引，不产生任何跨节点副作用，因此可以完整留在本域。
 *
 * 通知与跨节点广播（notifyObservers / NotifyObserversDirect /
 * broadcastObserverNotification / sendToObserver）**不在此处**：它们依赖
 * routeToCluster 的集群分发决策与 observerBatcher 的批量调度，归属 cluster 域。
 * 硬搬过来只会给本域端口塞进一个沉重的集群派发契约。
 *
 * 性能：通过注册表 observerIdx 三级索引 O(k) 查找（k=匹配的观察者数），
 * 替代旧版 ForEachObserver O(n) 全量扫描
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package group

import "github.com/kamalyes/go-wsc/models"

// ObserverManager 观察者查询与统计管理器
type ObserverManager struct {
	host Host
}

// NewObserverManager 创建观察者管理器
func NewObserverManager(host Host) *ObserverManager {
	return &ObserverManager{host: host}
}

// ============================================================================
// 观察者查询 - 基于三级索引，O(k) 查找
// ============================================================================

// GetObserverClients 获取所有观察者客户端（所有设备）- O(n) n=观察者设备数
// 仅用于统计场景，消息通知走 GetObserversForMessage
func (m *ObserverManager) GetObserverClients() []*models.Client {
	observers := make([]*models.Client, 0)
	m.host.GetShardedRegistry().ForEachObserver(func(_, _ string, client *models.Client) bool {
		if !client.IsClosed() {
			observers = append(observers, client)
		}
		return true
	})
	return observers
}

// GetObserverClientsByNamespace 获取指定命名空间的观察者客户端（兼容接口）
// 使用三级索引查找：全局观察者 + 命名空间级观察者（不含群组级）
// 群组级观察者请使用 GetObserversForMessage(namespace, groupID)
func (m *ObserverManager) GetObserverClientsByNamespace(namespace string) []*models.Client {
	return m.host.GetShardedRegistry().GetObserversForMessage(namespace, "")
}

// GetObserversForMessage 获取应接收指定命名空间+群组消息的观察者
// 合并三级：全局 + 命名空间 + 各群组，按 clientID 去重，O(k) k=匹配的观察者数
func (m *ObserverManager) GetObserversForMessage(namespace string, groupIDs ...string) []*models.Client {
	return m.host.GetShardedRegistry().GetObserversForMessage(namespace, groupIDs...)
}

// GetObserverCount 获取观察者数量（用户数，非设备数）- O(1)
func (m *ObserverManager) GetObserverCount() int {
	return m.host.GetShardedRegistry().GetObserverUserCount()
}

// GetObserverDeviceCount 获取观察者设备总数 - O(n) n=观察者数量
func (m *ObserverManager) GetObserverDeviceCount() int {
	return m.host.GetShardedRegistry().GetObserverDeviceCount()
}

// IsObserver 检查用户是否为观察者 - O(1)
func (m *ObserverManager) IsObserver(userID string) bool {
	return m.host.GetShardedRegistry().HasObserver(userID)
}

// ============================================================================
// 观察者统计信息
// ============================================================================

// GetObserverStats 获取所有观察者的统计信息 - O(n) n=观察者设备数
func (m *ObserverManager) GetObserverStats() []*models.ObserverStats {
	observers := m.GetObserverClients()
	stats := make([]*models.ObserverStats, 0, len(observers))

	for _, observer := range observers {
		stats = append(stats, &models.ObserverStats{
			ObserverID: observer.UserID,
			ClientID:   observer.ID,
			Namespace:  observer.Namespace,
			// GroupID 用原值而非 GetGroupID()：后者会把空组名归一化成
			// constants.DefaultGroupID（__default_gp__），那是 P2P 路由的
			// 哨兵，不是观察者订阅范围。归一化后「命名空间级观察者」与
			// 「__default_gp__ 群组级观察者」无法区分，GetObserverManagerStats
			// 里 stat.GroupID == "" 的分流分支会永远不可达
			GroupID:     observer.GetGroupIDRaw(),
			ConnectedAt: observer.ConnectedAt,
			BufferSize:  cap(observer.SendChan),
			BufferUsage: len(observer.SendChan),
			IsConnected: true,
			UserType:    observer.UserType.String(),
			ClientType:  observer.ClientType.String(),
		})
	}

	return stats
}

// GetObserverManagerStats 获取观察者管理器统计信息（按 namespace/groupID 分组）- O(n) n=观察者设备数
func (m *ObserverManager) GetObserverManagerStats() *models.ObserverManagerStats {
	observerStats := m.GetObserverStats()

	// 按 namespace → groupID 分组
	nsStatsMap := make(map[string]*models.NamespaceObserverStats)
	for _, stat := range observerStats {
		ns := stat.Namespace
		if nsStatsMap[ns] == nil {
			nsStatsMap[ns] = &models.NamespaceObserverStats{
				Namespace: ns,
			}
		}
		nsStat := nsStatsMap[ns]
		nsStat.TotalDevices++

		if stat.GroupID == "" {
			// 命名空间级观察者（非群组级）
			nsStat.ObserverStats = append(nsStat.ObserverStats, stat)
			nsStat.TotalUsers++
		} else {
			// 群组级观察者：找到或创建对应的 GroupObserverStats
			var groupStat *models.GroupObserverStats
			for i := range nsStat.GroupStats {
				if nsStat.GroupStats[i].GroupID == stat.GroupID {
					groupStat = &nsStat.GroupStats[i]
					break
				}
			}
			if groupStat == nil {
				nsStat.GroupStats = append(nsStat.GroupStats, models.GroupObserverStats{
					GroupID: stat.GroupID,
				})
				groupStat = &nsStat.GroupStats[len(nsStat.GroupStats)-1]
			}
			groupStat.ObserverStats = append(groupStat.ObserverStats, stat)
			groupStat.TotalDevices++
			groupStat.TotalUsers++
		}
	}

	return &models.ObserverManagerStats{
		TotalObservers:      m.GetObserverCount(),
		TotalDevices:        m.GetObserverDeviceCount(),
		TotalNotifications:  0,
		FailedNotifications: 0,
		DroppedMessages:     0,
		NamespaceStats:      nsStatsMap,
	}
}
