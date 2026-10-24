# ADR-0007: 有序投递按目标和 ordering_key 保证受理顺序，前序死信默认阻塞

- **Status**: Accepted
- **Date**: 2026-10-24
- **Deciders**: Message Hub 技术负责人（用户确认，2026-10-24）
- **Related**: PRD Open Question"有序投递的阻塞策略"；ADR-0003；`docs/design/domain-model.md` §5

## Context

部分业务事件之间有前后关联，例如同一订单先"下单"后"取消"，库存系统必须按顺序处理；其他场景则不需要顺序。PoC（`docs/spike/04-poc-results.md` S7）表明候选开源方案都不保证顺序。需要定义：顺序的范围、顺序依据，以及前序事件进入 DEAD 后后续事件怎么处理。

## Options Considered

前序事件进入 DEAD 后的处理：

| Option | Pros | Cons |
|---|---|---|
| **按策略可配，默认阻塞** | 不同业务各取所需；默认值偏安全 | 多一个配置项 |
| 一律阻塞 | 最安全 | 一个坏事件会卡住整条 key |
| 一律跳过 | 不会卡住 | 对端可能先收到"取消"后收到"下单" |

## Decision

- **有序范围**：`(destination_id, ordering_key)`。不同目标之间互不影响；没有 `ordering_key` 的事件不受约束
- **顺序依据**：Hub 的**受理顺序**（`ingest_seq`），而不是业务方提供的 `occurred_at`
- **开关**：Delivery Policy 的 `ordering.mode: none | per_key`，默认 `none`
- **前序死信处理**：`ordering.on_predecessor_dead: block | skip`，默认 `block`
  - `block`：后续 Delivery 保持 PENDING，不被领取，查询 API 返回 `blocked_by`
  - `skip`：DEAD 视为已结清，后续 Delivery 继续投递
- **解除阻塞**：运维对 DEAD 的前序 Delivery 执行 replay（重新投递）或 skip（标记为 SKIPPED），两种操作都写审计记录
- 不设显式的 BLOCKED 状态，阻塞由调度查询隐式实现
- **跨来源的 ordering_key**（2026-10-24 补充，用户确认维持现状）：有序范围**不包含 source**。不同来源对同一目标使用相同的 `ordering_key` 时，会落在同一个有序范围内互相等待（一方的 DEAD 在 `block` 模式下会阻塞另一方）。约定 `ordering_key` 由业务方按来源取值（例如带上业务前缀 `order:1001`），写入 `docs/design/domain-model.md` §6
  - 查询隔离：`source` 角色查询时，属于其他来源的阻塞前序不会出现在 `blocked_by` 中（返回 null），见 domain-model.md §6、§9
  - 替代方案见下文"跨来源 ordering_key 的替代方案"

### 跨来源 ordering_key 的替代方案（未采用，留作演进）

| Option | 做法 | Pros | Cons |
|---|---|---|---|
| **A. 维持现状 + 命名约定**（当前） | 有序范围为 `(destination_id, ordering_key)`；规格要求业务方给 key 加来源前缀 | 无需改动；允许不同来源**有意**共享顺序（例如订单服务与支付服务对同一订单的事件需要整体有序） | 约定无法强制；误用时一个来源可以阻塞另一个来源，且被阻塞方（source 角色）看不到阻塞者 |
| B. 有序范围加入 source | 有序范围改为 `(destination_id, source, ordering_key)`；需要在 deliveries 上冗余 `source` 列并调整 `deliveries_ordering_idx` | 来源之间互不干扰；`blocked_by` 不再涉及跨来源可见性 | 失去跨来源共享顺序的能力；需要迁移与修订本 ADR；Phase 4 的领取 SQL 要随之调整 |
| C. 配置化 | Policy 增加 `ordering.scope: destination \| source`，默认其一 | 两种需求都能满足 | 配置项与实现复杂度增加；需要先确认确有两类需求 |

## Rationale

- 用户确认的方向（2026-10-24）：库存这类业务用 `block`，其他业务按需选 `skip`
- 以受理顺序为准，是因为 Hub 只能可靠地知道自己的受理顺序；`occurred_at` 可能由不同来源系统的时钟生成
- 隐式阻塞让状态机保持 6 个状态，减少迁移路径

## Consequences

- **正面**：库存类场景的顺序有保证；不需要顺序的场景没有任何额外开销
- **负面**：
  - `block` 模式下一个 DEAD 会阻塞同 key 的后续事件，需要告警和人工处理（Phase 5）
  - 业务方如果**并发**提交同一个 key 的事件，Hub 无法知道它们的业务先后，**业务方必须对同一个 key 串行提交**
- **实现要求**：领取 SQL 必须利用 `(destination_id, ordering_key, ingest_seq)` 索引，Phase 4 用 EXPLAIN 验证

## Revisit When

- 需要跨目标的全局顺序
- 需要按业务时间戳（而非受理顺序）排序
- 出现跨来源 `ordering_key` 冲突导致的误阻塞（运营排障中发现，或接入新来源时 key 命名无法统一）：评估改为方案 B 或 C
- 确认没有任何业务需要跨来源共享顺序：可直接改为方案 B，消除干扰

## Revision History

| 日期 | 变更 |
|---|---|
| 2026-10-24 | 初版 |
| 2026-10-24 | 补充跨来源 `ordering_key` 的行为、查询隔离与替代方案 A/B/C；用户确认维持方案 A（Phase 3 安全审查发现） |
