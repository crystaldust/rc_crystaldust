# Plan: 领域模型与契约（Message Hub Phase 2）

## Summary
把 PRD 与 ADR 0001–0004 落成可执行的契约：统一事件格式、配置模型（YAML）、Delivery 状态机、Ingest / Query / Replay API（OpenAPI）、数据库 schema（迁移），以及模板语言、成功判定、重试计算的黄金用例。同时初始化 Go 工程骨架，用契约测试锁定这些约定。Phase 3（受理与查询）和 Phase 4（投递引擎）将并行开发，两者只通过本阶段定义的数据库表和领域类型交互。

## User Story
As a 实现 Phase 3 / Phase 4 的开发者（含 AI 编码代理）,
I want 一套无歧义、可被测试验证的数据格式、API、数据库 schema 与行为规格,
So that 两个阶段可以并行开发而不互相等待，也不在实现中各自发明约定。

## Problem → Solution
PRD 中只有概念草案（实体名称、状态机草图），没有字段级定义，也没有接口和 schema → 产出机器可校验的契约（OpenAPI、JSON Schema、SQL 迁移、Go 领域类型）、行为规格文档和黄金用例，并由 CI 级测试守护。

## Metadata
- **Complexity**: Large（新建工程骨架，跨 API / 配置 / 数据库 / 领域四类契约）
- **Source PRD**: `.claude/PRPs/prds/message-hub.prd.md`
- **PRD Phase**: Phase 2 — 领域模型与契约
- **Depends on**: Phase 1（ADR 0001–0004 Accepted）
- **Estimated Files**: 约 40 个

---

## 本阶段已确定的设计决策（用户确认，2026-10-24）

| 决策 | 结论 | 落地位置 |
|---|---|---|
| 有序投递时前序事件进入 DEAD 的处理 | 按策略可配：`ordering.on_predecessor_dead: block \| skip`，默认 `block` | ADR-0007、配置 schema、领域规格 §5 |
| 配置管理方式 | YAML 文件 + Git，CI 校验；凭据只写引用（`env:NAME`），不写明文 | ADR-0005、`schemas/config.schema.json` |
| 模板能力 | 字段路径取值 + 固定的少量内置函数；不支持条件和循环 | ADR-0006、领域规格 §6、黄金用例 |

以下默认决策由本计划确定，执行中如需调整，在 ADR 中记录：

| 决策 | 默认 | 理由 |
|---|---|---|
| API 风格 | REST + JSON，OpenAPI 3.0.3 | 业务系统接入门槛最低；kin-openapi 对 3.0 支持最成熟 |
| 响应格式 | 统一信封 `{success, data, error, meta}` | 用户全局编码规范（API Response Format） |
| ID | UUIDv7（时间有序），PG 中用 `uuid` 类型 | 可排序、可按主键做时间范围查询（借鉴 Svix KSUID 的思路）；Go 用 `github.com/google/uuid` 的 `NewV7` |
| 重复受理 | 返回 **200**，`duplicate: true`，并带回原 `event_id` | 业务方按 at-least-once 重试提交时无需特殊处理 409 |
| 无匹配路由 | 事件照常落库，`routing_status = unrouted`，返回 202，`deliveries: []`，响应中显式带 `routing_status` | 零静默丢失：任何受理的事件都可查询；`unrouted` 事件可按字段筛选、统计和告警（用户要求，2026-10-24） |
| 鉴权 | 每个来源系统、每个运维主体各一个 API Key（`Authorization: Bearer`）；配置中保存 key 的 SHA-256 引用 | MVP 最简；角色分为 `source` / `operator` / `viewer` |
| 渲染时机 | 每次 attempt 用**当前配置**渲染；attempt 记录 `config_hash` | 修复错误模板后可直接重放；attempt 可追溯当时用的是哪版配置 |
| BLOCKED 状态 | 不建显式状态；有序阻塞由调度查询隐式实现，查询 API 返回推导字段 `blocked_by` | 状态机更简单，避免大量额外迁移路径 |

---

## UX Design

### Before
```
业务系统 ──(各自拼装 URL/Header/Body、各自重试)──► 供应商 API
```

### After
```
业务系统 ── POST /v1/events {统一事件} ──► Message Hub ──(按 YAML 配置路由、渲染、投递)──► 供应商 API
        ◄─ 202 {event_id, deliveries[]} ─┘
运营/客服/运维 ── GET /v1/events?…  /v1/deliveries/{id} ──► 状态与 attempt 明细
运维 ── POST /v1/deliveries/{id}/replay | /skip ──► 重放 / 跳过（带审计）
接入新供应商 = 提交一份 YAML 变更（Git 评审 + CI 校验），不改业务代码
```

### Interaction Changes
| Touchpoint | Before | After | Notes |
|---|---|---|---|
| 发送通知 | 业务代码直接调用供应商 | 提交统一事件 | 业务方只关心 `event_type` 与 `payload` |
| 查询状态 | 翻各系统日志 | 查询 API | 按角色限制可见范围 |
| 接入新供应商 | 改代码、发版 | 改 YAML、评审、合并 | 配置变更需要重新加载 |
| 处理失败 | 无统一手段 | replay / skip API | 每次操作写审计记录 |

---

## Mandatory Reading

| Priority | File | Section | Why |
|---|---|---|---|
| P0 | `.claude/PRPs/prds/message-hub.prd.md` | 核心概念模型、MoSCoW、Decisions Log | 需求与已定决策的唯一来源 |
| P0 | `docs/adr/0003-dispatch-and-retry-queue.md` | Decision | 调度只用 PG：本阶段的表结构与索引必须支撑其领取、重试、有序查询 |
| P0 | `docs/adr/0004-state-store.md` | Decision | 分区、payload 分表、受理去重部分索引 |
| P0 | `docs/adr/0001-build-vs-extend.md` | 借鉴清单 | ID、受理去重、attempt 记录、错误三分类、JSON 匹配等设计来源 |
| P1 | `docs/spike/04-poc-results.md` | S2、S6 | 配置变更需立即生效；按目标隔离必须是一等设计（`max_in_flight`） |
| P1 | `docs/spike/02-outpost-evaluation.md` | §6 | `simplejsonmatch` 的匹配算子设计 |
| P1 | `docs/spike/03-svix-evaluation.md` | §7 | 退避与 jitter、Retry-After 上下界、响应体截断 |
| P2 | `original_requirement.md` | all | 三个示例场景，用于编写示例配置 |

全新 Go 工程，仓库内没有既有 Go 代码；本阶段建立的约定（见 Patterns to Mirror）是后续阶段的基线。

## External Documentation

| Topic | Source | Key Takeaway |
|---|---|---|
| kin-openapi | https://github.com/getkin/kin-openapi | `openapi3.NewLoader().LoadFromFile` + `doc.Validate(ctx, openapi3.EnableExamplesValidation())`；`schema.VisitJSON(value)` 校验 fixture |
| santhosh-tekuri/jsonschema v6 | https://github.com/santhosh-tekuri/jsonschema | 支持 Draft 2020-12；`jsonschema.NewCompiler().Compile(path)`，`sch.Validate(v)`，v 需要先用 `jsonschema.UnmarshalJSON` 或从 YAML 转换为 `any` |
| go.yaml.in/yaml/v3 | https://github.com/yaml/go-yaml | 原 `gopkg.in/yaml.v3` 已归档，由 YAML 组织维护的新导入路径；用 `yaml.Decoder.KnownFields(true)` 拒绝未知字段 |
| goose v3 | https://github.com/pressly/goose | SQL 迁移文件使用 `-- +goose Up` / `-- +goose Down` 注解；`goose.SetBaseFS(embedFS)` + `goose.Up(db, dir)` |
| testcontainers-go postgres | https://golang.testcontainers.org/modules/postgres/ | `postgres.Run(ctx, "postgres:16-alpine", ...)`，配合 `postgres.BasicWaitStrategies()` |
| PostgreSQL 声明式分区 | https://www.postgresql.org/docs/16/ddl-partitioning.html | 分区表的主键 / 唯一约束必须包含分区键；`DEFAULT` 分区兜底 |
| PostgreSQL 部分索引 | https://www.postgresql.org/docs/16/indexes-partial.html | 待领取扫描用 `WHERE status IN (...)` 的部分索引 |
| UUIDv7 | RFC 9562 | 时间有序；`uuid.NewV7()` |

依赖版本（遵循"发布至少两周"原则，2026-10-24 核对）：

| Module | Version | 发布日期 |
|---|---|---|
| Go toolchain | 1.26（本机 go1.26.2） | — |
| `go.yaml.in/yaml/v3` | v3.0.5 | 2026-07-26 |
| `github.com/santhosh-tekuri/jsonschema/v6` | v6.0.3 | 2026-06-28 |
| `github.com/getkin/kin-openapi` | **v0.149.0** | 2026-08-28（v0.150.0 / v0.151.0 / v0.152.0 发布不足两周，暂不使用） |
| `github.com/google/uuid` | v1.6.0 | 2024-01-23 |
| `github.com/pressly/goose/v3` | v3.28.0 | 2026-09-02（v3.29.0 发布不足两周） |
| `github.com/jackc/pgx/v5` | v5.11.0 | 2026-09-07 |
| `github.com/testcontainers/testcontainers-go/modules/postgres` | v0.44.0 | 2026-08-07 |

```
KEY_INSIGHT: kin-openapi 对 OpenAPI 3.1 的支持不完整，选用 3.0.3
APPLIES_TO: Task 6
GOTCHA: 3.0.3 中可空写成 `nullable: true`，不能写 `type: [string, "null"]`

KEY_INSIGHT: PG 分区表的唯一约束必须包含分区键
APPLIES_TO: Task 7（attempts 按 started_at 分区）
GOTCHA: attempts 主键为 (started_at, id)；events、deliveries 在 MVP 不分区，否则 (source, source_event_id) 的全局唯一约束无法成立

KEY_INSIGHT: gopkg.in/yaml.v3 已归档
APPLIES_TO: Task 4
GOTCHA: 导入路径必须是 go.yaml.in/yaml/v3
```

---

## Patterns to Mirror

全新 Go 工程。以下约定由本阶段建立，写入 `docs/design/coding-conventions.md`，后续阶段严格沿用。

### NAMING_CONVENTION
// SOURCE: 本计划定义（Go 惯例 + 用户全局规范）
```go
// 包名：小写单词，按领域划分：domain, config, api（不使用 utils、common 之类的名字）
// 导出类型 PascalCase；常量用类型化枚举
type DeliveryStatus string

const (
    StatusPending        DeliveryStatus = "PENDING"
    StatusInFlight       DeliveryStatus = "IN_FLIGHT"
    StatusRetryScheduled DeliveryStatus = "RETRY_SCHEDULED"
    StatusSucceeded      DeliveryStatus = "SUCCEEDED"
    StatusDead           DeliveryStatus = "DEAD"
    StatusSkipped        DeliveryStatus = "SKIPPED"
)
// 布尔字段读起来像判断：IsTerminal()、HasOrderingKey()
// 文件名小写加下划线：delivery_state.go、delivery_state_test.go
```

### ERROR_HANDLING
// SOURCE: 本计划定义
```go
// 哨兵错误 + %w 包装；校验错误聚合返回，不在第一个错误处停止
var ErrInvalidTransition = errors.New("invalid delivery status transition")

type FieldError struct {
    Field  string // 例如 "destinations[2].request.url"
    Reason string
}

type ValidationError struct{ Problems []FieldError }

func (e *ValidationError) Error() string { /* 列出全部 Problems，每行一个 */ }

// 包装底层错误时带上下文
return fmt.Errorf("load config %q: %w", path, err)
```

### IMMUTABILITY
// SOURCE: 用户全局规范 coding-style.md
```go
// 状态迁移是纯函数：输入当前状态与触发器，返回新状态，不修改入参
func Transition(from DeliveryStatus, trigger Trigger) (DeliveryStatus, error)
// 配置加载后得到只读的 *config.Config；查找表在构建时一次性生成，之后不再修改
```

### TEST_STRUCTURE
// SOURCE: 本计划定义（AAA + 表驱动）
```go
func TestTransition(t *testing.T) {
    tests := []struct {
        name    string
        from    DeliveryStatus
        trigger Trigger
        want    DeliveryStatus
        wantErr error
    }{
        {"pending is claimed", StatusPending, TriggerClaim, StatusInFlight, nil},
        {"succeeded cannot be claimed", StatusSucceeded, TriggerClaim, "", ErrInvalidTransition},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Act
            got, err := Transition(tt.from, tt.trigger)
            // Assert
            if !errors.Is(err, tt.wantErr) { t.Fatalf("err = %v, want %v", err, tt.wantErr) }
            if got != tt.want { t.Fatalf("got %q, want %q", got, tt.want) }
        })
    }
}
// 集成测试使用 build tag：//go:build integration
```

### LOGGING_PATTERN
本阶段不产出带日志的运行时代码。约定写入 coding-conventions.md：使用 `log/slog` JSON handler；每次受理、每次 attempt 各输出一条字段齐全的宽事件日志（借鉴 Outpost 宽事件）；日志中禁止出现密钥、Authorization 头和 payload 原文。

---

## Files to Change

| File | Action | Justification |
|---|---|---|
| `go.mod`, `go.sum` | CREATE | module `messagehub`（本地模块路径；接入代码托管后可再改），go 1.26 |
| `Makefile` | CREATE | `test`、`test-integration`、`lint`（gofmt + go vet）、`cover` |
| `.gitignore` | UPDATE | 加入 `coverage.out` |
| `docs/design/domain-model.md` | CREATE | 行为规格（实体、状态机、有序、去重、模板语言、成功判定、重试、权限、API 概览） |
| `docs/design/coding-conventions.md` | CREATE | Go 编码约定（ADR-0002 的后续要求） |
| `docs/adr/0005-config-as-yaml-in-git.md` | CREATE | 配置管理决策 |
| `docs/adr/0006-template-language-scope.md` | CREATE | 模板语言范围决策 |
| `docs/adr/0007-ordering-semantics.md` | CREATE | 有序投递语义决策 |
| `docs/adr/README.md` | UPDATE | 索引新增 0005–0007 |
| `internal/domain/ids.go` | CREATE | `NewID()`（UUIDv7） |
| `internal/domain/event.go` | CREATE | `Event` 及其校验规则常量（长度上限、正则） |
| `internal/domain/delivery.go` | CREATE | `Delivery`、`DeliveryStatus`、`IsTerminal`、`IsOrderingSettled` |
| `internal/domain/delivery_state.go` | CREATE | `Trigger` 与 `Transition` 纯函数 |
| `internal/domain/attempt.go` | CREATE | `Attempt`、`Outcome`、`AttemptTrigger` |
| `internal/domain/*_test.go` | CREATE | 状态机全表测试、事件校验测试、ID 有序性测试 |
| `internal/config/types.go` | CREATE | 配置结构体（与 YAML 一一对应） |
| `internal/config/load.go` | CREATE | `Load(path) (*Config, error)`：严格 YAML 解码 → JSON Schema 校验 → 引用完整性校验 → 构建查找表 |
| `internal/config/validate.go` | CREATE | 引用完整性与语义校验（聚合错误） |
| `internal/config/*_test.go` + `testdata/` | CREATE | 有效 / 无效配置用例 |
| `schemas/config.schema.json` | CREATE | 配置 JSON Schema（Draft 2020-12） |
| `schemas/embed.go` | CREATE | `//go:embed config.schema.json` 供 config 包使用 |
| `config/examples/hub.example.yaml` | CREATE | 覆盖原始需求三个场景的示例配置 |
| `api/openapi.yaml` | CREATE | Ingest / Query / Replay / Skip / Health API |
| `api/openapi_test.go` | CREATE | 文档校验 + 示例校验 + fixture 校验 |
| `api/testdata/events/valid/*.json`、`invalid/*.json` | CREATE | 受理请求 fixture |
| `db/migrations/00001_init.sql` | CREATE | 全部表、约束、索引、分区 |
| `db/embed.go` | CREATE | `//go:embed migrations/*.sql` |
| `db/migrations_integration_test.go` | CREATE | testcontainers：up / down / 约束行为 |
| `testdata/contracts/template/cases.yaml` | CREATE | 模板黄金用例（≥ 20 条） |
| `testdata/contracts/success/cases.yaml` | CREATE | 成功判定黄金用例（≥ 12 条） |
| `testdata/contracts/retry/cases.yaml` | CREATE | 退避计算黄金用例（jitter=0，≥ 8 条） |
| `internal/contracts/cases.go` + `cases_test.go` | CREATE | 黄金用例的类型定义、加载器与结构校验（Phase 4 复用加载器跑引擎） |
| `.claude/PRPs/prds/message-hub.prd.md` | UPDATE | Phase 2 状态、Open Questions、Decisions Log |

## NOT Building

- 任何 HTTP 服务、数据库访问代码、调度器、投递 worker（Phase 3 / 4）
- 模板引擎、成功判定、退避计算的**实现**：本阶段只定义规格和黄金用例，Phase 4 实现并用这些用例验收
- 模板的**语法**校验接入配置加载：引擎在 Phase 4 实现后，再在 `config.Load` 中接入（本阶段预留 `TemplateValidator` 接口，默认实现为空操作）
- 限流、熔断、脱敏的配置字段（Phase 6 再加入 schema，避免出现"配置了却不生效"的字段；Svix `throttleRate` 只存不用就是反例）
- Admin API、管理界面（ADR-0005：配置走 YAML + Git）
- 终态回调 push、业务方指定目标地址（PRD Won't）
- events / deliveries 表分区（ADR-0004 的演进项）

---

## Step-by-Step Tasks

### Task 1: 初始化 Go 工程与工具链
- **ACTION**: 创建 `go.mod`、`Makefile`，更新 `.gitignore`
- **IMPLEMENT**:
  - `go mod init messagehub`，在 go.mod 中写 `go 1.26`
  - 按上表的版本 `go get` 依赖，并执行 `go mod tidy`
  - Makefile 目标：
    - `lint`：`test -z "$$(gofmt -l $$(git ls-files '*.go'))"` 加 `go vet ./...`
    - `test`：`go test -race ./...`
    - `test-integration`：`go test -race -tags integration ./...`
    - `cover`：`go test -coverprofile=coverage.out ./internal/... && go tool cover -func=coverage.out`
  - `.gitignore` 加入 `coverage.out`
- **MIRROR**: NAMING_CONVENTION
- **IMPORTS**: N/A
- **GOTCHA**:
  - `.spike/src/` 下有第三方 Go 源码（自带 go.mod 的嵌套模块），`go ... ./...` 会自动排除它，但 `gofmt -l .` 会扫描进去，所以 lint 只扫描 `git ls-files` 列出的文件
  - shell 设置了代理，`go get` 走 GOPROXY 正常
- **VALIDATE**: `go build ./...` 成功；`make lint` 通过

### Task 2: 编写 ADR-0005 / 0006 / 0007
- **ACTION**: 按 ADR_FORMAT（`.claude/PRPs/plans/completed/tech-spike-and-selection.plan.md`）编写三份 ADR，状态直接写 `Accepted`（用户已在 2026-10-24 确认），并更新 `docs/adr/README.md`
- **IMPLEMENT**:
  - **0005 配置管理：YAML + Git**
    - Context：PRD Open Question；接入目标是"只改配置、少于 1 天"
    - Options：YAML + Git / DB + Admin API / 两者都做
    - Decision：配置存放在 Git 仓库中的 YAML 文件；CI 跑 `config.Load` 校验；服务启动时加载，并支持收到 SIGHUP 时重新加载（Phase 3/4 实现）；凭据只写 `env:NAME` 引用
    - Consequences：
      - 正面：配置变更自带评审和审计
      - 负面：变更需要触发重新加载；多实例一致性依赖发布流程
    - Revisit When：需要运营人员自助配置，或配置变更频率超过每天多次
  - **0006 模板语言范围**：字段路径加 12 个内置函数（列表见领域规格 §6）；不支持条件和循环；不支持脚本。Revisit When：出现无法用"路径 + 函数"表达的供应商格式，且无法要求业务方预处理
  - **0007 有序投递语义**：
    - 有序范围是 `(destination_id, ordering_key)`
    - 顺序依据受理顺序 `ingest_seq`，而不是 `occurred_at`
    - `on_predecessor_dead: block | skip`，默认 `block`
    - 运维可以通过 skip 解除阻塞
    - Revisit When：需要跨目标的全局顺序
- **MIRROR**: ADR_FORMAT
- **GOTCHA**: ADR-0007 必须写明"顺序依据受理顺序"。业务方如果并发提交同一个 key 的事件，Hub 无法知道它们的业务先后，需要业务方自己串行提交
- **VALIDATE**: `grep -L "## Decision" docs/adr/000{5,6,7}-*.md` 输出为空；README 索引有 7 行

### Task 3: 编写领域规格 `docs/design/domain-model.md`
- **ACTION**: 编写行为规格。这是 Phase 3/4 的权威文档；其他契约文件（OpenAPI、schema、SQL）必须与它一致
- **IMPLEMENT** — 章节与要点：
  1. **实体与关系**：Event 1—N Delivery（一个 Event 匹配到的每个 Destination 一条）；Delivery 1—N Attempt；OperatorAction 记录 replay / skip 审计
  2. **Event 信封**（受理请求字段）：

     | 字段 | 必填 | 规则 |
     |---|---|---|
     | `source_event_id` | 是 | 1–200 字符；与认证得到的 `source` 组成去重键 |
     | `event_type` | 是 | `^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`，≤ 100 字符 |
     | `occurred_at` | 是 | RFC 3339 |
     | `payload` | 是 | JSON object；序列化后 ≤ 256 KiB |
     | `idempotency_key` | 否 | ≤ 200 字符；原样透传给对端（由模板 `{{event.idempotency_key}}` 引用） |
     | `ordering_key` | 否 | ≤ 200 字符 |
     | `metadata` | 否 | string → string，≤ 20 项，key ≤ 64 字符，value ≤ 256 字符 |

     `source` 不由请求体提供，而是从 API Key 推导，防止冒充其他来源。

  3. **受理语义**：
     - 在**同一事务**中写入 events、event_payloads 以及匹配到的全部 deliveries，提交后执行 `NOTIFY hub_delivery_ready`
     - 重复的 `(source, source_event_id)` 返回 200，`duplicate: true`，带回原始 `event_id` 和 deliveries；**不比较 payload 是否相同**，也不重新路由
     - **路由结果显式记录**：
       - 每个事件都有 `routing_status`：`routed`（至少匹配到一个 Destination）或 `unrouted`（没有匹配到任何路由）
       - 同时记录 `matched_route_ids`（`unrouted` 时为空数组）和 `routing_config_hash`（受理时生效配置的 `ConfigHash`）
       - 无匹配路由时照常落库，返回 202，`routing_status: unrouted`，`deliveries: []`
       - 受理时输出一条 warn 级别日志（`event.unrouted`）；Phase 5 基于该字段提供指标与告警
       - `unrouted` 事件**不会自动补路由**。补充路由配置后，已受理的 `unrouted` 事件需要业务方重新提交（换新的 `source_event_id`），或等待后续阶段的"重新路由"能力（列入 PRD Open Questions）
  4. **Delivery 状态机**：

     | From | Trigger | To | 说明 |
     |---|---|---|---|
     | PENDING | Claim | IN_FLIGHT | 领取时设置 lease |
     | RETRY_SCHEDULED | Claim | IN_FLIGHT | 到期后领取 |
     | IN_FLIGHT | Reclaim | IN_FLIGHT | lease 过期后被其他 worker 接管（原 worker 视为崩溃） |
     | IN_FLIGHT | Succeed | SUCCEEDED | 成功判定通过 |
     | IN_FLIGHT | Retry | RETRY_SCHEDULED | 可重试失败，且尚有重试预算 |
     | IN_FLIGHT | InfraRetry | RETRY_SCHEDULED | 基础设施故障（出口代理不可用、渲染所需的 secret 读取失败等），**不消耗重试预算**（借鉴 Outpost 错误三分类） |
     | IN_FLIGHT | GiveUp | DEAD | 不可重试的失败，或重试预算耗尽 |
     | DEAD / SUCCEEDED / SKIPPED | Replay | PENDING | 运维重放：`replay_count+1`，`attempt_count` 归零（新一轮预算），写 OperatorAction |
     | DEAD | Skip | SKIPPED | 运维跳过，用于解除有序阻塞，写 OperatorAction |

     其余所有组合都是非法迁移（返回 `ErrInvalidTransition`）。终态为 SUCCEEDED、DEAD、SKIPPED。

     **Attempt 计数规则**：只有实际发出（或可能已发出）HTTP 请求时，`attempt_count` 才加 1。Reclaim 时，原 attempt 视为"可能已发出"，计入预算。

  5. **有序投递**（ADR-0007）：
     - 某个 Delivery 可被领取，当且仅当同一 `(destination_id, ordering_key)` 下所有 `ingest_seq` 更小的 Delivery 都已"有序结清"
     - 有序结清：状态为 SUCCEEDED 或 SKIPPED；`on_predecessor_dead=skip` 时 DEAD 也算结清
     - 没有 ordering_key，或策略为 `ordering.mode: none` 时，不受约束
     - 查询 API 为未结清的 Delivery 返回 `blocked_by: <delivery_id>`
  6. **模板语言**（ADR-0006）：
     - 语法：`{{ path [| func [arg]]... }}`
     - 路径根：`event.`、`secret.<NAME>`、`delivery.id`、`delivery.attempt`、`now`；Header 模板中还可以用 `request.body`（已渲染的 Body 字符串，用于签名）
     - 数组下标：`event.payload.items.0.sku`
     - **类型保留**：JSON Body 模板中，若某个字符串值**恰好是**一个表达式，则保留结果的原始类型（number、bool、object、array、null）；若表达式嵌在其他文本中，则转为字符串拼接
     - URL 模板中，表达式结果自动进行路径转义
     - **内置函数**（固定 12 个）：
       - `lower`、`upper`、`trim`
       - `sha256`（输出 hex）、`base64`、`urlencode`
       - `default <literal>`
       - `format_time <rfc3339|unix|unix_ms>`
       - `to_string`、`json`
       - `hmac_sha256_hex <secret.NAME>`、`hmac_sha256_base64 <secret.NAME>`
     - **错误**：路径不存在且没有 `default`、函数名未知、参数类型错误 → 渲染错误 → **永久失败**（GiveUp → DEAD，outcome=`render_error`）。修复配置后可以重放
  7. **成功判定**：
     - `success.status`：状态码列表，元素为精确值（如 `200`）或类别（如 `"2xx"`）
     - `success.body`：可选，所有规则都要满足（all-of）；算子为 `eq`、`ne`、`in`、`exists`、`not_exists`；路径为点分路径
     - 响应不是 JSON 但配置了 body 规则 → 不满足
     - **结果分类**：
       - 满足成功判定 → `success`
       - 网络错误或超时 → `retryable`
       - 状态码命中 `retry.retry_on.status` → `retryable`
       - 其余 → `permanent`
       - 例：HTTP 200 但 body 规则不满足 → `permanent`
  8. **重试计算**：
     - 第 n 次重试的延迟（n 从 1 开始）：`min(max, initial × multiplier^(n−1))`，再乘以 `(1 + U(−jitter, +jitter))`
     - 响应为 429 / 503 且带 `Retry-After` 时，取 `max(计算值, Retry-After)`，并以 `backoff.max` 封顶（借鉴 Svix 的上下界）
     - `max_attempts` 包含首次投递
  9. **按目标隔离**：每个 Destination 有 `max_in_flight`（默认 10）；领取时同一目标的 IN_FLIGHT 数不得超过该值（PoC S6 的教训）
  10. **权限**：

      | 角色 | 受理 | 查询 | replay / skip |
      |---|---|---|---|
      | `source` | ✅（`source` 为自身） | 只能查自身来源的事件 | ❌ |
      | `viewer` | ❌ | 全部 | ❌ |
      | `operator` | ❌ | 全部 | ✅ |

  11. **Attempt 记录内容**：
      - 请求：方法、URL、Header（凡值来自 `secret.*` 的 Header，以及 `Authorization`，一律记为 `***`）、Body（截断到 16 KiB）
      - 响应：状态码、Content-Type、Body（截断到 16 KiB，超出时记 `truncated: true`）
      - 其他：耗时、outcome、错误信息、`config_hash`、`next_attempt_at`
  12. **API 概览**：列出端点，并指向 `api/openapi.yaml`
- **MIRROR**: N/A（文档）
- **GOTCHA**: 这是唯一的行为权威文档。Task 4–9 中任何与它不一致之处，都应先改这里，再改契约文件
- **VALIDATE**: 12 节齐全；状态迁移表与 Task 5 的测试表逐行对应

### Task 4: 配置模型、JSON Schema 与加载器
- **ACTION**: 编写 `schemas/config.schema.json`、`schemas/embed.go`、`internal/config/{types,load,validate}.go` 与测试，以及 `config/examples/hub.example.yaml`
- **IMPLEMENT**:
  - **YAML 顶层结构**：`version: 1`，加上 `principals`、`policies`、`destinations`、`routes`
    ```yaml
    version: 1
    principals:
      - id: order-service          # role=source 时即为事件的 source
        role: source               # source | viewer | operator
        api_key_sha256_ref: env:HUB_KEY_ORDER_SERVICE_SHA256
    policies:
      - id: default
        timeout: 5s
        retry:
          max_attempts: 8
          backoff: { initial: 5s, multiplier: 2.0, max: 1h, jitter: 0.2 }
          retry_on: { status: [408, 425, 429, "5xx"] }
        success:
          status: ["2xx"]
          body: [ { path: code, op: eq, value: 0 } ]   # 可选
        ordering: { mode: none }   # none | per_key；per_key 时 on_predecessor_dead: block | skip（默认 block）
    destinations:
      - id: crm
        policy: default
        max_in_flight: 10
        secrets: { CRM_TOKEN: env:CRM_TOKEN }
        auth: { type: none }      # none | oauth2_client_credentials（token_url, client_id_ref, client_secret_ref, scopes）
        request:
          method: POST             # POST | PUT | PATCH
          url: "https://crm.example.com/contacts/{{event.payload.contact_id}}/status"
          headers: { Content-Type: application/json, Authorization: "Bearer {{secret.CRM_TOKEN}}" }
          body: { status: paid, paid_at: "{{event.occurred_at}}" }   # 任意 YAML 值，叶子字符串可以是模板
    routes:
      - id: order-paid
        event_type: order.paid
        sources: [order-service]   # 可选
        match: [ { path: payload.channel, op: eq, value: ads } ]  # 可选，算子同成功判定
        destinations: [crm]
    ```
  - **JSON Schema**（Draft 2020-12）：
    - 所有对象都设 `additionalProperties: false`
    - id 模式 `^[a-z][a-z0-9-]{1,62}$`
    - duration 模式 `^[0-9]+(ms|s|m|h)$`
    - 引用格式 `^env:[A-Z][A-Z0-9_]*$`
    - 数值范围：`jitter` 在 [0, 1)，`multiplier` ≥ 1，`max_attempts` 在 1..50，`max_in_flight` 在 1..1000
    - `ordering.on_predecessor_dead` 只在 `mode=per_key` 时允许出现（用 `if/then`）
  - **加载流程** `Load(path string) (*Config, error)`：
    1. 用 `yaml.Decoder` 并开启 `KnownFields(true)`，解码到 `any`
    2. 用 JSON Schema 校验
    3. 解码到类型化结构体
    4. 语义校验（聚合全部错误）：
       - 每类 id 唯一
       - route 引用的 destination / source、destination 引用的 policy 都存在
       - `event_type` 符合 §2 的正则
       - `timeout` 小于 `backoff.max`
       - 同一 `event_type` 下多个 route 指向同一 destination 时，Delivery 去重（同一事件对同一目标只生成一条），并在加载时给出 **warning**（返回值中的 `Warnings []string`）
    5. 构建只读查找表：`RoutesByEventType`、`Destination(id)`、`Policy(id)`、`PrincipalByKeyHash`
    6. 计算 `ConfigHash`（规范化 JSON 的 SHA-256），用于写入 attempt 记录
  - **不在加载时读取环境变量里的密钥值**：只校验引用格式，密钥值在运行期解析（Phase 3/4）
  - 预留 `type TemplateValidator func(field, tmpl string) error` 选项（`WithTemplateValidator`），默认为空操作
  - **示例配置**覆盖原始需求的三个场景：
    1. 注册 → 广告平台：邮箱 `lower | trim | sha256`；Header 签名用 `hmac_sha256_hex`；成功判定为 `2xx` 且 body `code eq 0`
    2. 付款 → CRM：OAuth2 client credentials；PATCH
    3. 购买 → 库存：`ordering: per_key`（key 为 sku），`block`；扇出加上第二个目标（例如数据仓库 webhook），用于演示扇出
- **MIRROR**: ERROR_HANDLING、IMMUTABILITY、TEST_STRUCTURE
- **IMPORTS**: `go.yaml.in/yaml/v3`、`github.com/santhosh-tekuri/jsonschema/v6`、`messagehub/schemas`
- **GOTCHA**:
  - YAML 会把 `2xx` 这类无引号的值解析为字符串，但把 `200` 解析为 int，schema 中 status 元素用 `oneOf: [integer 100–599, string ^[1-5]xx$]`
  - duration 用自定义类型实现 `UnmarshalYAML`
  - 用 jsonschema 校验前，YAML 解出的 `map[string]any` 中可能有 int、float 混合；需要先转换为 jsonschema 期望的类型（JSON round-trip 最简单）
- **VALIDATE**:
  - 示例配置加载成功
  - `testdata/invalid/*.yaml` 每个文件都对应一条期望错误（字段路径加原因），覆盖：未知字段、重复 id、悬空引用、坏的引用格式、非法 duration、per_key 缺省值、`jitter = 1`
  - 覆盖率 ≥ 80%

### Task 5: 领域类型与状态机
- **ACTION**: 编写 `internal/domain/*.go` 与测试
- **IMPLEMENT**:
  - `Event`：字段同 §2，外加 `ID`、`Source`、`IngestSeq`、`ReceivedAt`、`RoutingStatus`、`MatchedRouteIDs`、`RoutingConfigHash`。提供 `ValidateEnvelope(e) error`，返回 `*ValidationError`，规则与上限都用常量定义
  - `RoutingStatus` 枚举：`RoutingRouted = "routed"`、`RoutingUnrouted = "unrouted"`；提供纯函数 `RoutingStatusFor(matchedRouteIDs []string) RoutingStatus`，使"是否匹配"的判定只有一处实现
  - `Delivery`：`ID`、`EventID`、`DestinationID`、`PolicyID`、`Status`、`OrderingKey`、`IngestSeq`、`AttemptCount`、`ReplayCount`、`ScheduledAt`、`LeaseOwner`、`LeaseExpiresAt`、`Last*`、时间戳。方法 `IsTerminal()`、`IsOrderingSettled(onPredecessorDead)`
  - `Trigger` 枚举：`Claim`、`Reclaim`、`Succeed`、`Retry`、`InfraRetry`、`GiveUp`、`Replay`、`Skip`
  - `Transition` 用一张静态的 `map[DeliveryStatus]map[Trigger]DeliveryStatus` 实现
  - `Attempt`、`Outcome`（`success`、`retryable`、`permanent`、`render_error`、`infra_error`）、`AttemptTrigger`（`auto`、`replay`）
  - `NewID()` 返回 `uuid.NewV7()`；生成失败时 panic 并附说明（只有系统熵源失效时才会发生）
- **MIRROR**: NAMING_CONVENTION、IMMUTABILITY、TEST_STRUCTURE
- **IMPORTS**: `github.com/google/uuid`
- **GOTCHA**: 状态机测试必须是 **6 个状态 × 8 个触发器 = 48 个组合的全表**，确保每个非法组合都返回 `ErrInvalidTransition`，而不只是测试合法路径
- **VALIDATE**:
  - `go test ./internal/domain/...` 通过
  - 覆盖率 ≥ 90%
  - 连续生成的 1000 个 ID 按字节序递增

### Task 6: OpenAPI 契约
- **ACTION**: 编写 `api/openapi.yaml`、fixture 与 `api/openapi_test.go`
- **IMPLEMENT**:
  - 端点（前缀 `/v1`）：

    | Method | Path | 角色 | 说明 |
    |---|---|---|---|
    | POST | `/v1/events` | source | 受理；新事件返回 202，重复事件返回 200 |
    | GET | `/v1/events/{event_id}` | all | 事件 + payload + deliveries 摘要（含 `blocked_by`） |
    | GET | `/v1/events` | all | 过滤：`source_event_id`、`event_type`、`ordering_key`、`routing_status`（`routed` \| `unrouted`）、`status`（任一 delivery 处于该状态）、`received_from`、`received_to`；分页：`cursor`、`limit`（≤ 100，默认 50） |
    | GET | `/v1/deliveries/{delivery_id}` | all | Delivery + attempts（分页） |
    | GET | `/v1/deliveries` | all | 过滤：`event_id`、`destination_id`、`status`、时间范围；分页同上 |
    | POST | `/v1/deliveries/{delivery_id}/replay` | operator | Body `{reason}`；202；对非法状态返回 409 |
    | POST | `/v1/deliveries/{delivery_id}/skip` | operator | Body `{reason}`；只允许 DEAD；200 |
    | GET | `/healthz`、`/readyz` | 无 | 存活 / 就绪 |

  - 统一信封：
    - 成功：`{success: true, data, error: null, meta}`
    - 失败：`{success: false, data: null, error: {code, message, details[{field, reason}]}, meta}`
    - 分页的 `meta` 包含 `next_cursor`、`limit`
  - 错误码：
    - 400 `validation_failed`
    - 401 `unauthenticated`
    - 403 `forbidden`
    - 404 `not_found`
    - 409 `invalid_state`
    - 413 `payload_too_large`
    - 429 `rate_limited`（预留，Phase 6 使用）
    - 500 `internal`
  - 受理响应（202 / 200）与事件详情、事件列表中都包含 `routing_status`、`matched_route_ids`；`unrouted` 的受理响应示例必须写进 OpenAPI examples
  - source 角色查询其他来源的事件时返回 **404**，而不是 403，避免泄露事件是否存在
  - 每个请求和响应 schema 都附 `example`
  - fixture：`api/testdata/events/valid/` 中至少 4 个（最小字段集、全字段、嵌套 payload、metadata 到上限）；`invalid/` 中至少 8 个（缺必填、坏 event_type、payload 不是对象、metadata 超项、key 超长、多余字段、坏时间格式、带 source 字段）
  - 测试：
    1. 加载并 `Validate(ctx, openapi3.EnableExamplesValidation())`
    2. 用 `IngestEventRequest` schema 的 `VisitJSON` 校验全部 fixture：valid 必须通过，invalid 必须失败
    3. 一致性测试：fixture 同时经 `domain.ValidateEnvelope` 校验，两者结论必须一致（防止 OpenAPI 与 Go 校验漂移）
- **MIRROR**: TEST_STRUCTURE
- **IMPORTS**: `github.com/getkin/kin-openapi/openapi3`、`messagehub/internal/domain`
- **GOTCHA**:
  - 请求 schema 中**不包含** `source` 字段，且 `additionalProperties: false`，确保带 `source` 的请求被拒绝
  - payload 大小（256 KiB）无法用 schema 表达，只在 `domain.ValidateEnvelope` 中检查；一致性测试中排除这一条，并加注释说明
- **VALIDATE**: `go test ./api/...` 通过

### Task 7: 数据库 schema 迁移
- **ACTION**: 编写 `db/migrations/00001_init.sql`（goose 格式）、`db/embed.go` 与集成测试
- **IMPLEMENT**:
  - `events`：
    - `id uuid PK`
    - `ingest_seq bigint GENERATED ALWAYS AS IDENTITY UNIQUE`
    - `source text`、`source_event_id text`、`event_type text`
    - `occurred_at timestamptz`、`received_at timestamptz DEFAULT now()`
    - `idempotency_key text NULL`、`ordering_key text NULL`
    - `metadata jsonb DEFAULT '{}'`
    - `routing_status text NOT NULL CHECK (routing_status IN ('routed','unrouted'))`
    - `matched_route_ids text[] NOT NULL DEFAULT '{}'`
    - `routing_config_hash text NOT NULL`
    - 一致性约束：`CHECK ((routing_status = 'unrouted') = (cardinality(matched_route_ids) = 0))`，保证两个字段不会互相矛盾
    - `UNIQUE (source, source_event_id)`
    - 索引：`(event_type, received_at DESC)`、`(received_at DESC)`、`(ordering_key) WHERE ordering_key IS NOT NULL`、`(received_at DESC) WHERE routing_status = 'unrouted'`（供排查与告警查询）
  - `event_payloads`：`event_id uuid PK REFERENCES events ON DELETE CASCADE`、`payload jsonb NOT NULL`、`expires_at timestamptz NULL`，部分索引 `(expires_at) WHERE expires_at IS NOT NULL`
  - `deliveries`：
    - 字段同 Task 5 的 `Delivery`
    - `status text CHECK (status IN (...6 个状态))`
    - `UNIQUE (event_id, destination_id)`
    - 索引：
      - 待领取：`(scheduled_at) WHERE status IN ('PENDING','RETRY_SCHEDULED')`
      - lease 扫描：`(lease_expires_at) WHERE status='IN_FLIGHT'`
      - 有序：`(destination_id, ordering_key, ingest_seq) WHERE ordering_key IS NOT NULL`
      - 在途计数：`(destination_id) WHERE status='IN_FLIGHT'`
      - 查询：`(destination_id, status, created_at DESC)`、`(event_id)`
  - `attempts`：
    - `PARTITION BY RANGE (started_at)`，`PRIMARY KEY (started_at, id)`
    - 字段：`delivery_id`、`number`、`trigger`、`duration_ms`、`outcome`、`status_code`、`error`、`request jsonb`、`response jsonb`、`config_hash`、`next_attempt_at`
    - 索引 `(delivery_id, number)`
    - 创建 `attempts_default` DEFAULT 分区
    - 按月创建分区由运行时任务负责（Phase 5），本迁移只建 DEFAULT 分区
  - `operator_actions`：`id uuid PK`、`delivery_id uuid REFERENCES deliveries`、`action text CHECK (action IN ('replay','skip'))`、`actor text`、`reason text NOT NULL`、`from_status text`、`created_at`
  - Down 迁移按依赖逆序 `DROP`
  - 集成测试（`//go:build integration`）：
    1. 启动 PG16 容器
    2. 执行 `goose.Up`
    3. 断言：重复 `(source, source_event_id)` 插入违反唯一约束；非法 status 违反 CHECK；`routing_status='unrouted'` 但 `matched_route_ids` 非空（以及反过来）违反一致性 CHECK；向 attempts 插入任意时间的数据都会落入 DEFAULT 分区
    4. `goose.Down` 后所有表都不存在
    5. 再次 `goose.Up` 成功（可重复执行）
- **MIRROR**: TEST_STRUCTURE
- **IMPORTS**: `github.com/pressly/goose/v3`、`github.com/jackc/pgx/v5/stdlib`、`github.com/testcontainers/testcontainers-go/modules/postgres`
- **GOTCHA**:
  - PG16 允许分区表（attempts）对普通表（deliveries）建外键。MVP 加上 `attempts.delivery_id REFERENCES deliveries ON DELETE CASCADE`，但删除 delivery 时会扫描所有分区，在 SQL 注释中说明；归档与清理策略在 Phase 5 处理
  - testcontainers 需要 Docker；Docker 已配置代理，可以拉取镜像
  - goose 的 SQL 文件中，含 `$$` 的语句需要 `-- +goose StatementBegin/End`（本迁移不需要，但留意）
- **VALIDATE**: `make test-integration` 通过

### Task 8: 黄金用例与加载器
- **ACTION**: 编写 `testdata/contracts/{template,success,retry}/cases.yaml`、`internal/contracts/cases.go`、`cases_test.go`
- **IMPLEMENT**:
  - `template/cases.yaml`：每条用例包含 `name`、`location`（body | header | url）、`template`（字符串或 YAML 值）、`event`、`secrets`、可选 `request_body`，以及 `want` 或 `want_error`（`missing_path` | `unknown_function` | `bad_argument` | `syntax`）。至少 20 条，覆盖：
    - 单表达式类型保留（number、bool、object、null）
    - 嵌入文本时字符串化
    - 数组下标
    - 12 个函数各至少 1 条
    - 函数链 `lower | trim | sha256`
    - `default`
    - URL 路径转义
    - Header 中用 `hmac_sha256_hex secret.X` 对 `request.body` 签名（给出固定 secret 与预期 hex）
    - 4 类错误各至少 1 条
  - `success/cases.yaml`：每条用例包含 `policy_success`、`retry_on`、`response{status, content_type, body}` 或 `network_error`，以及 `want_outcome`（success | retryable | permanent）。至少 12 条，必须包含 PoC S3 的场景：200 + `{"code":1001}` 且规则为 `code eq 0` → permanent
  - `retry/cases.yaml`：每条用例包含 `backoff`（jitter=0）、`retry_number`、可选 `retry_after`，以及 `want_delay`。至少 8 条，覆盖：封顶、`multiplier=1`、Retry-After 大于计算值、Retry-After 超过 max
  - `internal/contracts`：用类型化结构体加载这三个文件（`KnownFields(true)`）；测试校验：每条用例 `name` 唯一；`want` 与 `want_error` 二选一；hmac 用例的预期值用 `crypto/hmac` 在测试中重新计算核对，确保黄金值本身正确
- **MIRROR**: TEST_STRUCTURE
- **IMPORTS**: `go.yaml.in/yaml/v3`、`crypto/hmac`、`crypto/sha256`、`encoding/hex`
- **GOTCHA**: sha256 / hmac 的黄金值**必须用代码计算后再写入**（例如 `printf ... | shasum -a 256`），不要手写，并在测试中复算
- **VALIDATE**: `go test ./internal/contracts/...` 通过；三个文件的用例数达到下限

### Task 9: 编码约定文档
- **ACTION**: 编写 `docs/design/coding-conventions.md`
- **IMPLEMENT**：包含以下内容
  - 包结构与依赖方向：`domain` 不依赖任何内部包；`config` 只依赖 `domain`；服务层依赖两者
  - 错误处理、不可变、命名、测试（AAA + 表驱动 + 集成测试 tag）
  - 日志（slog 宽事件，敏感信息禁入日志）
  - context 传递，所有 I/O 函数第一个参数为 `ctx`
  - 依赖版本"发布至少两周"原则
  - 覆盖率 ≥ 80%
  - 质量门禁：`make lint test`
- **VALIDATE**: 与 Patterns to Mirror 各节一致

### Task 10: 更新 PRD
- **ACTION**: 更新 `.claude/PRPs/prds/message-hub.prd.md`
- **IMPLEMENT**:
  - Phase 2 状态改为 `complete`，并链接 plan 和 report（由 prp-implement 完成）
  - Open Questions 中勾选"有序投递的阻塞策略""配置变更的管理方式"
  - Open Questions 中新增："`unrouted` 事件在补充路由配置后，是否需要提供'重新路由'能力（运维对已受理事件按当前配置重新匹配并生成 Delivery）"
  - Decisions Log 新增一行："无匹配路由的记录方式 → 显式 `routing_status` 字段 + `routing_config_hash`"
  - Decisions Log 新增三行（链接 ADR-0005 至 0007）
  - 核心概念模型表注明"字段级定义见 `docs/design/domain-model.md`"
- **VALIDATE**: PRD 中的链接都指向存在的文件

---

## Testing Strategy

### Unit Tests

| Test | Input | Expected Output | Edge Case? |
|---|---|---|---|
| 状态机全表 | 6 状态 × 8 触发器 | 合法的 11 个组合返回目标状态，其余 37 个返回 `ErrInvalidTransition` | 是 |
| `IsOrderingSettled` | DEAD + block / DEAD + skip / SKIPPED / RETRY_SCHEDULED | false / true / true / false | 是 |
| 事件信封校验 | 各字段处于上限、超出上限 | 上限通过，超出 1 即失败，错误中带字段路径 | 是 |
| payload 大小 | 256 KiB / 256 KiB + 1 字节 | 通过 / 失败 | 是 |
| ID 有序 | 连续 1000 个 `NewID()` | 严格递增 | 否 |
| 路由状态判定 | `RoutingStatusFor(nil)` / `([]string{})` / `([]string{"r1"})` | unrouted / unrouted / routed | 是 |
| 配置：示例 | `hub.example.yaml` | 加载成功，查找表正确，`ConfigHash` 稳定（加载两次相同） | 否 |
| 配置：无效用例 | 各 `testdata/invalid/*.yaml` | 每个都给出期望的字段路径与原因 | 是 |
| 配置：多个错误 | 一个文件包含 3 处错误 | 一次返回 3 条 Problems | 是 |
| 配置：重复路由 | 两个 route 指向同一 destination | 加载成功并返回 1 条 warning | 是 |
| OpenAPI 文档 | `openapi.yaml` | 结构与示例都合法 | 否 |
| Fixture 一致性 | valid / invalid 事件 | OpenAPI 与 `ValidateEnvelope` 结论一致 | 是 |
| 黄金用例结构 | 三个 cases.yaml | 结构合法、name 唯一、hmac 预期值复算一致 | 否 |

### Integration Tests（`-tags integration`）

| Test | Expected |
|---|---|
| 迁移 up → 约束 → down → up | 全部按预期 |

### Edge Cases Checklist
- [x] Empty input（空配置文件、空 payload 对象 `{}` 合法）
- [x] Maximum size input（payload 256 KiB、metadata 20 项）
- [x] Invalid types（status 元素类型、duration 格式、payload 不是对象）
- [ ] Concurrent access（本阶段没有并发代码，N/A）
- [ ] Network failure（N/A；黄金用例中定义了网络错误的分类）
- [x] Permission denied（权限矩阵写入规格与 OpenAPI，实现在 Phase 3）

---

## Validation Commands

### Static Analysis
```bash
make lint
```
EXPECT: gofmt 无输出，go vet 无错误

### Unit Tests
```bash
make test
```
EXPECT: 全部通过（含 `-race`）

### Coverage
```bash
make cover
```
EXPECT: `internal/domain` ≥ 90%，`internal/config` ≥ 80%，`internal/contracts` ≥ 80%

### Integration
```bash
make test-integration
```
EXPECT: 迁移测试通过（需要 Docker）

### Build
```bash
go build ./...
```
EXPECT: 成功

### Manual Validation
- [ ] 抽查领域规格中的状态迁移表与 `delivery_state.go` 的 map 逐行一致
- [ ] 示例配置能看懂原始需求的三个场景，且不包含任何明文凭据
- [ ] OpenAPI 用 Swagger Editor 或 Redocly 打开后可读（可选）

---

## Acceptance Criteria
- [ ] 所有任务完成
- [ ] 所有验证命令通过
- [ ] 覆盖率达到上述阈值
- [ ] ADR 0005–0007 已编写并加入索引
- [ ] 领域规格是唯一的行为权威，OpenAPI、schema、SQL、Go 类型都与之一致
- [ ] PRD 已更新

## Completion Checklist
- [ ] 代码遵循 coding-conventions.md
- [ ] 错误处理使用聚合 ValidationError 与 `%w` 包装
- [ ] 没有可变的全局状态（只读的查找表 map 可以例外）
- [ ] 测试遵循 AAA 与表驱动
- [ ] 魔法数字都已定义为命名常量（长度上限、默认值）
- [ ] 没有超出 NOT Building 范围的工作
- [ ] 计划自洽，执行中不需要再提问

## Risks
| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| OpenAPI 与 Go 校验规则漂移 | M | M | fixture 一致性测试 |
| 黄金用例本身写错（尤其是哈希值） | M | H | 用代码生成，并在测试中复算 |
| schema 过度设计，出现不生效的字段 | M | M | NOT Building 明确排除 Phase 6 的字段；schema 设 `additionalProperties: false` |
| 有序查询在 PG 中性能不佳 | L | M | 有序索引已建；性能在 Phase 4 实现领取 SQL 时用 EXPLAIN 验证 |
| 依赖版本过新带来供应链风险 | L | M | 只使用发布至少两周的版本，并在计划中列明 |
| `.spike/` 中的第三方 Go 代码干扰 lint | M | L | lint 只扫描 `git ls-files` 中的文件 |

## Notes
- 执行顺序：Task 1 → 2 → 3（规格先行）→ 4、5、6、7、8 可并行（都以 Task 3 为准）→ 9 → 10
- Task 4–8 适合交给多个子 agent 并行，但都必须先读 Task 3 的产出；主线程最后做一致性核对
- 本阶段的黄金用例是 Phase 4 投递引擎的验收标准：Phase 4 实现模板引擎、成功判定、退避计算时，直接复用 `internal/contracts` 的加载器跑这些用例
