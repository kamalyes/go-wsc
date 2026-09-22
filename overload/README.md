# overload（过载域）

> AIMD 削峰填谷 + 按用户批量投递——流量进来时的分级裁决，与出口的批量发送

## 这个域干什么

`overload` 在消息洪峰与后端容量之间做缓冲：入口用准入闸门和速率整形削峰，
出口把同用户的多条消息合并批量投递batcher.攒批落库已拆分至独立的 `batcher` 域batcher.

## 核心概念

### 入口半边：削峰（REFACTORING_DESIGN §五.7）

```
消息入口 → AdmissionGate(AIMD) → BroadcastShaper(GCRA)
              │                        │
              ▼                        ▼
         分级裁决              DelayQueue(填谷)
        (VIP/普通)            Coalescer(Latest-Wins 合并)
```

| 概念 | 说明 |
|------|------|
| `AdmissionGate` | AIMD 准入：水位线 + 快降慢升（连续 N 周期确认才恢复），防抖动 |
| `Shaper` | GCRA 虚调度令牌桶：CAS 单点更新 `tat`，无锁零分配 |
| `Coalescer` | Latest-wins 合并：同 key 覆盖旧值，16 分片防内存膨胀 |
| `BroadcastDelayQueue` | 填谷延迟队列：超载消息延迟放行而非丢弃 |
| `FallbackAction` | 降级动作：VIP 直通 / 普通延迟 / 低优丢弃 |

队列优先级容量：system 10% / critical 15% / high 25% / normal 35% / low 15%

### 出口半边：批量投递

| 概念 | 说明 |
|------|------|
| `BatchSender` | 按用户聚合批量发送：同用户多条消息合并为一次投递 |
| `BatchSendResult` / `UserResult` | 批量发送结果与单用户结果 |
| `BatchSendFailureCallback` | 失败回调：批量发送失败时逐用户回报 |

## 怎么用

```go
package main

import (
    "github.com/kamalyes/go-wsc/overload"
)

// 入口：准入 + 整形
gate := overload.NewAdmissionGate(high, low, evalInterval)
if gate.Admit(priority) != overload.AdmitPass {
    // 走 FallbackAction：延迟 / 降级 / 丢弃
}

shaper := overload.NewShaper(constants.DefaultBroadcastShaperRate)
ok := shaper.Allow()
```

## 端口与依赖

- 消费者端口：`UserMessageSender`（hub 实现，BatchSender 的单用户投递能力）
- 指标经 `OverloadMetrics` 暴露，由编排层恒注入消息域

## 性能与陷阱

- P10：GCRA 多 worker 必须 CAS 单点更新 `tat`，否则整形失效
- AIMD 参数：降载立即生效，恢复要连续 N 周期确认——两者不对称是防抖的关键
- `Coalescer` 只适合可丢弃的 ephemeral 消息（Latest-Wins 覆盖旧值）

## 后端绑定

- 无直接存储绑定：本域纯内存组件，落库由 `batcher` 域与 spi 契约承担
