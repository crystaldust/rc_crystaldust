# 05 · 自建技术栈评估

> 前提：在"自建"的前提下独立评估，不受 02/03 评估倾向影响。
> 评分：本文件使用 **1–5 分制**（5 = 最优），加权得分 = Σ(分值 × 权重)。
> 证据说明：本节多数评分是基于公开、成熟的技术事实做出的**定性判断**，不像 02/03 那样有逐行源码证据。涉及具体机制的，附官方文档链接；需要实测才能确定的数字阈值，统一标为"估计值，Phase 7 压测校准"。

## 输入约束（来自 PRD）

- 选型原则：性能、可维护性、开发速度，外加水平扩展能力、混合环境运维成熟度、AI 编码友好度
- 规模未知：选型重点是**演进路径**，不追求一开始的峰值能力
- 延迟：首次投递 P99 < 1s
- 零静默丢失：受理与入队必须原子（02/03 评估都发现候选方案在这一点上有缺陷，见下文）
- 无云依赖，按混合环境部署

---

## A. 实现语言

| 维度 | 权重 | Go | Java（Spring Boot 3 / 虚拟线程） | TypeScript（Node.js） | Rust |
|---|---|---|---|---|---|
| 性能（高并发出站 HTTP、内存占用） | 3 | 4 | 4 | 3 | 5 |
| 可维护性（类型系统、并发模型的可理解性） | 3 | 4 | 4 | 3 | 3 |
| 开发速度 | 3 | 4 | 4 | 5 | 2 |
| AI 编码友好度 | 3 | 5 | 4 | 4 | 3 |
| Kafka / PostgreSQL / OTel 客户端成熟度 | 2 | 5 | 5 | 4 | 4 |
| 混合环境部署（产物体积、启动速度） | 2 | 5 | 3 | 3 | 5 |
| 可借鉴代码的语言 | 1 | 5 | 2 | 2 | 4 |
| **加权得分（满分 85）** | | **76** | **66** | **61** | **61** |

**判断依据**
- **Go**
  - goroutine 加 `net/http` 适合"大量并发出站 HTTP、各自等待响应"的负载
  - 单一静态二进制，镜像小、启动快，便于混合环境分发
  - `pgx`、`franz-go`、`go-redis`、OpenTelemetry Go SDK 都成熟
  - 语言特性少、gofmt 统一风格、编译器严格（未使用变量、导入即报错），AI 生成代码的可预期性和可审查性最好
  - Outpost（02 评估 §6）的可借鉴模块都是 Go 代码，可以直接对照
- **Java**
  - 生态与客户端库最完整，虚拟线程解决了阻塞 I/O 并发问题
  - 扣分项是 JVM 镜像体积、启动时间和内存基线
  - 框架层（Spring）的"魔法"会降低 AI 生成代码的可审查性
- **TypeScript**
  - 开发最快
  - 单线程事件循环下 CPU 密集操作（模板渲染、JSON 处理）会影响投递延迟
  - Kafka 客户端生态有过维护断档，主流 `kafkajs` 长期少有更新
- **Rust**
  - 性能与产物体积最优
  - 开发速度和团队可维护性明显偏低（无固定技术栈的团队，借用检查器学习曲线陡）
  - 对"尽快上线"的目标不利

**推荐：Go**。Revisit When：团队有明确的 JVM 技术栈积累，且运维平台已统一为 JVM 体系。

---

## B. 调度队列与重试调度

"首次分发"和"延迟重试"对队列的要求不同，分开评估；每个组合都以 PostgreSQL 作为状态存储（见 C 节）。

| 维度 | 权重 | 仅 PostgreSQL（`scheduled_at` + `FOR UPDATE SKIP LOCKED` + `LISTEN/NOTIFY`） | PG + Kafka | PG + Redis（Streams + ZSET） | PG + RabbitMQ |
|---|---|---|---|---|---|
| 按目标隔离及目标数增长的代价 | 3 | 4 | 2 | 3 | 4 |
| 延迟重试实现 | 3 | 5 | 1 | 4 | 3 |
| 按 key 有序 | 2 | 4 | 5 | 3 | 3 |
| 吞吐上限与水平扩展 | 2 | 2 | 5 | 4 | 4 |
| 混合环境运维复杂度 | 3 | 5 | 2 | 4 | 3 |
| MVP 开发速度 | 2 | 5 | 2 | 3 | 3 |
| **加权得分（满分 75）** | | **64** | **39** | **53** | **50** |

**各组合要点**

- **仅 PostgreSQL**
  - **隔离**：Delivery 行带 `destination_id`。领取任务时，在 SQL 中按目标做公平轮转，并限制每个目标的在途数量，无需为每个目标建队列；目标数增长只增加行数，不增加基础设施
  - **延迟重试**：`scheduled_at` 加部分索引 `WHERE status IN ('PENDING','RETRY_SCHEDULED')` 原生支持（[`SKIP LOCKED`](https://www.postgresql.org/docs/current/sql-select.html#SQL-FOR-UPDATE-SHARE)）
  - **有序**：领取时只取每个 `ordering_key` 最早的未完成 Delivery，用行锁串行化
  - **延迟**：受理事务提交后 `NOTIFY` 唤醒 worker（[`LISTEN/NOTIFY`](https://www.postgresql.org/docs/current/sql-notify.html)），空闲时退化为短间隔轮询。首次投递不依赖轮询间隔，P99 < 1s 可期（估计值，Phase 7 压测校准）
  - **零静默丢失**：Event 和 Delivery 在**同一事务**中写入，本身就是 Transactional Outbox。这直接规避了 Svix"先落库后入队、两步之间崩溃则永不投递"的缺陷（03 评估 §C1），以及 Outpost"发布时只进 MQ、不进 DB"的缺陷（02 评估摘要）
  - **短板**：吞吐上限受单库写入能力约束。轮询和状态更新有写放大；Attempt 写入应走异步批量（见 C 节）
- **PG + Kafka**
  - 吞吐和分区有序最好
  - 但没有原生延迟消息，需要分级重试 topic 或外部调度器
  - 分区内会出现 head-of-line 阻塞：一个慢目标会阻塞同分区的其他目标，要按目标隔离就得在 consumer 内再建按目标的内存队列
  - 状态在 PG、分发在 Kafka，两者之间仍需要 outbox 中继
  - 在混合环境中运维 Kafka 集群（即使是 KRaft 模式）是本阶段最重的负担
- **PG + Redis**
  - 用 ZSET 做延迟队列很成熟，Svix 和 Outpost 都这么做（03 评估 §7、02 评估 §6）
  - 但 Redis 不是事务内组件，仍需要 outbox 中继，并要处理 Redis 持久化与 PG 状态不一致的问题
- **PG + RabbitMQ**
  - 每个目标一个队列很自然
  - 延迟重试依赖插件或 TTL + DLX 分级，目标数量多时队列管理复杂

**推荐：MVP 采用"仅 PostgreSQL"**，在代码中通过 `Dispatcher` 接口（领取 / 确认 / 重排程）与具体实现解耦。

**演进路径**
1. **MVP**：仅 PG。分发、重试、有序都由 SQL 实现
2. **第二档**：吞吐接近 PG 能力上限时，把"首次分发"迁到 Redis Streams 或 Kafka（按目标或分片分区），由 outbox 中继从 PG 发出。PG 继续负责状态、延迟重试和查询
3. **第三档**：Attempt 日志迁到列式存储（见 C 节）

**Revisit When**（阈值为估计值，Phase 7 压测校准）：
- 持续投递量超过约 1–2k deliveries/s
- 或领取查询 P99 > 50ms
- 或 PG 主库 CPU 持续 > 60%

---

## C. 状态存储

| 维度 | 权重 | PostgreSQL | MySQL 8 |
|---|---|---|---|
| 声明式分区表（按时间） | 3 | 5 | 4 |
| JSON 查询（payload / metadata 检索） | 2 | 5（JSONB + GIN） | 3 |
| 队列化能力（`SKIP LOCKED`、部分索引、`LISTEN/NOTIFY`） | 3 | 5 | 3（有 `SKIP LOCKED`，无部分索引和 NOTIFY） |
| 生态成熟度（驱动、迁移工具、运维经验） | 2 | 5 | 5 |
| 混合环境运维（高可用方案、备份） | 2 | 4 | 4 |
| **加权得分（满分 60）** | | **58** | **45** |

**推荐：PostgreSQL**。B 节的"仅 PG"方案依赖部分索引（受理去重 `UNIQUE (source, source_event_id)`、待领取任务扫描）与 `LISTEN/NOTIFY`，这两项 MySQL 都没有。

**Attempt 日志存储策略**
- **MVP**：与主库同库，按月分区（参考 Outpost `migrator/migrations/postgres/000001_init.up.sql`）。异步批量写入，与投递主路径解耦（参考 Outpost `logmq/batchprocessor.go`）。响应体设置大小上限（参考 Svix 的 20KB 截断、Outpost 的整体替换为占位符）
- **演进**：Attempt 量增长后，迁往 ClickHouse 等列存（Outpost 的 `logstore` 已有 PG/CH 双驱动，可作为接口设计参考）。Event 和 Delivery 主状态保留在 PG
- **Revisit When**（估计值）：单月 Attempt 分区超过约 1 亿行，或 Attempt 查询 P95 > 1s
- **保留期**：payload 单独存放并按到期时间批量清理（参考 Svix `expired_message_cleaner.rs`），具体保留时长仍是 PRD 中的 Open Question

---

## 汇总

| 决策 | 推荐 | 加权得分 | ADR |
|---|---|---|---|
| 实现语言 | Go | 76 / 85 | ADR-0002 |
| 调度与重试 | 仅 PostgreSQL（`Dispatcher` 接口隔离，可演进到 Redis / Kafka） | 64 / 75 | ADR-0003 |
| 状态存储 | PostgreSQL（Attempt 同库分区，可演进到列存） | 58 / 60 | ADR-0004 |

最终栈只有 **Go 服务 + PostgreSQL** 两个组件，最小化混合环境的运维面，同时为规模增长保留了明确的演进路径。
