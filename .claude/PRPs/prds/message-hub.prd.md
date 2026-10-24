# Message Hub — 外部通知可靠投递中枢

## Problem Statement

企业内多个业务系统（注册、支付、订单等）在关键事件发生时，需要调用不同外部供应商的 HTTP(S) API 进行通知，而各供应商的地址、Header、Body 格式、认证方式、成功判定标准各不相同。目前若由各业务系统各自实现调用与重试，会导致可靠性参差不齐、失败无法追溯、接入新供应商需改代码发版；外部系统抖动时通知可能静默丢失，进而造成广告归因缺失、CRM 状态不一致、库存偏差等业务损失。

## Evidence

- Assumption - 痛点为预判（重试缺失、静默丢失、排障困难、接入成本高），尚无事故记录或量化数据，需在开发和试运行过程中通过实际接入方反馈与投递数据持续验证
- Assumption - 业务规模（QPS、日事件量、供应商数量）未知，需在首批业务接入后通过监控数据确认
- 业界佐证：出站 Webhook / 事件投递是成熟问题域，已有 Hookdeck Outpost、Svix 等开源实现，说明"可靠投递需要独立基础设施"是普遍共识

## Proposed Solution

构建一个内部基础服务 **Message Hub**：业务系统以**统一事件格式**提交事件，Hub 负责持久化、按配置路由并扇出到一个或多个外部目标，按目标端配置完成请求构造（基于预定义数据格式的简单模板映射，不引入脚本或复杂 DSL）、认证、发送与成功判定，并对失败投递执行重试、死信、告警与人工重放。所有事件与投递尝试集中记录，供业务系统、运维、运营/客服按权限查询。

选择"集中中转层 + 统一数据模型"而非"各业务直连"或"业务方直接用 MQ 自写消费者"，是因为只有在中转层才能统一做状态管理、存储、可观测性与运维操作。初步调研显示开源方案（Outpost、Svix）面向"SaaS 推送给客户"模型，缺少按供应商的转换/认证/成功判定/限流等核心能力，倾向基于成熟组件自建，最终结论由 Phase 1 技术验证确定。

## Key Hypothesis

We believe 一个基于统一事件模型、按目标端可配置投递策略的 Message Hub will 让业务系统无需自行实现投递逻辑即可可靠地通知外部供应商 for 企业内各业务系统开发者及其运维/运营/客服人员.
We'll know we're right when 首批接入业务上线后：失败消息零静默丢失且 100% 可追溯、可重放；接入一个新外部目标只需修改 Hub 配置而无需业务代码变更发版。

## What We're NOT Building

- **端到端幂等 / exactly-once 保障** - HTTP 超时场景下无法确知对端是否已处理，严格 exactly-once 不可实现；"仅一次"语义由业务模块与外部模块共同约束（Hub 透传业务提供的幂等键，见下文）
- **通用转换引擎（脚本 / JSONata 等 DSL）** - 避免为通用性引入复杂度与沙箱安全风险；以定义良好的数据格式 + 简单模板映射满足需求
- **业务系统在请求中指定/覆盖目标地址（方案 B）** - 路由与目标全部在 Hub 配置（方案 A），降低 SSRF 风险；未来有极特殊需求再评估
- **终态回调（push）通知业务系统** - MVP 仅提供状态查询（pull）
- **Hub 与其他系统的整体交互设计**（入站 Webhook、内部服务事件总线、终端用户通知、与运营/客服系统的集成方式） - 在更高维度另行讨论，本 PRD 只聚焦 Hub 模块本身
- **云托管 / 商业 SaaS 投递服务** - 不使用；仅采用开源或自建组件（Kafka / RabbitMQ / Redis / 关系型数据库等）

## Success Metrics

所有指标均被视为重要，目标值在获得真实流量后校准。

| Metric | Target | How Measured |
|--------|--------|--------------|
| 静默丢失率 | 0（每条已受理事件都有可查询的终态或进行中状态） | 对账任务：已受理事件 vs 投递记录 |
| 投递成功率（排除对端永久性错误） | ≥ 99.9%（TBD - 首批流量后校准） | 投递终态统计 |
| 投递延迟（受理 → 首次尝试发出，对端正常时） | P99 < 1s | OpenTelemetry 指标 |
| Ingest API 可用性 | ≥ 99.9%（TBD） | 健康检查 + SLO 监控 |
| 新外部目标接入耗时 | 仅改配置，无需业务代码发版；目标 < 1 天 | 接入记录 |
| 失败消息可追溯 / 可重放比例 | 100% | 死信记录与重放审计 |

## Open Questions

- [ ] 实际业务规模：峰值 QPS、日事件量、外部目标数量、单事件平均扇出数
- [ ] 各供应商的限流规则、幂等支持能力、认证方式（API Key / OAuth / 签名）清单
- [ ] 事件与投递记录的数据保留时长、归档策略
- [ ] 各供应商的脱敏要求（字段级哈希、加密存储范围）
- [x] 配置（路由、目标、策略）变更的管理方式：配置文件 + 版本库 / 管理 API / 审批流 → YAML + Git，ADR-0005
- [ ] 混合环境下的固定出口 IP 方案（供应商 IP 白名单）
- [x] Outpost / Svix 二次开发 vs 组件自建的最终结论（Phase 1 输出）→ 自建，ADR-0001（Accepted）
- [x] 技术栈（语言、MQ、状态存储）的最终选型（Phase 1 输出）→ Go + PostgreSQL，ADR-0002 至 0004（Accepted）
- [x] 有序投递的阻塞策略：同一顺序键的前序事件进入死信时，后续事件是阻塞、跳过还是一并进入死信 → 按策略可配，默认阻塞，ADR-0007
- [ ] `unrouted` 事件在补充路由配置后，是否需要提供"重新路由"能力（运维对已受理事件按当前配置重新匹配并生成 Delivery）
- [x] secret manager 选型与密钥注入方式 → 当前阶段 Kubernetes Secret（ADR-0008 D3，`docs/deploy/k8s-secrets.md`）
- [ ] 后续接入的 secret manager（Vault / 云 KMS 等），以及非 Kubernetes 环境（虚拟机）的密钥注入方式

---

## Users & Context

**Primary User — 业务系统开发者**
- **Who**: 注册、支付、订单等业务系统的后端开发者，负责在业务事件发生时通知外部供应商
- **Current behavior**: 在业务代码中直接调用供应商 API，各自实现（或缺失）重试与日志
- **Trigger**: 新业务需要对接新供应商；或已有供应商出现通知失败需要排查
- **Success state**: 只需按统一格式提交事件，即可依赖 Hub 完成可靠投递；需要时可查询投递状态并据此做业务处理

**Secondary Users**
- **运维 / SRE / 值班人员**: 监控投递健康度，处理告警、死信与人工重放
- **运营 / 客服**: 按权限查询某用户/订单相关通知的投递情况（直接查询或经由运营/客服系统接入）

**Job to Be Done**
When 我的业务发生需要通知外部供应商的关键事件, I want to 按统一格式提交一个事件并可在需要时查询其投递状态, so I can 专注业务逻辑，不必为每个供应商编写调用、重试和排障代码，同时在投递失败时仍能做出业务处理。

**Non-Users**
- 需要接收外部回调（入站 Webhook）的系统
- 内部服务之间的事件通信（直接使用 MQ）
- 面向终端用户的通知（短信、邮件、推送）

以上场景本期不考虑，属于更高维度的系统交互设计范畴。

---

## Solution Detail

### 核心概念模型（草案；字段级定义与行为规格见 [docs/design/domain-model.md](../../../docs/design/domain-model.md)）

| 概念 | 说明 |
|------|------|
| **Event（事件）** | 统一信封：`event_id`、`event_type`、`source`（来源业务系统）、`occurred_at`、`idempotency_key`（可选，业务提供，透传给对端）、`ordering_key`（可选）、`payload`（结构化业务数据）、`metadata` |
| **Destination（外部目标）** | 一个供应商端点：URL、HTTP 方法、静态/模板化 Header、Body 模板（基于 Event 字段的简单映射）、认证配置、超时、脱敏规则 |
| **Route（路由 / 订阅）** | `event_type`（+ 可选来源/简单条件）→ 一个或多个 Destination，实现扇出 |
| **Delivery Policy（投递策略）** | 绑定在 Route 或 Destination 上：最大重试次数、退避策略（指数 + jitter）、可重试条件、**成功判定规则**（状态码集合，可选响应体字段匹配）、顺序要求（无序 / 按 `ordering_key` 有序）、耗尽后处置（死信 + 告警） |
| **Delivery（投递任务）** | 一个 Event × 一个 Destination 的投递单元，拥有独立状态机 |
| **Attempt（投递尝试）** | 每次实际 HTTP 调用的记录：请求摘要（脱敏后）、响应码、响应摘要、耗时、错误原因 |

**Delivery 状态机（草案）**

```
PENDING → IN_FLIGHT → SUCCEEDED
              │
              ├─→ RETRY_SCHEDULED → IN_FLIGHT …
              │
              └─→ DEAD（重试耗尽 / 不可重试错误）→ 告警
                    │
                    └─ 人工重放 → PENDING（保留原记录与重放审计）
```

### Core Capabilities (MoSCoW)

| Priority | Capability | Rationale |
|----------|------------|-----------|
| Must | 统一事件模型 + Ingest API（校验、持久化后返回 `event_id`；按 `source` + 业务事件 ID 去重，防止业务方提交重试产生重复事件） | 统一处理的前提；受理即持久化是"零静默丢失"的基础 |
| Must | 路由与扇出（`event_type` → 多个 Destination，全部在 Hub 配置） | 多目标通知是核心场景 |
| Must | 按 Destination 的请求构造（简单模板映射）与认证 | 屏蔽供应商格式差异 |
| Must | 可配置的成功判定规则 | 各供应商"成功"定义不同，平台不预设 |
| Must | 重试（指数退避 + jitter）、死信、人工重放 | 可靠投递与失败处置的核心 |
| Must | 状态查询 API（按 event_id / 业务键 / 时间范围 / 状态，带权限控制） | 业务系统需据状态做业务处理；运维/运营/客服排查 |
| Must | 持久化的 Event / Delivery / Attempt 记录 | 可追溯、可审计、可重放 |
| Must | 基础可观测性：指标、链路追踪、结构化日志，死信与失败率告警 | 运维必需 |
| Must | 按 Destination 隔离的调度（避免慢供应商阻塞其他目标） | 业界共识的 head-of-line blocking 规避方案 |
| Should | 按 `ordering_key` 的有序投递（策略可选） | 存在有前后关联的场景（如下单→取消），但并非所有场景需要 |
| Should | 按 Destination 的限流与熔断 | 供应商普遍有限流；保护对端与自身 |
| Should | 幂等键透传（Header 注入） | 便于业务与对端共同实现"仅一次"效果 |
| Should | 字段级脱敏（存储与日志中） | 合规；按供应商要求细化 |
| Could | 运营 / 客服查询界面 | 可先通过 API 满足 |
| Could | 配置管理界面 | 可先通过配置文件 / 管理 API 满足 |
| Could | 批量重放（按条件筛选死信） | 大面积故障恢复时提效 |
| Won't | 端到端幂等 / exactly-once 保障 | 技术上不可实现，由业务与对端约束 |
| Won't | 脚本 / DSL 通用转换引擎 | 复杂度与安全风险，当前不需要 |
| Won't | 业务方指定目标地址（方案 B） | 安全与定位考量，按需再评估 |
| Won't | 终态回调 push | MVP 仅 pull |
| Won't | 多区域部署 | 规模未明确前不做 |

### MVP Scope

打通一条完整链路：**1 种事件类型，扇出到 2 个外部目标**（各自不同的请求模板、认证方式与成功判定规则），覆盖：

- 统一事件格式受理与持久化
- 路由与扇出
- at-least-once 投递、指数退避重试、死信、人工重放
- 状态查询 API
- 基础指标、追踪与告警

外部目标在 MVP 阶段可用模拟供应商（可注入延迟、错误码、超时）验证，再接入首个真实业务。

### User Flow

1. （一次性）运维/开发在 Hub 中配置 Destination、Route、Delivery Policy
2. 业务系统发生事件 → 按统一格式调用 Ingest API → 得到 `event_id`（已持久化）
3. Hub 按路由扇出生成 Delivery → 构造请求 → 发送 → 按规则判定成功
4. 失败则按策略重试；耗尽进入死信并告警
5. 业务系统按需查询状态并做业务处理；运维按需人工重放

---

## Technical Approach

**Feasibility**: HIGH — 可靠异步投递是业界成熟模式，难点在配置模型与策略层设计，而非技术突破。

**Architecture Notes**
- **受理即持久化**：Ingest API 在事件写入持久存储后才返回成功，确保零静默丢失
- **状态存储与调度队列分离**：数据库承载 Event / Delivery / Attempt 状态（可查询、可重放）；消息队列 / 流负责调度分发。避免把 MQ 当唯一状态存储导致无法按条件查询与精确重放
- **按 Destination 分区 / 隔离**：调度队列、并发与限流按目标端隔离；有序策略下按 `ordering_key` 分区串行
- **无状态 Worker 水平扩展**：Ingest、Delivery Worker、重试调度器均可独立扩缩容，应对规模不确定性
- **请求构造采用简单模板映射**：基于统一 Event 字段的变量替换，不引入脚本执行
- **混合环境部署**：容器化交付，不依赖特定云厂商服务；需规划固定出口 IP 以满足供应商白名单
- **可观测性**：OpenTelemetry 标准（指标 / 追踪 / 日志），事件全链路可按 `event_id` 关联
- **技术选型原则**：性能、可维护性、开发速度、水平扩展能力、混合环境运维成熟度、AI 编码友好度（生态成熟、类型清晰、测试工具完善）；候选组件 Kafka / RabbitMQ / Redis Streams + PostgreSQL 等，Phase 1 定案
- **AI Vibe Coding 交付**：以规格文档、契约测试、故障注入测试作为质量护栏

**Technical Risks**

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| 慢供应商 / 热点目标拖垮整体投递 | M | 按目标隔离队列与并发；超时 + 熔断 |
| 状态存储写入压力随规模增长成为瓶颈 | M | 早期设计分区表与归档策略；Attempt 记录可降级存储 |
| PostgreSQL 派发达到吞吐上限，需要迁移到 MQ | M | ADR-0009：PostgreSQL 始终是状态唯一来源，MQ 只派发；Phase 4 先落实 `Dispatcher` 接口与幂等领取；迁移按"影子 → 试点 → 全量"分步进行，任一步可回滚 |
| 有序投递与重试、死信交互复杂（前序失败阻塞后续） | M | 有序作为可选策略；阻塞策略列入 Open Questions，Phase 2 明确 |
| 简单模板映射无法覆盖某些供应商格式 | L-M | 先满足已知供应商；不满足时评估扩展模板能力，而非直接引入脚本 |
| 混合环境出口 IP 不固定导致供应商白名单失败 | M | 部署设计中预留出口网关 / NAT 方案 |
| 敏感数据在存储与日志中泄露 | M | 字段级脱敏规则；日志默认只记录摘要 |
| 开源方案评估结论偏差（自建 vs 二次开发） | L | Phase 1 用对照清单限时验证 |
| Vibe Coding 产出质量不稳定 | M | 规格先行、TDD、契约测试、故障注入、代码评审 |

---

## Implementation Phases

<!--
  STATUS: pending | in-progress | complete
  PARALLEL: phases that can run concurrently (e.g., "with 3" or "-")
  DEPENDS: phases that must complete first (e.g., "1, 2" or "-")
  PRP: link to generated plan file once created
-->

| # | Phase | Description | Status | Parallel | Depends | PRP Plan |
|---|-------|-------------|--------|----------|---------|----------|
| 1 | 技术验证与选型 | Outpost / Svix 对照评估，确定自建 vs 二次开发；确定语言、MQ、状态存储 | complete | - | - | [plan](../plans/completed/tech-spike-and-selection.plan.md) · [report](../reports/tech-spike-and-selection-report.md) |
| 2 | 领域模型与契约 | 统一事件格式、配置模型（Destination / Route / Policy）、状态机、API 契约、数据库 schema | complete | - | 1 | [plan](../plans/completed/domain-model-and-contracts.plan.md) · [report](../reports/domain-model-and-contracts-report.md) |
| 3 | 受理与查询 | Ingest API、持久化、去重、状态查询 API、权限 | pending | with 4 | 2 | - |
| 4 | 投递引擎 | 路由扇出、模板构造、认证、发送、成功判定、重试、死信 | pending | with 3 | 2 | - |
| 5 | 运维能力 | 人工重放、指标 / 追踪 / 日志、告警、对账任务 | pending | with 6 | 3, 4 | - |
| 6 | 稳健性增强 | 按目标限流与熔断、有序投递、幂等键透传、脱敏 | pending | with 5 | 4 | - |
| 7 | 验证与首批接入 | 模拟供应商 + 压测 + 故障注入；首个真实业务接入 | pending | - | 5, 6 | - |

### Phase Details

**Phase 1: 技术验证与选型**
- **Goal**: 用证据决定"二次开发 Outpost/Svix"还是"基于组件自建"，并定案技术栈
- **Scope**: 按本 PRD Must 能力逐项对照两个项目（转换/认证、成功判定、策略、限流、重放、扩展性）；评估候选语言与 MQ / 存储组合；输出 ADR
- **Success signal**: ADR 评审通过，包含对照清单与选型理由

**Phase 2: 领域模型与契约**
- **Goal**: 固化数据格式与配置模型，作为后续 AI 编码的规格基础
- **Scope**: Event 信封 schema、Destination / Route / Delivery Policy 配置 schema、Delivery 状态机、Ingest / Query / Replay API 契约、有序投递阻塞策略决定
- **Success signal**: schema 与 API 契约文档化并评审通过；契约测试骨架就绪

**Phase 3: 受理与查询**
- **Goal**: 事件可被可靠受理并可查询
- **Scope**: Ingest API（校验、持久化、去重、返回 event_id）；查询 API（多维筛选、分页、权限）
- **Success signal**: 受理接口在故障注入下不丢已确认事件；查询覆盖 Must 场景

**Phase 4: 投递引擎**
- **Goal**: 事件按配置可靠投递到多个外部目标
- **Scope**: 路由扇出、按目标隔离调度、模板请求构造、认证、HTTP 发送、可配置成功判定、指数退避重试、死信
- **必做的迁移前置要求**（[ADR-0009](../../../docs/adr/0009-pg-to-mq-migration-path.md)，保证日后迁移到 MQ 时不重写 worker）：
  - M1 `Dispatcher` 接口（领取 / 确认结果 / 重排程），worker 只依赖接口，MVP 实现为 `pgDispatcher`
  - M2 幂等领取：状态迁移一律经条件更新（`WHERE id=$1 AND status IN (...)`）完成，SKIP LOCKED 只用于找出候选 id
  - P1–P5 PostgreSQL 队列陷阱的对策：HTTP 调用在事务之外；deliveries 的 autovacuum 与 `fillfactor` 调优；每个受理事务最多一次无 payload 的 NOTIFY；LISTEN 使用直连数据库的专用连接；轮询兜底
- **Success signal**: 对模拟供应商（错误码、超时、延迟）的投递行为与策略配置一致；黄金用例全部通过；多 worker 竞争领取、重复领取同一 id、lease 过期接管、唤醒丢失后轮询兜底，四类测试全部通过

**Phase 5: 运维能力**
- **Goal**: 失败可感知、可处置、可追溯
- **Scope**: 单条 / 批量人工重放（含审计）、OpenTelemetry 指标与追踪、告警规则、受理 vs 投递对账
- **Success signal**: 死信产生即告警；任意 event_id 可完整追溯并重放

**Phase 6: 稳健性增强**
- **Goal**: 满足供应商约束与差异化场景
- **Scope**: 按目标限流与熔断、按 ordering_key 有序投递、幂等键透传、字段级脱敏
- **Success signal**: 限流 / 熔断 / 有序场景的集成测试通过；存储与日志中无明文敏感字段

**Phase 7: 验证与首批接入**
- **Goal**: 验证核心假设
- **Scope**: 压测确定容量基线；故障注入（对端宕机、Hub 节点崩溃、存储抖动）；接入首个真实业务
- **Success signal**: 满足 Success Metrics 的延迟与零静默丢失目标；新目标接入仅改配置

### Parallelism Notes

- Phase 3（受理与查询）与 Phase 4（投递引擎）共享 Phase 2 的模型与契约，彼此仅通过存储与队列交互，可并行开发
- Phase 5（运维能力）与 Phase 6（稳健性增强）分别建立在已完成的受理/投递链路之上，关注点互不重叠，可并行
- Phase 1、2 为串行前置：选型与契约决定后续所有实现

---

## Decisions Log

| Decision | Choice | Alternatives | Rationale |
|----------|--------|--------------|-----------|
| 产品定位 | Message Hub（统一事件模型 + 中转层集中管理） | 简单 HTTP 透传代理；业务方直接使用 MQ | 只有中转层才能统一做状态管理、存储、可观测与运维 |
| 投递语义 | at-least-once，策略按业务可配置 | 平台统一语义 | 不同业务对可靠性定义不同 |
| 幂等 / 仅一次 | Hub 不提供端到端幂等保障；可透传业务幂等键 | Hub 内置去重保证 exactly-once | HTTP 超时下不可实现；由业务与对端共同约束 |
| 受理去重 | Ingest 层按来源 + 业务事件 ID 去重 | 不去重 | 防止业务方提交重试产生重复事件，属受理层基本保障，非端到端幂等 |
| 成功判定 | 按目标可配置，平台不预设 | 固定 2xx 判定 | 各供应商业务语义不同 |
| 转换层 | 定义良好的数据格式 + 简单模板映射 | 脚本 / JSONata 等 DSL | 避免过度通用化与沙箱风险 |
| 目标地址来源 | 方案 A：全部在 Hub 配置 | 方案 B：业务请求指定/覆盖 | 安全（防 SSRF）、符合 Hub 定位；B 按需再评估 |
| 状态反馈 | MVP 仅 pull（查询 API） | 终态回调 push | 降低 MVP 复杂度 |
| 顺序 | 按业务需要可选（按 ordering_key 有序 / 无序） | 全局有序；全部无序 | 部分场景有前后关联，其余不需要 |
| 扇出 | 支持一个事件路由到多个目标 | 单目标 | 核心业务场景 |
| 外部服务 | 不使用云 / 商业服务，采用开源组件或自建 | AWS EventBridge、Hookdeck、Svix Cloud | 业务决策 |
| 开源复用 | 基于开源组件自建，系统借鉴 Outpost / Svix 的设计（[ADR-0001](../../../docs/adr/0001-build-vs-extend.md)，Accepted） | 基于 Outpost / Svix 二次开发 | 按锁定的决策规则：Outpost 82/144、Svix 88/144，均低于 101，且改动面超过 3 个核心模块；两者都存在与零静默丢失冲突的设计 |
| 实现语言 | Go（[ADR-0002](../../../docs/adr/0002-implementation-language.md)，Accepted） | Java、TypeScript、Rust | 加权 76/85；单一二进制适合混合环境；AI 编码友好；与借鉴代码同语言 |
| 分发与重试 | MVP 只用 PostgreSQL（SKIP LOCKED + LISTEN/NOTIFY），通过 Dispatcher 接口可演进（[ADR-0003](../../../docs/adr/0003-dispatch-and-retry-queue.md)，Accepted） | PG+Kafka、PG+Redis、PG+RabbitMQ | 加权 64/75；受理事务即 outbox，从架构上保证零静默丢失；组件最少 |
| 迁移到 MQ 的路径 | PostgreSQL 始终是状态唯一来源，MQ 只派发 delivery_id；幂等领取；重试调度留在 PostgreSQL；按目标切换、分步迁移、可回滚（[ADR-0009](../../../docs/adr/0009-pg-to-mq-migration-path.md)） | 迁移后以 MQ 为主；引入 pgmq 等扩展；MVP 直接上 MQ | 用户判断未来大概率迁移到 MQ（2026-10-24）；该路径迁移时只需替换 Dispatcher 实现并增加中继 |
| 状态存储 | PostgreSQL，Attempt 同库分区，可演进到列存（[ADR-0004](../../../docs/adr/0004-state-store.md)，Accepted） | MySQL 8 | 加权 58/60；部分索引与 NOTIFY 是 ADR-0003 的前提 |
| 配置管理 | YAML + Git，CI 校验，凭据只写 `env:` 引用（[ADR-0005](../../../docs/adr/0005-config-as-yaml-in-git.md)） | DB + Admin API；两者都做 | 配置变更直接影响对外请求，先评审后生效；Git 自带审计 |
| 模板能力 | 字段路径 + 12 个固定内置函数，无条件与循环（[ADR-0006](../../../docs/adr/0006-template-language-scope.md)） | 只做字段映射；Go text/template | 覆盖哈希、签名等需求，同时保持可预测、易评审 |
| 有序投递 | 按 `(destination, ordering_key)` 保证受理顺序；前序 DEAD 时按策略 block / skip，默认 block（[ADR-0007](../../../docs/adr/0007-ordering-semantics.md)） | 一律阻塞；一律跳过 | 库存类业务需要阻塞，其他业务可选跳过 |
| 无匹配路由的记录方式 | 显式 `routing_status`（routed / unrouted）+ `routing_config_hash`，数据库约束保证一致 | 只看 `matched_route_ids` 是否为空 | 用户要求（2026-10-24）；便于筛选、统计与告警 |
| 密钥管理 | 配置只写 `env:` 引用；敏感名称字段禁止字面量（`allow_literal` 显式例外）；attempt 记录在截断前脱敏；密钥只从环境变量注入；入站 API Key 为 256 位随机值并支持双哈希轮换（[ADR-0008](../../../docs/adr/0008-secrets-management.md)） | 依赖评审发现明文；只对 Header 脱敏 | 用户要求（2026-10-24）；把"禁止明文"做成加载失败，配置校验与记录脱敏共用一条规则 |
| 部署 | 混合环境 | 单一云 / 单一 IDC | 业务要求 |
| 范围边界 | 只聚焦 Hub 模块本身 | 同时设计上下游交互 | 系统间交互在更高维度讨论 |
| 交付方式 | AI Vibe Coding，尽快上线 | 传统排期 | 业务决策；以规格与测试作质量护栏 |

---

## Research Summary

**Market Context**
- [Hookdeck Outpost](https://hookdeck.com/docs/outpost/overview)（[GitHub](https://github.com/hookdeck/outpost)，最新 v1.7.0 / 2026-10-23）：Go，Apache-2.0，依赖 Redis + PostgreSQL + MQ；支持 topic 订阅扇出、重试、手动重放、OpenTelemetry；**明确不内置转换与按租户限流**；at-least-once
- [Svix server](https://github.com/svix/svix-webhooks)：Rust，MIT，依赖 PostgreSQL（Redis 可选）；支持事件类型过滤、OpenTelemetry、DLQ 指标；托管版有开源版不具备的功能，转换能力在 OSS 中未见说明
- Convoy：据 [Svix 对比页](https://svix.com/alternatives/convoy/)称已转为副业维护，不建议生产使用（厂商对比，需核实）
- 上述方案均面向"SaaS 平台推送给客户 endpoint"模型；本项目是"内部系统调用异构供应商 API"，转换、认证、成功判定、供应商限流是核心差异
- 业界共识（[Convoy outbox 文章](https://www.getconvoy.io/blog/webhooks-with-transactional-outbox)、[dev.to: webhooks at scale](https://dev.to/antfarm-official/webhooks-at-scale-designing-an-idempotent-replay-safe-and-observable-webhook-system-7lk)）：at-least-once + 稳定幂等键实现"效果上的仅一次"；幂等键应源自业务对象 + 动作；按目标隔离防 head-of-line blocking；指数退避 + jitter；死信 + 重放；生产方可采用 Transactional Outbox 保证不丢
- 反模式：MQ 作为唯一状态存储；宣称 exactly-once；所有目标共用单队列

**Technical Context**
- 全新项目，仓库中仅有原始需求文档，无既有代码约束
- 无已有基础设施约束（无既定 Kafka 集群、K8s 平台或监控体系）
- 关键设计要点：受理即持久化、状态存储与调度队列分离、按目标隔离、无状态 Worker 水平扩展

---

*Generated: 2026-10-24*
*Status: DRAFT - needs validation*
