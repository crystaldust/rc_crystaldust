# ADR-0004: 状态存储采用 PostgreSQL，Attempt 日志同库分区并可演进到列存

- **Status**: Accepted
- **Date**: 2026-10-24
- **Deciders**: Message Hub 技术负责人（用户确认，2026-10-24）
- **Related**: ADR-0003；`docs/spike/05-stack-evaluation.md` §C

## Context

Event、Delivery、Attempt 需要持久化，并支持以下用途：
- 可追溯
- 可重放
- 多维查询（event_id、业务键、状态、时间范围）

ADR-0003 依赖数据库提供以下队列化能力：
- `SKIP LOCKED`
- 部分索引
- `LISTEN/NOTIFY`

Attempt 的写入量是 Delivery 的数倍（每次重试一行），是写入压力的主要来源。

## Options Considered

| Option | 加权得分（满分 60） | 关键权衡 |
|---|---|---|
| **PostgreSQL** | **58** | 声明式分区、JSONB + GIN、部分索引、`LISTEN/NOTIFY`、`SKIP LOCKED` 齐全 |
| MySQL 8 | 45 | 有 `SKIP LOCKED`；没有部分索引和 NOTIFY，ADR-0003 的方案无法直接成立 |

## Decision

- **主状态存储采用 PostgreSQL（16+）**
- **Attempt 日志与主库同库，按月分区**
  - **MVP：与 Delivery 状态更新在同一事务中同步写入**（2026-10-24 修订，原为"异步批量写入"）。保证状态与记录始终一致，进程崩溃不会丢失 attempt；代价是每次尝试多一次写入
  - 异步批量写入改为演进项：当 attempt 写入成为瓶颈时再引入（见 Revisit When）
  - 响应体设大小上限
- **payload 单独存放**，按保留期批量清理

## Rationale

- 加权得分最高，并且是 ADR-0003 方案成立的前提
- 受理去重依赖部分唯一索引：`UNIQUE (source, source_event_id) WHERE source_event_id IS NOT NULL`（参考 Svix 初始迁移 L74）
- 分区表思路参考 Outpost 的 `migrator/migrations/postgres/000001_init.up.sql`
- payload 清理参考 Svix 的 `expired_message_cleaner.rs`

## Consequences

- **正面**：一个数据库同时承担状态、队列和查询，运维面最小；可以直接用 SQL 排障
- **负面**：Attempt 量大时同库会挤占主库资源，需要监控分区大小并提前规划迁移
- **未验证项**：容量阈值为估计值；数据保留时长仍是 PRD 中的 Open Question

## Evolution Path

- Attempt 日志迁往 ClickHouse 等列存，Event 和 Delivery 主状态保留在 PG
- 迁移时定义 `AttemptStore` 接口，可参考 Outpost `logstore` 的 PG/CH 双驱动和共用一致性测试套件

## Revision History

| 日期 | 变更 |
|---|---|
| 2026-10-24 | 初版：attempt 异步批量写入 |
| 2026-10-24 | 修订（用户确认）：MVP 改为与状态更新同一事务同步写入；异步批量写入作为演进项 |

## Revisit When

- attempt 同步写入使 Complete 事务的 P99 明显升高，或成为数据库写入的主要来源（Phase 7 压测确认后考虑改为异步批量写入）

以下阈值为估计值，Phase 7 压测校准：
- 单月 Attempt 分区超过约 1 亿行
- 或 Attempt 查询 P95 > 1s
