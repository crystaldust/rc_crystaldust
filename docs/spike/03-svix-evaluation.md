# 03 · Svix server（含 Bridge）源码级能力评估

> 评估对象：`svix/svix-webhooks` tag `server-v1.102.1`（commit `6e014fd`，2026-10-20），本地只读克隆 `.spike/src/svix`。
> 评分规则、权重、证据格式与决策规则均按 `01-evaluation-checklist.md` 执行，未作任何调整。
> 本文所有分值基于源码与仓库内文档。PoC（S1–S8）已于 2026-10-24 补跑，全部与本文结论一致，分数无调整；另发现一个新缺陷：修改 Header 不会让配置缓存失效（见 `04-poc-results.md` §S2）。
> 证据前缀统一为 `svix@server-v1.102.1:`。队列底层库 omniqueue 不在仓库内，按 `server/svix-server/Cargo.toml:L62` 钉死的 rev 通过 GitHub API 只读获取，前缀为 `omniqueue@acb4098:`。

## 1. 结论摘要

- **加权得分：88 / 144（61.1%）**。
- Svix 的设计目标是"SaaS 把同一份 JSON 推给客户的 endpoint"：Body 恒为原始 payload，方法恒为 POST，成功恒为 2xx，重试表全局唯一，全部 endpoint 共用一条 Redis Stream。我们的核心差异化能力（C3/C4/C5/C6/C9/C12/C13/C14/C15）在 OSS 版中都没有扩展点，只能改核心代码。
- 能直接复用、质量较好的部分：受理与持久化（C1/C10）、event_type 扇出（C2）、查询 API（C8）、部署形态（C17）、首投路径（C18）。
- 托管版有、OSS 没有的功能，恰好与我们的缺口重合：endpoint OAuth、Transformation、mTLS、限流状态、bulk-replay、replay-missing、FIFO destination。OSS 中的 `throttleRate` 字段会被接收并持久化，但 worker 从不执行。
- **决策规则（仅 Svix）**：条件 1 ✗（88 < 101）；条件 2 ✓（C3/C4/C5 均为 1 分，无 0 分）；条件 3 ✗（1 分项涉及 `worker`、`core`、`db`、`v1`、`queue` 共 5 个 crate 顶层模块，> 3）。→ **不满足"二次开发"条件**。

## 2. 架构与数据流

```
 业务系统 ──POST /api/v1/app/{app}/msg (JWT)──▶ ┌──────────── svix-server (api_enabled) ────────────┐
                                               │ IdempotencyService(Idempotency-Key, cache 12h)    │
                                               │ v1::message::create_message_inner                 │
                                               │  ① PG 事务: INSERT message + messagecontent         │
                                               │     (app_id, eventId) 唯一索引 → 409               │
                                               │  ② 事务提交后 XADD MessageBatch(无延迟)  ←─ 非事务 │
                                               └───────────────┬───────────────────────────────────┘
                                                               │ 单一共享 Redis Stream
                                                               ▼ {queue}_svix_v3_main
   ┌────────── Redis ──────────────────────────────────────────────────────────────────┐
   │ MAIN(stream, 单 consumer group, 所有 worker 抢同一 consumer 名)                      │
   │ DELAYED(zset, 重试用；后台 500ms 轮询搬回 MAIN)    DLQ(list, 仅内部处理异常 ≥3 次)     │
   │ cache: CreateMessageApp(app+全部 endpoint, TTL 30s)、失败计时 FailureCacheKey      │
   └──────────────────────────────┬────────────────────────────────────────────────────┘
                                  │ XREADGROUP BLOCK(≤20s) COUNT 128
                                  ▼
 ┌──────────────── svix-server (worker_enabled) ──────────────────────────────────────┐
 │ queue_handler: 全局 worker_max_tasks=500，每条任务 tokio::spawn                     │
 │ process_queue_task_inner: 读 message+content(PG) → CreateMessageApp(cache/PG)       │
 │   filtered_endpoints(event_type, channels, !disabled) → join_all 并发分发           │
 │ prepare_dispatch: POST endp.url, Body=原始 payload, Content-Type=application/json  │
 │   headers = svix-id/timestamp/signature + UA + endpoint 静态 headers               │
 │ make_http_call: 超时 ≤30s；is_success()(2xx) 即成功；响应体截断 20KB               │
 │ 失败: cfg.retry_schedule[n] ±20% jitter（429/超时 ≥60s；参考 Retry-After）          │
 │       → 写 attempt(next_attempt) → 投递到 DELAYED                                  │
 │ 耗尽: 写 attempt(next_attempt=NULL) → operational webhook → 持续失败超时则 disable │
 └──────────┬─────────────────────────────────────────┬──────────────────────────────┘
            ▼ HTTPS(SSRF 过滤/可选代理)                 ▼
       外部 endpoint                          PG: messageattempt(每次尝试一行)
                                              expired_message_cleaner: 到期删 messagecontent
```

**Application / Endpoint 映射与别扭之处**

| Svix 概念 | 可映射为 | 问题 |
|---|---|---|
| Organization | 整个 Hub（OSS 版 JWT `sub` 固定一个 org） | — |
| Application | 方案 A：整个 Hub 只用 1 个 App；方案 B：每个来源业务系统 1 个 App | A：去重范围变成 `(hub, eventId)`，source 只能靠约定拼进 eventId；B：路由只在 App 内生效（`filtered_endpoints` 只遍历本 App 的 endpoint），同一个供应商要在每个来源 App 下各建一份 endpoint，凭据和 header 也要各配一份 |
| Endpoint | 供应商的一个 API 操作 | 只有 URL、静态 headers、event_types/channels 过滤、签名密钥；没有方法、模板、认证、成功判定、重试等策略字段 |
| EventType / Channel | event_type / 来源或业务键标签 | 每条消息最多 5 个 channel；channel 同时参与路由过滤 |
| 签名头 svix-* / webhook-* | 无对应需求 | 每个请求都会带上，只能改名，无法关闭 |

## 3. 能力评分

| ID | 能力 | 分值 | 权重 | 加权 | 证据 | 缺失时的扩展方案与改动位置（模块） |
|---|---|---|---|---|---|---|
| C1 | 统一事件受理 + 受理层去重 | 2 | 3 | 6 | 去重：`svix@server-v1.102.1:server/svix-server/migrations/20220123205937_initial.up.sql:L74-L74`（`(app_id, uid)` 部分唯一索引，永久生效，与 `server/svix-server/src/v1/endpoints/message.rs:L333-L341` 文档里"仅 1 天"的描述不一致）；`server/svix-server/src/error.rs:L447-L453`（冲突返回 409）；受理即持久化：`server/svix-server/src/v1/endpoints/message.rs:L430-L441`；信封：`server/svix-server/src/v1/endpoints/message.rs:L111-L145`（只有 eventId/eventType/payload/channels/retention）；**非事务入队**：`server/svix-server/src/v1/endpoints/message.rs:L443-L459`；API 幂等头：`server/svix-server/src/core/idempotency.rs:L1-L34`、`server/svix-server/config.default.toml:L89-L94`（多实例需 Redis cache） | 去重本身原生可用。扣 1 分的原因：(a) 没有 source、occurred_at、ordering_key、idempotency_key 等信封字段，只能放进 payload，而 payload 会原样发给供应商（见 C3）；(b) PG 提交后才 XADD，进程崩溃或 Redis 故障时消息已落库却永不投递，也没有对账任务（recover 只扫描 Fail 状态的 attempt）。补救办法是外置对账器，通过公开 API（list msg + list attempted destinations）补投，不改核心 |
| C2 | event_type 路由 + 扇出 | 3 | 3 | 9 | `server/svix-server/src/core/message_app.rs:L124-L159`；`server/svix-server/src/worker.rs:L935-L965`；`server/svix-server/src/v1/endpoints/endpoint/mod.rs:L183-L195` | 原生支持。限制：路由只在单个 Application 内生效，没有"来源 + 简单条件"的过滤 |
| C3 | 按目标构造请求 | 1 | 3 | 3 | 方法固定 POST、URL 固定、Body 为原始 payload：`server/svix-server/src/worker.rs:L290-L331`；Content-Type 固定为 JSON：`server/svix-server/src/worker.rs:L351-L360`；静态 header：`server/svix-server/src/worker.rs:L230-L243`、`server/svix-server/src/v1/endpoints/endpoint/mod.rs:L623-L637`；`content-type` 被禁止自定义：`server/svix-server/src/core/types/mod.rs:L1139-L1191`；仅限发布方、按消息生效的 `transformationsParams.rawPayload`（schema 中隐藏）：`server/svix-server/src/v1/endpoints/message.rs:L134-L157`；托管版 Transformation 接口：`codegen/lib-openapi.json:L8735-L8735`、`codegen/lib-openapi.json:L9128-L9128` | **worker**：`prepare_dispatch` 中加入模板渲染（URL、方法、Header、Body）；**core**：`CreateMessageEndpoint` 增加模板字段，放开 content-type 校验；**db**：endpoint 表新增列及迁移；**v1**：endpoint API。`rawPayload` 需要业务方自己做转换，违背 Hub 定位，不加分 |
| C4 | 按目标认证 | 1 | 3 | 3 | 静态 API Key 可通过 endpoint headers 实现（`Authorization`、`X-Api-Key` 未被禁止，读取时脱敏）：`server/svix-server/src/v1/endpoints/endpoint/mod.rs:L655-L678`；headers 以明文 jsonb 存储：`server/svix-server/migrations/20220218061228_endpoint_headers.up.sql:L1-L1`；签名只有 Svix 格式（`msg_id.ts.body`，固定头名）：`server/svix-server/src/worker.rs:L174-L227`；OSS 无 OAuth2/mTLS；托管版有：`codegen/lib-openapi.json:L9707-L9707`、`codegen/lib-openapi.json:L10480-L10480` | 静态 Key 原生可用；OAuth2 client credentials 与供应商自定义签名都缺失。**core**：新增 auth 模块与 token 缓存（可复用 `core::cache`）；**worker**：在 `prepare_dispatch` 中注入；**db/v1**：认证配置与加密存储 |
| C5 | 可配置成功判定 | 1 | 3 | 3 | 硬编码 `res.status().is_success()`：`server/svix-server/src/worker.rs:L377-L427` | **worker**：在 `make_http_call` 中按规则判定状态码集合与响应体字段（响应体已读出并截断到 20KB，可直接用于匹配）；**core/db/v1**：规则配置 |
| C6 | 按目标重试策略 | 1 | 3 | 3 | 全局 `retry_schedule`：`server/svix-server/src/cfg.rs:L162-L164`、`server/svix-server/config.default.toml:L62-L63`；使用：`server/svix-server/src/worker.rs:L577-L631`；jitter ±20%、429/超时罚时、Retry-After：`server/svix-server/src/worker.rs:L52-L55`、`server/svix-server/src/worker.rs:L500-L545`；任何非 2xx 或传输错误都会重试，没有"不可重试"分类：`server/svix-server/src/worker.rs:L416-L443`；连接 1s 内失败会立即重试一次且不记录：`server/svix-server/src/core/webhook_http_client.rs:L188-L193` | 指数退避（表驱动）和 jitter 原生支持，但只能全局配置，也无法定义可重试条件。**worker**：`handle_failed_dispatch` 改为读取 endpoint 级策略；**core/db/v1**：策略配置 |
| C7 | 死信 + 单条人工重放（含审计） | 2 | 3 | 6 | 耗尽状态（`next_attempt=NULL` 且 Fail）及 operational webhook：`server/svix-server/src/worker.rs:L634-L658`；单条重放：`server/svix-server/src/v1/endpoints/attempt.rs:L981-L1030`；重放记为 `trigger_type=Manual`、`attempt_number=-1`：`server/svix-server/src/worker.rs:L757-L760`；**手动重放失败后不再重试**：`server/svix-server/src/worker.rs:L580-L584`；不记录操作人 | 死信状态可查询，单条重放原生可用，原记录保留。审计（操作人、原因）可在 API 网关层记录，不改核心，因此给 2 分。偏差：重放只投 1 次，不会回到 PENDING 重新走完整重试，这与 PRD 状态机不同，修改需动 **worker** |
| C8 | 状态查询 API | 3 | 3 | 9 | 按 id/eventId 查询：`server/svix-server/src/v1/endpoints/message.rs:L507-L519`；按 endpoint 查询状态、时间、event_type、channel：`server/svix-server/src/v1/endpoints/attempt.rs:L164-L180`、`server/svix-server/src/v1/endpoints/attempt.rs:L445-L462`；按消息查询：`server/svix-server/src/v1/endpoints/attempt.rs:L594-L614`；路由清单：`server/svix-server/src/v1/endpoints/attempt.rs:L1059-L1115`；单次查询窗口 90 天：`server/svix-server/src/v1/endpoints/attempt.rs:L186-L187`；App 级 token 权限：`server/svix-server/src/core/permissions.rs:L22-L56` | 原生支持。限制：业务键只能用 channel（每条消息 ≤5 个）承载；按状态的查询以 endpoint 为维度，没有跨 endpoint 的全局失败列表；托管版才有 `msg/search`（`codegen/lib-openapi.json:L18336-L18336`） |
| C9 | 按目标隔离 | 1 | 3 | 3 | 单一共享 Stream，所有 worker 竞争同一 consumer：`server/svix-server/src/queue/redis.rs:L43-L72`；全局并发上限：`server/svix-server/src/cfg.rs:L240-L241`、`server/svix-server/src/worker.rs:L1028-L1035`；同一消息的所有 endpoint 在一个任务里 `join_all`，ack 要等最慢的那个：`server/svix-server/src/worker.rs:L945-L974` | 没有按 endpoint 的队列或并发配额。慢供应商（≤30s 超时）会占满全局 500 个槽位。**worker**：增加按 endpoint 的并发信号量，超额时退回 DELAYED；**queue**：如需真正隔离，要做按 endpoint 分区的 stream |
| C10 | Event/Delivery/Attempt 持久化 | 3 | 3 | 9 | `server/svix-server/src/db/models/message.rs:L12-L26`、`server/svix-server/src/db/models/messagecontent.rs:L9-L17`、`server/svix-server/src/db/models/messageattempt.rs:L12-L32`；attempt 写入内容：`server/svix-server/src/worker.rs:L362-L414`；Delivery 状态由最新 attempt 推导：`server/svix-server/src/worker.rs:L839-L871` | Event 和 Attempt 原生持久化在 PG。没有显式的 Delivery 行（`messagedestination` 已废弃），尚未尝试的投递没有记录。attempt 不保存请求头和请求体（托管版有 attempt headers：`codegen/lib-openapi.json:L20425-L20425`） |
| C11 | OpenTelemetry 指标/追踪/日志 | 2 | 3 | 6 | OTLP traces/logs/metrics：`server/svix-server/src/lib.rs:L348-L420`、`server/svix-server/src/lib.rs:L476-L506`、`OpenTelemetry.md:L1-L14`；**内置指标只有 4 个队列深度 gauge**：`server/svix-server/src/metrics/redis.rs:L48-L60`；worker 的 span/日志带 msg_id/endp_id/response_code：`server/svix-server/src/worker.rs:L725-L731`、`server/svix-server/src/worker.rs:L447-L475` | 追踪和日志原生支持。投递成功率、延迟等指标需要用 Collector spanmetrics，或用 operational webhook 外部统计，不改核心。队列任务（`server/svix-server/src/queue/mod.rs:L63-L71`）不携带 trace context，API 到 worker 的链路断开，只能靠 msg_id 关联 |
| C12 | 按目标限流与熔断 | 1 | 2 | 2 | `throttleRate` 被接收和持久化：`server/svix-server/src/v1/endpoints/endpoint/mod.rs:L148-L162`、`server/svix-server/src/core/message_app.rs:L162-L185`；**worker 和 queue 都不读取它**（`rate_limit` 只出现在 v1、db、core/message_app 中）；连续失败若干小时后才自动 disable（默认 120h）：`server/svix-server/src/worker.rs:L119-L172`、`server/svix-server/config.default.toml:L105-L107`；托管版限流状态：`codegen/lib-openapi.json:L11432-L11432` | **worker** 在分发前检查令牌桶，超额时延迟重投；**core** 基于 Redis 实现分布式令牌桶和熔断状态。现有的 disable 机制不是熔断：disable 后新消息会被直接过滤，不产生 attempt（`server/svix-server/src/core/message_app.rs:L132-L134`），这是一个静默跳过风险 |
| C13 | 按 ordering_key 有序投递 | 1 | 2 | 2 | 没有 ordering 相关字段；消费者竞争消费、单批 128 条并发 spawn：`server/svix-server/src/queue/redis.rs:L66-L72`、`server/svix-server/src/worker.rs:L1057-L1100`；重试经 DELAYED 时会被后续消息超车：`server/svix-server/src/worker.rs:L623-L631`；FIFO 仅托管版提供：`codegen/lib-openapi.json:L52870-L52880` | **v1/db**：message 增加 ordering_key 及索引；**worker**：分发前检查同 key 的前序是否未完成，未完成则延迟；**queue**：按 key 分区。改动大、易出错，接近 0 分的边缘 |
| C14 | 幂等键透传到对端 Header | 1 | 2 | 2 | 只发送 `svix-id`/`webhook-id`，值为服务端生成的 msg_id，重试间稳定，但发布方无法控制取值，头名也不可配置：`server/svix-server/src/worker.rs:L61-L67`、`server/svix-server/src/worker.rs:L198-L227`；eventId 不会被发送 | **worker** 的 `generate_msg_headers` 支持把 eventId 或信封字段写入可配置的头名；**core/db/v1**：配置项 |
| C15 | 字段级脱敏 | 1 | 2 | 2 | payload 明文存储；只有整体过期（每条消息 5–90 天）：`server/svix-server/src/v1/endpoints/message.rs:L130-L133`、`server/svix-server/src/expired_message_cleaner.rs:L41-L93`；手动 expunge 响应体：`server/svix-server/src/v1/endpoints/attempt.rs:L1032-L1057`；读取 header 时脱敏：`server/svix-server/src/v1/endpoints/endpoint/mod.rs:L655-L678` | 不支持字段级脱敏。**v1**：受理时按规则做哈希或加密；**db/core**：规则配置；**worker**：响应体脱敏 |
| C16 | 批量重放 | 2 | 1 | 2 | `POST .../endpoint/{id}/recover`：只处理单个 endpoint、只处理 Fail、窗口 ≤14 天、上限 1 万条，用 `tokio::spawn` 执行，不持久化，任务 id 无法查询：`server/svix-server/src/v1/endpoints/endpoint/recovery.rs:L24-L158`；托管版 bulk-replay/replay-missing：`codegen/lib-openapi.json:L9277-L9277`、`codegen/lib-openapi.json:L9492-L9492` | 按时间窗重放原生可用；按 event_type 等条件筛选需要外部脚本组合 list API 和 resend API，不改核心 |
| C17 | 无云依赖、混合部署、无状态扩展 | 3 | 3 | 9 | 必需 PG，Redis/RabbitMQ/内存可选：`server/svix-server/src/cfg.rs:L166-L200`、`server/svix-server/src/queue/mod.rs:L32-L61`；API 与 worker 可分开部署：`server/svix-server/src/lib.rs:L235-L299`；官方 compose（PG+pgbouncer+Redis）：`server/docker-compose.yml:L1-L70`；出站代理（固定出口 IP）：`server/svix-server/src/cfg.rs:L255-L257`；SSRF 默认拦截内网：`README.md:L274-L275` | 原生支持。多 API 实例必须使用 Redis cache，否则 Idempotency-Key 失效（`server/svix-server/config.default.toml:L89-L94`）；memory 队列只适合单实例 |
| C18 | 首投 P99 < 1s | 3 | 3 | 9 | 受理后立即 XADD（无延迟）：`server/svix-server/src/v1/endpoints/message.rs:L443-L459`；XREADGROUP BLOCK 有消息即返回：`omniqueue@acb4098:omniqueue/src/backends/redis/streams.rs:L89-L124`；worker 立即 spawn：`server/svix-server/src/worker.rs:L1057-L1100`；分发前读 2 次 PG 加 1 次 cache：`server/svix-server/src/worker.rs:L817-L933`；重试走 500ms 轮询（不影响首投）：`omniqueue@acb4098:omniqueue/src/backends/redis/mod.rs:L634-L712` | 源码路径上没有轮询延迟。风险：worker 槽位饱和时每 100ms 自旋等待（`server/svix-server/src/worker.rs:L1029-L1035`）。待 S8 实测 |
| **合计** | | | **48** | **88** | | 88 / 144 = 61.1% |

`UNVERIFIED` 项：无。所有分值都有源码或仓库内文档支撑，并已经过 PoC 复核：
- S8 的 P99 为 8.5ms
- S6 在默认 500 槽位下没有表现出问题；把槽位降到 10 后，vendor-b 最大延迟 10.0s，证实共享槽位加 `join_all` 的机制

PoC 另发现：`headers.rs` 没有调用 `CreateMessageApp::invalidate`，Header 修改要等缓存 TTL（约 30s）过期后才生效。这是 C3/C4 的补充负面证据，分数不变。

### 额外发现（不计分，但影响"零静默丢失"目标）

1. **提交后入队不在同一事务内**（C1）：PG 提交与 XADD 之间若发生故障，消息有记录但永不投递，系统内也没有补偿机制。
2. **endpoint 被自动 disable 后，新消息直接跳过**（C12）：不产生 attempt，`recover` 也找不到（它只扫描 Fail）。托管版提供 `replay-missing` 处理这种情况，OSS 没有。
3. **扇出批任务部分失败会导致整批重投**：`MessageBatch` 的 status 恒为 Sending（`server/svix-server/src/worker.rs:L882-L900`），任何一个 endpoint 出现内部错误都会 nack 整个任务，已成功的 endpoint 会被重复投递。这符合 at-least-once，但会放大重复。
4. 托管版有、OSS 版缺失的功能（依据仓库内 `codegen/lib-openapi.json` 与 OSS `server/openapi.json` 的路径差集；README 只笼统说明有差异，见 `README.md:L409-L412`）：endpoint Transformation 与 simulate、OAuth 配置、mTLS、rate-limit-status（限流执行）、bulk-replay、replay-missing、msg/search、attempt headers、stats/attempts、cancel-msgs、FIFO/polling destination、connector。

## 4. 得分为 1 的项及涉及的核心模块（决策条件 3）

模块按 `svix-server` crate 的顶层模块计（对应 Go 的顶层包）：`worker`、`core`、`db`（含 `migrations/`）、`v1`、`queue`、`cfg`。

| ID | 涉及模块 |
|---|---|
| C3 | worker、core、db、v1 |
| C4 | worker、core、db、v1 |
| C5 | worker、core、db、v1 |
| C6 | worker、core、db、v1 |
| C9 | worker、queue |
| C12 | worker、core（必要时加 queue） |
| C13 | worker、queue、db、v1 |
| C14 | worker、core、db、v1 |
| C15 | v1、db、core、worker |
| **并集** | **worker、core、db、v1、queue = 5 个**（即使 C9、C13 不动 queue，也有 4 个），> 3 → 条件 3 不满足 |

如果把整个 `svix-server` crate 视为 1 个模块，条件 3 会形式上成立。但这与"按顶层包计"的本意不符：Go 侧按 `internal/xxx` 计，Rust 侧对应的是 crate 内的顶层模块。而且无论怎么算，条件 1 都已不满足，不影响结论。

## 5. Svix Bridge

- **Senders**（`bridge/svix-bridge.example.senders.yaml:L23-L50`）：从 GCP Pub/Sub、RabbitMQ、Redis、SQS、Kafka 消费 → 可选 JS 转换（quick-js 或 deno，`bridge/svix-bridge/src/runtime/mod.rs:L9-L16`）→ **唯一的输出是调用 Svix Create Message API**（`bridge/svix-bridge-types/src/lib.rs:L244-L264`）。它解决的是业务系统如何把事件送进 Svix，可作为 MQ 入口，与出站请求构造无关。
- **Receivers + HTTP output**：可以在 Svix endpoint 后面接 Bridge receiver（校验 svix 签名）→ JS 转换 → `http` 输出到供应商。但 `http_output` 只 POST JSON、只带静态 header，**不检查响应状态码，也没有重试**（`bridge/svix-bridge/src/http_output.rs:L61-L76`）。receiver 只在传输错误时向 Svix 返回 500（`bridge/svix-bridge/src/webhook_receiver/mod.rs:L153-L165`）。
- **结论：Bridge 不改变评估结果。** ① JS 转换属于 PRD 的 Won't（脚本转换），按规则不能给 C3 加分；② 如果串成 Svix → Bridge → 供应商，Svix 记录的是 Bridge 的响应，供应商返回 4xx/5xx 时 Bridge 仍回 204，Svix 会判为成功，这反而破坏 C5、C7、C10 的语义，还多出一跳额外的可用性与延迟。

## 6. 非功能项 N1–N4

| ID | 结论 | 依据 |
|---|---|---|
| N1 许可证 | MIT（server、bridge、仓库根目录均为 MIT），允许内部使用、修改和闭源分叉，无传染性 | `server/LICENSE`、`bridge/LICENSE`、`LICENSE`；GitHub `license.spdx_id=MIT` |
| N2 社区活跃度 | 活跃，但由厂商主导。stars 3462、forks 293、open issues 68（含 PR），最近 push 为 2026-10-23。仓库近 6 个月 540 个提交、30 位作者；server 与 bridge 目录近 6 个月的提交高度集中在 2 名 Svix 员工（Jonas Platte 88 次、James Brown 48 次），存在明显的 bus factor。server 自 2026-08 起单独发版：`server-v1.100.0`(08-03)、`1.101.0`(08-26)、`1.102.0`(10-19)、`1.102.1`(10-20)，约每 1–2 个月一版 | `gh api repos/svix/svix-webhooks`；`git log --since="6 months ago"`；`git tag --sort=-creatordate` |
| N3 fork 跟进可行性 | **差**。需要改动的核心路径及近 6 个月提交数：`server/svix-server/src/worker.rs` 2、`server/svix-server/src/core` 14（其中 `core/types/mod.rs` 4、`core/message_app.rs` 2）、`server/svix-server/src/db` 6、`server/svix-server/src/v1` 18（其中 `v1/endpoints/endpoint` 12、`v1/endpoints/message.rs` 3）、`server/svix-server/src/queue` 4、`server/svix-server/src/cfg.rs` 6、`migrations` 0；整个 `server/svix-server/src` 共 40 次（12 个月 64 次）。`worker.rs` 本身变化少，但我们必须改的 `v1`、`core` 正是上游最活跃的区域。预计要改的文件约 12–15 个（worker、cfg、core/types、core/message_app、新增 core 认证与限流模块、db/models/endpoint、新迁移、v1 endpoint 的 mod/crud/headers、v1 message、queue/redis 等）。上游 open 列表中还有 `deps: upgrade to sqlx 0.9 / seaorm 2`，会带来大面积 ORM 层冲突 | `git -C .spike/src/svix fetch --unshallow` 后执行 `git log HEAD --since="6 months ago" --oneline -- <path> \| wc -l` |
| N4 团队与 AI 编码 | Rust（axum、sea-orm、tokio、omniqueue git 依赖）。团队没有固定技术栈，Rust 的学习曲线和编译时间会拖慢"尽快上线"的节奏。AI 辅助方面：类型系统和编译器能拦截大量错误，这是优点；但 sea-orm 宏、自定义 derive（`svix-server-derive`）、async trait 生命周期等会让 AI 产出更难一次编译通过，fork 后还要理解大量与本场景无关的 SaaS 代码（签名轮换、operational webhook、app portal、legacy 队列迁移等）。整体评价：中偏低 | 定性 |

## 7. 可借鉴的设计

| 模块 | 源码位置 | 借鉴什么 |
|---|---|---|
| 重试延迟计算 | `server/svix-server/src/worker.rs:L500-L545` | 表驱动退避加 ±20% jitter；对 429 和超时设置最低 60s 罚时；用 `[base, 2×base]` 区间约束 Retry-After，防止对端把我们拖得过久或过快 |
| 成功/失败分支与 attempt 写入 | `server/svix-server/src/worker.rs:L333-L445`、`L547-L709` | prepare → call → handle_success/failed 三段式；每次尝试一行 attempt（状态码、截断后的响应、耗时、trigger_type、next_attempt）；响应体限制 20KB，非 UTF-8 时转 base64 |
| Delivery 状态推导 | `server/svix-server/src/worker.rs:L839-L871` | 用"最新 attempt + next_attempt 是否为空"推导 Sending/Fail/Success，避免额外的状态表写放大（我们仍建议保留显式 Delivery 行，用于覆盖"尚未尝试"的状态） |
| 受理去重 | `server/svix-server/migrations/20220123205937_initial.up.sql:L74-L74`、`server/svix-server/src/error.rs:L447-L453` | 部分唯一索引 `(scope, uid) WHERE uid IS NOT NULL`，冲突映射为 409；我们的 scope 用 `source`，并且必须**与入队放在同一事务里（outbox）**，避开 Svix 的非事务入队缺陷 |
| ID 设计 | `server/svix-server/src/core/types/mod.rs:L587-L587`、`server/svix-server/src/v1/endpoints/attempt.rs:L186-L237` | 带前缀的 KSUID 作为主键，同时承担时间排序；时间范围查询直接改写为主键范围（`start_id/end_id`），再加 90 天窗口限制以防止全表扫描 |
| 延迟队列 | `server/svix-server/src/queue/redis.rs:L1-L24`、`omniqueue@acb4098:omniqueue/src/backends/redis/mod.rs:L634-L712` | Redis Stream 作主队列，ZSET 作延迟队列，后台任务带 NX 锁搬运到期任务；ack 超时自动重领；内部异常超过 N 次进入独立 DLQ，并提供 redrive 接口（`server/svix-server/src/v1/endpoints/admin.rs:L11-L25`）。我们需要在此基础上**按 destination 分区** |
| 配置缓存 | `server/svix-server/src/core/message_app.rs:L21-L122` | 按 app 把全部 endpoint 配置打包缓存（TTL 30s），写配置时主动 invalidate（`server/svix-server/src/v1/endpoints/endpoint/crud.rs:L98-L98` 等），热路径不查 PG |
| 端点自动禁用 | `server/svix-server/src/worker.rs:L101-L172` | "首次失败时间 + 宽限期"的判定方式，缓存 TTL 设为 2× 宽限期以宽恕偶发失败。可作为熔断的长周期兜底，但禁用期间的消息必须记录为 SKIPPED 并可补投 |
| SSRF 与出站代理 | `server/svix-server/src/core/webhook_http_client.rs:L135-L193`、`L483-L660`、`server/svix-server/src/cfg.rs:L229-L238`、`L255-L330` | 在 DNS 解析层拦截内网 IP，支持白名单子网和域名；支持 socks5 或 http 代理与 noproxy，可满足供应商 IP 白名单（固定出口） |
| 敏感 header 脱敏输出 | `server/svix-server/src/v1/endpoints/endpoint/mod.rs:L655-L678`、`server/svix-server/src/core/types/mod.rs:L1139-L1230` | 读取接口对 Authorization 等头只返回 key；校验禁止覆盖的 hop-by-hop 头和平台头前缀（我们还需要加密存储，Svix 是明文 jsonb） |
| API 幂等中间件 | `server/svix-server/src/core/idempotency.rs:L1-L80` | `Idempotency-Key` + token + URL 哈希；先写 Start 锁，再缓存完整响应 12h，并发请求等待复用。可作为 Ingest API 的第二层保护 |
| 过期清理 | `server/svix-server/src/expired_message_cleaner.rs:L41-L93` | payload 单独成表，按 expiration 批量 `DELETE ... FOR UPDATE SKIP LOCKED`，元数据行保留以维持分页；保留期在受理时按消息写入 |
| 运行角色拆分 | `server/svix-server/src/lib.rs:L235-L299` | 同一二进制通过 `api_enabled`、`worker_enabled`、`expired_message_cleaner_enabled` 切换角色，便于独立扩缩容 |
