# ADR-0010: max_in_flight 暂采用软上限，保留改为严格上限的路径

- **Status**: Accepted（暂定方案，预期日后可能修订）
- **Date**: 2026-10-24
- **Deciders**: Message Hub 技术负责人（用户确认，2026-10-24："先不做硬上限，但是这个设计未来可能会改动"）
- **Related**: ADR-0003、ADR-0009（M6：按目标的 `max_in_flight` 由领取逻辑控制）；`docs/design/domain-model.md` §8；`docs/testing/coverage-gaps.md` B16、B20

## Context

每个 Destination 有 `max_in_flight`（默认 10，范围 1–1000），用于按目标隔离、保护对端（§8）。Phase 4 的领取在一条 SQL 中完成（`internal/dispatch/pg.go` `claimSQL`）：

1. 统计每个目标**已提交**的 IN_FLIGHT 行数
2. 空闲槽位 = `least(max_in_flight − 在途数, 批大小)`，批大小为 `min(worker 空闲并发槽位, 32)`
3. 按目标用 `LATERAL … FOR UPDATE SKIP LOCKED` 取最多"空闲槽位"条到期 delivery，再以条件更新改为 IN_FLIGHT

PostgreSQL 的读已提交隔离下，并发领取的 worker 看不到彼此尚未提交的领取，因此会基于同一个空闲数各自领取。

## Decision

**当前采用软上限**，不在领取时对目标加锁：

- 单个 worker：严格不超过 `max_in_flight`
- N 个 worker 并发：同时在途数最多为 `max_in_flight + (N − 1) × min(32, max_in_flight)`。例如 `max_in_flight: 2`、4 个 worker，最坏为 8
- 该上界由 `TestMaxInFlightIsASoftLimitAcrossWorkers` 断言；单 worker 的严格性由 `TestMaxInFlightIsRespected` 断言
- domain-model.md §8 已写明软上限及其上界

## Options Considered

| Option | 做法 | Pros | Cons |
|---|---|---|---|
| **A. 软上限**（当前） | 只按已提交的在途数计算空闲槽位 | 领取无额外锁、无等待；实现最简单；单 worker 部署时就是严格的 | 多 worker 时可能短暂超出，上界见上 |
| B. 按目标 advisory lock | 领取事务内对每个待领取目标先 `pg_advisory_xact_lock(hashtext(destination_id))` 再计数 | 严格上限；改动集中在 `claimSQL` 与 `Postgres.claim`，worker 与接口不变 | 同一目标的并发领取串行化，领取吞吐下降；锁数量随目标数增长；hash 冲突会让无关目标互相等待（可改用两参数版本或目标序号） |
| C. 计数表 + 行锁 | 新增 `destination_slots(destination_id, in_flight)`，领取与完成时在同一事务中增减并 `FOR UPDATE` | 严格上限；计数 O(1) | 每次完成都多一次热点行更新；接管、手工修复时计数可能漂移，需要对账；新增迁移 |
| D. 降低超出量 | 保持软上限，但把 `max_in_flight` 小的目标的批大小限制为 1，或按 worker 数分摊槽位 | 改动小，超出量下降 | 仍不严格；按 worker 数分摊需要知道存活 worker 数 |
| E. 交给 Phase 6 限流 | 用按目标的速率限制 / 并发令牌保护对端 | 与熔断、限流统一 | 依赖 Phase 6 的设计；跨进程令牌同样需要共享状态 |

## Rationale

- MVP 的部署规模下 worker 数少，超出量有明确上界，且只在并发领取的瞬间出现
- `max_in_flight` 的主要作用是按目标隔离（一个慢目标不拖垮其他目标），软上限已满足；真正"超出一个都不行"的对端尚未出现
- 方案 B 的改动范围小且局部，日后需要时可以直接切换，不影响 `Dispatcher` 接口（迁移到 MQ 时 M6 仍由领取逻辑控制）

## Consequences

- **正面**：领取路径无锁等待；实现与测试简单
- **负面**：多 worker 部署时，对并发极敏感的对端可能短暂收到超过 `max_in_flight` 的并发请求
- **待办**：如采纳 B 或 C，需同步修改 domain-model.md §8、本 ADR（改为 Superseded 或修订）、`TestMaxInFlightIsASoftLimitAcrossWorkers`（改为断言严格上限），并在 EXPLAIN / 压测中确认领取延迟

## Revisit When

- 出现不能承受任何超出的对端（例如供应商合同限定严格并发数，或超出即封禁）
- 生产部署的 worker 数增加，实测超出量造成对端 429 / 5xx
- Phase 6 设计按目标限流与熔断时（评估方案 E 是否取代本 ADR）
- Phase 7 压测显示领取延迟有余量，方案 B 的串行化代价可以接受
