# Plan: 受理与查询（Message Hub Phase 3）

## Summary
实现 `api` 角色：HTTP 服务、入站认证、受理（校验 → 去重 → 路由 → 同一事务写入 events / event_payloads / deliveries → NOTIFY）、事件与投递的查询（含游标分页、按角色限定范围、`blocked_by` 推导）、运维的 replay / skip（条件更新 + 审计），以及健康检查。所有响应符合 `api/openapi.yaml`，由契约测试逐一校验。

## User Story
As a 业务系统开发者（以及运营、客服、运维）,
I want 用统一格式提交事件并按权限查询投递状态、对失败的投递执行重放或跳过,
So that 业务系统无需自行实现调用、重试与排障，同时在投递失败时仍能做出业务处理。

## Problem → Solution
契约与运行底座已就绪，但没有对外服务 → `messagehub serve --role=api` 提供 `api/openapi.yaml` 中的全部 8 个端点，受理即持久化，查询按角色隔离。

## Metadata
- **Complexity**: Large
- **Source PRD**: `.claude/PRPs/prds/message-hub.prd.md`
- **PRD Phase**: Phase 3 — 受理与查询
- **Depends on**: Phase 2b 共享运行底座（`runtime-foundation.plan.md`）
- **Parallel with**: Phase 4（投递引擎），两者只通过数据库表交互
- **Estimated Files**: 约 30 个

---

## UX Design

### Before
无对外服务。

### After
```
业务系统  POST /v1/events  ──► 202 {event_id, duplicate:false, routing_status, matched_route_ids, deliveries[]}
                            └─► 200 {…, duplicate:true}（同一 source_event_id 再次提交）
运营/客服 GET  /v1/events?event_type=…&routing_status=unrouted&cursor=…
          GET  /v1/events/{id}            ──► 事件 + payload + deliveries（含 blocked_by）
          GET  /v1/deliveries/{id}        ──► delivery + attempts（分页）
运维      POST /v1/deliveries/{id}/replay {reason}  ──► 202 OperatorAction
          POST /v1/deliveries/{id}/skip   {reason}  ──► 200 OperatorAction
探针      GET  /healthz  /readyz
```

### Interaction Changes
| Touchpoint | Before | After | Notes |
|---|---|---|---|
| 提交通知 | 业务代码直接调用供应商 | `POST /v1/events` | `source` 由 API Key 推导 |
| 查询状态 | 无 | `GET /v1/events…`、`/v1/deliveries…` | source 角色只能看到自己的数据，越权返回 404 |
| 失败处置 | 无 | replay / skip | 只有 operator 角色可用，每次写审计 |

---

## Mandatory Reading

| Priority | File | Lines / Section | Why |
|---|---|---|---|
| P0 | `docs/design/domain-model.md` | §2、§3、§4、§5.2（含持久化要求）、§6、§9、§12、§13.1、§13.4 | 受理、路由、权限、API 行为的唯一权威 |
| P0 | `api/openapi.yaml` | all | 每个端点的请求 / 响应 schema、错误码、示例 |
| P0 | `db/migrations/00001_init.sql` | 9–170 | 表结构、约束、索引（列名必须逐一对应） |
| P0 | `internal/domain/event.go` | 84–117 | `ParseEnvelope`、`ErrPayloadTooLarge`、`RoutingStatusFor` |
| P0 | `internal/domain/delivery_state.go` | 26–46 | `Transition`：replay / skip 的合法性判断只能经过它 |
| P0 | `.claude/PRPs/plans/completed/runtime-foundation.plan.md`（实际接口与偏离见 `.claude/PRPs/reports/runtime-foundation-report.md`） | Task 4–9 | `app.Runtime`、`ConfigHolder`、`secrets.Resolver`、`apikey`、`rules`、`pgtest` 的接口 |
| P1 | `docs/adr/0009-pg-to-mq-migration-path.md` | M2、P3 | 条件更新；每个事务最多一次无 payload 的 NOTIFY |
| P1 | `docs/adr/0008-secrets-management.md` | D4 | 入站 key 的哈希匹配与双哈希 |
| P1 | `api/openapi_test.go` | all | 加载 spec 的方式；fixture 校验 |
| P2 | `docs/design/coding-conventions.md` | §3、§7、§8 | 错误映射、宽事件日志、测试约定 |

## External Documentation

| Topic | Source | Key Takeaway |
|---|---|---|
| Go 1.22+ ServeMux | https://pkg.go.dev/net/http#ServeMux | 支持 `"POST /v1/events"`、`"GET /v1/events/{event_id}"`；`r.PathValue("event_id")` |
| http.MaxBytesReader | https://pkg.go.dev/net/http#MaxBytesReader | 超限时读取返回 `*http.MaxBytesError` |
| kin-openapi openapi3filter | `github.com/getkin/kin-openapi/openapi3filter`（v0.149.0） | `ValidateRequest`、`ValidateResponse`；路由用 `routers/legacy.NewRouter(doc)`（不引入新依赖） |
| pgx 事务 | https://pkg.go.dev/github.com/jackc/pgx/v5 | `pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {…})` |
| pg_notify | PostgreSQL 文档 NOTIFY | 事务内调用 `SELECT pg_notify('hub_delivery_ready', '')`，提交时才发出 |

```
KEY_INSIGHT: INSERT … ON CONFLICT (source, source_event_id) DO NOTHING RETURNING 在冲突时不返回行
APPLIES_TO: Task 3
GOTCHA: 冲突时需要再查询原事件；并发重复提交由唯一约束保证只有一条成功

KEY_INSIGHT: openapi3filter 校验请求时会执行 security 校验
APPLIES_TO: Task 9
GOTCHA: 测试中设置 Options.AuthenticationFunc = openapi3filter.NoopAuthenticationFunc
```

---

## Patterns to Mirror

### ERROR_HANDLING
// SOURCE: internal/domain/event.go:84-117
```go
env, err := domain.ParseEnvelope(raw)
switch {
case domain.IsPayloadTooLarge(err):                       // → 413 payload_too_large
case errors.As(err, &verr /* *domain.ValidationError */): // → 400 validation_failed，details 来自 verr.Problems
}
```
// SOURCE: internal/domain/delivery_state.go:43-46
```go
if _, err := domain.Transition(from, domain.TriggerReplay); errors.Is(err, domain.ErrInvalidTransition) { /* → 409 invalid_state */ }
```

### NAMING / IMMUTABILITY
// SOURCE: internal/domain/delivery.go（类型化枚举）、internal/config/config.go:74-130（访问器返回副本）
查询结果使用值类型（`EventView`、`DeliveryView`）；不在 handler 中修改 store 返回的结构。

### TEST_STRUCTURE
// SOURCE: internal/domain/event_test.go（表驱动 + AAA）、db/migrations_integration_test.go（集成测试 tag + SQLSTATE 断言）

### LOGGING_PATTERN（宽事件）
// SOURCE: docs/design/coding-conventions.md §7
```go
log.Info("event.received", "event_id", id, "source", src, "event_type", et,
    "routing_status", rs, "delivery_count", n, "duplicate", dup, "config_hash", h)
log.Warn("event.unrouted", /* 同上字段 */)
```

---

## Files to Change

| File | Action | Justification |
|---|---|---|
| `internal/routing/routing.go` + `_test.go` | CREATE | 纯函数：按 §4 匹配 Route，返回去重后的 route id 与 destination id |
| `internal/store/store.go` | CREATE | `Store` 结构（持有 pool）、共享的扫描辅助函数、`ErrNotFound`、`ErrInvalidState` |
| `internal/store/ingest.go` | CREATE | 受理事务 |
| `internal/store/query.go` | CREATE | 事件、投递、attempt 的查询；`blocked_by` 推导 |
| `internal/store/operator.go` | CREATE | replay / skip |
| `internal/store/cursor.go` + `_test.go` | CREATE | 不透明游标编解码 |
| `internal/store/*_integration_test.go` | CREATE | 用 `pgtest` 的真实数据库测试 |
| `internal/api/run.go` | UPDATE | 替换底座占位：启动 HTTP 服务、优雅关闭 |
| `internal/api/server.go` | CREATE | 路由注册、超时常量 |
| `internal/api/middleware.go` | CREATE | request id、recover、访问日志、请求体上限 |
| `internal/api/auth.go` + `_test.go` | CREATE | Bearer key → Principal；按 ConfigHash 缓存哈希索引 |
| `internal/api/jsonstrict.go` + `_test.go` | CREATE | 拒绝重复 JSON 键（B3） |
| `internal/api/envelope.go` | CREATE | 统一响应信封与错误映射 |
| `internal/api/ingest.go` | CREATE | `POST /v1/events` |
| `internal/api/query.go` | CREATE | 4 个 GET 端点 |
| `internal/api/operator.go` | CREATE | replay / skip |
| `internal/api/health.go` | CREATE | `/healthz`、`/readyz` |
| `internal/api/*_test.go` | CREATE | handler 单元测试（假 store） |
| `internal/api/contract_integration_test.go` | CREATE | 真实数据库 + HTTP + openapi3filter 校验每个请求与响应（B2） |
| `docs/testing/coverage-gaps.md` | UPDATE | B2、B3 关闭；新增未覆盖条目 |

**不修改**：`cmd/**`、`internal/app/**`、`internal/dispatch/**`、`internal/worker/**`、`db/migrations/**`（如需新增索引，先与 Phase 4 协调，由一方以新迁移文件提交）。

## NOT Building

- 投递、领取、重试（Phase 4）
- 限流（Phase 6；OpenAPI 已预留 429）
- 管理配置的 API（ADR-0005）
- 终态回调 push、"重新路由" unrouted 事件（PRD Open Questions）
- 指标与追踪（Phase 5）

---

## Step-by-Step Tasks

### Task 1: 路由 `internal/routing`
- **ACTION**: `func Match(cfg *config.Config, env domain.Envelope, source string) (routeIDs, destinationIDs []string, err error)`
- **IMPLEMENT**:
  - 候选来自 `cfg.RoutesForEventType(env.EventType)`
  - `sources` 非空时要求包含 `source`
  - `match` 规则用 `rules.EvalAll`，规则路径以 `payload.` / `metadata.` 开头，因此传入的文档为 `map[string]any{"payload": <payload 解码结果>, "metadata": <metadata>}`；payload 用 `json.Decoder.UseNumber()` 解码
  - destination 按首次出现的顺序去重；route id 保持配置顺序
- **MIRROR**: IMMUTABILITY（纯函数）
- **GOTCHA**: payload 解码失败（理论上不会，已校验为对象）时返回错误而不是当作不匹配
- **VALIDATE**: 表驱动：无匹配、单 route、多 route 指向同一 destination（去重）、sources 过滤、match 过滤、metadata 规则

### Task 2: 游标 `internal/store/cursor.go`
- **ACTION**: 不透明游标 `base64url(JSON{"t": <RFC3339Nano>, "id": <uuid>})`
- **IMPLEMENT**: `EncodeCursor(t time.Time, id uuid.UUID) string`、`DecodeCursor(s string) (Cursor, error)`；列表按 `(time DESC, id DESC)` 排序，下一页条件为 `(received_at, id) < ($t, $id)`
- **GOTCHA**: 非法游标返回 `validation_failed`（字段 `cursor`），不得 500
- **VALIDATE**: 编解码往返；非法 base64、非法 JSON、缺字段

### Task 3: 受理事务 `internal/store/ingest.go`
- **ACTION**: `func (s *Store) Ingest(ctx context.Context, in IngestInput) (IngestResult, error)`
- **IMPLEMENT**（一个 `pgx.BeginFunc` 事务）：
  1. `INSERT INTO events (id, source, source_event_id, event_type, occurred_at, idempotency_key, ordering_key, metadata, routing_status, matched_route_ids, routing_config_hash) VALUES (…) ON CONFLICT (source, source_event_id) DO NOTHING RETURNING ingest_seq, received_at`
  2. 无返回行 → **重复**：回滚，查询原事件及其 deliveries，返回 `Duplicate: true`
  3. `INSERT INTO event_payloads (event_id, payload) VALUES (…)`
  4. 每个 destination 一条 `INSERT INTO deliveries (id, event_id, destination_id, policy_id, ordering_key, ingest_seq) …`（可用一条多行 INSERT）；`policy_id` 取 `cfg.Destination(id).Policy`
  5. deliveries 非空时执行一次 `SELECT pg_notify('hub_delivery_ready', '')`（ADR-0009 P3）
  - 可选字符串字段为空时写 NULL（数据库 CHECK 不允许空字符串）
  - `routing_status` 用 `domain.RoutingStatusFor(routeIDs)`
- **GOTCHA**:
  - 不能在事务外先查询再插入（并发竞争）；去重只依赖唯一约束
  - `ordering_key` 复制到 deliveries；`ingest_seq` 复制自 events 的返回值
- **VALIDATE**（集成测试）：新事件写入 1 + 1 + N 行；重复提交返回原 event_id；100 个并发 goroutine 同时提交同一 `source_event_id`，最终只有 1 个事件、恰好 1 个 `Duplicate:false`；unrouted 事件写入 0 条 delivery 且不发 NOTIFY（另开连接 LISTEN 验证）

### Task 4: 查询 `internal/store/query.go`
- **ACTION**: `GetEvent`、`ListEvents`、`GetDelivery`、`ListDeliveries`
- **IMPLEMENT**:
  - 所有查询接受 `Scope{Source string}`；Source 非空时追加 `AND e.source = $n`（对 deliveries 通过 JOIN events）。越权与不存在**都**返回 `ErrNotFound`
  - 过滤参数与 OpenAPI 一致：events 的 `source_event_id`、`event_type`、`ordering_key`、`routing_status`、`status`（`EXISTS (SELECT 1 FROM deliveries …)`）、`received_from`（含）/ `received_to`（不含）；deliveries 的 `event_id`、`destination_id`、`status`、`created_from` / `created_to`
  - `limit` 默认 50、最大 100（命名常量）；多取一行判断是否有下一页
  - **blocked_by**：仅对非终态、有 `ordering_key`、且其 policy 为 `per_key` 的 delivery 计算：
    ```sql
    SELECT p.id FROM deliveries p
    WHERE p.destination_id = $1 AND p.ordering_key = $2 AND p.ingest_seq < $3
      AND NOT (p.status = ANY($4))      -- 结清状态：SUCCEEDED、SKIPPED，以及 skip 模式下的 DEAD
    ORDER BY p.ingest_seq LIMIT 1
    ```
    结清状态集合由 `domain.DeliveryStatus.IsOrderingSettled` 推导（遍历 `domain.AllStatuses`），不在 SQL 中手写；策略从 `ConfigHolder.Current()` 取；策略已被删除时不计算（返回 null）
  - attempts 分页按 `(started_at DESC, id DESC)`
- **GOTCHA**: 列表查询必须命中已有索引（`events_received_at_idx`、`events_event_type_received_at_idx`、`deliveries_destination_status_created_at_idx`）；集成测试中对主要查询执行 `EXPLAIN` 断言没有对大表的 Seq Scan（造 1 万行数据，先 `ANALYZE`）
- **VALIDATE**: 集成测试覆盖每个过滤条件、分页边界（恰好 limit 条、limit+1 条）、scope 越权返回 ErrNotFound、blocked_by 在 block / skip 两种模式下的结果

### Task 5: 运维操作 `internal/store/operator.go`
- **ACTION**: `Replay(ctx, id, actor, reason)`、`Skip(ctx, id, actor, reason)`，返回 `OperatorAction`
- **IMPLEMENT**（一个事务）：
  1. `SELECT status FROM deliveries WHERE id = $1 FOR UPDATE`；不存在 → `ErrNotFound`
  2. `domain.Transition(status, TriggerReplay|TriggerSkip)`；非法 → `ErrInvalidState`（携带当前状态）
  3. 条件更新（ADR-0009 M2）：
     - Replay：`UPDATE deliveries SET status='PENDING', replay_count = replay_count + 1, attempt_count = 0, scheduled_at = now(), lease_owner = NULL, lease_expires_at = NULL, completed_at = NULL, updated_at = now() WHERE id = $1 AND status = $2`
     - Skip：`UPDATE … SET status='SKIPPED', completed_at = now(), updated_at = now() WHERE id = $1 AND status = 'DEAD'`
     - 更新到 0 行 → `ErrInvalidState`（并发变更）
  4. `INSERT INTO operator_actions (id, delivery_id, action, actor, reason, from_status)`
  5. 执行一次 `pg_notify('hub_delivery_ready', '')`（skip 可能解除后续 delivery 的阻塞）
- **VALIDATE**: 集成测试：DEAD → replay、SUCCEEDED → replay、SKIPPED → replay、DEAD → skip 成功；PENDING / IN_FLIGHT 上 replay、非 DEAD 上 skip 返回 ErrInvalidState；两个并发 replay 只有一个成功；审计行字段正确

### Task 6: 认证 `internal/api/auth.go`
- **ACTION**: 中间件：`Authorization: Bearer <key>` → `Principal{ID, Role}` 放入 context
- **IMPLEMENT**:
  - 索引：对 `cfg.Principals()`，用 `secrets.Resolver` 解析 `api_key_sha256_ref`，`apikey.ParseHashes` 得到 1–2 个哈希，构建 `[]principalHashes`
  - 索引按 `cfg.Hash()` 缓存在 `atomic.Pointer` 中；请求时若 `ConfigHolder.Current().Hash()` 与缓存不同则重建（配置热重载后立即生效）
  - 匹配：`apikey.Matches(key, hashes)`，对全部 principal 逐个常量时间比较（数量很少，不做 map 查找，避免时序差异）
  - 缺少或格式错误 → 401 `unauthenticated`；角色不允许 → 403 `forbidden`
- **GOTCHA**: 日志与错误中绝不出现 key；索引构建失败（变量缺失）时该 principal 不可用并记录 error 日志，不影响其他 principal
- **VALIDATE**: 单元测试：正确 key、轮换期间新旧 key 都通过、错误 key、缺失头、`Basic` 方案、配置热重载后新 key 生效

### Task 7: 严格 JSON 与请求体上限
- **ACTION**: `internal/api/jsonstrict.go`：`CheckNoDuplicateKeys(raw []byte) error`；中间件中的请求体上限
- **IMPLEMENT**:
  - 用 `json.Decoder.Token()` 遍历，为每层对象维护已见键集合；发现重复返回带 JSON 路径的错误 → 400 `validation_failed`（字段为重复键的路径）
  - 请求体上限 = `domain.MaxPayloadBytes` + 信封余量（命名常量 `maxIngestBodyBytes = 512 KiB`）；超限 → 413
  - 校验顺序：读取（上限）→ 重复键 → `domain.ParseEnvelope`
- **VALIDATE**: 单元测试：顶层重复、payload 内部嵌套重复、数组中对象重复、无重复的大文档；超限请求返回 413（domain-model.md §13.1 / coverage-gaps B3）

### Task 8: HTTP 层 `internal/api`
- **ACTION**: 服务、信封、各 handler、`run.go`
- **IMPLEMENT**:
  - `server.go`：`http.Server{ReadHeaderTimeout: 5s, ReadTimeout: 10s, WriteTimeout: 15s, IdleTimeout: 60s}`（命名常量）；ServeMux 注册 8 个端点；未知路径 404 信封
  - `envelope.go`：`writeData(w, status, data, meta)`、`writeError(w, status, code, msg, details)`；错误码与 HTTP 状态对应 domain-model.md §12；500 时只返回通用信息，详细错误进日志
  - `ingest.go`：认证（role=source）→ 读取 → 严格 JSON → `ParseEnvelope` → `routing.Match` → `store.Ingest` → 202 / 200；记录 `event.received` / `event.unrouted`（warn）/ `event.duplicate`（info）
  - `query.go`：参数解析与校验（UUID、时间、limit、枚举）→ store → 响应；source 角色传 `Scope{Source: principal.ID}`
  - `operator.go`：role=operator；请求体 `{reason}`（1–500 字符）；`ErrInvalidState` → 409，`details` 中给出当前状态
  - `health.go`：`/healthz` 恒 200；`/readyz` 在 `pool.Ping` 失败或配置未加载时 503（code `internal`，§13.4）
  - `run.go`：`api.Run(ctx, rt)` 监听 `rt.Settings.HTTPAddr`；ctx 取消时 `Shutdown`
  - 访问日志：method、路由模式、状态码、耗时、principal id、request id；不记录请求体
- **MIRROR**: ERROR_HANDLING、LOGGING_PATTERN
- **GOTCHA**: store 接口在 api 包内以小接口定义（`ingestStore`、`queryStore`、`operatorStore`），便于假实现做单元测试；`*store.Store` 满足它们
- **VALIDATE**: handler 单元测试覆盖每个错误分支（400 / 401 / 403 / 404 / 409 / 413 / 500）

### Task 9: 契约与端到端测试（B2）
- **ACTION**: `internal/api/contract_integration_test.go`（`//go:build integration`）
- **IMPLEMENT**:
  - `pgtest.Start` + 示例配置（测试中设置所需环境变量，key 用 `apikey.Generate` 现场生成）+ `httptest.NewServer`
  - 包装 `http.Client`：每个请求发送前用 `openapi3filter.ValidateRequest`、收到后用 `ValidateResponse` 校验（`routers/legacy.NewRouter(doc)`；`AuthenticationFunc: openapi3filter.NoopAuthenticationFunc`）
  - 场景：受理 routed / unrouted / duplicate；各角色查询；越权 404；replay、skip 的成功与 409；分页遍历全部结果；`/readyz`
  - 需要 DEAD / SUCCEEDED 的 delivery 时，测试直接用 SQL 更新状态（Phase 4 尚未提供投递）
- **GOTCHA**: 校验失败时测试输出 OpenAPI 路径与具体字段，便于定位是实现还是契约的问题；以 `docs/design/domain-model.md` 为准修正
- **VALIDATE**: `make test-integration`

### Task 10: 文档
- **ACTION**: 更新 `docs/testing/coverage-gaps.md`
- **IMPLEMENT**: B2、B3 标记完成（指向测试名）；`make cover-gaps` 的新条目写入第 2、3 节
- **VALIDATE**: 文档第 2 节与 `make cover-gaps` 输出一致

---

## Testing Strategy

### Unit Tests
| Test | Input | Expected Output | Edge Case? |
|---|---|---|---|
| routing.Match | 多 route 指向同一 destination | destination 去重 | 是 |
| routing.Match | match 规则引用 metadata | 正确匹配 | 是 |
| cursor | 非法游标 | validation_failed | 是 |
| jsonstrict | 嵌套重复键 | 400，字段为路径 | 是 |
| auth | 双哈希轮换 | 新旧 key 都通过 | 是 |
| auth | 配置热重载 | 使用新索引 | 是 |
| ingest handler | 413 / 400 / 403 | 对应错误信封 | 是 |
| operator handler | 非 operator | 403 | 是 |
| query handler | 非法 UUID、limit=101 | 400 | 是 |

### Integration Tests
| Test | Expected |
|---|---|
| 并发重复受理（100 并发） | 只有 1 个事件 |
| unrouted | 0 条 delivery，无 NOTIFY |
| NOTIFY | routed 受理与 replay / skip 后各收到一次通知 |
| 查询过滤与分页 | 与数据一致；主要查询无大表 Seq Scan |
| blocked_by | block / skip 两种模式正确 |
| 并发 replay | 只有一个成功 |
| 契约 | 所有请求与响应通过 openapi3filter 校验 |

### Edge Cases Checklist
- [x] Empty input（空请求体、`{}` payload）
- [x] Maximum size input（payload 恰好 256 KiB、请求体超过 512 KiB）
- [x] Invalid types（非法 UUID、时间、枚举）
- [x] Concurrent access（并发重复受理、并发 replay）
- [x] Network failure（数据库不可达：`/readyz` 503，受理 500）
- [x] Permission denied（401、403、跨来源 404）

---

## Validation Commands

```bash
make lint              # EXPECT: 无输出
make test              # EXPECT: 全部通过
make test-integration  # EXPECT: 全部通过（含契约测试）
make cover             # EXPECT: internal/api、internal/store、internal/routing ≥ 80%
make check-secrets     # EXPECT: 通过
make cover-gaps        # EXPECT: 与 docs/testing/coverage-gaps.md 一致
```

### Manual Validation
- [ ] 本地启动 PostgreSQL，`bin/messagehub migrate up`，`serve --role=api`，用 curl 走一遍 routed、unrouted、duplicate、查询、replay
- [ ] 确认访问日志与 `event.received` 中没有 API Key 或 payload 原文

---

## Acceptance Criteria
- [ ] 8 个端点全部实现，契约测试通过
- [ ] 受理即持久化：受理成功返回后，事件与全部 delivery 都已提交
- [ ] source 角色越权查询返回 404
- [ ] replay / skip 只经 `domain.Transition` 判断并以条件更新落库，每次写审计
- [ ] coverage-gaps 的 B2、B3 关闭

## Completion Checklist
- [ ] 遵循 coding-conventions.md（错误、日志、不可变、测试）
- [ ] SQL 全部参数化，无字符串拼接用户输入
- [ ] 魔法数字为命名常量（超时、limit、body 上限）
- [ ] 未修改其他阶段拥有的文件

## Risks
| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| OpenAPI 与实现不一致 | M | M | 契约测试覆盖每个端点 |
| 列表查询性能 | M | M | EXPLAIN 断言；必要时与 Phase 4 协调新增索引迁移 |
| 认证索引在热重载时出现短暂不一致 | L | M | 按 ConfigHash 原子替换；请求只读取一次快照 |
| 并发受理的唯一约束冲突处理错误 | L | H | 100 并发集成测试 |

## Notes
- 与 Phase 4 的接口只有数据库表：Phase 3 写入 PENDING 的 delivery 并 NOTIFY；Phase 4 领取并更新状态、写 attempt。Phase 3 的查询读取 Phase 4 写入的 attempts
- 安全规则要求所有端点限流，本阶段未实现，按 PRD 放在 Phase 6（OpenAPI 已预留 429）
