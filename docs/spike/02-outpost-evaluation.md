# 02 · Hookdeck Outpost 源码能力评估

> 评估对象：Hookdeck Outpost `v1.7.0`（`.spike/src/outpost`，只读，未构建、未执行仓库内任何代码）
> 评分依据：`docs/spike/01-evaluation-checklist.md`（C1–C18、权重、评分标准、决策规则均按锁定版本执行，未做调整）
> 证据格式：`outpost@v1.7.0:<path>:L<a>-L<b>`，行号均已逐条核对。PoC（S1–S8）已于 2026-10-24 补跑，全部与本文结论一致，分数无调整（见 `04-poc-results.md`）

## 1. 结论摘要

- **加权得分：82 / 144（56.9%）**，没有达到 101 的门槛。
- Outpost 的核心模型是"SaaS 平台把原始事件以 Webhook POST 推给租户 endpoint"。请求构造只有固定 POST + 原始 `data` + 静态 Header；成功判定写死为 `< 400`；重试策略只有全局一套，不带 jitter；没有按目标的隔离、限流和有序投递。这几项正是本场景的核心差异。
- 可以直接复用的部分：topic + 内容过滤扇出、OpenTelemetry、无云依赖部署（RabbitMQ / NATS + Redis + PostgreSQL）、手动重放 API、首次投递路径上没有批处理或轮询延迟。
- Destination Provider 接口是一个干净的内部扩展点，C3/C4/C5 可以集中在 `destregistry` 内以"新增 provider"的方式实现。但它是编译期注册的 `internal` 包，必须 fork，所以按评分标准记 1 分。
- **决策规则（仅 Outpost）**：条件 1 ✗（82 < 101）；条件 2 ✓（C3/C4/C5 都是 1 分，没有 0 分）；条件 3 ✗（1 分项涉及 ≥ 8 个顶层模块，远超 3 个）。**结论：Outpost 不满足"二次开发"条件。**
- 敏感性：即使把 C3/C4/C5 都上调为 2 分（+9），总分也只有 91，仍低于 101，结论不变。

## 2. 架构与数据流（按源码校正）

```
                         ┌──────────── API Service ─────────────┐
 业务系统 ──POST /publish─▶│ apirouter.Ingest                      │
 (AdminOnly API Key)      │   └─ publishmq.eventHandler.Handle     │◀── (可选) publishmq consumer：
                         │       1. tenantstore.MatchEvent         │    从客户自有队列拉取事件，
                         │          (Redis：topic + filter)        │    走同一个 Handle
                         │       2. idempotence.Exec               │
                         │          key=idempotency:publishmq:<id> │
                         │       3. 每个匹配的 destination 一条     │
                         │          DeliveryTask → deliverymq      │
                         │   ◀─ 202 {id, duplicate, destination_ids}│  ※ 此时事件只在 MQ 中，未写入 DB
                         │                                         │
                         │ retry scheduler (RSMQ on Redis)  ◀──────┼── 到期：从 logstore 读事件 + 上次 attempt
                         │   └─ 重新发布 DeliveryTask → deliverymq  │      （logstore 是重试的事件来源）
                         └─────────────────────────────────────────┘
                                         │ deliverymq（单一共享队列：RabbitMQ/NATS/SQS/PubSub/ASB）
                                         ▼
                         ┌──────────── Delivery Service ─────────┐
                         │ consumer（全局信号量 DELIVERY_MAX_CONCURRENCY）
                         │ deliverymq.messageHandler              │
                         │   ├─ tenantstore.RetrieveDestination   │  disabled/deleted → ack 丢弃（不记 attempt）
                         │   ├─ idempotence key=<evt>:<dest>:<n>   │
                         │   ├─ destregistry.PublishEvent          │──▶ provider.Publisher.Publish
                         │   │    (全局 DELIVERY_TIMEOUT, 默认 5s) │     webhook：POST 原始 data，<400 即成功
                         │   ├─ 失败且 attempt ≤ 全局上限 → RSMQ 排程重试（全局 backoff，无 jitter）
                         │   └─ LogEntry{Event, Attempt, Destination} → logmq
                         └─────────────────────────────────────────┘
                                         │ logmq
                                         ▼
                         ┌──────────── Log Service ──────────────┐
                         │ logmq batchprocessor（默认 10s / 1000 条）
                         │   ├─ logstore.InsertMany → PG/CH：events、attempts
                         │   └─ alert.Evaluator（Redis 计数）→ opevents（告警）/ 自动禁用 destination
                         └─────────────────────────────────────────┘
 查询：GET /events、/attempts（读 logstore）；重放：POST /retry（读 logstore → deliverymq）
```

与任务描述中假设的链路相比，有以下校正：
1. **HTTP 受理不经过 publishmq 队列**。`POST /publish` 直接调用 `eventHandler.Handle` 并同步写入 deliverymq；publishmq 只是一个可选入口，用来从客户自有 MQ 消费事件（`outpost@v1.7.0:internal/apirouter/publish_handlers.go:L37-L82`、`outpost@v1.7.0:internal/services/builder.go:L245-L263`）。
2. **事件在第一次投递尝试之后才落库**，并且是异步批量写入；没有匹配到任何目标的事件永远不会落库（见 C10）。
3. **重试调度器运行在 API Service 内**，而且依赖 logstore 回读事件（`outpost@v1.7.0:internal/services/builder.go:L182-L185`、`L240-L243`；`outpost@v1.7.0:docs/content/self-hosting/data-model.mdoc:L238-L247`）。
4. 告警和自动禁用发生在 Log Service 的 logmq 批处理中，而不是投递路径上。

## 3. 能力评分表

| ID | 能力 | 分值 | 权重 | 加权 | 证据 | 缺失时的扩展方案与改动位置（模块） |
|---|---|---|---|---|---|---|
| C1 | 统一事件受理 + 受理层去重 | 2 | 3 | 6 | 发布方可传 `id`，否则自动生成：`outpost@v1.7.0:internal/apirouter/publish_handlers.go:L84-L124`；去重键为 `idempotency:publishmq:<event.ID>`，**不含 tenant**：`outpost@v1.7.0:internal/publishmq/eventhandler.go:L159-L177`、`L243-L245`；Redis SETNX，TTL 默认 1h：`outpost@v1.7.0:internal/idempotence/idempotence.go:L100-L169`、`L188-L194`，`outpost@v1.7.0:internal/config/config.go:L98`、`L208`；没有匹配目标时直接返回，不经过去重：`outpost@v1.7.0:internal/publishmq/eventhandler.go:L155-L157` | 约定 `id = <source>:<业务事件ID>` 即可满足"source + 业务 ID"去重，无需改代码。限制：去重只在 TTL 窗口内、只存在 Redis；受理返回前没有写 DB（见 C10）；统一信封里的 `ordering_key`、`idempotency_key` 只能放进 `metadata`（`map[string]string`） |
| C2 | 按 event_type 路由并扇出 | 3 | 3 | 9 | topic（可通配）+ 基于 data/metadata 的 JSON 过滤：`outpost@v1.7.0:internal/models/entities.go:L49-L93`；按租户遍历 destination 匹配：`outpost@v1.7.0:internal/tenantstore/redistenantstore/redistenantstore.go:L642-L664`；每个目标一条 DeliveryTask 并发入队：`outpost@v1.7.0:internal/publishmq/eventhandler.go:L179-L202` | 原生满足。注意：每个租户默认最多 20 个 destination（`outpost@v1.7.0:internal/config/config.go:L94`、`L206`），可通过配置调整 |
| C3 | 按目标构造请求（URL/方法/Header/Body 模板） | 1 | 3 | 3 | 方法写死 POST，Body = 原始 `event.Data`，Content-Type 固定：`outpost@v1.7.0:internal/destregistry/providers/destwebhook/destwebhook.go:L870-L884`；每个目标只能配 URL 和静态 `custom_headers`：`outpost@v1.7.0:internal/destregistry/providers/destwebhook/destwebhook.go:L565-L587`；保留 Header：`L47-L54`；Provider 接口：`outpost@v1.7.0:internal/destregistry/registry.go:L60-L85`；provider 只能在编译期注册：`outpost@v1.7.0:internal/destregistry/providers/default.go:L83-L151` | 新增 `destregistry/providers/destvendorhttp`（方法、URL/Header/Body 模板保存在 `Destination.Config` 中，`map[string]string`），在 `providers/default.go` 注册，并在 `destregistry/metadata/providers/` 下补 metadata.json。**模块：destregistry**（新增为主，另需改 1 处注册） |
| C4 | 按目标认证（API Key / OAuth2 CC / 自定义签名） | 1 | 3 | 3 | 静态 API Key 只能写在 `custom_headers` 里，它属于 Config，不在 Credentials 中，也没有标记为 sensitive，API 返回时会明文显示：`outpost@v1.7.0:internal/destregistry/metadata/providers/webhook/metadata.json:L12-L22`、`outpost@v1.7.0:internal/destregistry/providers/destwebhook/destwebhook.go:L440-L474`；HMAC 签名模板、算法、Header 是**进程级全局配置**，不能按目标设置：`outpost@v1.7.0:internal/config/destinations.go:L86-L95`；算法只有 hmac-sha256：`outpost@v1.7.0:internal/destregistry/providers/destwebhook/destwebhook.go:L1025-L1034`；OAuth2：源码中未找到 | 在 C3 的新 provider 中实现：OAuth2 client credentials（token 缓存可放在 Publisher 实例内，`registry.go:L315-L334` 的 LRU 会缓存 Publisher）、按目标配置的签名模板，凭据放在 `Credentials`。**模块：destregistry** |
| C5 | 可配置成功判定（状态码集合 + 响应体字段） | 1 | 3 | 3 | `StatusCode >= 400` 判失败，其余都算成功，写死在代码里：`outpost@v1.7.0:internal/destregistry/providers/destwebhook/httphelper.go:L89-L129`；响应体只作为字符串存档：`L184-L201` | 新 provider 的 `Publish` 返回自定义的 `Delivery.Status` 和 `ErrDestinationPublishAttempt`（`registry.go:L76-L85`），在里面实现状态码集合 + 响应体 JSON 字段匹配（可复用 `simplejsonmatch`）。**模块：destregistry** |
| C6 | 按目标重试策略（次数、退避 + jitter、可重试条件） | 1 | 3 | 3 | 退避器和次数上限在进程启动时注入，全局唯一：`outpost@v1.7.0:internal/services/builder.go:L344-L357`、`outpost@v1.7.0:internal/config/config.go:L82-L91`、`L689-L703`；退避实现没有 jitter：`outpost@v1.7.0:internal/backoff/backoff.go:L12-L78`（`scheduler.go` 中的 jitter 只作用于调度器自身的执行失败：`outpost@v1.7.0:internal/scheduler/scheduler.go:L386-L430`）；凡是 `ErrDestinationPublishAttempt` 都会重试（包括 4xx），唯一的开关是事件级的 `eligible_for_retry`：`outpost@v1.7.0:internal/deliverymq/messagehandler.go:L350-L360`；重试延迟最小值被抬到日志批处理间隔（默认 10s）：`outpost@v1.7.0:internal/config/config.go:L650-L656` | 在 `deliverymq.messageHandler` 中按 destination 读取策略（次数、退避、jitter），在 `shouldScheduleRetry` 里加入"不可重试"分类（由 provider 返回错误类型标记）；`alert.Evaluator` 判断重试耗尽时用的是全局 `retryMaxLimit`（`outpost@v1.7.0:internal/alert/evaluator.go:L148-L154`），也要一起改。**模块：deliverymq、alert、destregistry** |
| C7 | 死信 + 单条人工重放（含审计） | 2 | 3 | 6 | `POST /retry {event_id, destination_id}`：`outpost@v1.7.0:internal/apirouter/retry_handlers.go:L49-L167`；Attempt 带 `manual` 标记并持久化：`outpost@v1.7.0:internal/models/entities.go:L129-L144`；审计只是一条日志：`retry_handlers.go:L158-L162`；没有显式的 DEAD 状态，只有"重试耗尽"告警事件：`outpost@v1.7.0:internal/opevents/event.go:L10-L12`、`outpost@v1.7.0:internal/alert/evaluator.go:L148-L154` | 死信 = 查询最新一次 attempt 为 failed 且已耗尽的记录（结合 opevents `alert.attempt.exhausted_retries` 告警），重放走 API，无需改核心。缺少：持久化的审计记录（操作人、原因）和显式的 DEAD 状态，这两项需要改 logstore/apirouter |
| C8 | 状态查询 API（event_id / 业务键 / 状态 / 时间） | 2 | 3 | 6 | `/events`、`/attempts` 支持按 tenant、event_id、destination_id、destination_type、status（attempt 级 success/failed）、topic、时间范围过滤，并支持游标分页：`outpost@v1.7.0:internal/apirouter/log_handlers.go:L199-L291`、`outpost@v1.7.0:internal/logstore/driver/driver.go:L39-L69` | 业务键只能靠"event id = 业务键"的约定；没有 metadata 过滤，也没有投递级的终态（是否 DEAD）；事件要在首次 attempt 并刷盘之后（≤ 10s）才能查到。按 metadata 过滤需改 `logstore` + `apirouter` |
| C9 | 按目标隔离（慢目标不阻塞其他目标） | 1 | 3 | 3 | 所有目标共用一个 deliverymq 队列：`outpost@v1.7.0:internal/deliverymq/deliverymq.go:L29-L49`；消费者是全局信号量，默认并发 1：`outpost@v1.7.0:internal/consumer/consumer.go:L209-L236`、`outpost@v1.7.0:internal/services/builder.go:L361-L364`、`outpost@v1.7.0:internal/config/config.go:L79`、`L196`；超时为全局值（默认 5s），只能限制阻塞时长：`outpost@v1.7.0:internal/destregistry/registry.go:L196-L198`、`L393-L397` | 方案 A：按 destination 分队列（改 `deliverymq` + `mqs` 拓扑声明）；方案 B：在 handler 中做按 destination 的信号量，超额时 `RetryLater` 延后（改 `deliverymq`、`consumer`）。**模块：deliverymq、consumer（或 mqs）** |
| C10 | Event / Delivery / Attempt 持久化 | 1 | 3 | 3 | 只有 Event 和 Attempt 两个实体，没有 Delivery 实体或状态机：`outpost@v1.7.0:internal/models/entities.go:L95-L144`；受理路径不依赖 logstore（202 返回时事件只在 MQ 中）：`outpost@v1.7.0:internal/publishmq/eventhandler.go:L37-L46`；事件随 attempt 经 logmq 写入：`outpost@v1.7.0:internal/deliverymq/messagehandler.go:L311-L328`；批量写入（默认 10s/1000 条）：`outpost@v1.7.0:internal/services/builder.go:L432-L456`、`outpost@v1.7.0:internal/config/config.go:L210-L211`；PG 写入 events（含 data）和 attempts（含 event_data、response_data）：`outpost@v1.7.0:internal/logstore/pglogstore/pglogstore.go:L788-L835`；待执行的重试只存在 Redis RSMQ 中：`outpost@v1.7.0:docs/content/self-hosting/data-model.mdoc:L174-L176` | 事件载荷**会持久化**，但时机在首次 attempt 之后；未匹配的事件、被禁用目标上的投递都不留记录。要满足"受理即持久化 + Delivery 状态机"，需要在 `publishmq` 受理时写 DB，在 `logstore`/`migrator` 增加 deliveries 表和状态，在 `apirouter` 暴露查询。**模块：publishmq、logstore、migrator、apirouter** |
| C11 | OpenTelemetry 指标、追踪、日志 | 3 | 3 | 9 | traces/metrics/logs 三类分别配置：`outpost@v1.7.0:internal/config/otel.go:L19-L21`、`L206-L215`；投递延迟、成功/失败等指标：`outpost@v1.7.0:internal/emetrics/emetrics.go:L63-L137`；事件跨 publish→deliver 传播 trace：`outpost@v1.7.0:internal/eventtracer/eventtracer.go:L38-L138`；文档：`docs/content/features/opentelemetry.mdoc` | 原生满足。注意：指标维度只有 destination `type`，没有 destination_id（`emetrics.go:L122-L137`），按供应商统计失败率需要加维度或依赖 logstore 的 metrics API |
| C12 | 按目标限流与熔断 | 1 | 2 | 2 | 源码中没有任何限流实现；官方文档说明这是有意为之：`outpost@v1.7.0:docs/content/overview.mdoc:L14`；只有"连续失败 N 次后自动禁用"（默认 100），禁用后匹配阶段直接跳过该目标，投递阶段 ack 丢弃，不记录 attempt：`outpost@v1.7.0:internal/alert/evaluator.go:L119-L157`、`outpost@v1.7.0:internal/config/config.go:L581-L582`、`outpost@v1.7.0:internal/tenantstore/redistenantstore/redistenantstore.go:L650-L653`、`outpost@v1.7.0:internal/deliverymq/messagehandler.go:L160-L174`、`L458-L466` | 在 `deliverymq` handler 中加 Redis 令牌桶，超限时用 RSMQ 延后（不消耗重试次数）；熔断改成"打开 → 延后投递"，不再"禁用 + 丢弃"（改 `alert` + `deliverymq`）。**模块：deliverymq、alert**。※ 现有的自动禁用会造成静默丢失，与 PRD"零静默丢失"冲突，启用前必须改造 |
| C13 | 按 ordering_key 有序投递 | 0 | 2 | 0 | 共享队列 + 并发消费（`consumer.go:L209-L236`）；失败的消息移出主队列，进入 RSMQ 延迟队列（`deliverymq/messagehandler.go:L404-L427`），后续事件照常投递；mqs 层没有 FIFO / 分区 / ordering key（`internal/mqs` 中检索不到）；`partitionkey` 只用于向 Kafka/Kinesis **出站**时设置分区键：`outpost@v1.7.0:internal/destregistry/partitionkey/partitionkey.go:L9-L40` | 要按键串行，并支持"前序失败时阻塞后续"，需要重做 deliverymq 和 retry scheduler 的调度模型（按 (destination, key) 分区的持久化待投递队列），与现有"共享队列 + 独立延迟重试"的模型冲突，成本接近重写投递引擎 |
| C14 | 幂等键透传到对端 Header | 2 | 2 | 4 | 系统 metadata `event-id` 等于发布方传入的 `id`，作为 Header 发送：`outpost@v1.7.0:internal/destregistry/basepublisher.go:L67-L94`、`outpost@v1.7.0:internal/destregistry/providers/destwebhook/destwebhook.go:L901-L923`；Header 名可以全局改名或取消前缀：`outpost@v1.7.0:internal/config/destinations.go:L67-L75`；事件 metadata 也会以 `<prefix><key>` 形式透传：`destwebhook.go:L935-L951` | 通过配置即可满足（例如 `EVENT_ID_HEADER_NAME=Idempotency-Key`）。限制：Header 名是全局的，不能按供应商设置；不同供应商要求不同 Header 名或要求写进 Body 时，需要用 C3 的新 provider 解决 |
| C15 | 存储 / 日志中的字段级脱敏 | 1 | 2 | 2 | events.data、attempts.event_data、response_data 原样写入：`outpost@v1.7.0:internal/logstore/pglogstore/pglogstore.go:L788-L860`；现有的 `[REDACTED]` 只用于 API 请求日志中的 destination 凭据：`outpost@v1.7.0:internal/apirouter/sanitizer.go:L14-L35`、`L121-L131` | 在 `logstore.InsertMany` 之前（logmq batchprocessor 或 logstore 驱动层），按 destination 规则对 data/response 做哈希或掩码。注意：重试会从 logstore 回读事件（`deliverymq/retry.go:L135-L157`），脱敏后的数据会被当成重试载荷，因此需要另存一份原文（加密）或改变重试的事件来源。**模块：logstore（+ deliverymq）** |
| C16 | 批量重放 | 2 | 1 | 2 | 没有批量接口（路由表：`outpost@v1.7.0:internal/apirouter/router.go:L161-L200`）；可以用 `GET /attempts?status=failed&time[gte]=…` 加逐条 `POST /retry` 组合 | 用外部脚本或运维工具组合公开 API，无需改核心。"耗尽"需要在客户端判断（attempt_number 是否达到上限） |
| C17 | 无云依赖、可混合部署、无状态水平扩展 | 3 | 3 | 9 | MQ 可选 RabbitMQ / NATS JetStream：`outpost@v1.7.0:internal/config/mq.go:L17-L23`；依赖 Redis + PG/CH，三类服务可拆分部署：`outpost@v1.7.0:docs/content/self-hosting/architecture.mdoc:L8-L23`、`outpost@v1.7.0:internal/config/config.go:L48`；compose 示例：`examples/docker-compose/compose-rabbitmq.yml`、`compose-postgres.yml`；Helm chart：`deployments/kubernetes/charts/outpost` | 原生满足。注意：Redis 是配置和待执行重试的**唯一真相源**，必须开启持久化（`data-model.mdoc:L174-L176`）；Redis Cluster 已支持 |
| C18 | 首次投递 P99 < 1s | 3 | 3 | 9 | 首次投递路径：API 同步写入 deliverymq → 消费者推送 → HTTP，中间没有批处理和轮询：`outpost@v1.7.0:internal/publishmq/eventhandler.go:L159-L202`、`outpost@v1.7.0:internal/deliverymq/messagehandler.go:L124-L158`、`L199-L205`；10s 批处理只作用于 logmq 和重试，不影响首次投递 | 源码层面没有发现会增加延迟的结构；需要 S8 实测确认。注意：默认 `DELIVERY_MAX_CONCURRENCY=1`（`config.go:L196`），压测前要调大；只有并发重复请求才会触发 idempotence 的 5s 等待（`idempotence.go:L121-L122`） |
| **合计** | | | **48** | **82** | | 满分 144；82 / 144 = 56.9% |

**UNVERIFIED 项**：无。所有分值都有源码或仓库内文档证据。C9、C13、C18 已经过 S6、S7、S8 实测复核：S6 中 vendor-b 的 P50 被拖到 4.9s，S7 无序，S8 的 P99 为 57ms。

## 4. 1 分项及涉及的核心模块（决策规则条件 3）

| ID | 涉及的顶层模块（`internal/<pkg>`） | 改动性质 |
|---|---|---|
| C3 | destregistry | 新增 provider 包、在 `providers/default.go` 注册、补 metadata.json |
| C4 | destregistry | 同一个 provider 内实现 OAuth2 和按目标签名 |
| C5 | destregistry | 同一个 provider 内实现成功判定 |
| C6 | deliverymq、alert、destregistry | 修改 handler 中的重试决策，改造耗尽判定 |
| C9 | deliverymq、consumer（或 mqs） | 修改调度和并发模型 |
| C10 | publishmq、logstore、migrator、apirouter | 受理时持久化，新增 deliveries 表/状态及查询 |
| C12 | deliverymq、alert | 限流和熔断（替换"禁用 + 丢弃"） |
| C15 | logstore（+ deliverymq） | 写库前脱敏，并调整重试的事件来源 |

**去重后的模块并集**：destregistry、deliverymq、alert、consumer、publishmq、logstore、migrator、apirouter，**共 8 个**（另外 `services/builder.go` 和 `config` 的装配也必然要改）。即使不计 C10，也有 5 个以上。**条件 3 不满足。**

## 5. 非功能评估（N1–N4）

**N1 许可证**：Apache-2.0（`LICENSE`）。允许内部使用、修改和闭源分发，fork 后需保留 NOTICE 和许可证声明。✓

**N2 社区活跃度**（`gh api repos/hookdeck/outpost`，2026-10-24 采集）：
- Star 1070，fork 73，open issue 56，创建于 2024-08，最近 push 2026-10-23
- 贡献者 28 人（含 github-actions、speakeasybot 等机器人），提交高度集中：leggetter 627、alexluong 535、alexbouchardd 113，之后的贡献者均 ≤ 12。**实际核心维护者约 2–3 人，均为 Hookdeck 公司员工**，项目走向受商业公司左右
- 发布节奏密集：v1.0.4（2026-05-29）→ v1.7.0（2026-10-23），5 个月内发布 11 个服务端版本，约 2 周一次
- 结论：活跃，但维护者集中；版本节奏快对 fork 跟进不利（见 N3）

**N3 fork 后跟进上游的可行性**（`git fetch --unshallow` 后，截至 v1.7.0 统计近 6 个月提交数；全仓库同期共 214 次提交）：

| 需修改的路径 | 近 6 月提交 | 说明 |
|---|---|---|
| internal/destregistry（整体） | 23 | 其中 providers/destwebhook 14 次 |
| internal/destregistry/providers/default.go | 10 | provider 注册点，几乎每次发版都会改，**冲突热点** |
| internal/destregistry/registry.go | 3 | |
| internal/deliverymq（整体） | 9 | messagehandler.go 4 次，retry.go 5 次 |
| internal/consumer/consumer.go | 2 | |
| internal/scheduler | 7 | |
| internal/alert/evaluator.go | 2 | |
| internal/publishmq/eventhandler.go | 3 | |
| internal/logstore（整体） | 9 | pglogstore.go 5 次，driver.go 3 次 |
| internal/logmq/batchprocessor.go | 7 | |
| internal/migrator/migrations/postgres | 2 | 自增迁移序号与上游冲突风险高 |
| internal/apirouter | 19 | log_handlers.go 3 次 |
| internal/services/builder.go | 15 | 装配点，必改，**冲突热点** |
| internal/config/config.go | 25 | 必改（新增策略配置），**冲突热点** |

结论：如果只做 C3/C4/C5（新增 provider），跟进成本较低，冲突集中在 `default.go` 和 `metadata` 两处。但要补齐 C6/C9/C10/C12/C15，就要改动 deliverymq、logstore、迁移、装配和配置等高频变更文件，大约 15–20 个核心文件，每次上游发版（约 2 周）都可能冲突。**N3 评价：差。**

**N4 语言与 AI 编码友好度**：Go 1.x，代码风格统一，接口边界清晰（`Provider`/`Publisher`、`LogStore` driver、`Idempotence`、`Scheduler`），并有 driver 一致性测试套件（`internal/logstore/drivertest`、`internal/tenantstore/drivertest`），对 AI 辅助阅读和修改比较友好。团队 Go 能力未知，需要单独确认；如果团队主力不是 Go，fork 维护成本还会上升。

## 6. 可借鉴的设计（自建时参考）

| 模块 | 源码位置 | 借鉴什么 |
|---|---|---|
| Destination Provider / Publisher 抽象 | `internal/destregistry/registry.go:L41-L85` | "Provider（校验、脱敏展示、创建 Publisher）+ Publisher（Publish 返回 Delivery + 分类错误）"两层接口，正好可以作为"供应商适配器"的边界；成功判定和认证都收敛在 Publisher 内 |
| Publisher LRU 缓存 + 配置哈希键 | `internal/destregistry/registry.go:L293-L334` | 以 config + credentials + metadata 的 FNV 哈希作为缓存键，配置变更后自动失效；适合缓存 OAuth2 token 或 HTTP 连接池 |
| 投递错误三分类 | `internal/deliverymq/messagehandler.go:L30-L65`、`L362-L402` | PreDelivery / Attempt / PostDelivery 三类错误分别决定 ack/nack/重试：对端失败 → ack 并排程重试；基础设施失败 → nack 重投，不消耗重试预算 |
| 代理基础设施错误不计入重试预算 | `internal/destregistry/providers/destwebhook/httphelper.go:L37-L52` | 出口代理（固定出口 IP 场景）故障时 nack，不计入供应商失败；直接对应 PRD 中的固定出口 IP 需求 |
| 两段式幂等（publish 级 + delivery 级） | `internal/idempotence/idempotence.go:L22-L169`、`internal/models/tasks.go:L59-L72` | Redis SETNX processing/processed 两态 + TTL；投递键 `event:dest:attempt` 让 MQ 重投去重，同时允许新 attempt 执行；`RetryID=event:dest` 配合 upsert 语义，让手动重放覆盖待执行的自动重试 |
| 延迟重试调度器 | `internal/scheduler/scheduler.go:L36-L60`、`L386-L430`；`internal/deliverymq/retry.go:L176-L234` | 基于 Redis 的可见性超时队列：按任务 ID upsert、`RetryLater`（数据未就绪时不计入接收次数）、执行失败时带 jitter 退避、超过最大接收次数进入 DLQ |
| 重试由存储驱动，而非 MQ 载荷 | `internal/deliverymq/retry.go:L129-L158` | 重试任务只携带 ID，执行时从存储读取事件和上一次 attempt 号，保证状态以 DB 为准（与 PRD 的"状态存储与调度队列分离"一致）；自建时应把它改成受理时就落库，从而消除"attempt 未刷盘"的竞态（`retry.go:L176-L197`） |
| 简单 JSON 过滤匹配 | `internal/simplejsonmatch/match.go`、`operators.go`；`internal/models/entities.go:L62-L93` | 声明式的 `$eq/$in/$gte/...` 匹配，可直接用于路由条件和 C5 的响应体字段判定，不引入脚本 |
| JMESPath 字段提取 | `internal/destregistry/partitionkey/partitionkey.go:L9-L40` | 可作为 ordering_key 提取或模板映射中"取字段"的轻量方案（PRD 禁止通用 DSL，JMESPath 只用于取值，不用于转换，边界需要在 ADR 中写明） |
| 告警评估器（纯函数 + 外部执行） | `internal/alert/evaluator.go:L1-L157` | "评估器只产出信号、副作用由调用方执行"的分层；连续失败阈值（50/70/90/100%）和耗尽信号。注意不要沿用"禁用 + 丢弃"的处置方式 |
| 日志批写 + 双驱动 | `internal/logmq/batchprocessor.go`、`internal/logstore/driver/driver.go:L20-L37`、`internal/logstore/pglogstore/pglogstore.go:L788-L835` | Attempt 写入走异步批量（unnest 批插 + ON CONFLICT），与投递主路径解耦；PG/CH 驱动共用一致性测试套件（`internal/logstore/drivertest`） |
| 分区表 + 迁移 | `internal/migrator/migrations/postgres/000001_init.up.sql` | events/attempts 按时间分区（DEFAULT 分区 + 后续按月），对应 PRD 风险项"状态存储写入压力" |
| 响应体大小上限 | `internal/destregistry/providers/destwebhook/httphelper.go:L173-L201` | 超限时整体替换为占位符而不是截断，防止 attempt 日志超过 MQ 单条消息大小限制、导致无限重试 |
| 宽事件日志 | `internal/publishmq/eventhandler.go:L84-L120`、`internal/deliverymq/messagehandler.go:L274-L309` | 每次受理和每次 attempt 只输出一条结构化日志（event.received / delivery.attempted），字段齐全，便于按 event_id 追溯 |

## 7. 租户模型映射分析

Outpost 的租户模型是：Tenant 拥有 Destinations（存放在 Redis），事件发布时**必须带 `tenant_id`**，只与该租户下的 destination 匹配（`redistenantstore.go:L642-L664`）。

| 映射方式 | 可行性 | 别扭之处 |
|---|---|---|
| tenant = 来源业务系统 | 可行 | 同一个供应商要在每个来源租户下重复配置一份 destination（凭据、限流都无法共享）；重放和查询按租户隔离，这一点是合理的 |
| tenant = 供应商 | 不可行 | 发布方必须知道要发给哪个供应商，并按供应商分别发布，违反 PRD "路由全部在 Hub 配置"（方案 A），也失去一次发布扇出的能力 |
| **单一 tenant（"hub"）** | 最贴合 | 所有供应商 destination 放在同一个租户下，用 topic + filter（如 `metadata.source`）路由；默认每个租户 20 个 destination 的上限要调大（`config.go:L94`）；租户维度的权限隔离失效，`/publish` 只有一把全局 Admin API Key（`router.go:L168`），按来源系统的鉴权和查询权限需要另做 |

结论：只能用"单租户 + filter"的方式勉强套用。Outpost 的多租户、JWT 和 portal 能力在本场景中派不上用场，不计分。来源系统级的权限控制（PRD 中"按权限查询"）需要在 Outpost 前面加网关或另行开发。
