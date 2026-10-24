# Architecture Decision Records

| 编号 | 标题 | 状态 | 日期 |
|---|---|---|---|
| [ADR-0001](0001-build-vs-extend.md) | 基于开源组件自建，不在 Outpost / Svix 上二次开发 | Accepted | 2026-10-24 |
| [ADR-0002](0002-implementation-language.md) | 实现语言采用 Go | Accepted | 2026-10-24 |
| [ADR-0003](0003-dispatch-and-retry-queue.md) | MVP 以 PostgreSQL 承担分发与重试调度，通过 Dispatcher 接口保留演进空间 | Accepted | 2026-10-24 |
| [ADR-0004](0004-state-store.md) | 状态存储采用 PostgreSQL，Attempt 日志同库分区并可演进到列存 | Accepted | 2026-10-24 |
| [ADR-0005](0005-config-as-yaml-in-git.md) | 配置以 YAML 文件形式存放在 Git 中管理 | Accepted | 2026-10-24 |
| [ADR-0006](0006-template-language-scope.md) | 请求模板只支持字段路径取值与固定的内置函数 | Accepted | 2026-10-24 |
| [ADR-0007](0007-ordering-semantics.md) | 有序投递按目标和 ordering_key 保证受理顺序，前序死信默认阻塞 | Accepted | 2026-10-24 |
| [ADR-0008](0008-secrets-management.md) | 密钥管理：配置只写引用、运行时注入、记录脱敏、API Key 强度约束 | Accepted | 2026-10-24 |
| [ADR-0009](0009-pg-to-mq-migration-path.md) | 从 PostgreSQL 派发平滑迁移到 MQ 的路径（细化 ADR-0003） | Accepted | 2026-10-24 |
| [ADR-0010](0010-max-in-flight-soft-limit.md) | max_in_flight 暂采用软上限，保留改为严格上限的路径 | Accepted（暂定，预期修订） | 2026-10-24 |

格式约定见 `.claude/PRPs/plans/completed/tech-spike-and-selection.plan.md` 的 ADR_FORMAT。评审通过后，把状态改为 `Accepted`。
