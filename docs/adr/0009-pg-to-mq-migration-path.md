# ADR-0009: 从 PostgreSQL 派发平滑迁移到 MQ 的路径

- **Status**: Accepted
- **Date**: 2026-10-24
- **Deciders**: Message Hub 技术负责人（用户确认，2026-10-24）
- **Related**: 细化 ADR-0003 的 Evolution Path（不取代 ADR-0003）；ADR-0004；ADR-0007；`docs/design/domain-model.md` §5、§6、§8

## Context

ADR-0003 决定 MVP 只用 PostgreSQL 承担分发与重试调度（`FOR UPDATE SKIP LOCKED` + `LISTEN/NOTIFY`），并给出了"规模增长后把首次分发迁到 MQ"的演进方向，但没有说明：
- 迁移后状态以谁为准
- MQ 的至少一次投递会不会造成重复投递
- 有序和按目标隔离如何保持
- 如何分步切换、如何回滚
- MVP 阶段现在就要做哪些事，才能让迁移时不重写 worker

业务方判断未来大概率要迁移到真正的 MQ 系统，所以需要现在就把迁移路径定下来，并把低成本的前置要求放进 Phase 4。

### PostgreSQL 能力与 MVP 适配性（结论摘要）

| 能力 | 在 Hub 中的用途 | 限制 |
|---|---|---|
| `SELECT … FOR UPDATE SKIP LOCKED` | 领取任务（工作队列） | 持久、事务性；吞吐受单库约束（估计每秒约 1–2 千次投递，Phase 7 压测校准） |
| `LISTEN / NOTIFY` | 新任务的唤醒提示 | 不持久、只发给在线连接、payload ≤ 8000 字节、高频时提交阶段争抢全局锁。**正确性不依赖它**，轮询兜底 |
| 逻辑解码（CDC） | 迁移时的 outbox 中继选项 | 需要复制槽与运维 |

MVP 的零静默丢失、P99 < 1s、延迟重试、按目标隔离、有序、可查询可重放，都能由上述能力满足；独立 MQ 反而会引入"写库与入队不在同一事务"的风险（PoC 中 Svix、Outpost 都有该缺陷）。

## Decision

### 原则：PostgreSQL 始终是状态的唯一来源，MQ 只负责派发

迁移前后，Delivery 状态、重试调度、查询与审计**一直在 PostgreSQL 中**。MQ 只承担"通知某个 worker 去处理哪个 delivery"，消息丢失或重复都不影响状态正确性。

```
MVP：    受理 ──事务──► deliveries ──NOTIFY/轮询──► Worker（SKIP LOCKED 领取）

迁移后：  受理 ──事务──► deliveries ──outbox 中继 / CDC──► MQ（按目标分区）──► Worker
                            ▲                                                   │
                            └──── 条件更新领取 / 写结果 / 重排程 ◄──────────────┘
          重试调度器：扫描到期的 RETRY_SCHEDULED ──► 重新发布到 MQ
```

### 设计要点 M1–M6

| # | 要点 | 何时实施 |
|---|---|---|
| M1 | **`Dispatcher` 接口**：领取（Claim）、确认结果（Complete）、重排程（Reschedule）。Worker 只依赖接口，MVP 实现为 `pgDispatcher` | **Phase 4（必做）** |
| M2 | **幂等领取**：领取以条件更新实现，`UPDATE deliveries SET status='IN_FLIGHT', lease_owner=…, lease_expires_at=… WHERE id=$1 AND status IN ('PENDING','RETRY_SCHEDULED') RETURNING …`；更新到 0 行即视为已被他人领取，直接放弃。lease 过期接管（Reclaim）同样用带条件的更新。SKIP LOCKED 只用于"找出候选 id"，状态迁移一律经条件更新完成 | **Phase 4（必做）** |
| M3 | **消息契约**：MQ 消息只携带 `delivery_id`，以及用于路由的 `destination_id`、`ordering_key`；不携带 payload 或模板结果 | 迁移时 |
| M4 | **重试调度留在 PostgreSQL**：退避计算与 `scheduled_at` 不变；迁移后由调度器把到期任务重新发布到 MQ，不依赖 MQ 的延迟消息能力 | MVP 已如此；迁移时加发布 |
| M5 | **按目标切换派发方式**：Destination 配置增加 `dispatch: pg \| mq`（默认 `pg`），可按目标试点与回滚 | 迁移时（MVP 不加该字段，避免"配置了却不生效"） |
| M6 | **有序与隔离不依赖 MQ 语义**：分区键为 `destination_id + ordering_key`；领取前仍在库中检查前序 Delivery 是否结清（ADR-0007）；按目标的 `max_in_flight` 仍由领取逻辑控制 | 迁移时（复用现有检查） |

### MVP 阶段必须规避的 PostgreSQL 队列陷阱（Phase 4 必做）

| # | 陷阱 | 对策 |
|---|---|---|
| P1 | HTTP 调用期间持有行锁，锁时长等于对端响应时间 | 领取事务只做状态迁移并立即提交；HTTP 调用在事务之外（lease 机制） |
| P2 | deliveries 高频更新导致死元组堆积、表与索引膨胀 | 对 deliveries 单独调整 autovacuum 参数；设置 `fillfactor` 以利于 HOT 更新；终态行不再更新 |
| P3 | 高频 NOTIFY 在提交阶段争抢全局锁 | 每个受理事务最多发一次 NOTIFY，不带 payload；worker 收到后批量领取 |
| P4 | LISTEN 需要长连接，事务模式的 pgbouncer 不支持 | LISTEN 使用一条直连数据库的专用连接；业务查询照常经过连接池 |
| P5 | 唤醒丢失（连接断开、实例重启） | 轮询兜底（间隔为命名常量），保证任何到期任务最终都会被领取 |

### 迁移步骤（每步可回滚）

| 步骤 | 做法 | 回滚 |
|---|---|---|
| 1. 引入中继（影子模式） | 上线 outbox 中继（轮询 deliveries 或逻辑解码），把 delivery_id 发布到 MQ；worker 仍从 PostgreSQL 领取，MQ 只用于核对消息完整性与延迟 | 停止中继 |
| 2. 试点 | 少数低风险目标切换为 `dispatch: mq` | 改回 `pg` |
| 3. 全量 | 全部目标切换为 MQ；PostgreSQL 轮询改为对账任务，发现遗漏的到期任务时重新发布 | 改回 `pg` |
| 4. 收尾 | 视情况关闭 NOTIFY 唤醒，保留对账 | — |

回滚始终安全：状态在 PostgreSQL 中，领取是幂等的，任何一步切回都不会丢任务或重复投递。

### 触发迁移的条件

满足任一条件时启动迁移评估（阈值为估计值，Phase 7 压测后校准，与 ADR-0003 一致）：
- 持续投递量超过约每秒 1–2 千次
- 领取查询 P99 > 50ms
- PostgreSQL 主库 CPU 持续 > 60%，且主要消耗来自领取与状态更新
- deliveries 表膨胀在调优后仍无法控制

## Options Considered

| Option | 说明 | 结论 |
|---|---|---|
| **PostgreSQL 为唯一状态来源，MQ 只派发** | 本 ADR | 采用：迁移风险最小，可分步、可回滚 |
| 迁移后以 MQ 为主、PostgreSQL 只做记录 | 状态由消息流驱动 | 不采用：查询、重放、有序检查都要重新设计，且重新引入"写库与入队不一致"的问题 |
| 现在就引入 pgmq 等 PostgreSQL 队列扩展 | 用扩展替代自研领取 | 不采用：deliveries 表本身已是状态来源，再加一层队列等于两份数据；也不能减少迁移到 MQ 时的工作 |
| MVP 直接上 MQ | 跳过 PostgreSQL 派发 | 不采用：ADR-0003 已评估（运维成本高、延迟重试与 outbox 需另建） |

## MQ 选型

到迁移时再定。影响选型的因素：

| 候选 | 特点 |
|---|---|
| RabbitMQ | 每个目标一个队列，按目标隔离最自然；重试由 PostgreSQL 调度，不需要延迟插件 |
| Kafka | 吞吐最高、分区有序；分区内存在队头阻塞，worker 内仍需按目标做并发控制 |
| Redis Streams | 运维最轻；持久性弱于前两者，但状态在 PostgreSQL，可以接受 |

## Consequences

- **正面**：
  - 迁移只替换 `Dispatcher` 实现和增加中继，worker、状态机、查询 API 不变
  - 迁移过程中随时可以按目标回滚
  - MQ 的至少一次投递、乱序、重平衡都不会破坏投递语义（由 M2、M6 保证）
- **负面**：
  - 迁移后每次领取仍需一次数据库写入，PostgreSQL 的写入压力不会完全消失，只是去掉了轮询与 SKIP LOCKED 扫描的开销
  - 需要额外运维 outbox 中继（或 CDC 复制槽）
- **对 Phase 4 的要求**：M1、M2、P1–P5 是 Phase 4 的必做项，验收时需要有对应测试（多 worker 竞争领取、重复领取同一 id、lease 过期接管、唤醒丢失后轮询兜底）

## Revisit When

- 达到"触发迁移的条件"中的任一项
- 业务要求跨目标的全局顺序，或单目标吞吐超过单库能力
