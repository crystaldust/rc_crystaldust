# Architecture Decision Records

| 编号 | 标题 | 状态 | 日期 |
|---|---|---|---|
| [ADR-0001](0001-build-vs-extend.md) | 基于开源组件自建，不在 Outpost / Svix 上二次开发 | Proposed | 2026-10-24 |
| [ADR-0002](0002-implementation-language.md) | 实现语言采用 Go | Proposed | 2026-10-24 |
| [ADR-0003](0003-dispatch-and-retry-queue.md) | MVP 以 PostgreSQL 承担分发与重试调度，通过 Dispatcher 接口保留演进空间 | Proposed | 2026-10-24 |
| [ADR-0004](0004-state-store.md) | 状态存储采用 PostgreSQL，Attempt 日志同库分区并可演进到列存 | Proposed | 2026-10-24 |

格式约定见 `.claude/PRPs/plans/completed/tech-spike-and-selection.plan.md` 的 ADR_FORMAT。评审通过后，把状态改为 `Accepted`。
