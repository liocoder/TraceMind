---
name: downstream_timeout
description: 排查下游依赖超时导致的连锁故障
triggers: downstream, dependency, payment-service, 下游, 依赖服务, 调用超时
recommended_tools: query_metrics, search_logs
---

# 下游依赖超时

## 典型表现

- 当前服务的请求延迟升高，与下游请求延迟升高的时间接近。
- 日志中出现下游调用超时、连接失败或重试。
- 下游异常可能引起线程、连接池或请求队列堆积。

## 调查步骤

1. 检查下游服务的延迟、错误率及可用性。
2. 搜索调用超时、重试和熔断相关日志。
3. 比较上游和下游异常的时间顺序，避免把结果当作原因。

## 判断原则

仅仅发现下游服务异常，并不足以证明它导致了当前故障。需要进一步核对时间关系和请求链路。
