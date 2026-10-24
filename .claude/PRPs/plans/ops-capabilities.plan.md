# Plan: 运维能力（Message Hub Phase 5）

## Summary
让失败可感知、可处置、可追溯：OpenTelemetry 指标与追踪（OTLP 导出 + Prometheus `/metrics`）、告警规则与本地可观测性栈、批量重放 / 跳过 API、受理与投递对账任务、数据保留（payload / attempts 分区 / 事件）与分区维护；顺带关闭 Phase 4 遗留的 B13（`created_at` 索引）、D8（子进程启动完整 `serve`）。

## User Story
As a Message Hub 的值班运维,
I want 死信、积压、阻塞与漏投在发生时就收到告警，并能按 event_id 追溯全过程、按条件批量重放,
So that 任何已受理的事件都不会无人知晓地失败。

## Problem → Solution
目前只有宽事件日志与单条 replay / skip；`dispatch.unknown_destinations` 等问题只能翻日志；attempts 全在 DEFAULT 分区、payload 永不清理 → 指标 + 追踪 + 告警 + 对账 + 批量运维 + 保留与分区任务。

## Metadata
- **Complexity**: XL（建议按"实施分组"分三批提交）
- **Source PRD**: `.claude/PRPs/prds/message-hub.prd.md`
- **PRD Phase**: Phase 5 — 运维能力
- **Depends on**: Phase 3、Phase 4（已合并）
- **Parallel with**: Phase 6（文件归属见"与 Phase 6 的并行约定"）
- **Estimated Files**: 约 40 个

### 用户已确认的决定（2026-10-24）
- **可观测性导出**：代码只依赖 OpenTelemetry SDK，通过 OTLP 导出（标准 `OTEL_*` 环境变量配置 endpoint）；另提供 Prometheus `/metrics` 端点；本地用 docker compose 起 OTel Collector + Prometheus + Grafana + Tempo 验证，不绑定具体平台
- **数据保留**：保留期为进程参数，默认 payload 30 天、attempts 90 天（按月分区整块删除）、events / deliveries 180 天；正式值上线前再定

### 实施分组
| 组 | 任务 | 说明 |
|---|---|---|
| A 可观测性 | Task 1–5 | 遥测底座、受理与投递埋点、追踪贯通、DB 快照指标、告警与本地栈 |
| B 运维操作 | Task 6–7 | 批量 replay / skip、对账 |
| C 数据维护与遗留 | Task 8–12 | 迁移 00003 / 00004、保留与分区任务、维护调度、D8、文档 |

---

## UX Design

### Before
```
值班：翻 JSON 日志找 delivery.attempted / dispatch.unknown_destinations
处置：逐条 POST /v1/deliveries/{id}/replay
追溯：GET /v1/events/{id} + 逐个 GET /v1/deliveries/{id}
```

### After
```
Grafana 看板：受理量、投递结果、首次投递延迟 P99、各目标积压 / 在途 / 阻塞
告警：死信产生、积压超时、infra 错误持续、对账不一致、未配置目标仍有待投递
处置：POST /v1/deliveries/replay  {filter, reason, dry_run}  → 批量、逐条审计
追溯：Tempo 中按 event_id / trace_id 看到 受理 → 每次 attempt 的完整链路
```

### Interaction Changes
| Touchpoint | Before | After | Notes |
|---|---|---|---|
| `/metrics`（新端口 `HUB_METRICS_ADDR`） | 无 | Prometheus 文本格式 | 无需认证，只监听内网；api 与 worker 角色都暴露 |
| `POST /v1/deliveries/replay`、`/skip`（批量） | 无 | operator 角色，按条件或 id 列表，支持 `dry_run` | 每条仍写一条 OperatorAction |
| 日志 | 无 trace_id | `event.received`、`delivery.attempted`、`http.request` 带 `trace_id` | 日志与追踪互相跳转 |
| 数据库 | attempts 只有 DEFAULT 分区 | 按月分区，过期分区整块删除 | 见 Task 9 GOTCHA |

---

## Mandatory Reading

| Priority | File | Lines / Section | Why |
|---|---|---|---|
| P0 | `docs/design/domain-model.md` | §3、§5、§6、§10、§12、§13.4、§13.6 | 受理、状态机、有序、记录、API 约定、OperatorAction 不级联 |
| P0 | `docs/design/coding-conventions.md` | §1、§3、§6、§7、§8 | 依赖方向、错误、context、宽事件日志、测试 |
| P0 | `docs/testing/coverage-gaps.md` | §2–§4（B13、B14、B17、B19、B20、D8、D11） | 本阶段要关闭或更新的条目 |
| P0 | `internal/store/operator.go` | all | 单条 replay / skip：锁行 → `domain.Transition` → 条件更新 → 审计 → NOTIFY |
| P0 | `internal/api/server.go`、`operator.go`、`middleware.go` | all | 路由注册、角色、请求体解析、访问日志 |
| P0 | `internal/worker/run.go`、`process.go` | `Run`、`complete` | 埋点接入点（见并行约定） |
| P0 | `db/migrations/00001_init.sql` | 112–150 | attempts 分区表定义、FK 级联说明 |
| P1 | `internal/app/settings.go` | all | 进程参数的读取、聚合校验、`String()` 脱敏 |
| P1 | `cmd/messagehub/serve.go` | `roles`、`runServe`、`buildRuntime` | 遥测初始化与关闭的接入点 |
| P1 | `internal/store/query.go` | `DeliveryFilter`、`where` | 批量操作复用的过滤条件 |
| P1 | `internal/api/contract_integration_test.go` | all | OpenAPI 契约测试，批量端点要纳入 |
| P1 | `docs/adr/0009-pg-to-mq-migration-path.md` | 触发迁移的条件 | 指标要能度量这些阈值 |
| P2 | `docs/adr/0010-max-in-flight-soft-limit.md` | Revisit When | 在途数指标为日后评估提供数据 |

## External Documentation

| Topic | Source | Key Takeaway |
|---|---|---|
| OTel Go SDK | https://opentelemetry.io/docs/languages/go/ | `sdktrace.NewTracerProvider` / `sdkmetric.NewMeterProvider`；进程退出前 `Shutdown(ctx)` 刷新 |
| OTLP 导出器配置 | https://opentelemetry.io/docs/specs/otel/protocol/exporter/ | 由 `OTEL_EXPORTER_OTLP_ENDPOINT`、`OTEL_EXPORTER_OTLP_PROTOCOL` 等标准变量配置；未设置 endpoint 时不启用 OTLP |
| OTel Prometheus 导出器 | `go.opentelemetry.io/otel/exporters/prometheus` | 作为 MeterProvider 的 Reader，配合 `promhttp.Handler()` |
| W3C Trace Context | https://www.w3.org/TR/trace-context/ | `traceparent` 格式；用 `propagation.TraceContext{}` 序列化 |
| PostgreSQL 分区 | https://www.postgresql.org/docs/16/ddl-partitioning.html | DEFAULT 分区中已有某范围的行时，不能再 `CREATE TABLE … PARTITION OF … FOR VALUES` 该范围 |

新增依赖（实施时用 `go list -m -json <mod>@<ver>` 确认**发布至少两周**，在报告中写明版本与日期）：

| Module | 用途 |
|---|---|
| `go.opentelemetry.io/otel`、`otel/sdk`、`otel/sdk/metric`（与现有间接依赖 v1.46.0 对齐） | API 与 SDK |
| `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp`、`otlpmetric/otlpmetrichttp` | OTLP/HTTP 导出（避免引入 gRPC 依赖树） |
| `go.opentelemetry.io/otel/exporters/prometheus` + `github.com/prometheus/client_golang` | `/metrics` |

```
KEY_INSIGHT: 指标标签只用配置中有界的值（destination_id、source、event_type、outcome、status）
APPLIES_TO: Task 2、3、5
GOTCHA: 不得把 event_id、delivery_id、ordering_key、URL 放进标签（基数爆炸）；它们只进 span 属性与日志

KEY_INSIGHT: "当前积压 / 在途 / 阻塞"这类状态量来自数据库快照，不靠进程内计数
APPLIES_TO: Task 4
GOTCHA: 多个进程同时采集会重复；快照任务与其他维护任务一样用 advisory lock 保证同一时刻只有一个进程执行，其余进程不上报该组指标

KEY_INSIGHT: 维护任务（快照、对账、保留、分区）在 worker 角色中运行，用 pg_try_advisory_lock 选出执行者
APPLIES_TO: Task 10
GOTCHA: advisory lock 是会话级的，必须在同一条连接上加锁与解锁，不能经过 transaction 模式的 pgbouncer —— 与 LISTEN 一样使用 `HUB_DATABASE_LISTEN_URL` 的直连
```

---

## Patterns to Mirror

### SETTINGS（进程参数）
// SOURCE: internal/app/settings.go `LoadSettings`
```go
timeout, err := parseShutdownTimeout(get(EnvShutdownTimeout, DefaultShutdownTimeout.String()))
if err != nil {
	add(EnvShutdownTimeout, err.Error())
}
s.ShutdownTimeout = timeout
```
所有问题聚合为 `*domain.ValidationError`，字段名为环境变量名；新增参数同时加入 `Settings.String()`。

### OPERATOR_ACTION（单条运维操作，批量复用）
// SOURCE: internal/store/operator.go `operate`
```go
from, err := lockStatus(ctx, tx, in.DeliveryID)
to, err := domain.Transition(from, trigger)
tag, err := tx.Exec(ctx, updateSQL, in.DeliveryID, string(from), string(to))
action, err = insertAction(ctx, tx, in, kind, from, to)
return notifyReady(ctx, tx)
```

### ROUTES / ROLES
// SOURCE: internal/api/server.go `newHandler`
```go
mux.HandleFunc("POST /v1/deliveries/{delivery_id}/replay", s.auth.require(rolesOperator, s.replay))
```

### WIDE_EVENT_LOG
// SOURCE: internal/api/operator.go `operate`、internal/worker/process.go `complete`
```go
s.log.Info("delivery.operator_action",
	slog.String("request_id", requestID(r.Context())),
	slog.String("action", string(action.Action)), …)
```

### DB_INTEGRATION_TEST
// SOURCE: internal/store/main_integration_test.go（`pgtest.StartShared`、`pgtest.Reset`）、internal/dispatch/pg_integration_test.go（`seedSQL` 批量造数、`explain` 断言无 Seq Scan）

### ROLE_RUN
// SOURCE: cmd/messagehub/serve.go `roles`、`runRoles`；internal/worker/run.go `Run`（每个角色 `Run(ctx, rt) error`，ctx 取消后在 `ShutdownTimeout` 内返回）

---

## 与 Phase 6 的并行约定

| 区域 | Phase 5 | Phase 6 |
|---|---|---|
| 新包 | `internal/telemetry`、`internal/maintenance` | `internal/ratelimit` |
| `internal/worker/process.go` | 只改 `complete()`：调用 `w.observer.Attempted(...)`；`attempt()` 开头创建 span | 改 `attempt()` 中发送前的检查与 `decide()` |
| `internal/worker/run.go` | `Worker` 增加 `observer` 字段与 `Run` 中的接线；用装饰器包装 Dispatcher 计时 | 增加限流 / 熔断相关字段与接线 |
| `internal/dispatch/**` | **不修改** | 修改领取 SQL 与 Complete（限流、熔断） |
| `internal/config/**`、`schemas/` | **不修改** | 新增配置字段 |
| `internal/store/**`、`internal/api/**`、`api/openapi.yaml` | 批量操作、ingest 写 traceparent、埋点 | 查询响应中的 payload 字段级脱敏（`views.go`） |
| 迁移编号 | `00003`、`00004` | `00005`、`00006` |
| 底座文件 | `internal/app/settings.go`、`internal/app/runtime.go`、`cmd/messagehub/serve.go`：**单独提交**并告知用户 | 不修改 |

合并顺序：先合并的阶段不受影响；后合并的阶段 rebase 后只会在上表列出的函数上产生冲突。goose 默认不允许乱序迁移：若 Phase 6 先合并，开发库需重建（生产尚未部署）。

---

## Files to Change

| File | Action | Justification |
|---|---|---|
| `internal/telemetry/{telemetry,metrics,trace}.go` + tests | CREATE | Provider 初始化与关闭、指标定义、traceparent 编解码 |
| `internal/app/settings.go` + test | UPDATE（底座，单独提交） | `HUB_METRICS_ADDR`、`HUB_RETENTION_*`、`HUB_MAINTENANCE_INTERVAL` |
| `internal/app/runtime.go` | UPDATE（底座，单独提交） | `Runtime.Telemetry` |
| `cmd/messagehub/serve.go` | UPDATE（底座，单独提交） | 初始化遥测、`/metrics` 监听、退出时刷新；worker / all 角色加入 `maintenance.Run` |
| `internal/api/{middleware,ingest}.go` | UPDATE | 请求 span、受理指标、`trace_id` 日志字段 |
| `internal/api/operator_batch.go` + test | CREATE | 批量 replay / skip |
| `internal/api/server.go` | UPDATE | 注册批量路由 |
| `api/openapi.yaml` + `api/openapi_test.go` | UPDATE | 批量端点契约 |
| `internal/store/ingest.go` | UPDATE | 写入 `events.traceparent` |
| `internal/store/operator_batch.go` + integration test | CREATE | 批量选择与分块执行 |
| `internal/worker/{run,process,observe}.go` + tests | UPDATE / CREATE | 见并行约定 |
| `internal/worker/events.go` | UPDATE | 读取 `traceparent` |
| `internal/maintenance/{run,lock,snapshot,reconcile,retention,partitions}.go` + tests | CREATE | 维护任务 |
| `db/migrations/00003_deliveries_created_at_idx.sql` | CREATE | B13 |
| `db/migrations/00004_events_traceparent.sql` | CREATE | 追踪贯通 |
| `deploy/observability/{docker-compose.yaml,otel-collector.yaml,prometheus.yaml,alerts.yaml,grafana/…}` | CREATE | 本地栈、告警规则、看板 |
| `cmd/messagehub/serve_integration_test.go` | CREATE | D8：子进程启动 `serve` 并发送 SIGTERM / SIGHUP |
| `docs/design/domain-model.md`、`docs/design/observability.md`、`docs/testing/coverage-gaps.md`、`docs/deploy/runbook.md` | UPDATE / CREATE | 规格、指标目录、缺口、值班手册 |

## NOT Building

- 自动修复（对账只报告，不自动重建 delivery 或重放）
- unrouted 事件的"重新路由"（PRD Open Question 未决）
- 向供应商请求注入 `traceparent` 头（对端未必接受，留作按目标的可选项）
- 告警通知渠道（Alertmanager 路由、IM / 电话），只交付规则文件
- 限流、熔断、字段级脱敏（Phase 6）
- 归档到冷存储（只删除过期数据）

---

## Step-by-Step Tasks

### Task 1: 遥测底座 `internal/telemetry`
- **ACTION**: `func Setup(ctx, s app.Settings, role string) (*Telemetry, error)`；`func (t *Telemetry) Shutdown(ctx) error`；`Telemetry{Meter metric.Meter; Tracer trace.Tracer; MetricsHandler http.Handler}`
- **IMPLEMENT**:
  - Resource：`service.name=messagehub`、`service.instance.id = Settings.WorkerID`、`messagehub.role`
  - Metrics：Prometheus Reader 恒启用；`OTEL_EXPORTER_OTLP_ENDPOINT`（或 `_METRICS_ENDPOINT`）存在时追加 OTLP/HTTP PeriodicReader
  - Traces：endpoint 存在时 BatchSpanProcessor + OTLP/HTTP；否则 no-op TracerProvider（不采集也不报错）
  - propagator 为 `propagation.TraceContext{}`
  - `serve.go`：`buildRuntime` 之后初始化；`HUB_METRICS_ADDR`（默认 `:9464`）起独立 `http.Server`（沿用 api 的超时常量模式）；`runServe` 返回前 `Shutdown`（受 `ShutdownTimeout` 约束）
- **MIRROR**: SETTINGS、ROLE_RUN
- **GOTCHA**: 测试用 `sdkmetric.NewManualReader` 与 `tracetest.NewInMemoryExporter`，不依赖网络；OTLP 导出失败只记日志，不影响投递
- **VALIDATE**: 单元测试：无 endpoint 时只有 Prometheus；有 endpoint（httptest 接收 OTLP）时两者都有；`Shutdown` 刷新数据

### Task 2: 受理埋点 `internal/api`
- **ACTION**: 中间件 span + 指标；ingest 指标
- **IMPLEMENT**:
  - `withObservability` 为每个请求开 span（名称为匹配的路由，如 `POST /v1/events`），属性 `http.response.status_code`、`messagehub.principal_id`；`http.request` 访问日志加 `trace_id`
  - 指标：`hub_http_requests_total{route,status}`、`hub_http_request_duration_seconds{route}`、`hub_events_received_total{source,event_type,routing_status,duplicate}`
  - 新事件受理时把当前 span 的 traceparent 放入 `store.IngestInput.TraceParent`
- **MIRROR**: WIDE_EVENT_LOG
- **GOTCHA**: route 标签用 mux 匹配的模式（`r.Pattern`），不用原始路径；未匹配统一记为 `route="unmatched"`
- **VALIDATE**: 现有 api 测试全部通过；新增测试用 ManualReader 断言计数与标签

### Task 3: 投递埋点 `internal/worker`
- **ACTION**: `observe.go` 定义 `observer` 接口与 OTel 实现；`run.go` 用 `instrumentedDispatcher` 装饰器包装 `Dispatcher`
- **IMPLEMENT**:
  - `hub_delivery_attempts_total{destination_id,outcome,attempt_trigger}`、`hub_delivery_attempt_duration_seconds{destination_id,outcome}`
  - `hub_delivery_first_attempt_latency_seconds{destination_id}`：`attempt_count == 0 && replay_count == 0` 的首次 attempt，`started_at − received_at`（PRD：P99 < 1s）
  - `hub_deliveries_completed_total{destination_id,status}`（进入 SUCCEEDED / DEAD）
  - `hub_dispatch_claim_duration_seconds`、`hub_dispatch_claimed_total{reclaimed}`、`hub_delivery_lease_lost_total`、`hub_delivery_complete_failed_total`
  - 每次 attempt 一个 span（`delivery.attempt`），父上下文取自事件的 traceparent；属性 `messagehub.event_id`、`delivery_id`、`destination_id`、`attempt`、`outcome`、`http.response.status_code`；`delivery.attempted` 日志加 `trace_id`
- **MIRROR**: 并行约定中 Phase 5 的改动范围
- **GOTCHA**: `received_at` 已在 `template.Event.ReceivedAt` 中，经 `try` 传给 observer，不另查库；span 中不得出现 URL、Header、Body
- **VALIDATE**: `process_test.go` / `run_test.go` 注入假 observer 断言调用；e2e 集成测试断言指标值

### Task 4: 数据库快照指标 `internal/maintenance/snapshot.go`
- **ACTION**: 每 `HUB_MAINTENANCE_INTERVAL`（默认 30s）由持锁进程执行一次，结果写入 OTel 异步 Gauge
- **IMPLEMENT**:
  - `hub_deliveries{destination_id,status}`（非终态各状态行数）
  - `hub_deliveries_due_age_seconds{destination_id}`：最早到期未领取的 delivery 已等待时长
  - `hub_deliveries_blocked{destination_id}`：有序阻塞数（判定同 store `blockedBy`，按目标聚合）
  - `hub_deliveries_unknown_destination{status}`：目标已不在配置中的待投递数（B19）与 IN_FLIGHT 数（B20-4）
- **GOTCHA**: 查询都要 EXPLAIN 断言走索引（`deliveries_claimable_destination_idx`、`deliveries_in_flight_destination_idx`、`deliveries_ordering_idx`）
- **VALIDATE**: 集成测试造数后断言 Gauge；EXPLAIN 断言

### Task 5: 告警规则、看板与本地栈 `deploy/observability`
- **ACTION**: docker compose（otel-collector、prometheus、tempo、grafana，镜像版本固定）、`alerts.yaml`、Grafana 看板 JSON
- **IMPLEMENT**（阈值写在文件头注释中，Phase 7 校准）：
  | 告警 | 表达式要点 |
  |---|---|
  | DeliveryDead | `increase(hub_deliveries_completed_total{status="DEAD"}[5m]) > 0` |
  | DeliveryBacklog | `hub_deliveries_due_age_seconds > 60` 持续 5m |
  | InfraErrorsPersisting | infra_error 占比 > 50% 持续 10m（B17） |
  | OrderingBlocked | `hub_deliveries_blocked > 0` 持续 15m（B14） |
  | UnknownDestinationDeliveries | `hub_deliveries_unknown_destination > 0`（B19、B20-4） |
  | ReconcileMismatch | `hub_reconcile_findings > 0`（Task 7） |
  | FirstAttemptLatencyHigh | P99 > 1s 持续 10m |
  | LeaseLost / CompleteFailed | 速率 > 0 持续 10m |
- **GOTCHA**: 在容器内执行 `promtool check rules` 校验规则文件，不新增本机依赖
- **VALIDATE**: `docker compose up` 后手工验证：提交事件 → Grafana 可见、Tempo 按 trace_id 查到受理与 attempt；人为制造 DEAD 后告警 firing

### Task 6: 批量 replay / skip
- **ACTION**: `POST /v1/deliveries/replay`、`POST /v1/deliveries/skip`（operator）
- **IMPLEMENT**:
  - 请求体：`{ "reason": "...", "dry_run": false, "limit": 100, "filter": { "delivery_ids": [...] | "event_ids": [...] | "destination_id", "status", "created_from", "created_to" } }`；id 列表与其余条件互斥；`limit` 1–1000（命名常量），默认 100
  - replay 只作用于终态（DEAD / SUCCEEDED / SKIPPED），skip 只作用于 DEAD；不符合的行计入 `skipped`
  - 按 `(created_at, id)` 选出 id，每 `batchChunkSize = 100` 条一个事务，逐条复用 `operate` 的锁行 → Transition → 条件更新 → 审计；**每个事务**最多一次 NOTIFY
  - 响应 `202`：`{ matched, applied, skipped, dry_run, results: [{delivery_id, from_status, to_status | error}] }`；`dry_run` 不写库
  - 日志：一条 `delivery.operator_batch`（filter 摘要、计数、actor）；每条变更仍写 OperatorAction
- **MIRROR**: OPERATOR_ACTION、ROUTES / ROLES、`parseReason` 的严格请求体校验
- **GOTCHA**: 空 filter 拒绝（防止误操作全表）；有序目标的批量 skip 会一次解除多个阻塞，响应按 destination 汇总；分块之间状态可能变化，条件更新保证不重复
- **VALIDATE**: store 集成测试（分块边界、与单条 replay 并发不重复、dry_run 不写库）；api 单元测试（校验、403）；契约测试纳入两个端点

### Task 7: 对账 `internal/maintenance/reconcile.go`
- **ACTION**: 每 5 分钟（命名常量）由持锁进程执行；只报告不修复
- **IMPLEMENT**（计数写入 `hub_reconcile_findings{check}`，warn 日志最多带 20 个样例 id）：
  1. `routed_without_deliveries`：`routing_status='routed'` 却没有任何 delivery 的事件
  2. `stuck_in_flight`：`lease_expires_at < now() − 宽限期` 仍为 IN_FLIGHT（未被接管，如目标已删除）
  3. `stale_due`：到期超过 `staleDueAfter`（默认 10 分钟）仍未领取
  4. `missing_payload`：非终态 delivery 的事件缺少 payload（保留任务误删的防线）
- **GOTCHA**: 第 1 项按 `received_at` 窗口增量扫描，起点为"上次上界 − 重叠量"以覆盖提交可见性延迟（同 B15）
- **VALIDATE**: 集成测试为每项造一个违例并断言；EXPLAIN 断言

### Task 8: 迁移 00003 / 00004
- **ACTION**: `00003_deliveries_created_at_idx.sql`：`deliveries (created_at DESC)`（B13）；`00004_events_traceparent.sql`：`events.traceparent text NULL`，`CHECK (traceparent ~ '^[0-9a-f]{2}-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$')`
- **GOTCHA**: goose 事务内不能 `CREATE INDEX CONCURRENTLY`；生产尚无数据，文件头注释写明有数据后的上线做法
- **VALIDATE**: `db` 迁移测试 up / down；store 的 `TestListQueriesUseIndexes` 加入不带过滤、按 destination 过滤的两条查询

### Task 9: 保留与分区 `internal/maintenance/{retention,partitions}.go`
- **ACTION**: 分区维护与三类数据清理
- **IMPLEMENT**:
  - 参数：`HUB_RETENTION_PAYLOAD`（720h）、`HUB_RETENTION_ATTEMPTS`（2160h）、`HUB_RETENTION_EVENTS`（4320h）；校验三者 > 0 且 events ≥ attempts
  - 分区：保证存在下个月起 `partitionsAhead = 2` 个月的月分区 `attempts_YYYY_MM`；超过保留期的整月分区 `DETACH` 后 `DROP`；DEFAULT 分区中超期的行分批删除
  - payload：`received_at` 早于保留期且所有 delivery 都终态的事件，分批（`retentionBatchSize = 1000`）删除 `event_payloads`
  - events：早于保留期且全部终态的事件，先删其 `operator_actions`（§13.6 不级联），再删事件（级联 deliveries / attempts / payload）
  - 每类每轮最多删除 `maxDeletesPerRun` 行；耗时与删除数写入指标与 `maintenance.retention` 日志
- **GOTCHA**:
  - DEFAULT 分区已有当月数据时不能创建当月分区，所以只创建未来月份；当月数据留在 DEFAULT，按行删除
  - 未结清 delivery 的 payload / 事件绝不删除（零静默丢失）；payload 被删的事件 replay 会 render_error（`pgEvents` 读为 `{}`），runbook 中说明
  - 删除事件会级联到 attempts 各分区，代价高：先由分区 DROP 清掉旧 attempts，再删事件
- **VALIDATE**: 集成测试回拨 `received_at` / `started_at` 造旧数据，断言只删应删的；分区创建幂等；EXPLAIN 断言

### Task 10: 维护调度 `internal/maintenance/run.go`、`lock.go`
- **ACTION**: `func Run(ctx, rt *app.Runtime) error`，在 `worker` 与 `all` 角色中与 worker 并行运行（`serve.go` 的 `roles` 映射加入）
- **IMPLEMENT**: 每个任务（snapshot、reconcile、partitions、retention）独立间隔；执行前在直连连接上 `pg_try_advisory_lock(<任务常量>)`，拿不到就跳过本轮；每个任务输出一条 `maintenance.<job>` 宽事件日志（耗时、结果、是否持锁）
- **GOTCHA**: 锁 key 为命名常量并登记在 `docs/design/observability.md`；连接断开时锁自动释放；任务失败只记录、不退出进程
- **VALIDATE**: 集成测试：两个 `Run` 并发，每个任务同一时刻只有一个执行；ctx 取消后及时返回

### Task 11: 完整进程测试（D8）
- **ACTION**: `cmd/messagehub/serve_integration_test.go`
- **IMPLEMENT**: `go build` 到 `t.TempDir()`，子进程运行 `serve --role=all`（pgtest + httptest 供应商）：受理并投递成功；SIGHUP 后新配置生效；SIGTERM 后在超时内以退出码 0 结束；`/metrics` 可抓取
- **VALIDATE**: `make test-integration`；coverage-gaps 的 D8、D9 条目消失

### Task 12: 文档
- **ACTION**: `docs/design/observability.md`（指标目录、标签、span、日志字段、advisory lock key）；`docs/deploy/runbook.md`（每条告警的含义、排查步骤、批量重放用法）；domain-model.md 新增 §14 运维（批量操作、保留规则）；coverage-gaps 更新（B13、B14、B17、B19、B20-4、D8、D9）；PRD 将 Phase 5 标为 complete
- **VALIDATE**: 第 2 节与 `make cover-gaps-all` 逐条一致

---

## Testing Strategy

### Unit Tests
| Test | Input | Expected Output | Edge Case? |
|---|---|---|---|
| 遥测初始化 | 有 / 无 OTLP endpoint | Reader 与 TracerProvider 组合正确 | 是 |
| 受理指标 | 新事件、重复、unrouted | 标签正确 | 是 |
| 首次投递延迟 | 首次 / 重试 / replay | 只有首次记录 | 是 |
| 批量请求校验 | 空 filter、互斥条件、limit 越界、未知字段 | 400 | 是 |
| 保留参数 | events < attempts、非法 duration | 启动失败，字段为变量名 | 是 |

### Integration Tests
批量操作（分块、并发、dry_run）、对账四项、保留与分区、维护任务互斥、快照 Gauge、EXPLAIN、完整进程（D8）、e2e 指标。

### Edge Cases Checklist
- [ ] 空输入：空 filter 被拒绝；无数据时快照为 0
- [ ] 最大输入：批量 limit 1000；单轮删除上限
- [ ] 非法类型：保留期、metrics 地址非法
- [ ] 并发：两个进程争抢维护锁；批量与单条 replay 同时作用于同一行
- [ ] 网络故障：OTLP endpoint 不可达时不影响投递
- [ ] 权限：source / viewer 调用批量端点 → 403

---

## Validation Commands

```bash
make lint              # EXPECT: 无输出（新文件未被 git 跟踪时另跑 gofmt -l）
make test              # EXPECT: 全部通过
make test-integration  # EXPECT: 全部通过（含 D8 子进程测试）
make cover-gaps-all    # EXPECT: 与 docs/testing/coverage-gaps.md 一致；telemetry、maintenance ≥ 80%
make check-secrets     # EXPECT: 通过（本机无 gitleaks 时如实说明）
docker compose -f deploy/observability/docker-compose.yaml config   # EXPECT: 合法
```

### Manual Validation
- [ ] 本地栈 + `serve --role=all` + mock-vendor：Grafana 看板有数据；Tempo 按 trace_id 看到受理 span 与每次 attempt span
- [ ] 制造 DEAD、积压、未配置目标三种情况，对应告警 firing
- [ ] 批量 dry_run 与实际执行结果一致；OperatorAction 每条一行

---

## Acceptance Criteria
- [ ] 死信产生即告警（PRD success signal）
- [ ] 任意 event_id 可在追踪与查询 API 中完整追溯，并可按条件批量重放
- [ ] 对账任务能发现四类不一致
- [ ] 保留与分区任务只删除过期且已结清的数据
- [ ] B13、D8 关闭；B14、B17、B19 有告警覆盖
- [ ] 底座文件改动单独提交

## Completion Checklist
- [ ] 遵循 coding-conventions.md；指标标签有界
- [ ] SQL 全部参数化；新查询有 EXPLAIN 断言
- [ ] 间隔、阈值、上限都是命名常量或进程参数
- [ ] 日志、span、指标中无密钥与 payload 原文
- [ ] 未修改 Phase 6 拥有的文件

## Risks
| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| 指标标签基数失控 | M | H | 只用有界标签；测试断言标签集合 |
| 保留任务误删未结清数据 | L | H | 删除条件含"全部终态"；对账 `missing_payload` 兜底；集成测试 |
| DEFAULT 分区与月分区冲突 | M | M | 只创建未来月份 |
| 维护任务与投递争抢数据库 | M | M | 分批、每轮上限、耗时指标 |
| OTel 依赖版本与现有间接依赖冲突 | M | L | 对齐 v1.46.x；`go mod tidy` 后跑全部测试 |

## Notes
- 维护任务放在 worker 角色中而不新增角色，部署不变；日后需要独立扩缩时拆为 `--role=maintenance`
- Phase 7 的压测与故障注入依赖本阶段的指标与对账结果判定"零静默丢失"
