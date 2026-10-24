# Plan: 稳健性增强（Message Hub Phase 6）

## Summary
满足供应商约束与差异化场景：按目标限流（集群范围的令牌桶，在领取时扣减）、按目标熔断（连续失败后暂停领取，不消耗重试预算、不丢弃）、429 驱动的目标级背压、OAuth2 token 在 401 时失效、字段级脱敏（attempt 记录与查询 API 中的 payload）、以及有序投递与幂等键透传的补充测试。Phase 4 已实现有序投递与 `{{event.idempotency_key}}` 透传，本阶段只补测试与文档，不重做。

## User Story
As a 接入 Message Hub 的业务方与值班运维,
I want Hub 遵守每个供应商的速率约束、在供应商故障时自动暂停而不是耗尽重试、并且不在存储与查询结果中暴露敏感字段,
So that 不会因为 Hub 的流量把供应商打挂或被封禁，敏感数据也不会经查询 API 泄露。

## Problem → Solution
只有 `max_in_flight`（软上限，ADR-0010）与单条 delivery 的退避；供应商宕机时每条 delivery 各自重试直到 DEAD；429 只影响当前 delivery；记录与查询 API 只按凭据名称脱敏 → 目标级限流、熔断、背压；可配置的字段级脱敏。

## Metadata
- **Complexity**: Large
- **Source PRD**: `.claude/PRPs/prds/message-hub.prd.md`
- **PRD Phase**: Phase 6 — 稳健性增强
- **Depends on**: Phase 4（已合并）
- **Parallel with**: Phase 5（文件归属见 `ops-capabilities.plan.md` 的"与 Phase 6 的并行约定"，本计划完全遵守该表）
- **Estimated Files**: 约 30 个

### 需要在实施第一步由用户确认的设计（Task 1 产出 ADR-0011）
本计划推荐 **方案 A**。实施时先写 ADR-0011（Status: Proposed），用户确认后再写代码；若用户选其他方案，Task 3–4 按 ADR 调整。

| 方案 | 做法 | 结论 |
|---|---|---|
| **A. PG 中的按目标状态行，领取时扣减**（推荐） | 新表 `destination_state`；领取事务对涉及的目标行 `FOR UPDATE`，按经过时间补充令牌，允许领取数 = `min(空闲槽位, floor(tokens))` | 集群范围准确；计量单位是"领取"≈"请求"（重试也计入）；每轮领取每个目标一次行锁，开销与领取频率成正比而非请求数 |
| B. 每进程令牌桶（`golang.org/x/time/rate`） | worker 发送前 `Wait` | 简单，但实际速率 = 进程数 × 配置值，扩容即超限 |
| C. 每进程令牌桶 + 按存活 worker 数分摊 | 需要 worker 心跳表 | 复杂，扩缩容瞬间不准 |
| D. 外部组件（Redis 等） | — | 违背技术栈约束（ADR-0002 / 0004） |

```
KEY_INSIGHT: 方案 A 的目标状态行同时承载熔断与 429 背压，也让 ADR-0010 的"严格 max_in_flight"日后可以低成本实现（同一行锁已串行化同一目标的领取）
APPLIES_TO: Task 3、4
GOTCHA: ADR-0010 中用户决定暂不做严格上限——本阶段不改变 max_in_flight 语义，只在 ADR-0011 与 ADR-0010 的 Revisit When 中记录这一可能性
```

---

## UX Design

### Before / After（运维视角）
```
Before: 供应商宕机 → 每条 delivery 各自重试直到预算耗尽 → 大量 DEAD → 运维批量重放
After : 连续 5 次可重试失败 → 熔断打开 30s（指数增长，封顶 10m）→ 期间不领取该目标
        → half_open 放行 1 条探测 → 成功则关闭继续投递；delivery 一直待投递，不消耗预算
        429 + Retry-After → 整个目标暂停到该时刻
        rate_limit 10 rps → 集群范围不超过 10 次领取 / 秒（含 burst）
```

### Interaction Changes
| Touchpoint | Before | After | Notes |
|---|---|---|---|
| 配置 `destinations[].rate_limit` | 无 | `{ requests_per_second, burst }`，可选 | 不配置即不限流 |
| 配置 `policies[].circuit_breaker` | 无 | `{ failure_threshold, open_for, max_open_for }`，可选 | 不配置即不熔断 |
| 配置 `destinations[].record_mask`、`record_response_body`、`payload_mask` | 无 | 见 Task 6 | 字段级脱敏 |
| 查询 API | payload 原样返回 | 配置的敏感路径为 `***` | 默认所有角色都脱敏 |
| 指标（Phase 5 合并后） | — | `hub_destination_breaker_state`、`hub_destination_rate_limited_total` | Phase 5 未合并时只输出日志 |

---

## Mandatory Reading

| Priority | File | Lines / Section | Why |
|---|---|---|---|
| P0 | `internal/dispatch/pg.go` | `claimSQL`、`Claim`、`completeSQL`、`Complete` | 限流与熔断在这里接入 |
| P0 | `internal/dispatch/targets.go` | all | 配置 → 领取参数 |
| P0 | `docs/adr/0010-max-in-flight-soft-limit.md` | all | 不改变软上限决定 |
| P0 | `docs/adr/0009-pg-to-mq-migration-path.md` | M2、M6 | 领取仍为条件更新；迁移 MQ 后仍在领取逻辑执行 |
| P0 | `docs/design/domain-model.md` | §5.3、§7.3、§7.4、§8、§10、§11、§13.3 | 计数、分类、退避、隔离、脱敏、配置 |
| P0 | `internal/config/{types,schema,validate,load,clone}.go`、`schemas/config.schema.json` | all | 新配置字段的完整链路 |
| P0 | `internal/outbound/record.go` | `maskByName`、`BuildRequestRecord`、`BuildResponseRecord` | 字段级脱敏扩展点 |
| P1 | `internal/outbound/auth.go` | `TokenProvider` | 401 时失效 |
| P1 | `internal/worker/process.go` | `attempt`、`decide` | 并行约定中 Phase 6 可修改的范围 |
| P1 | `internal/api/views.go` | payload 输出 | 查询 API 脱敏 |
| P1 | `internal/dispatch/pg_integration_test.go` | `TestMaxInFlight*`、`TestBusyDestinationDoesNotStarveOthers`、`explain` | 测试与 EXPLAIN 模式 |
| P2 | `docs/spike/02-outpost-evaluation.md` | §6 | "禁用后丢弃"违反零静默丢失：熔断不得丢弃 |

## External Documentation

| Topic | Source | Key Takeaway |
|---|---|---|
| 令牌桶 | 经典算法 | `tokens = min(burst, tokens + rate × elapsed)`，领取 n 条扣 n |
| 熔断器 | Martin Fowler, *CircuitBreaker* | closed → open（计时）→ half-open（有限探测）→ closed / open |
| 行锁 | PostgreSQL 16 文档 | 目标状态行 `FOR UPDATE`（不 SKIP），保证计数正确；候选 delivery 仍 `SKIP LOCKED` |

No new external dependencies（方案 A 不需要；方案 B 才需要 `golang.org/x/time`）。

---

## Patterns to Mirror

### CONFIG_FIELD（新增配置字段的完整链路）
// SOURCE: `schemas/config.schema.json` `$defs.destination.properties.max_in_flight`；`internal/config/types.go` `Destination.MaxInFlight`；`load.go` `applyDefaults`；`validate.go`（错误聚合到 `problems`）
```go
// DefaultMaxInFlight applies when a destination omits max_in_flight (§8).
const DefaultMaxInFlight = 10
```
新增字段要：schema（不允许未知字段）→ 类型（`json` tag、`omitempty`）→ 默认值 → 语义校验 → `clone.go` 深拷贝 → ConfigHash 自然包含 → 示例配置 → domain-model §11 表格。

### CLAIM_SQL（领取中加入目标级上限）
// SOURCE: internal/dispatch/pg.go `claimSQL` 的 `caps` / `avail` CTE
```sql
avail AS (
	SELECT c.destination_id, c.lease_ms, least(c.max_in_flight - coalesce(i.n, 0), $4) AS free
	FROM caps c LEFT JOIN inflight i USING (destination_id)
	WHERE c.max_in_flight - coalesce(i.n, 0) > 0
)
```

### CONDITIONAL_UPDATE / FENCING
// SOURCE: internal/dispatch/pg.go `completeSQL`（`WHERE id = $1 AND status = $11 AND lease_owner = $2 AND lease_expires_at = $12`）

### TYPED_ENUM
// SOURCE: internal/domain/delivery.go（`type DeliveryStatus string` + 常量）——熔断状态同样定义为类型化常量，SQL 中不手写

### MASKING
// SOURCE: internal/outbound/record.go `maskByName`（返回副本，不修改输入）

### TEST_STRUCTURE
// SOURCE: internal/dispatch/main_integration_test.go（fixture、`seedSQL`）；internal/worker/e2e_integration_test.go（vendor 模拟、`waitStatus`）

---

## Files to Change

| File | Action | Justification |
|---|---|---|
| `docs/adr/0011-destination-rate-limit-and-circuit-breaker.md`、`docs/adr/README.md` | CREATE / UPDATE | Task 1，先于代码 |
| `db/migrations/00005_destination_state.sql` | CREATE | 目标状态表 |
| `schemas/config.schema.json`、`internal/config/{types,load,validate,clone}.go` + tests、`internal/config/testdata/**` | UPDATE | 新配置字段 |
| `config/examples/hub.example.yaml` | UPDATE | 示例 |
| `internal/dispatch/{targets,pg,state}.go` + tests | UPDATE / CREATE | 领取时限流与熔断；Complete 时更新熔断计数与 429 暂停 |
| `internal/dispatch/state_integration_test.go` | CREATE | 集群范围限流、熔断状态机、背压 |
| `internal/testsupport/pgtest/pgtest.go` | UPDATE（底座，单独提交） | `Tables` 加入 `destination_state` |
| `internal/worker/process.go`、`run.go` | UPDATE | 401 时失效 token；脱敏规则传入记录构建 |
| `internal/outbound/{auth,record}.go` + tests | UPDATE | `Invalidate`；按路径脱敏 |
| `internal/api/views.go` + tests | UPDATE | 查询响应中 payload 按路径脱敏 |
| `internal/worker/e2e_integration_test.go` | UPDATE | 熔断、限流、有序 + 重试 + 接管、幂等键稳定性 |
| `docs/design/domain-model.md`、`docs/testing/coverage-gaps.md`、`docs/adr/0010-max-in-flight-soft-limit.md` | UPDATE | §8、§10、§11、新增 §15；B18、B20；ADR-0010 Revisit When 引用 ADR-0011 |

## NOT Building

- `max_in_flight` 严格上限（ADR-0010 用户决定暂不做）
- payload 加密存储、字段级哈希（PRD Open Question"各供应商的脱敏要求"未决）
- 按供应商自动探测速率（只用配置值与 429 / Retry-After）
- 熔断打开时把 delivery 转为 DEAD 或丢弃（违反零静默丢失）
- 固定出口 IP（PRD Open Question，属部署）
- Phase 5 的指标基础设施（若 Phase 5 未合并，本阶段只输出日志）

---

## Step-by-Step Tasks

### Task 1: ADR-0011（先于代码，需用户确认）
- **ACTION**: 写 `docs/adr/0011-destination-rate-limit-and-circuit-breaker.md`（Status: Proposed），格式同 ADR-0010
- **IMPLEMENT**: 四个方案与推荐理由；熔断状态机与"不消耗预算、不丢弃"原则；429 背压；与 ADR-0009、ADR-0010 的关系；Revisit When（单目标状态行锁竞争成为瓶颈、需要按 source 限流等）
- **VALIDATE**: **停下来请用户确认方案**；确认后改为 Accepted 再继续

### Task 2: 配置字段
- **ACTION**: 新增可选字段
- **IMPLEMENT**:
  - `destinations[].rate_limit: { requests_per_second: number (0.01–10000), burst: integer (1–10000，默认 ceil(rps)) }`
  - `policies[].circuit_breaker: { failure_threshold: 1–1000, open_for: duration, max_open_for: duration }`，校验 `max_open_for ≥ open_for`
  - `destinations[].record_mask: ["body.<path>", "url.query.<name>", "headers.<Name>", "response.body.<path>"]`（路径语法复用 `allow_literal`）
  - `destinations[].record_response_body: full | json_only | none`（默认 `full`）
  - 顶层 `payload_mask: [{ event_type, paths: ["payload.<path>", "metadata.<key>"] }]`（查询 API 输出用）
- **MIRROR**: CONFIG_FIELD
- **GOTCHA**: 不配置新字段时行为与 Phase 4 完全一致；回归测试：示例配置不加新字段时 ConfigHash 不变（`omitempty` 且无默认值写回）
- **VALIDATE**: `internal/config` valid / invalid fixture 各补用例；schema 测试

### Task 3: 迁移与目标状态 `00005_destination_state.sql`、`internal/dispatch/state.go`
- **ACTION**: 表与读写函数
- **IMPLEMENT**:
  ```sql
  CREATE TABLE destination_state (
      destination_id       text PRIMARY KEY,
      tokens               double precision NOT NULL,
      refilled_at          timestamptz NOT NULL DEFAULT now(),
      paused_until         timestamptz NULL,
      breaker_state        text NOT NULL DEFAULT 'closed',
      consecutive_failures integer NOT NULL DEFAULT 0,
      opened_until         timestamptz NULL,
      open_count           integer NOT NULL DEFAULT 0,
      updated_at           timestamptz NOT NULL DEFAULT now(),
      CONSTRAINT destination_state_breaker_check CHECK (breaker_state IN ('closed', 'open', 'half_open'))
  );
  ```
  - 行在首次需要时 `INSERT … ON CONFLICT DO NOTHING`；配置中删除的目标行保留
- **MIRROR**: TYPED_ENUM
- **VALIDATE**: 迁移 up / down；`pgtest.Tables` 更新（底座，单独提交）

### Task 4: 领取时限流、熔断与背压 `internal/dispatch/pg.go`
- **ACTION**: 对配置了 `rate_limit` 或 `circuit_breaker` 的目标，在领取事务内先锁定并更新状态行，得到本轮"允许领取数"，作为 `avail` 的额外上限
- **IMPLEMENT**:
  - 允许数 = `min(空闲槽位, 批大小, floor(补充后的 tokens))`；`paused_until > now()` 或熔断 open 且未到期时为 0；open 到期 → half_open，允许数为 `halfOpenProbes = 1`
  - 实际领取 n 条后扣 n 个令牌
  - 多个目标按 `destination_id` 排序加锁，避免死锁
  - `Complete`（同一事务）：`retryable` → `consecutive_failures + 1`，达到阈值 → open，`opened_until = now() + min(max_open_for, open_for × 2^open_count)`；`success` → 清零、half_open → closed；half_open 探测失败 → 再次 open；`permanent` / `render_error` / `infra_error` 不影响熔断；429 且有合法 Retry-After → `paused_until = greatest(paused_until, now() + Retry-After)`
  - `Completion` 增加 `RetryAfter *time.Duration`（由 worker 从响应解析）
- **MIRROR**: CLAIM_SQL、CONDITIONAL_UPDATE / FENCING
- **GOTCHA**:
  - 熔断打开期间 delivery 保持 PENDING / RETRY_SCHEDULED，不领取、不计 attempt、不 DEAD
  - 未配置新字段的目标不锁状态行，领取路径与 Phase 4 相同（性能回归测试）
  - 成功路径只在计数非零时 UPDATE（`WHERE consecutive_failures > 0 OR breaker_state <> 'closed'`），避免热点写
  - 熔断状态只是"暂停领取"，Reclaim 不受影响
- **VALIDATE**: 集成测试：4 个 dispatcher 并发、rps=5 时 3 秒内领取数 ≤ 5×3 + burst；熔断全路径；429 暂停整个目标；未配置目标的 EXPLAIN 与领取结果不变

### Task 5: 401 时失效 OAuth2 token
- **ACTION**: `func (p *TokenProvider) Invalidate(destinationID string)`；worker 在 `auth.type = oauth2_client_credentials` 且响应 401 时调用（`decide()` 中）
- **GOTCHA**: 只丢弃缓存，不改变本次 outcome；下次 attempt 重新取 token
- **VALIDATE**: 单元测试：失效后下一次调用重新请求 token 端点；worker 测试断言 401 时调用；coverage-gaps B20 中该项关闭

### Task 6: 字段级脱敏
- **ACTION**: attempt 记录按 `record_mask` / `record_response_body`；查询 API 的 payload 按 `payload_mask`
- **IMPLEMENT**:
  - `outbound.BuildRequestRecord` / `BuildResponseRecord` 增加规则参数，在名称规则之后、截断之前应用；`response.body.<path>` 只对合法 JSON 生效
  - `record_response_body: json_only` 时非 JSON 响应体记为空字符串；`none` 时不记录响应体（保留状码与 Content-Type）——关闭 B18
  - `api/views.go` 输出事件 payload / metadata 时按 `payload_mask` 替换为 `***`
- **MIRROR**: MASKING
- **GOTCHA**: 数据库中的 payload 仍为原文（投递需要）；默认所有角色都脱敏，是否给 operator 开放原文写入 ADR-0011 的待定项；规则变化后历史 attempt 不回溯
- **VALIDATE**: 表驱动测试覆盖三个位置、数组路径、缺失路径；查询 API 测试；契约测试仍通过

### Task 7: 有序与幂等的补充测试
- **ACTION**: e2e 测试
- **IMPLEMENT**:
  - 有序 + 重试：前序 500 两次后成功，后序在前序成功后才到达 mock（按到达时间断言）
  - 有序 + DEAD + replay / skip：block 模式下 replay 前序成功后后序自动投递；skip 前序后后序投递（通过 Phase 3 的 operator API）
  - 有序 + 接管：前序 worker 停止、lease 过期后，后序仍等待前序
  - 幂等键：同一 delivery 的重试与 replay 中 `Idempotency-Key: {{ event.idempotency_key }}` 不变；缺省时 `default` 兜底
- **VALIDATE**: `make test-integration`

### Task 8: 文档与缺口
- **ACTION**: domain-model 新增 §15（目标保护：限流、熔断、背压），更新 §8、§10、§11；coverage-gaps：B18 关闭、B20 中 OAuth2 401 关闭；ADR-0010 Revisit When 引用 ADR-0011；PRD Phase 6 complete
- **VALIDATE**: 第 2 节与 `make cover-gaps-all` 一致

---

## Testing Strategy

### Unit Tests
| Test | Input | Expected Output | Edge Case? |
|---|---|---|---|
| 令牌补充 | elapsed、rps、burst | 不超过 burst | 是 |
| 熔断打开时长 | open_count 0..n | 指数增长、封顶 max_open_for | 是 |
| 配置校验 | 越界、`max_open_for < open_for`、非法路径 | 聚合错误 | 是 |
| 字段级脱敏 | 嵌套、数组、缺失路径 | 只替换存在的路径，输入不变 | 是 |

### Edge Cases Checklist
- [ ] 空输入：未配置任何新字段 → 行为与 Phase 4 一致
- [ ] 最大输入：rps 10000、burst 10000
- [ ] 非法类型：rps 为 0 / 负数
- [ ] 并发：多 worker 同时领取同一目标；熔断状态并发更新
- [ ] 网络故障：供应商宕机 → 熔断；恢复后 half_open 探测
- [ ] 权限：viewer / operator 查询时 payload 脱敏

---

## Validation Commands

```bash
make lint
make test
make test-integration   # 并发限流、熔断、e2e
make cover-gaps-all     # EXPECT: 与 coverage-gaps.md 一致；dispatch ≥ 85%
make check-secrets
```

### Manual Validation
- [ ] mock-vendor 设为 500：熔断打开、delivery 保持待投递、恢复后自动继续，无 DEAD
- [ ] mock-vendor 返回 429 + Retry-After 30：整个目标暂停约 30s
- [ ] rate_limit 5 rps、两个 worker 进程：mock 收到的速率不超过 5 rps（含 burst）

---

## Acceptance Criteria
- [ ] ADR-0011 经用户确认（Accepted）
- [ ] 限流 / 熔断 / 有序场景的集成测试通过（PRD success signal）
- [ ] attempt 记录、查询 API、日志中，配置的敏感字段无明文
- [ ] 未配置新字段时行为与 Phase 4 完全一致
- [ ] 遵守与 Phase 5 的文件归属约定；底座改动单独提交

## Completion Checklist
- [ ] 遵循 coding-conventions.md；状态枚举为类型化常量
- [ ] SQL 参数化；领取 SQL 的 EXPLAIN 断言更新
- [ ] 阈值、时长、上限为命名常量或配置
- [ ] 熔断不丢弃、不消耗预算

## Risks
| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| 目标状态行成为领取热点 | M | M | 只对配置了限流 / 熔断的目标加锁；按 id 排序加锁；Phase 7 压测 |
| 熔断误判（对端偶发错误） | M | M | 阈值按策略配置；half_open 探测 |
| 限流与 max_in_flight 叠加后吞吐不及预期 | M | L | 文档说明两者取较小值；指标可见 |
| 与 Phase 5 合并冲突 | M | L | 并行约定；冲突只在约定的函数中 |

## Notes
- 限流的计量单位是"领取"，包含重试；文档中要写明，避免被理解为"每秒新事件数"
- 迁移 MQ（ADR-0009）后，限流与熔断仍在领取（条件更新）时执行，不依赖 MQ 的能力
