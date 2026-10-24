# Plan: 投递引擎（Message Hub Phase 4）

## Summary
实现 `worker` 角色：基于 PostgreSQL 的派发器（`Dispatcher` 接口 + `pgDispatcher`：候选查询、幂等领取、lease、接管、按目标并发上限、有序检查、LISTEN 唤醒与轮询兜底），以及投递流水线（模板渲染 → 认证 → HTTP 发送 → 成功判定与结果分类 → 退避计算 → 状态更新与 attempt 记录**在同一事务**写入，记录先脱敏再截断）。模板引擎、成功判定与退避计算用 Phase 2 的黄金用例验收。

## User Story
As a 依赖 Message Hub 的业务系统,
I want 已受理的事件被可靠地投递到每个外部目标，失败时按策略重试或进入死信，过程全部有记录,
So that 我不必关心各供应商的格式、认证、超时与重试，只在需要时查询结果。

## Problem → Solution
Phase 3 写入了 PENDING 的 delivery，但没有任何组件去投递 → `messagehub serve --role=worker` 持续领取到期任务、按目标配置投递、记录每次尝试，并为日后迁移到 MQ 保留 `Dispatcher` 边界（ADR-0009）。

## Metadata
- **Complexity**: XL（建议按下方"实施分组"分两到三批提交）
- **Source PRD**: `.claude/PRPs/prds/message-hub.prd.md`
- **PRD Phase**: Phase 4 — 投递引擎
- **Depends on**: Phase 2b 共享运行底座（`runtime-foundation.plan.md`）
- **Parallel with**: Phase 3（受理与查询）
- **Estimated Files**: 约 40 个

### 实施分组
| 组 | 任务 | 说明 |
|---|---|---|
| A 纯函数引擎 | Task 1–4 | 模板、成功判定、退避、记录脱敏；只依赖黄金用例，可最先完成 |
| B 出站调用 | Task 5–6 | 认证（含 OAuth2）、HTTP 发送 |
| C 派发与流水线 | Task 7–12 | 迁移 00002、Dispatcher、唤醒、worker、接入 serve、并发与端到端测试 |

---

## UX Design

### Before
delivery 停留在 PENDING。

### After（运维视角）
```
serve --role=worker
  ├─ LISTEN hub_delivery_ready（直连）+ 每 1s 轮询兜底
  ├─ 领取：按目标 max_in_flight、按 ordering_key 保序
  ├─ 投递：渲染 → 认证 → 发送 → 判定 → 退避
  └─ 结果：SUCCEEDED / RETRY_SCHEDULED / DEAD，每次尝试一条 attempt（已脱敏）
日志：delivery.attempted（每次尝试一条宽事件）
```

### Interaction Changes
| Touchpoint | Before | After | Notes |
|---|---|---|---|
| 投递 | 无 | 自动 | 查询 API（Phase 3）可看到 attempts |
| 配置中的模板 | 只校验结构 | 加载时同时校验模板语法、函数与路径根 | 通过 `config.WithTemplateValidator` 接入 |

---

## Mandatory Reading

| Priority | File | Lines / Section | Why |
|---|---|---|---|
| P0 | `docs/design/domain-model.md` | §5（含持久化要求）、§6、§7.1–§7.4、§8、§10、§13.2、§13.3 | 状态机、有序、模板、成功判定、退避、隔离、记录与脱敏的唯一权威 |
| P0 | `docs/adr/0009-pg-to-mq-migration-path.md` | M1、M2、P1–P5 | Phase 4 的必做项 |
| P0 | `docs/adr/0004-state-store.md` | Decision（2026-10-24 修订） | attempt 与状态更新在同一事务中同步写入 |
| P0 | `docs/adr/0008-secrets-management.md` | D2 | 记录脱敏规则 |
| P0 | `internal/domain/delivery_state.go` | 26–46 | 所有状态变化先经 `Transition` |
| P0 | `internal/domain/attempt.go` | all | `Outcome`、`AttemptTrigger`、`RecordedRequest/Response`、`MaxRecordedBodyBytes`、`MaskedValue` |
| P0 | `internal/domain/sensitive.go` | all | `IsSensitiveName`（与配置校验共用） |
| P0 | `internal/contracts/template.go`、`success.go`、`retry.go` | 类型定义与 `Load*Cases` | 黄金用例的结构（注意 `contracts.BodyRule` 需转换为 `config.Rule`） |
| P0 | `db/migrations/00001_init.sql` | 60–150 | deliveries / attempts 的列与索引 |
| P0 | `.claude/PRPs/plans/completed/runtime-foundation.plan.md`（实际接口与偏离见 `.claude/PRPs/reports/runtime-foundation-report.md`） | Task 1、4、6、7、8、9 | Settings、Resolver、rules、ConfigHolder、Runtime、`configOptions()` |
| P1 | `docs/adr/0007-ordering-semantics.md` | Decision | 有序范围与 block / skip |
| P1 | `internal/config/types.go` | Policy、Destination、Auth、Request | 策略与请求模板的结构 |
| P1 | `docs/spike/03-svix-evaluation.md` | §7 | 退避与 jitter、响应体截断的参考实现 |
| P2 | `docs/spike/02-outpost-evaluation.md` | §6 | 错误三分类、出口代理故障不计入预算 |

## External Documentation

| Topic | Source | Key Takeaway |
|---|---|---|
| x/oauth2 clientcredentials | https://pkg.go.dev/golang.org/x/oauth2/clientcredentials | `(&clientcredentials.Config{…}).TokenSource(ctx)` 自带缓存与过期刷新 |
| pgx LISTEN | https://pkg.go.dev/github.com/jackc/pgx/v5 | 直连 `*pgx.Conn`：`Exec(ctx, "LISTEN hub_delivery_ready")` 后 `WaitForNotification(ctx)` |
| CTE 中的 FOR UPDATE | PostgreSQL SELECT 文档 | `FOR UPDATE SKIP LOCKED` 不能与窗口函数出现在同一层 SELECT，可放在 CTE 中先锁定候选 |
| net/http Client | https://pkg.go.dev/net/http | 每次请求用 `context.WithTimeout`；共享 `Transport`；`io.LimitReader` 限制读取 |

新增依赖（遵循"发布至少两周"）：

| Module | Version | 发布日期 |
|---|---|---|
| `golang.org/x/oauth2` | v0.37.0 | 2026-08-25 |

```
KEY_INSIGHT: SKIP LOCKED 只负责"找出候选"，状态迁移一律经条件更新（ADR-0009 M2）
APPLIES_TO: Task 8
GOTCHA: 多个 worker 同时计算 max_in_flight 时，各自看不到对方未提交的领取，上限是"软上限"；超出量不超过 (worker 数 − 1) × 批大小，测试中要验证该上界

KEY_INSIGHT: 渲染时同时产出"实际值"与"脱敏值"
APPLIES_TO: Task 1、Task 4
GOTCHA: 不能用"脱敏后的值重新渲染"来生成记录，否则 hmac 签名会按脱敏后的 Body 计算，记录中的签名与实际发出的不一致
```

---

## Patterns to Mirror

### STATE_TRANSITION
// SOURCE: internal/domain/delivery_state.go:26-46
```go
to, err := domain.Transition(d.Status, domain.TriggerRetry) // 先在 Go 中确认迁移合法
// 再以条件更新落库：UPDATE … SET status=$to WHERE id=$1 AND status='IN_FLIGHT' AND lease_owner=$owner
```

### RECORD_TYPES
// SOURCE: internal/domain/attempt.go
```go
const MaxRecordedBodyBytes = 16 * 1024
const MaskedValue = "***"
type RecordedRequest struct { Method, URL string; Headers map[string]string; Body string; Truncated bool }
type RecordedResponse struct { StatusCode int; ContentType, Body string; Truncated bool }
```

### SENSITIVE_RULE
// SOURCE: internal/domain/sensitive.go
```go
domain.IsSensitiveName(name) // 配置校验与记录脱敏共用，不得另写规则
```

### GOLDEN_CASES
// SOURCE: internal/contracts/cases.go:23-35、template.go:101、success.go:130、retry.go:50
```go
cases, err := contracts.LoadTemplateCases(contracts.TemplateCasesPath())
for _, c := range cases { t.Run(c.Name, func(t *testing.T) { /* 渲染并比较 c.Want / c.WantError */ }) }
```

### ERROR_HANDLING / TEST_STRUCTURE
// SOURCE: internal/domain/errors.go、internal/domain/delivery_state_test.go（全组合表驱动）、db/migrations_integration_test.go（集成测试 tag）

### LOGGING_PATTERN（宽事件）
// SOURCE: docs/design/coding-conventions.md §7
```go
log.Info("delivery.attempted", "event_id", e, "delivery_id", d, "destination_id", dst,
    "attempt", n, "trigger", trg, "outcome", oc, "status_code", sc, "duration_ms", ms,
    "next_attempt_at", next, "config_hash", h)
```

---

## Files to Change

| File | Action | Justification |
|---|---|---|
| `internal/template/{lexer,parser,eval,funcs,render}.go` + tests | CREATE | 模板语言 §7.1、§13.2；静态校验；同时产出实际值与脱敏值 |
| `internal/template/golden_test.go` | CREATE | 跑 56 条模板黄金用例（B1） |
| `internal/outcome/outcome.go` + `golden_test.go` | CREATE | 成功判定与结果分类 §7.2、§7.3；跑 32 条黄金用例 |
| `internal/backoff/backoff.go` + `golden_test.go` | CREATE | 退避 §7.4、Retry-After；跑 15 条黄金用例 |
| `internal/outbound/record.go` + `_test.go` | CREATE | attempt 记录的脱敏与截断 §10（B9） |
| `internal/outbound/auth.go` + `_test.go` | CREATE | OAuth2 token 源缓存；token 获取失败归为 infra_error |
| `internal/outbound/client.go` + `_test.go` | CREATE | 共享 HTTP 客户端、超时、响应读取上限、错误分类 |
| `db/migrations/00002_deliveries_tuning.sql` | CREATE | deliveries 的 `fillfactor` 与 autovacuum 参数（ADR-0009 P2） |
| `internal/dispatch/dispatcher.go` | CREATE | `Dispatcher` 接口与数据类型（M1） |
| `internal/dispatch/pg.go` | CREATE | `pgDispatcher`：候选查询、幂等领取、接管、完成 |
| `internal/dispatch/wakeup.go` | CREATE | LISTEN 直连循环 + 轮询兜底 + 重连（P4、P5） |
| `internal/dispatch/*_integration_test.go` | CREATE | 并发与有序测试（B5） |
| `internal/worker/run.go` | UPDATE | 替换底座占位：循环、并发控制、优雅退出 |
| `internal/worker/process.go` + `_test.go` | CREATE | 单个 delivery 的完整流水线 |
| `internal/worker/e2e_integration_test.go` | CREATE | 真实数据库 + `httptest` 模拟供应商的端到端测试 |
| `cmd/messagehub/serve.go` | UPDATE | 在 `configOptions()` 中加入 `config.WithTemplateValidator(template.ValidateField)`（唯一允许的改动） |
| `go.mod` / `go.sum` | UPDATE | 新增 `golang.org/x/oauth2 v0.37.0` |
| `docs/testing/coverage-gaps.md` | UPDATE | B1、B5、B9 关闭；新条目 |

**不修改**：`internal/api/**`、`internal/store/**`（Phase 3）、`internal/app/**`。

## NOT Building

- 限流与熔断（Phase 6）
- 字段级业务数据脱敏（Phase 6）
- 指标、追踪、告警、对账、分区维护、payload 清理（Phase 5）
- MQ 派发实现与 `dispatch: pg | mq` 配置字段（ADR-0009 M3–M6，迁移时）
- 自动禁用失败目标（PoC 证明候选方案的"禁用后丢弃"违反零静默丢失；如需要在 Phase 6 设计）
- SSRF 私网拦截：目标 URL 只来自评审过的配置（ADR-0005），MVP 只限制协议为 http / https

---

## Step-by-Step Tasks

### Task 1: 模板引擎 `internal/template`
- **ACTION**: 词法、语法、求值、12 个函数、渲染、静态校验
- **IMPLEMENT**:
  - 文本拆分为字面量与 `{{ … }}` 表达式；表达式语法 `path ('|' func arg?)*`；参数为带引号字符串、数字、`true` / `false` / `null`、标识符（`format_time` 的布局名）或路径（`secret.NAME`、`request.body`）
  - 路径根与可用位置严格按 §7.1 表格；`request.body` 只能出现在 Header（否则 `syntax`）
  - 求值上下文：`Vars{Event, Secrets, Delivery, Now, RequestBody}`，`Now` 由调用方注入（测试确定性）
  - 类型规则：Body 中单表达式保留 JSON 类型；嵌入文本时字符串化（null → ""，object / array → 按键排序的紧凑 JSON）；时间默认 RFC 3339 秒级 UTC；`format_time unix / unix_ms` 在 Body 单表达式中为 number
  - URL：`?` 之前的表达式结果用 `url.PathEscape`，之后用 `url.QueryEscape`（§13.2）
  - **双输出**：每个表达式求值一次，得到 `actual`；若其路径根为 `secret.`，`masked` 为 `domain.MaskedValue`，否则 `masked = actual`。渲染结果为 `Rendered{Actual, Masked}`（URL 为字符串，Header 为 map，Body 为 JSON 值）
  - 渲染顺序：URL → Body → Headers（Header 中 `request.body` 取**实际** Body 的序列化字节）
  - 错误类型：`ErrorClass` 为 `syntax` | `missing_path` | `unknown_function` | `bad_argument`，与 `contracts.ErrorClass` 一一对应
  - 静态校验：`ValidateField(field, tmpl string) error`，根据 field 路径（`…request.url`、`…request.headers.X`、`…request.body…`）推断位置，检查语法、函数名、参数种类、路径根；不求值
- **MIRROR**: TEST_STRUCTURE、GOLDEN_CASES
- **GOTCHA**:
  - 不得使用 `text/template`（ADR-0006）
  - Body 序列化必须稳定（键排序），否则 hmac 签名不可复现
  - 递归深度与模板长度设上限（命名常量），防止病态配置
- **VALIDATE**: `golden_test.go` 跑全部 56 条模板用例；另有单元测试覆盖双输出（含 secret 的 Header 在 masked 中为 `***`，hmac 签名 Header 在 masked 中保持原值）

### Task 2: 成功判定与结果分类 `internal/outcome`
- **ACTION**: `func Classify(in Input, policy config.Policy) domain.Outcome`
- **IMPLEMENT**:
  - `Input{RenderErr error, InfraErr error, NetErr error, StatusCode int, ContentType string, Body []byte, BodyComplete bool}`
  - 顺序严格按 §7.3：render_error → infra_error → 网络错误 / 超时（retryable）→ success（状态码匹配 `policy.Success.Status` 且 `rules.EvalAll(policy.Success.Body, 解码后的 JSON)`）→ 状态码命中 `retry_on`（retryable）→ permanent
  - 响应体用 `json.Decoder.UseNumber()` 解码；非 JSON 或未读完整（超过读取上限）时，有 body 规则则不满足
  - 是否还有重试预算由调用方（Task 10）根据 `attempt_count` 决定，本函数只给出 outcome
- **GOTCHA**: Content-Type 不参与判定（§13.3）；空响应体不是 JSON
- **VALIDATE**: `golden_test.go` 跑全部 32 条成功判定用例（`contracts.BodyRule` 转换为 `config.Rule`）

### Task 3: 退避 `internal/backoff`
- **ACTION**: `func Delay(b config.Backoff, retryNumber int, statusCode int, retryAfter string, now time.Time, rnd func() float64) time.Duration`
- **IMPLEMENT**: §7.4：`base = min(max, initial × multiplier^(n−1))`；`× (1 + u)`，`u ∈ [−jitter, +jitter]`；再以 max 封顶；429 / 503 且 Retry-After 合法（秒数或 HTTP 日期，相对 `now`）时取 `max(实际, RetryAfter)` 并以 max 封顶；非法或负数忽略
- **GOTCHA**: 大 n 时用浮点计算并在超过 max 时提前截断，防止溢出
- **VALIDATE**: `golden_test.go` 跑全部 15 条（jitter = 0）；另测 jitter 上下界（`rnd` 返回 0 与 1）

### Task 4: attempt 记录 `internal/outbound/record.go`（B9）
- **ACTION**: `func BuildRequestRecord(r template.Rendered, method string) domain.RecordedRequest`、`func BuildResponseRecord(status int, contentType string, body []byte) domain.RecordedResponse`
- **IMPLEMENT**（§10，ADR-0008 D2）：
  - 从 `Rendered.Masked` 出发，再叠加名称规则：名称满足 `domain.IsSensitiveName` 的 Header 整体为 `***`；URL 中名称敏感的查询参数值为 `***`；Body 中 key 敏感的叶子值为 `***`
  - **先脱敏后截断**到 `domain.MaxRecordedBodyBytes`，截断时 `Truncated = true`
  - OAuth2 自动附加的 `Authorization` 按名称规则脱敏
- **GOTCHA**: 截断按字节，避免切断 UTF-8 字符（回退到合法边界）
- **VALIDATE**: 表驱动：Header / URL / Body 三个位置的两类规则；Header 名含 `signature` 时按名称规则脱敏；截断边界上没有残留的部分密钥

### Task 5: 出站认证 `internal/outbound/auth.go`
- **ACTION**: `TokenProvider`：按 `(destination_id, config_hash)` 缓存 `oauth2.TokenSource`
- **IMPLEMENT**:
  - `auth.type: oauth2_client_credentials`：用 `clientcredentials.Config{ClientID, ClientSecret, TokenURL, Scopes}`，client id / secret 通过 `secrets.Resolver` 解析
  - token 获取失败（网络、5xx、凭据错误）返回包装为 `ErrInfra` 的错误 → outcome `infra_error`（不消耗预算，§5.3）
  - token 请求与响应**不写入** attempt（ADR-0008 D2）
  - 配置变化（ConfigHash 不同）后旧 TokenSource 自然失效
- **VALIDATE**: `httptest` 模拟 token 端点：首次获取、缓存命中、过期刷新、端点 500 时返回 infra 错误

### Task 6: HTTP 发送 `internal/outbound/client.go`
- **ACTION**: `func (c *Client) Send(ctx, req Request, timeout time.Duration) (Response, error)`
- **IMPLEMENT**:
  - 进程共享 `http.Transport`（`MaxIdleConnsPerHost` 等为命名常量）；每次请求 `context.WithTimeout(policy.Timeout)`
  - 只允许 `http` / `https`；不跟随重定向（`CheckRedirect` 返回 `http.ErrUseLastResponse`）
  - 响应体读取上限 `maxResponseReadBytes = 1 MiB`（命名常量），超限标记 `BodyComplete = false`
  - 错误分类：DNS、连接、TLS、超时 → 网络错误；出口代理不可用（发生在代理连接阶段）→ infra 错误（借鉴 Outpost）
- **VALIDATE**: `httptest`：200、超时、连接拒绝、超大响应、重定向不跟随

### Task 7: 迁移 `db/migrations/00002_deliveries_tuning.sql`（P2）
- **ACTION**: goose 迁移
- **IMPLEMENT**:
  ```sql
  -- +goose Up
  ALTER TABLE deliveries SET (fillfactor = 80,
      autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.02);
  -- +goose Down
  ALTER TABLE deliveries RESET (fillfactor, autovacuum_vacuum_scale_factor, autovacuum_analyze_scale_factor);
  ```
  - 文件头注释说明参数为初始值，Phase 7 压测后校准
- **GOTCHA**: `fillfactor` 只影响此后写入的页；对已有数据无影响，无需重写表
- **VALIDATE**: 迁移集成测试（`db` 包）up / down 通过；`pg_class.reloptions` 中可见参数

### Task 8: 派发器 `internal/dispatch`（M1、M2）
- **ACTION**: 接口与 `pgDispatcher`
- **IMPLEMENT**:
  ```go
  type Claimed struct { DeliveryID, EventID uuid.UUID; DestinationID, PolicyID string; AttemptCount, ReplayCount int; Reclaimed bool; LeaseExpiresAt time.Time }
  type Completion struct { DeliveryID uuid.UUID; Trigger domain.Trigger; Outcome domain.Outcome; StatusCode *int; Error string
      NextAttemptAt *time.Time; CountsAttempt bool; Attempt domain.Attempt }
  type Dispatcher interface {
      Claim(ctx context.Context, limit int) ([]Claimed, error)
      Complete(ctx context.Context, c Completion) (applied bool, err error)
  }
  ```
  - **候选与领取**（一个事务）：
    ```sql
    WITH caps(destination_id, max_in_flight) AS (SELECT * FROM unnest($1::text[], $2::int[])),
    inflight AS (SELECT destination_id, count(*) AS n FROM deliveries WHERE status = 'IN_FLIGHT' GROUP BY destination_id),
    locked AS (
      SELECT d.id, d.destination_id, d.scheduled_at, d.ingest_seq, d.ordering_key
      FROM deliveries d
      WHERE d.status IN ('PENDING','RETRY_SCHEDULED') AND d.scheduled_at <= now()
        AND NOT EXISTS (              -- 有序：同目标同 key 的更早 delivery 未结清
          SELECT 1 FROM deliveries p
          WHERE d.ordering_key IS NOT NULL AND d.destination_id = ANY($3::text[])   -- per_key 的目标
            AND p.destination_id = d.destination_id AND p.ordering_key = d.ordering_key
            AND p.ingest_seq < d.ingest_seq AND NOT (p.status = ANY(<该目标的结清状态>)))
      ORDER BY d.scheduled_at
      LIMIT $4
      FOR UPDATE SKIP LOCKED
    ),
    ranked AS (
      SELECT l.*, row_number() OVER (PARTITION BY l.destination_id ORDER BY l.scheduled_at, l.ingest_seq) AS rn
      FROM locked l
    )
    UPDATE deliveries d SET status = 'IN_FLIGHT', lease_owner = $5, lease_expires_at = now() + <lease>, updated_at = now()
    FROM ranked r JOIN caps c USING (destination_id) LEFT JOIN inflight i USING (destination_id)
    WHERE d.id = r.id AND r.rn <= c.max_in_flight - coalesce(i.n, 0)
      AND d.status IN ('PENDING','RETRY_SCHEDULED')                      -- 条件更新（M2）
    RETURNING …
    ```
    - 结清状态由 `domain.DeliveryStatus.IsOrderingSettled` 推导，block 与 skip 两类目标分别传数组参数（不在 SQL 中手写状态列表）
    - lease 时长 = 该目标策略的 `timeout` + `leaseMargin`（30s，命名常量）；按目标传入
    - 配置中已删除的目标：不在 caps 中，不会被领取；每轮记录一次 warn（Phase 5 告警）
  - **接管**（每轮领取前执行）：`UPDATE deliveries SET lease_owner=$1, lease_expires_at=…, attempt_count = attempt_count + 1 WHERE status='IN_FLIGHT' AND lease_expires_at < now() … RETURNING`，并为每条写一条 attempt（`outcome = retryable`，`error = "lease expired; outcome of previous attempt unknown"`，§5.3 计入预算）；接管后与普通领取一样进入流水线
  - **完成**（一个事务，ADR-0004 修订）：
    1. `UPDATE deliveries SET status=$to, attempt_count = attempt_count + $counts, last_* = …, scheduled_at = coalesce($next, scheduled_at), lease_owner = NULL, lease_expires_at = NULL, completed_at = CASE WHEN $terminal THEN now() END, updated_at = now() WHERE id = $1 AND status = 'IN_FLIGHT' AND lease_owner = $owner`
    2. 无论第 1 步是否命中，都 `INSERT INTO attempts …`（尝试确实发生过；lease 被接管时记录照样保留）
    3. 返回 `applied`（第 1 步是否命中）；未命中时由调用方记录 warn（lease 已失效）
- **GOTCHA**:
  - 领取事务只做状态迁移并立即提交，HTTP 调用在事务之外（P1）
  - `max_in_flight` 为软上限（见 KEY_INSIGHT）
  - 有序子查询依赖 `deliveries_ordering_idx`；用 EXPLAIN 验证
- **VALIDATE**: 见 Task 11

### Task 9: 唤醒 `internal/dispatch/wakeup.go`（P4、P5）
- **ACTION**: `func Wakeups(ctx context.Context, listenDSN string, pollInterval time.Duration, log *slog.Logger) <-chan struct{}`
- **IMPLEMENT**:
  - 用 `pg.ConnectDirect(ctx, rt.Settings.DatabaseListenURL)` 建立直连，`LISTEN hub_delivery_ready`
  - 收到通知或轮询定时器到期都向 channel 发送一个信号（带缓冲 1，合并多次通知）
  - 连接断开时按退避重连；重连期间只靠轮询（`pollInterval = 1s`，命名常量）
- **VALIDATE**: 集成测试：NOTIFY 后 100ms 内收到信号；关闭连接（`pg_terminate_backend`）后仍能通过轮询收到信号，并最终重连

### Task 10: worker 流水线 `internal/worker`
- **ACTION**: `process.go`：`func (w *Worker) Process(ctx, c dispatch.Claimed)`；`run.go`：循环与并发控制
- **IMPLEMENT**:
  1. 取 `cfg := rt.Config.Current()`（每次投递取一次快照，**不缓存**）；目标或策略已不存在 → `GiveUp`，outcome `permanent`，error 说明配置缺失
  2. 读取事件与 payload（一次查询）
  3. 解析该目标声明的 secrets（`secrets.Resolver`）；失败 → `InfraRetry`
  4. `template.Render` → 失败 → `GiveUp`（render_error）
  5. 认证（OAuth2 时附加 `Authorization: Bearer`）；失败 → `InfraRetry`
  6. `outbound.Send`（策略 timeout）
  7. `outcome.Classify`
  8. 决定触发器：success → `Succeed`；retryable 且 `attempt_count + 1 < max_attempts` → `Retry`（`backoff.Delay` 计算 `scheduled_at`）；retryable 且预算耗尽、permanent、render_error → `GiveUp`；infra_error → `InfraRetry`（延迟同样用 `backoff.Delay`，但 `CountsAttempt = false`）
  9. `domain.Transition(IN_FLIGHT, trigger)` 确认合法 → `dispatcher.Complete`（attempt 记录用 Task 4 构建，`number` = 计入后的 `attempt_count`；infra_error 时为 `attempt_count + 1`）
  10. 输出一条 `delivery.attempted`
  - `run.go`：从 `Wakeups` 收到信号后循环 `Claim(limit = 可用并发槽)` 直到没有结果；全局并发上限 `HUB_WORKER_CONCURRENCY`（信号量）；ctx 取消后不再领取，等待在途投递完成（受 `HUB_SHUTDOWN_TIMEOUT` 限制），未完成的由 lease 过期后接管
- **GOTCHA**:
  - 不同目标的投递互不等待（PoC S6 中 Svix 的 `join_all` 问题）：每个 delivery 独立 goroutine、独立完成
  - 日志中不得出现密钥、Authorization 与 payload 原文
- **VALIDATE**: `process_test.go` 用假 Dispatcher、假 Resolver、`httptest` 供应商，表驱动覆盖每条触发器路径

### Task 11: 接入、并发与端到端测试（B5）
- **ACTION**: `cmd/messagehub/serve.go` 一处改动；集成测试
- **IMPLEMENT**:
  - `configOptions()` 中加入 `config.WithTemplateValidator(template.ValidateField)`；示例配置必须仍能加载
  - `internal/dispatch/*_integration_test.go`（`pgtest`）：
    - 4 个 dispatcher 并发领取 1000 条：每条恰好被领取一次
    - 同一 delivery 并发 `Complete` 两次：只有一次 `applied = true`，两条 attempt 都存在
    - lease 过期接管：attempt_count 加 1，有一条 lease 过期的 attempt
    - `max_in_flight = 2`：单 dispatcher 时在途不超过 2；4 个 dispatcher 时不超过 2 + 3 × 批大小
    - 有序：block 模式下前序 DEAD 时后续不被领取；skip 模式下被领取；前序 SKIPPED 后被领取
    - 主要 SQL 的 EXPLAIN 不含对 deliveries 的 Seq Scan（造 1 万行，`ANALYZE` 后）
  - `internal/worker/e2e_integration_test.go`：`pgtest` + 示例配置（目标 URL 指向 `httptest`）+ 直接向表中插入事件与 delivery（不依赖 Phase 3）：
    - 成功；前 3 次 500 后成功（间隔符合退避）；持续 500 直到 DEAD；200 + `{"code":1001}` → DEAD（PoC S3）
    - 慢目标（每次 5s）不影响另一目标的投递延迟（PoC S6）
    - OAuth2 目标：token 端点故障时 InfraRetry 且 attempt_count 不增加
    - attempt 记录中 `Authorization`、secret 引用的 Header / 查询参数 / Body 字段均为 `***`
    - worker 被取消后，在途投递由另一个 worker 在 lease 过期后接管并完成
- **VALIDATE**: `make test-integration`

### Task 12: 文档
- **ACTION**: 更新 `docs/testing/coverage-gaps.md`
- **IMPLEMENT**: B1（黄金用例由引擎执行）、B5（并发）、B9（脱敏）标记完成并指向测试；`make cover-gaps` 的新条目写入第 2、3 节
- **VALIDATE**: 文档第 2 节与 `make cover-gaps` 输出一致

---

## Testing Strategy

### Unit Tests
| Test | Input | Expected Output | Edge Case? |
|---|---|---|---|
| 模板黄金用例 | 56 条 | 全部通过 | 是 |
| 成功判定黄金用例 | 32 条 | 全部通过 | 是 |
| 退避黄金用例 | 15 条 | 全部通过 | 是 |
| 模板双输出 | secret Header、hmac Header | masked 分别为 `***` 与原值 | 是 |
| 记录脱敏 | 三个位置、两类规则 | 全部 `***`，截断后无残留 | 是 |
| OAuth2 | token 端点 500 | infra 错误 | 是 |
| HTTP 客户端 | 超时、连接拒绝、超大响应、重定向 | 正确分类与截断 | 是 |
| 流水线触发器 | 每种 outcome × 预算是否耗尽 | 正确的 Trigger 与 CountsAttempt | 是 |

### Integration Tests
见 Task 9、Task 11。

### Edge Cases Checklist
- [x] Empty input（空 Body 模板、空响应体）
- [x] Maximum size input（响应超过 1 MiB、记录截断到 16 KiB）
- [x] Invalid types（模板参数类型错误、Retry-After 非法）
- [x] Concurrent access（并发领取、并发 Complete、接管）
- [x] Network failure（超时、连接拒绝、LISTEN 连接断开）
- [x] Permission denied（供应商 401 / 403 → 默认 permanent）

---

## Validation Commands

```bash
make lint              # EXPECT: 无输出
make test              # EXPECT: 全部通过（含三组黄金用例）
make test-integration  # EXPECT: 全部通过（并发、有序、端到端）
make cover             # EXPECT: internal/template、outcome、backoff、outbound ≥ 90%；dispatch、worker ≥ 80%
make check-secrets     # EXPECT: 通过
make cover-gaps        # EXPECT: 与 docs/testing/coverage-gaps.md 一致
```

### Manual Validation
- [ ] 本地启动 PostgreSQL 与 `serve --role=all`，配合 `spike/mock-vendor`，用 Phase 3 的 API 提交事件，观察 SUCCEEDED / RETRY_SCHEDULED / DEAD 三种结果
- [ ] 查询 API 返回的 attempt 记录中没有任何密钥

---

## Acceptance Criteria
- [ ] 三组黄金用例全部由引擎执行并通过（B1）
- [ ] `Dispatcher` 接口存在，worker 只依赖接口（M1）
- [ ] 所有状态变化经 `domain.Transition` 并以条件更新落库（M2）
- [ ] P1–P5 全部落实并有测试
- [ ] attempt 与状态更新在同一事务中写入
- [ ] 并发测试（B5）、脱敏测试（B9）通过

## Completion Checklist
- [ ] 遵循 coding-conventions.md
- [ ] SQL 全部参数化
- [ ] 超时、上限、间隔都是命名常量
- [ ] 日志与记录中无密钥
- [ ] 未修改其他阶段拥有的文件（`cmd/messagehub/serve.go` 只有一处）

## Risks
| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| 领取 SQL 复杂、性能不佳 | M | H | EXPLAIN 断言；1 万行数据集成测试；Phase 7 压测 |
| `max_in_flight` 软上限被大幅突破 | M | M | 测试验证上界；必要时改为按目标的 advisory lock |
| 模板引擎与黄金用例的细节分歧 | M | M | 规格 §7.1、§13.2 为准，分歧先改规格 |
| 有序 + 重试 + 接管的交互错误 | M | H | 有序集成测试覆盖 block / skip / 接管 |
| OAuth2 token 端点故障导致大量 infra 重试 | M | M | infra 重试同样使用退避；Phase 5 告警 |

## Notes
- 与 Phase 3 的接口只有数据库表。端到端测试直接插入事件与 delivery，不依赖 Phase 3 的代码
- 迁移到 MQ 时（ADR-0009），只需新增一个 `Dispatcher` 实现与中继；本阶段的 worker、模板、判定、记录全部复用
