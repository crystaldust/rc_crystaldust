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
