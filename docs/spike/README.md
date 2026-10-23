# Phase 1 技术验证：结论摘要

- **目标**：决定"在 Outpost / Svix 上二次开发"还是"基于开源组件自建"，并定案技术栈
- **时间盒**：2–3 个工作日（实际 1 个会话完成；PoC 因环境原因跳过）
- **状态**：ADR 0001–0004 为 `Proposed`，**待评审**

## 结论

| 决策 | 结论 | 依据 |
|---|---|---|
| 二次开发 vs 自建 | **自建**，系统借鉴两者的设计 | Outpost 82 / 144、Svix 88 / 144，均低于 101 门槛，且条件 3 都不满足 → [ADR-0001](../adr/0001-build-vs-extend.md) |
| 语言 | **Go** | 76 / 85 → [ADR-0002](../adr/0002-implementation-language.md) |
| 分发与重试 | **MVP 仅用 PostgreSQL**（`SKIP LOCKED` + `LISTEN/NOTIFY`），通过 `Dispatcher` 接口可演进到 Redis / Kafka | 64 / 75 → [ADR-0003](../adr/0003-dispatch-and-retry-queue.md) |
| 状态存储 | **PostgreSQL**，Attempt 同库分区，可演进到列存 | 58 / 60 → [ADR-0004](../adr/0004-state-store.md) |

最终 MVP 只有 **Go 服务 + PostgreSQL** 两个组件。

**关键发现**：两个候选方案都存在与"零静默丢失"目标冲突的设计：
- Outpost 发布时只写 MQ，目标被禁用后消息直接丢弃
- Svix 非事务入队，禁用目标的消息被跳过且不记录

自建方案在同一事务中写入 Event 与 Delivery，本身就是 Transactional Outbox，从架构上消除这类问题。

## 文档索引

| 文档 | 内容 |
|---|---|
| [01-evaluation-checklist.md](01-evaluation-checklist.md) | 能力清单 C1–C18、权重、评分规则、锁定的决策规则（打分前单独提交，commit `f4bc44f`） |
| [02-outpost-evaluation.md](02-outpost-evaluation.md) | Outpost v1.7.0 源码级评估 |
| [03-svix-evaluation.md](03-svix-evaluation.md) | Svix server-v1.102.1 源码级评估（含 Bridge） |
| [04-poc-results.md](04-poc-results.md) | PoC 状态（未执行）、对决策影响的分析、证据抽查记录 |
| [05-stack-evaluation.md](05-stack-evaluation.md) | 语言、调度队列、状态存储对比 |
| `../../spike/mock-vendor/` | 模拟供应商（自检 9/9 通过），后续阶段可复用 |
| `../../spike/scenarios/` | PoC 执行步骤与 Outpost compose 文件 |

## 未验证事项

| 项 | 原因 | 影响 | 何时补 |
|---|---|---|---|
| PoC 场景 S1–S8（两个候选） | Docker daemon 无法访问 Docker Hub，本地也没有依赖服务 | 不影响 ADR-0001（见 04 中的影响分析） | 网络可用时按 `spike/scenarios/run_scenarios.md` 补跑，作为参考 |
| 首次投递 P99 < 1s、吞吐上限 | 自建实现尚不存在 | ADR-0003 的阈值是估计值 | Phase 7 压测 |
| ADR-0003 / 0004 的容量阈值 | 估计值 | 演进时机 | Phase 7 压测 |
| 团队 Go 能力（N4） | 无团队信息 | ADR-0002 的可维护性前提 | 评审时确认 |
