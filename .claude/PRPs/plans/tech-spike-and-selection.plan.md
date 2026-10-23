# Plan: 技术验证与选型（Message Hub Phase 1）

## Summary
在写任何生产代码之前，用一次限时的技术验证回答两个问题：(1) 基于 Hookdeck Outpost / Svix server 二次开发，还是基于开源组件自建；(2) 语言、调度队列、状态存储如何选型。产出是一组可评审的评估文档、可复跑的 PoC 场景和 ADR，作为 Phase 2（领域模型与契约）的输入。

## User Story
As a Message Hub 的技术负责人,
I want 基于对照清单和实测证据做出"二次开发 vs 自建"及技术栈决策,
So that 后续 Phase 2–7 的实现建立在经过验证的基础上，避免中途推翻架构。

## Problem → Solution
PRD 中"倾向自建"的结论仅基于文档与 README 的初步阅读，技术栈未定 → 有加权评分、代码级证据与 PoC 实测结果支撑的 ADR，评审通过后定案。

## Metadata
- **Complexity**: Medium（以文档与 PoC 为主，少量脚本）
- **Source PRD**: `.claude/PRPs/prds/message-hub.prd.md`
- **PRD Phase**: Phase 1 — 技术验证与选型
- **Estimated Files**: 约 16 个（文档 12 + 脚本/配置 4）
- **Timebox**: 建议 2–3 个工作日（AI 辅助）。超时则以已有证据做决策，并在 ADR 中注明未验证项

---

## UX Design

### Before
N/A — 内部技术决策阶段，无用户界面变化。

### After
N/A — 内部技术决策阶段，无用户界面变化。

### Interaction Changes
| Touchpoint | Before | After | Notes |
|---|---|---|---|
| 技术决策 | 仅有 PRD 中的初步倾向 | ADR 0001–0004 定案 | 评审通过后 Phase 2 才开始 |

---

## Mandatory Reading

本仓库为全新项目（仅有 `original_requirement.md` 与 PRD），无既有代码可参照。必读材料如下：

| Priority | File / Path | Lines | Why |
|---|---|---|---|
| P0 | `.claude/PRPs/prds/message-hub.prd.md` | Core Capabilities (MoSCoW)、Technical Approach、Decisions Log | 评估清单的唯一需求来源；所有 Must/Should 能力必须逐项覆盖 |
| P0 | `original_requirement.md` | all | 原始需求 |
| P1 | Outpost `internal/` 下 `deliverymq`、`destregistry`、`idempotence`、`backoff`、`scheduler`、`worker`、`logstore`、`simplejsonmatch` | all | 投递引擎、目标端注册、重试、过滤的实现位置；判断扩展点是否存在 |
| P1 | Outpost `AGENTS.md`、`CLAUDE.md`、`.outpost.yaml.example`、`deployments/`、`loadtest/` | all | 架构说明、配置面、部署方式与官方压测方法 |
| P1 | Svix `server/svix-server/src/` 下 `worker.rs`、`queue/`、`core/`、`db/`、`cfg.rs`、`v1/` | all | 投递 worker、队列抽象、重试与 API |
| P1 | Svix `bridge/`（`svix-bridge/src/runtime`、`svix-bridge.example.senders.yaml`） | all | Bridge 疑似提供 JS 转换能力；需确认能否覆盖"按供应商构造请求" |
| P2 | Svix `OpenTelemetry.md`、`server/docker-compose.yml`、`server/README.md` | all | 可观测性与部署 |

已确认的仓库现状（2026-10-24 通过 GitHub API 查询）：

| Repo | 语言 / License | 最新 Release | 最近 push | Stars |
|---|---|---|---|---|
| `hookdeck/outpost` | Go / Apache-2.0 | v1.7.0（2026-10-23） | 2026-10-23 | ~1.07k |
| `svix/svix-webhooks` | Rust / MIT | server-v1.102.1（2026-10-21） | 2026-10-23 | ~3.46k |

## External Documentation

| Topic | Source | Key Takeaway |
|---|---|---|
| Outpost 概览 | https://hookdeck.com/docs/outpost/overview | 依赖 Redis + PostgreSQL + MQ；topic 扇出；不内置转换与按租户限流；at-least-once |
| Outpost 目标端 | https://hookdeck.com/docs/outpost/destinations | 目标类型与配置面 |
| Outpost 仓库 | https://github.com/hookdeck/outpost | 源码与部署、压测脚本 |
| Svix server 仓库 | https://github.com/svix/svix-webhooks | PostgreSQL 必需、Redis 可选；OTel；托管版有 OSS 未包含的功能 |
| Transactional Outbox | https://www.getconvoy.io/blog/webhooks-with-transactional-outbox | at-least-once + 幂等键；生产方不丢事件 |
| 投递系统设计要点 | https://dev.to/antfarm-official/webhooks-at-scale-designing-an-idempotent-replay-safe-and-observable-webhook-system-7lk | 按目标隔离、退避 + jitter、死信 + 重放 |
| PostgreSQL `FOR UPDATE SKIP LOCKED` | https://www.postgresql.org/docs/current/sql-select.html#SQL-FOR-UPDATE-SHARE | 以数据库作为任务队列的基础机制（候选方案之一） |

```
KEY_INSIGHT: Outpost 与 Svix 都面向"SaaS 推送给客户 endpoint"模型，统一签名，按租户订阅
APPLIES_TO: Task 3、Task 4 的差距分析
GOTCHA: 厂商对比页（Svix 评价 Convoy、Hookdeck 评价竞品）有立场偏差，结论必须以源码和实测为准

KEY_INSIGHT: Outpost 的 logstore 有 ClickHouse 实现，说明投递日志量大时需要与状态存储分离
APPLIES_TO: Task 6（状态存储选型）
GOTCHA: 不要让 Attempt 日志的写入压力决定主状态存储选型，两者可以分开

KEY_INSIGHT: Kafka 无原生延迟消息，重试调度需另行实现（重试主题分级 / 外部调度器）；RabbitMQ 需延迟插件或 TTL+DLX；Redis 可用 ZSET；PostgreSQL 可用 scheduled_at + SKIP LOCKED
APPLIES_TO: Task 6（调度队列选型）
GOTCHA: "重试调度"和"首次分发"对队列的要求不同，选型时分开评估

KEY_INSIGHT: Svix Bridge 内置 JS 运行时做 payload 转换
APPLIES_TO: Task 4
GOTCHA: PRD 已决定不引入脚本转换（Won't）；即使 Bridge 能做，也只能作为参考，不能作为加分项
```

---

## Patterns to Mirror

全新项目，无既有代码模式。本阶段**建立**以下文档约定，后续阶段沿用。

### ADR_FORMAT
// SOURCE: 本计划定义（基于 MADR 精简）
```markdown
# ADR-000N: <决策标题>

- **Status**: Proposed | Accepted | Superseded by ADR-000M
- **Date**: YYYY-MM-DD
- **Deciders**: <角色>
- **Related**: PRD message-hub Phase 1；<其他 ADR>

## Context
<要解决的问题、约束（引用 PRD 条目）>

## Options Considered
| Option | Pros | Cons | Score |
|---|---|---|---|

## Decision
<选定方案，一句话>

## Rationale
<为什么，引用 docs/spike/ 中的证据，附文件与章节>

## Consequences
- 正面：
- 负面 / 需承担的成本：
- 未验证项（如超出 timebox）：

## Revisit When
<触发重新评估的条件，例如"日事件量 > X"或"出现 Y 需求">
```

### EVIDENCE_CITATION
// SOURCE: 本计划定义
每条评估结论必须附证据，格式为以下三种之一：
- 源码：`outpost@v1.7.0:internal/deliverymq/xxx.go:L10-L40`
- 文档：URL + 章节名
- 实测：`docs/spike/04-poc-results.md#S3`

无证据的结论标记为 `UNVERIFIED`，不得计入评分。

### SCORING_SCALE
// SOURCE: 本计划定义
| 分值 | 含义 |
|---|---|
| 3 | 原生支持，配置即可 |
| 2 | 通过官方扩展点 / 插件支持，无需改核心代码 |
| 1 | 需要修改核心代码才能支持（fork 并长期维护分叉） |
| 0 | 与其模型冲突，或实现成本接近重写 |

---

## Files to Change

| File | Action | Justification |
|---|---|---|
| `.gitignore` | CREATE | 忽略 `.spike/`（第三方源码与 PoC 运行数据） |
| `docs/spike/README.md` | CREATE | 验证阶段总览、时间盒、结论摘要、文档索引 |
| `docs/spike/01-evaluation-checklist.md` | CREATE | 能力清单 C1–C18、权重、评分规则、决策规则 |
| `docs/spike/02-outpost-evaluation.md` | CREATE | Outpost 逐项评分与代码级证据 |
| `docs/spike/03-svix-evaluation.md` | CREATE | Svix server（含 Bridge）逐项评分与代码级证据 |
| `docs/spike/04-poc-results.md` | CREATE | PoC 场景 S1–S8 的执行记录与结果 |
| `docs/spike/05-stack-evaluation.md` | CREATE | 语言、调度队列、状态存储的对比与评分 |
| `spike/mock-vendor/mock_vendor.py` | CREATE | 可控的模拟供应商（状态码、延迟、响应体、记录请求），Phase 7 可复用 |
| `spike/mock-vendor/README.md` | CREATE | 模拟供应商的用法 |
| `spike/mock-vendor/self_check.sh` | CREATE | 模拟供应商自检脚本 |
| `spike/scenarios/run_scenarios.md` | CREATE | 各候选方案执行 S1–S8 的具体步骤（命令、配置片段） |
| `docs/adr/README.md` | CREATE | ADR 索引 |
| `docs/adr/0001-build-vs-extend.md` | CREATE | 二次开发 vs 自建 |
| `docs/adr/0002-implementation-language.md` | CREATE | 实现语言 |
| `docs/adr/0003-dispatch-and-retry-queue.md` | CREATE | 调度队列与重试调度机制 |
| `docs/adr/0004-state-store.md` | CREATE | 状态存储（含 Attempt 日志存储策略） |
| `.claude/PRPs/prds/message-hub.prd.md` | UPDATE | 完成后：Phase 1 状态、Decisions Log 定案、Open Questions 勾选 |

## NOT Building

- 任何 Message Hub 生产代码（属于 Phase 2 之后）
- 领域模型 / API 契约的详细设计（Phase 2）
- 正式的性能压测与容量基线（Phase 7）；本阶段只做量级验证
- 对 Outpost / Svix 的任何修改或 fork
- Convoy、Hook0 的深入评估（Convoy 维护状态存疑，Hook0 定位小规模；仅在 checklist 中一句话说明排除理由）
- 云托管 / 商业服务的评估（PRD 已排除）

---

## Step-by-Step Tasks

### Task 1: 初始化验证工作区
- **ACTION**: 创建目录结构与 `.gitignore`，拉取两个候选项目的源码到隔离目录
- **IMPLEMENT**:
  - `.gitignore` 写入 `.spike/`
  - `git clone --depth 1 --branch v1.7.0 https://github.com/hookdeck/outpost .spike/src/outpost`
  - `git clone --depth 1 --branch server-v1.102.1 https://github.com/svix/svix-webhooks .spike/src/svix`
  - 创建 `docs/spike/`、`docs/adr/`、`spike/mock-vendor/`、`spike/scenarios/`
- **MIRROR**: N/A
- **IMPORTS**: N/A
- **GOTCHA**: 第三方源码视为不受信数据。每个项目放在独立目录；阅读源码不执行其中脚本；只通过各自官方 `docker compose` 方式运行。版本必须钉死在上述 tag，评估文档中所有源码引用带版本号
- **VALIDATE**: `git -C .spike/src/outpost describe --tags` 输出 `v1.7.0`；`git -C .spike/src/svix describe --tags` 输出 `server-v1.102.1`；`git status` 不显示 `.spike/`

### Task 2: 编写评估清单与决策规则
- **ACTION**: 编写 `docs/spike/01-evaluation-checklist.md`
- **IMPLEMENT**: 能力清单（来源于 PRD MoSCoW，权重 Must=3、Should=2、Could=1）：

  | ID | 能力 | PRD 优先级 | 权重 | 验证方式 |
  |---|---|---|---|---|
  | C1 | 统一事件受理 + 受理层去重（source + 业务事件 ID） | Must | 3 | 源码 + S1 |
  | C2 | 按 event_type 路由并扇出到多个目标 | Must | 3 | 源码 + S1 |
  | C3 | 按目标构造请求（URL、方法、Header、Body 模板映射） | Must | 3 | 源码 + S2 |
  | C4 | 按目标认证（静态 API Key Header、OAuth2 client credentials、自定义签名） | Must | 3 | 源码 |
  | C5 | 可配置成功判定（状态码集合 + 响应体字段匹配） | Must | 3 | 源码 + S3 |
  | C6 | 按目标重试策略（次数、退避 + jitter、可重试条件） | Must | 3 | 源码 + S4 |
  | C7 | 死信 + 单条人工重放（含审计） | Must | 3 | S5 |
  | C8 | 状态查询 API（event_id / 业务键 / 状态 / 时间范围） | Must | 3 | API 文档 + S5 |
  | C9 | 按目标隔离（慢目标不阻塞其他目标） | Must | 3 | S6 |
  | C10 | Event / Delivery / Attempt 持久化 | Must | 3 | 源码 |
  | C11 | OpenTelemetry 指标、追踪、日志 | Must | 3 | 文档 + S1 |
  | C12 | 按目标限流与熔断 | Should | 2 | 源码 |
  | C13 | 按 ordering_key 有序投递 | Should | 2 | 源码 + S7 |
  | C14 | 幂等键透传到对端 Header | Should | 2 | S1 |
  | C15 | 存储 / 日志中的字段级脱敏 | Should | 2 | 源码 |
  | C16 | 批量重放 | Could | 1 | 文档 |
  | C17 | 无云依赖、可混合部署、无状态水平扩展 | 约束 | 3 | 部署文档 + S8 |
  | C18 | 首次投递延迟 P99 < 1s（对端正常、低负载） | 约束 | 3 | S8 |

  非功能评估项（不计入能力分，单独结论）：
  - N1 许可证兼容性
  - N2 社区活跃度、维护者数量、发布节奏
  - N3 fork 后跟进上游的可行性：需改动的核心文件数，以及这些文件近 6 个月的提交频率
  - N4 团队维护该语言代码的能力，以及 AI 编码友好度

  决策规则（写入文档，评估前锁定，不得事后调整）：
  - **选择二次开发**的条件需同时满足：
    1. 候选方案能力加权得分 ≥ 满分的 70%
    2. C3、C4、C5 三项中没有任何一项得 0 分
    3. 所有得 1 分的项，改动集中在 ≤ 3 个核心模块
  - **否则选择自建**，并在 ADR 中列出可借鉴的设计（模块名 + 源码位置）
  - 两个候选都满足时，取加权得分高者；差距 < 5% 时取 N3 更优者
- **MIRROR**: SCORING_SCALE、EVIDENCE_CITATION
- **IMPORTS**: N/A
- **GOTCHA**: 决策规则必须在 Task 3/4 打分之前写定并提交，防止"先有结论后凑分"。PRD 中标为 Won't 的能力（脚本转换、exactly-once）不能成为加分项
- **VALIDATE**: 清单覆盖 PRD 中每一条 Must/Should 能力（逐条对照 PRD MoSCoW 表）；决策规则已单独提交一次 commit

### Task 3: Outpost 源码级评估
- **ACTION**: 编写 `docs/spike/02-outpost-evaluation.md`
- **IMPLEMENT**:
  - 先读 `AGENTS.md`、`CLAUDE.md`、`.outpost.yaml.example`，画出组件图：API → publishmq → deliverymq → worker → destregistry → logmq → logstore
  - 对 C1–C18 逐项给出分值、证据、缺失时的扩展方案与改动位置。重点核查：
    - `destregistry`：目标类型是否可插件化注册？能否新增"可模板化 HTTP 目标"而不改核心？（决定 C3/C4 是 1 分还是 2 分）
    - webhook 目标的成功判定代码位置：是否硬编码 2xx？（C5）
    - `backoff` / `scheduler`：重试策略是全局还是按目标？（C6）
    - `idempotence`：作用于受理还是投递？（C1 / C14）
    - 是否存在 per-destination 并发 / 限流；PRD 调研已知"按租户限流"被明确排除（C12）
    - tenant 模型能否映射为"来源业务系统"，以及这种映射是否别扭（影响 C2 与权限模型）
  - N3：对需改动的核心文件执行 `git log --since=6.months --oneline -- <path> | wc -l`（需要完整历史时，对该目录另行 `git fetch --unshallow`）
- **MIRROR**: EVIDENCE_CITATION、SCORING_SCALE
- **IMPORTS**: N/A
- **GOTCHA**: 不要把"多租户 portal""标准 webhook 签名"这类面向 SaaS 客户的能力算作加分项，它们与本场景无关。Outpost 的 tenant 是"推送给哪个客户"的概念，语义与我们的"来源业务系统"不同
- **VALIDATE**: C1–C18 全部有分值；每个分值有 EVIDENCE_CITATION 格式的证据，或标记 `UNVERIFIED`；加权得分已计算

### Task 4: Svix server（含 Bridge）源码级评估
- **ACTION**: 编写 `docs/spike/03-svix-evaluation.md`
- **IMPLEMENT**:
  - 读 `server/README.md`、`cfg.rs`、`worker.rs`、`queue/`、`v1/`，画出组件图
  - 对 C1–C18 逐项评分。重点核查：
    - `worker.rs`：成功判定与重试调度逻辑（C5、C6）；重试 schedule 是否可按 endpoint 配置
    - endpoint 模型是否支持自定义 Header；Body 是否只能原样发送（C3）
    - Bridge 的 senders 配置与 JS 转换：只记录"能做到什么"，按 PRD 决策该方式不采纳，C3 不因此加分
    - `queue/`：支持的后端（Redis / RabbitMQ）；是否按 endpoint 隔离（C9）
    - README 声明的"托管版有 OSS 不具备的功能"：列出 OSS 中确认缺失的能力
  - N3 同 Task 3
- **MIRROR**: EVIDENCE_CITATION、SCORING_SCALE
- **IMPORTS**: N/A
- **GOTCHA**: Svix 的 Application / Endpoint / Message 模型同样以"客户"为中心；Rust 代码的二次开发成本要结合 N4 评估
- **VALIDATE**: 同 Task 3

### Task 5: 模拟供应商与 PoC 场景
- **ACTION**: 编写 `spike/mock-vendor/mock_vendor.py` 与 `self_check.sh`，在两个候选方案上执行场景 S1–S8，结果记录到 `docs/spike/04-poc-results.md`
- **IMPLEMENT**:
  - **模拟供应商**：仅使用 Python 3 标准库（`http.server`、`json`、`threading`）
    - 按路径前缀区分供应商，如 `/vendor-a/...`、`/vendor-b/...`
    - 行为通过启动参数或 `POST /_control/<vendor>` 注入：
      - `status`：状态码
      - `delay_ms`：响应延迟
      - `body`：响应体
      - `fail_first_n`：前 N 次失败、之后成功
    - 所有收到的请求（时间戳、路径、Header、Body）追加写入 JSONL 文件
    - `GET /_requests/<vendor>` 返回已收到的请求；`DELETE /_requests` 清空记录
    - 使用 `ThreadingHTTPServer`，保证慢响应不阻塞其他供应商
    - 控制参数做类型校验，非法时返回 400 和错误信息，已有配置保持不变
  - **场景**（每个场景记录：配置片段、执行命令、预期、实际、结论）：
    - S1 基线：1 种事件类型扇出到 vendor-a、vendor-b，并带幂等键 → 两端各收到 1 次，检查 Header 中是否带幂等键，检查 OTel 输出
    - S2 请求构造：vendor-a 要求 Body 为 `{"uid": ..., "evt": ...}`，vendor-b 要求自定义 Header `X-Api-Key` → 能否仅靠配置做到
    - S3 成功判定：vendor-a 返回 HTTP 200 但 Body 为 `{"code": 1001}`，表示业务失败 → 能否被判定为失败
    - S4 重试：设置 `fail_first_n=3` → 观察重试次数与间隔，检查是否带 jitter
    - S5 死信与重放：持续返回 500 直到重试耗尽 → 查询该投递状态 → 人工重放 → 在 vendor 恢复后确认送达
    - S6 隔离：vendor-a 设置 `delay_ms=10000`，vendor-b 正常 → vendor-b 的投递延迟不受影响
    - S7 顺序：同一 ordering_key 的 3 个事件，第 1 个失败后 → 观察后续 2 个的行为
    - S8 部署与延迟：用官方 compose 启动，在低负载（例如 50 events/s，持续 60s）下统计"受理 → 首次请求到达 mock"的延迟 P50/P99；记录依赖组件清单，以及是否存在云依赖
  - `spike/scenarios/run_scenarios.md` 记录每个候选方案执行各场景的具体命令与配置
- **MIRROR**: EVIDENCE_CITATION（实测证据以 `04-poc-results.md#Sx` 引用）
- **IMPORTS**: Python 标准库：`http.server`、`socketserver`、`json`、`threading`、`time`、`argparse`、`urllib.parse`
- **GOTCHA**:
  - 容器内访问宿主机上的 mock 要使用 `host.docker.internal`；Linux 上需要在 compose 中加 `extra_hosts: ["host.docker.internal:host-gateway"]`
  - 部分候选方案默认禁止投递到私网地址（SSRF 防护），需要按其文档配置白名单，并在结果中记录这一点
  - 场景只要求能配置即可完成；做不到时记"不支持"，并引用 Task 3/4 中的源码证据，不要为了跑通去改候选方案的代码
  - S8 只是量级验证，不是正式压测，结论措辞要克制
- **VALIDATE**:
  - `bash spike/mock-vendor/self_check.sh` 全部输出 PASS
  - `04-poc-results.md` 中两个候选方案的 S1–S8 均有结果（通过 / 不支持 / 部分支持 / UNVERIFIED）

### Task 6: 自建技术栈评估
- **ACTION**: 编写 `docs/spike/05-stack-evaluation.md`
- **IMPLEMENT**: 三张对比表，每张表都有加权评分，权重来自 PRD 的选型原则。

  **A. 实现语言**：候选 Go / Java（Spring Boot 或 Quarkus）/ TypeScript（Node.js）/ Rust

  | 维度 | 权重 |
  |---|---|
  | 性能（高并发出站 HTTP、内存占用） | 3 |
  | 可维护性（类型系统、并发模型的可理解性） | 3 |
  | 开发速度 | 3 |
  | AI 编码友好度（语料丰富度、编译器与类型检查对生成代码的约束力、测试工具） | 3 |
  | Kafka / PostgreSQL / OpenTelemetry 客户端的成熟度 | 2 |
  | 混合环境部署（单二进制或容器镜像体积、启动速度） | 2 |
  | 可借鉴代码的语言（若自建，借鉴 Outpost 时 Go 更顺手） | 1 |

  **B. 调度队列与重试调度**：候选 Kafka / RabbitMQ / Redis Streams / PostgreSQL（`scheduled_at` + `FOR UPDATE SKIP LOCKED`）

  | 维度 | 权重 |
  |---|---|
  | 按目标隔离的实现方式（每个目标一个队列 / 分区键 / 动态队列）以及目标数增长时的代价 | 3 |
  | 延迟重试的实现方式（原生支持 / 插件 / 需另建调度器） | 3 |
  | 按 key 有序（partition key / single active consumer / 行锁） | 2 |
  | 吞吐上限与水平扩展 | 2 |
  | 混合环境运维复杂度（组件数量、集群管理） | 3 |
  | MVP 开发速度 | 2 |

  必须评估的组合：
  - 仅 PostgreSQL：状态存储与队列合一，MVP 组件最少
  - PostgreSQL + Kafka：状态与分发分离，扩展性最好，但重试调度需另建
  - PostgreSQL + Redis：ZSET 承担延迟重试

  对每个组合给出"从 MVP 方案演进到下一档方案的路径"，确保早期选型不会锁死后续扩展。

  **C. 状态存储**：候选 PostgreSQL / MySQL
  - 评估维度：分区表（按时间）、JSONB 查询、`SKIP LOCKED` 支持、生态成熟度、混合环境运维
  - 单独评估 Attempt 日志的存储策略：与主库同库分区表，还是后续迁往 ClickHouse 等列存。Outpost 的 logstore 有 ClickHouse 实现，可作参考
- **MIRROR**: SCORING_SCALE（此处改用 1–5 分制，并在文档开头说明）、EVIDENCE_CITATION
- **IMPORTS**: N/A
- **GOTCHA**:
  - 规模未知，不要按"百万 QPS"选型，也不要选一个日后无法演进的方案。评估重点是演进路径
  - 不要让 Task 3/4 的倾向影响这里的打分，这部分在"自建"的前提下独立评估
  - 若 ADR-0001 的结论是二次开发，语言实际由候选方案决定（Go 或 Rust），此时本任务的 A 表只用于确认团队能够维护该语言
- **VALIDATE**: 三张表都有加权得分与推荐项；每个推荐项写明"Revisit When"条件

### Task 7: 编写 ADR 并更新 PRD
- **ACTION**: 编写 `docs/adr/0001`–`0004` 与 `docs/adr/README.md`，并更新 PRD
- **IMPLEMENT**:
  - ADR-0001 二次开发 vs 自建：按 Task 2 锁定的决策规则得出结论。若结论是自建，列出"借鉴清单"（模块、源码位置、借鉴什么）
  - ADR-0002 语言、ADR-0003 调度与重试、ADR-0004 状态存储：结论来自 Task 6
  - `docs/adr/README.md`：编号、标题、状态、日期的索引表
  - `docs/spike/README.md`：一页结论摘要，加文档索引，并列出超出 timebox 未验证的事项
  - 更新 PRD：
    - Phase 1 状态改为 `complete`
    - Decisions Log 中"开源复用"一行改为定案，并新增技术栈三行（链接对应 ADR）
    - Open Questions 中勾选"自建 vs 二次开发""技术选型"两项
    - Research Summary 中 Outpost 的版本信息更新为 v1.7.0
- **MIRROR**: ADR_FORMAT
- **IMPORTS**: N/A
- **GOTCHA**:
  - ADR 状态先写 `Proposed`，评审通过后改为 `Accepted`。PRD Phase 1 在评审通过后才能标记为 `complete`；评审前保持 `in-progress`
  - 若结论与 PRD 中"初步倾向自建"相反，必须在 PRD Decisions Log 中如实更新，并说明推翻的理由
- **VALIDATE**:
  - `ls docs/adr/000{1,2,3,4}-*.md` 四个文件都存在
  - `grep -L "## Decision" docs/adr/0*.md` 输出为空，即每个 ADR 都有 Decision 段
  - `grep -n "UNVERIFIED" docs/spike/*.md` 中列出的每一项，都已同步写入 `docs/spike/README.md` 的未验证事项清单

---

## Testing Strategy

本阶段不产出生产代码，"测试"指评估过程本身的可复现性，以及模拟供应商的正确性。

### Unit Tests（模拟供应商自检，`spike/mock-vendor/self_check.sh`）

| Test | Input | Expected Output | Edge Case? |
|---|---|---|---|
| 记录请求 | `POST /vendor-a/hook`，Body 为 `{"a":1}` | `GET /_requests/vendor-a` 返回的内容包含该 Body | 否 |
| 注入状态码 | 设置 `status=503`，然后发送请求 | 返回 503 | 否 |
| 前 N 次失败 | 设置 `fail_first_n=2`，连续发送 3 次请求 | 依次返回 500、500、200 | 是 |
| 延迟隔离 | vendor-a 设置 `delay_ms=3000`，同时请求 vendor-b | vendor-b 在 100ms 内返回 | 是（并发） |
| 非法控制参数 | `POST /_control/vendor-a`，Body 为 `{"status":"abc"}` | 返回 400 和错误信息，已有配置不变 | 是 |
| 清空记录 | `DELETE /_requests` | 再次查询返回空列表 | 否 |

这些测试只覆盖验证阶段工具的正确性，不计入项目覆盖率要求；不为 PoC 工具引入测试框架。

### Edge Cases Checklist
- [ ] 空 Body 请求（mock 能正常记录）
- [ ] 大 Body（约 1MB，mock 不崩溃）
- [ ] 非法 JSON 控制参数（返回 400）
- [ ] 并发请求（ThreadingHTTPServer，慢目标不阻塞其他目标）
- [ ] 网络失败：S5 中候选方案对连接拒绝（mock 停止）的处理
- [ ] Permission denied：N/A（mock 不做认证，只记录认证 Header 用于 S2）

---

## Validation Commands

### Static Analysis
```bash
python3 -m py_compile spike/mock-vendor/mock_vendor.py
bash -n spike/mock-vendor/self_check.sh
```
EXPECT: 无输出（语法检查通过）

### Unit Tests
```bash
bash spike/mock-vendor/self_check.sh
```
EXPECT: 6 项全部输出 `PASS`，退出码为 0

### Full Test Suite
```bash
# 文档完整性检查
for f in docs/spike/0{1,2,3,4,5}-*.md docs/adr/000{1,2,3,4}-*.md docs/adr/README.md docs/spike/README.md; do test -s "$f" || echo "MISSING: $f"; done
grep -n "TBD" docs/spike/*.md docs/adr/*.md
```
EXPECT: 没有 `MISSING` 输出；`TBD` 只出现在明确标注为超出 timebox 的条目中

### Database Validation
N/A — 本阶段不创建 schema。

### Browser Validation
N/A

### Manual Validation
- [ ] 评估清单中的决策规则，其提交时间早于两份候选评估文档
- [ ] 抽查 3 条源码证据，确认在钉死版本的源码中位置正确
- [ ] 两个候选方案的 S1–S8 结果均可按 `run_scenarios.md` 复现
- [ ] ADR-0001 的结论可以由 checklist 中的规则和两份评估的得分机械推导出来
- [ ] ADR 评审（技术负责人）通过，状态改为 `Accepted`

---

## Acceptance Criteria
- [ ] 所有任务完成
- [ ] 所有验证命令通过
- [ ] 模拟供应商的自检通过
- [ ] 评估清单覆盖 PRD 中全部 Must/Should 能力
- [ ] 每个评分都有证据，或标记为 `UNVERIFIED`
- [ ] ADR 0001–0004 已完成并通过评审
- [ ] PRD 已更新：Phase 1 状态、Decisions Log、Open Questions

## Completion Checklist
- [ ] 文档遵循 ADR_FORMAT、EVIDENCE_CITATION、SCORING_SCALE 约定
- [ ] 第三方源码只在 `.spike/` 中，未提交进仓库
- [ ] 未修改或 fork 任何候选项目
- [ ] mock 的端口、路径等参数通过命令行传入，没有散落的硬编码
- [ ] 没有超出 NOT Building 范围的工作
- [ ] 计划自洽：执行过程中不需要再提问

## Risks
| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| 候选项目本地部署困难，PoC 跑不通 | M | M | 以源码证据为主、PoC 为辅；跑不通的场景记为 `UNVERIFIED`，不阻塞决策 |
| 先入为主（PRD 已倾向自建）导致评估偏差 | M | H | 决策规则先于评分锁定并提交；评分全部附证据 |
| 超出 timebox | M | M | 先做 Task 2 → 3/4 → 7（ADR-0001），Task 5 的 S6–S8 和 Task 6 的细节可以压缩 |
| 量级测试结论被误当作容量基线 | L | M | S8 的结论措辞明确为"量级验证"，正式压测在 Phase 7 |
| 候选项目版本快速迭代（Outpost 2026-10-23 刚发布 v1.7.0） | M | L | 版本钉死；ADR 的 "Revisit When" 写明上游重大版本变化时复评 |

## Notes
- 执行顺序建议：Task 1 → Task 2（单独提交）→ Task 3 和 Task 4 并行 → Task 5 → Task 6（可与 Task 3/4/5 并行）→ Task 7
- Task 3/4 适合交给两个独立的子 agent 并行完成；主线程只做汇总，并按决策规则得出结论
- 模拟供应商在 Phase 4（投递引擎集成测试）和 Phase 7（故障注入）中可以继续复用，届时视语言选型决定是否重写
- 自建时的借鉴清单是 ADR-0001 最有价值的产出之一，直接影响 Phase 2 的模型设计
