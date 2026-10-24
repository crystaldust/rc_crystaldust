# ADR-0003: MVP 以 PostgreSQL 承担分发与重试调度，通过 Dispatcher 接口保留演进空间

- **Status**: Accepted
- **Date**: 2026-10-24
- **Deciders**: Message Hub 技术负责人（用户确认，2026-10-24）
- **Related**: ADR-0001、ADR-0004；`docs/spike/05-stack-evaluation.md` §B；Evolution Path 由 [ADR-0009](0009-pg-to-mq-migration-path.md) 细化

## Context

需要一种机制完成以下工作：
- 首次分发 Delivery
- 延迟重试
- 按目标隔离（慢供应商不能拖慢其他供应商）
- 可选的按 `ordering_key` 有序投递

约束：
- 首次投递 P99 < 1s
- 零静默丢失
- 规模未知
- 混合环境下运维面尽量小

02 和 03 评估都发现，状态存储与队列分离、且没有 outbox 时，会出现静默丢失：
- Svix 先落库后入队，两步之间崩溃则消息永不投递
- Outpost 发布时只写 MQ、不写 DB

## Options Considered

| Option | 加权得分（满分 75） | 关键权衡 |
|---|---|---|
| **仅 PostgreSQL**（`scheduled_at` + `FOR UPDATE SKIP LOCKED` + `LISTEN/NOTIFY`） | **64** | 受理事务就是 outbox；延迟、有序、按目标限流都可用 SQL 表达；组件最少。吞吐受单库约束 |
| PG + Redis（Streams + ZSET） | 53 | 延迟队列成熟；需要 outbox 中继，要处理 Redis 与 PG 之间的一致性 |
| PG + RabbitMQ | 50 | 每个目标一个队列很自然；延迟重试依赖插件或 TTL 分级 |
| PG + Kafka | 39 | 吞吐和分区有序最好；无原生延迟；分区内 head-of-line 阻塞；混合环境运维最重 |

## Decision

**MVP 只使用 PostgreSQL 实现分发与重试调度**：
- **受理**：在同一事务中写入 Event 与全部 Delivery（`status=PENDING`、`scheduled_at=now()`），提交后执行 `NOTIFY`
- **领取**：worker 被 `LISTEN` 唤醒，空闲时短间隔轮询兜底。用 `FOR UPDATE SKIP LOCKED` 领取到期任务，在 SQL 中做按目标的公平轮转并限制每个目标的在途数量；有序策略下只领取每个 `ordering_key` 最早的未完成 Delivery
- **重试**：失败时把 `scheduled_at` 更新为下一次退避时间（带 jitter，参考 Svix `worker.rs:L500-L545`），状态改为 `RETRY_SCHEDULED`；重试耗尽进入 `DEAD`
- **抽象**：代码中定义 `Dispatcher` 接口（领取 / 确认 / 重排程），业务逻辑不直接依赖 SQL 队列实现

## Rationale

- 加权得分最高（`docs/spike/05-stack-evaluation.md` §B）
- 受理与入队在同一事务中，从架构上消除了候选方案中存在的静默丢失窗口，直接服务于 PRD 的头号指标
- 不需要为每个目标建队列：目标数增长只增加行数，不增加基础设施
- 混合环境中只需运维一个 PostgreSQL 集群

## Consequences

- **正面**：组件最少；一致性最强；可以用 SQL 直接查询和修复任意投递状态；MVP 开发最快
- **负面**
  - 吞吐上限受单库写入能力约束
  - 轮询和状态更新有写放大
  - 公平领取的 SQL 需要精心设计并压测，属于实现风险
- **未验证项**：P99 < 1s 与吞吐上限均为估计，需要在 Phase 7 压测校准

## Evolution Path

1. **MVP**：仅 PG
2. **第二档**：把首次分发迁到 Redis Streams 或 Kafka（按目标或分片分区），由 outbox 中继从 PG 发出。PG 继续负责状态、延迟重试与查询；只需替换 `Dispatcher` 实现
3. **第三档**：按业务域或租户对 PG 分片

## Revisit When

以下阈值为估计值，Phase 7 压测校准：
- 持续投递量超过约 1–2k deliveries/s
- 或领取查询 P99 > 50ms
- 或 PG 主库 CPU 持续 > 60%
