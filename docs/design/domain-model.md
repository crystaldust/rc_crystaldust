# Message Hub 领域模型与行为规格

> **状态**：Phase 2 契约，本文是 Message Hub 行为的**唯一权威**。
> `api/openapi.yaml`、`schemas/config.schema.json`、`db/migrations/`、`internal/domain`、`testdata/contracts/` 都必须与本文一致；如有冲突，先改本文，再改契约文件。
> **关联决策**：ADR-0001 至 ADR-0007（`docs/adr/`）。

---

## 1. 实体与关系

```
Principal ──提交──► Event 1 ───── N Delivery 1 ───── N Attempt
                      │                │
                      │                └── N OperatorAction（replay / skip 审计）
                      └── 1 EventPayload（payload 单独存放，便于按保留期清理）

配置（YAML）：Principal、Policy、Destination、Route
```

| 实体 | 说明 | 生命周期 |
|---|---|---|
| **Event** | 业务系统提交的一个事件，受理后不可变 | 受理时创建 |
| **EventPayload** | 事件的 payload，与 Event 一对一 | 按保留期清理（Phase 5） |
| **Delivery** | 一个 Event 对一个 Destination 的投递单元，有独立的状态机 | 受理时随 Event 在同一事务中创建 |
| **Attempt** | 一次实际的 HTTP 调用记录 | 每次调用后追加，只增不改 |
| **OperatorAction** | 运维的 replay / skip 操作审计 | 每次操作追加 |
| **Principal**（配置） | API 调用方：来源系统或运维主体 | 配置文件定义 |
| **Policy**（配置） | 投递策略：超时、重试、成功判定、顺序 | 配置文件定义 |
| **Destination**（配置） | 一个外部供应商端点及其请求模板 | 配置文件定义 |
| **Route**（配置） | `event_type`（加可选条件）→ 一个或多个 Destination | 配置文件定义 |

一个 Event 对同一个 Destination **最多只有一个** Delivery。多个 Route 指向同一 Destination 时去重。

---

## 2. Event 信封

业务系统提交（`POST /v1/events`）的请求体字段：

| 字段 | 必填 | 类型 | 规则 |
|---|---|---|---|
| `source_event_id` | 是 | string | 1–200 字符；与 `source` 组成去重键 |
| `event_type` | 是 | string | 匹配 `^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`，≤ 100 字符，例如 `order.paid` |
| `occurred_at` | 是 | string | RFC 3339 时间（带时区） |
| `payload` | 是 | object | JSON 对象（`{}` 合法）；序列化后 ≤ 256 KiB（262144 字节） |
| `idempotency_key` | 否 | string | 1–200 字符；Hub 不解释其含义，由模板 `{{event.idempotency_key}}` 透传给对端 |
| `ordering_key` | 否 | string | 1–200 字符；仅在 Policy `ordering.mode: per_key` 时生效 |
| `metadata` | 否 | object | string → string；≤ 20 项；key 匹配 `^[a-zA-Z0-9_.-]{1,64}$`；value ≤ 256 字符 |

- **不允许出现其他字段**，包括 `source`
- **`source` 由 API Key 推导**：等于通过认证的 Principal 的 `id`（角色必须为 `source`），防止冒充其他来源

受理后，Event 额外具有以下字段：

| 字段 | 说明 |
|---|---|
| `id` | UUIDv7，时间有序 |
| `source` | 提交方的 Principal id |
| `ingest_seq` | 受理顺序号，全局单调递增，用于有序投递 |
| `received_at` | 受理时间 |
| `routing_status` | `routed` \| `unrouted`，见 §3 |
| `matched_route_ids` | 匹配到的 Route id 列表 |
| `routing_config_hash` | 受理时生效配置的 `ConfigHash` |

---

## 3. 受理语义

1. **认证**：`Authorization: Bearer <api_key>`。计算 SHA-256 后查找 Principal；找不到返回 401；角色不是 `source` 返回 403
2. **校验**：按 §2 校验，失败返回 400 `validation_failed`；payload 超过 256 KiB 返回 413 `payload_too_large`
3. **去重**：若 `(source, source_event_id)` 已存在，返回 **200**，`duplicate: true`，带回**原始的** `event_id`、`routing_status` 和 deliveries。不比较 payload 是否相同，也不重新路由
4. **路由**：用当前配置找出匹配的 Route（§4），得到去重后的 Destination 集合
5. **持久化**：在**同一个数据库事务**中写入 events、event_payloads 以及每个 Destination 对应的一条 Delivery（`status=PENDING`，`scheduled_at=now()`）。事务提交后执行 `NOTIFY hub_delivery_ready`
6. **响应**：返回 **202**，包含 `event_id`、`duplicate: false`、`routing_status`、`matched_route_ids`、`deliveries[{delivery_id, destination_id, status}]`

**路由结果显式记录**：
- `routing_status = routed`：至少匹配到一个 Destination
- `routing_status = unrouted`：没有匹配到任何 Route；此时 `matched_route_ids = []`，`deliveries = []`
- 两个字段由数据库 CHECK 约束保持一致
- `unrouted` 事件照常落库并返回 202（零静默丢失），受理时输出一条 `warn` 日志 `event.unrouted`，Phase 5 基于该字段提供指标与告警
- `unrouted` 事件**不会自动补路由**。补充路由配置后，需要业务方重新提交（使用新的 `source_event_id`）。是否提供"重新路由"能力列入 PRD Open Questions

---

## 4. 路由

Route 匹配条件（全部满足才算匹配）：
- `event_type` 与事件的 `event_type` **完全相等**（不支持通配）
- 若配置了 `sources`，事件的 `source` 必须在列表中
- 若配置了 `match`，所有规则都必须满足（算子见 §7.2，路径根为 `payload.` 或 `metadata.`）

一个事件可以匹配多个 Route。所有匹配 Route 的 `destinations` 取并集并去重，每个 Destination 生成一条 Delivery。

---

## 5. Delivery 状态机

### 5.1 状态

| 状态 | 含义 | 终态 |
|---|---|---|
| `PENDING` | 等待首次领取（或重放后等待领取） | 否 |
| `IN_FLIGHT` | 已被 worker 领取，持有 lease | 否 |
| `RETRY_SCHEDULED` | 失败后等待到期重试 | 否 |
| `SUCCEEDED` | 成功判定通过 | 是 |
| `DEAD` | 永久失败或重试预算耗尽 | 是 |
| `SKIPPED` | 运维跳过 | 是 |

### 5.2 迁移

| From | Trigger | To | 说明 |
|---|---|---|---|
| PENDING | `Claim` | IN_FLIGHT | 领取时设置 `lease_owner`、`lease_expires_at` |
| RETRY_SCHEDULED | `Claim` | IN_FLIGHT | `scheduled_at` 到期后领取 |
| IN_FLIGHT | `Reclaim` | IN_FLIGHT | lease 过期后由其他 worker 接管（原 worker 视为崩溃） |
| IN_FLIGHT | `Succeed` | SUCCEEDED | 成功判定通过 |
| IN_FLIGHT | `Retry` | RETRY_SCHEDULED | 可重试的失败，且仍有重试预算 |
| IN_FLIGHT | `InfraRetry` | RETRY_SCHEDULED | 基础设施故障，**不消耗重试预算**（见 5.3） |
| IN_FLIGHT | `GiveUp` | DEAD | 永久失败，或重试预算耗尽 |
| DEAD | `Replay` | PENDING | 运维重放 |
| SUCCEEDED | `Replay` | PENDING | 运维重放（例如对端丢失数据，要求重发） |
| SKIPPED | `Replay` | PENDING | 运维重放 |
| DEAD | `Skip` | SKIPPED | 运维跳过，用于解除有序阻塞 |

共 11 个合法迁移。**其余 37 个"状态 × 触发器"组合都是非法迁移**，返回 `ErrInvalidTransition`。

**持久化要求**（ADR-0009 M2）：每个迁移在数据库中都以**带前置状态条件的更新**完成，例如 Claim 为 `UPDATE … WHERE id=$1 AND status IN ('PENDING','RETRY_SCHEDULED')`；更新到 0 行表示迁移已被其他 worker 完成，调用方放弃即可。这使同一个 delivery 被重复派发时（轮询竞争、日后 MQ 的至少一次投递）不会被重复投递。

### 5.3 计数与重放规则

- **`attempt_count`**：只在实际发出（或可能已发出）HTTP 请求时加 1
  - `Reclaim` 时，原 attempt 视为"可能已发出"，计入预算
  - `InfraRetry` 不加 1。基础设施故障指请求没有到达对端，例如：出口代理不可用、渲染所需的 secret 无法读取、OAuth2 token 端点不可用
- **重试预算**：`attempt_count < policy.retry.max_attempts` 时可以 `Retry`，否则 `GiveUp`。`max_attempts` 包含首次投递
- **重放（`Replay`）**：
  - `replay_count + 1`，`attempt_count` 归零（获得新一轮预算），`scheduled_at = now()`
  - 写一条 OperatorAction（actor、reason、from_status）
  - 重放后的 attempt 记录 `trigger = replay`
- **跳过（`Skip`）**：只允许从 DEAD 跳过，写一条 OperatorAction

---

## 6. 有序投递（ADR-0007）

- 只在 Policy `ordering.mode: per_key`，且事件带有 `ordering_key` 时生效
- **有序范围**：`(destination_id, ordering_key)`
- **顺序依据**：`ingest_seq`（受理顺序）
- **可领取条件**：同一范围内所有 `ingest_seq` 更小的 Delivery 都已"有序结清"
- **有序结清**：
  - 状态为 `SUCCEEDED` 或 `SKIPPED`
  - `on_predecessor_dead: skip` 时，`DEAD` 也算结清
  - `on_predecessor_dead: block`（默认）时，`DEAD` 不算结清，后续 Delivery 保持 PENDING，直到前序被 replay 成功或被 skip
- **可见性**：查询 API 为被阻塞的 Delivery 返回 `blocked_by`（最早一个未结清的前序 Delivery 的 id），该字段是推导值，不存库
- **业务方义务**：同一个 `ordering_key` 的事件必须**串行提交**（等上一个返回后再提交下一个），否则 Hub 无法知道其业务先后

---

## 7. 请求构造与结果判定

### 7.1 模板语言（ADR-0006）

**位置**：Destination 的 `request.url`、`request.headers` 的值、`request.body` 中的叶子字符串。

**语法**：`{{ path [| func [arg]]... }}`，`{{`、`}}` 内的空白可选。

**路径**：

| 根 | 可用位置 | 说明 |
|---|---|---|
| `event.<field>` | 全部 | `id`、`source`、`source_event_id`、`event_type`、`occurred_at`、`received_at`、`idempotency_key`、`ordering_key` |
| `event.payload.<path>` | 全部 | 点分路径；数组用数字下标，如 `event.payload.items.0.sku` |
| `event.metadata.<key>` | 全部 | |
| `secret.<NAME>` | 全部 | Destination `secrets` 中声明的名字，运行时从 `env:` 引用解析 |
| `delivery.id`、`delivery.attempt` | 全部 | 当前 Delivery id 与本次 attempt 序号（从 1 开始） |
| `now` | 全部 | 渲染时刻（UTC），类型为时间 |
| `request.body` | 仅 Header | 已渲染的 Body 的最终字节（字符串），用于签名 |

**渲染顺序**：URL → Body → Headers（Header 可以引用渲染后的 Body）。

**类型规则**：
- **Body 中的单表达式**：若某个字符串值**整体恰好是**一个表达式（首尾可以有空白），结果保留原始 JSON 类型（number、bool、string、object、array、null）
- **嵌入文本**：表达式嵌在其他文本中时，结果转为字符串后拼接。null 转为空字符串；object / array 转为紧凑 JSON
- **Header 的值**：一律是字符串
- **URL**：表达式结果先转为字符串，再按 URL **路径段**转义（`/` 也会被转义）；URL 中字面量部分不做转义
- **时间**：类型为时间的值（`now`、`event.occurred_at`、`event.received_at`），字符串化时默认格式为 RFC 3339（UTC，秒级精度）

**内置函数**（固定 12 个，左值为管道输入）：

| 函数 | 参数 | 输入 → 输出 |
|---|---|---|
| `lower` | — | string → string |
| `upper` | — | string → string |
| `trim` | — | string → string（去除首尾空白） |
| `sha256` | — | string → string（小写 hex） |
| `base64` | — | string → string（标准编码，带填充） |
| `urlencode` | — | string → string（查询参数编码，`url.QueryEscape`） |
| `default` | 字面量（带引号的字符串、数字、`true`/`false`、`null`） | 任意 → 输入为缺失或 null 时返回参数，否则返回输入 |
| `format_time` | `rfc3339` \| `unix` \| `unix_ms` | 时间或 RFC 3339 字符串 → string / number |
| `to_string` | — | 任意 → string（规则同"嵌入文本"） |
| `json` | — | 任意 → string（紧凑 JSON 序列化） |
| `hmac_sha256_hex` | `secret.<NAME>` | string → string（以该 secret 为密钥的 HMAC-SHA256，小写 hex） |
| `hmac_sha256_base64` | `secret.<NAME>` | string → string（同上，标准 base64） |

字符串函数遇到数字或布尔值时，先按"嵌入文本"规则转为字符串再处理。

**错误**（任一发生则渲染失败）：

| 错误类别 | 场景 |
|---|---|
| `syntax` | 表达式语法错误，例如 `{{ event.payload.x`、`{{ | lower }}` |
| `missing_path` | 路径不存在（包括中途遇到 null 或类型不符），且管道中没有 `default` |
| `unknown_function` | 函数名不在上表中 |
| `bad_argument` | 参数个数或类型错误，例如 `format_time` 的参数不是三种之一、`hmac_sha256_hex` 的参数不是 `secret.*` |

渲染失败 → outcome = `render_error` → `GiveUp` → DEAD。修复配置后可以 replay。

### 7.2 成功判定

`policy.success`：
- `status`：状态码列表，元素为精确值（如 `200`、`201`）或类别（`"2xx"`、`"4xx"` 等）
- `body`（可选）：规则列表，**全部满足**才算通过

规则结构：`{ path, op, value }`。

| 算子 | value | 满足条件 |
|---|---|---|
| `eq` | 任意 JSON 值 | 路径存在，且值等于 value（数字按数值比较，`0` 与 `0.0` 相等） |
| `ne` | 任意 JSON 值 | 路径不存在，或值不等于 value |
| `in` | 数组 | 路径存在，且值等于数组中任一元素 |
| `exists` | 不填 | 路径存在（值可以为 null） |
| `not_exists` | 不填 | 路径不存在 |

- 路径为点分路径，相对于响应体 JSON 的根，例如 `code`、`data.result.0.status`
- 配置了 `body` 规则但响应体不是合法 JSON → **不满足**
- Route 的 `match` 使用同样的算子，但路径以 `payload.` 或 `metadata.` 开头

### 7.3 结果分类

按以下顺序判断，取第一个命中的结果：

| 序 | 条件 | outcome | 触发器 |
|---|---|---|---|
| 1 | 渲染失败 | `render_error` | `GiveUp` |
| 2 | 基础设施故障（§5.3） | `infra_error` | `InfraRetry` |
| 3 | 网络错误（连接失败、DNS 失败、TLS 失败）或超时 | `retryable` | 有预算则 `Retry`，否则 `GiveUp` |
| 4 | 状态码满足 `success.status`，且 `success.body` 全部满足 | `success` | `Succeed` |
| 5 | 状态码命中 `retry.retry_on.status` | `retryable` | 有预算则 `Retry`，否则 `GiveUp` |
| 6 | 其余情况 | `permanent` | `GiveUp` |

例：状态码 200 满足 `2xx`，但 body 规则 `code eq 0` 不满足（响应为 `{"code":1001}`），且 200 不在 `retry_on.status` 中 → `permanent`。

### 7.4 重试延迟

- 第 n 次重试（n 从 1 开始，即第 n+1 次 attempt 之前）的基础延迟：`base(n) = min(backoff.max, backoff.initial × backoff.multiplier^(n−1))`
- 实际延迟：`base(n) × (1 + u)`，其中 `u` 均匀分布于 `[−jitter, +jitter]`，结果再以 `backoff.max` 封顶
- **Retry-After**：响应状态码为 429 或 503，且带有合法的 `Retry-After` 头（秒数或 HTTP 日期）时，延迟 = `min(backoff.max, max(实际延迟, Retry-After))`
- 黄金用例中 `jitter = 0`，结果确定

---

## 8. 按目标隔离

- 每个 Destination 有 `max_in_flight`（默认 10，范围 1–1000）
- 领取时，同一 Destination 处于 IN_FLIGHT 的 Delivery 数不得超过该值
- 同一个 Event 的多个 Delivery 彼此独立领取、独立完成，**不存在"一个事件的所有目标都完成才释放"的耦合**（PoC S6 中 Svix 的问题）

---

## 9. 权限

| 角色 | 受理事件 | 查询事件 / 投递 | replay / skip |
|---|---|---|---|
| `source` | ✅（事件的 `source` 为自身 id） | 只能查询 `source` 为自身的数据 | ❌ |
| `viewer` | ❌ | 全部 | ❌ |
| `operator` | ❌ | 全部 | ✅ |

- `source` 角色查询其他来源的数据时返回 **404**（而不是 403），避免泄露数据是否存在
- 角色不允许的操作返回 403
- API Key 只以 SHA-256 形式出现在配置引用中，Hub 不保存明文

**API Key 要求**（ADR-0008）：
- 格式：`mhk_` 前缀 + 32 字节密码学随机数的 base64url 编码（无填充），共 47 个字符。前缀便于密钥扫描工具识别
- 只能由 Hub 提供的工具生成（Phase 3），工具同时输出 key（交给调用方，只显示一次）和它的 SHA-256 小写 hex（放入环境变量）
- 环境变量中可以放**最多两个**以逗号分隔的哈希（当前 key 与新 key），用于无中断轮换；轮换完成后删除旧哈希
- Hub 在启动时校验环境变量内容为 1–2 个 64 位小写 hex，格式不符则拒绝启动

---

## 10. Attempt 记录内容

| 字段 | 内容 |
|---|---|
| `number` | 本轮内的序号（从 1 开始，replay 后重新计数） |
| `trigger` | `auto` \| `replay` |
| `started_at`、`duration_ms` | 开始时间与耗时 |
| `outcome` | `success` \| `retryable` \| `permanent` \| `render_error` \| `infra_error` |
| `status_code` | 响应状态码（没有响应时为空） |
| `error` | 错误描述（网络错误、渲染错误类别加原因等） |
| `request` | 方法、URL、Header、Body |
| `response` | 状态码、Content-Type、Body |
| `config_hash` | 渲染时生效配置的 `ConfigHash` |
| `next_attempt_at` | 若进入 RETRY_SCHEDULED，下一次的计划时间 |

**脱敏**（ADR-0008）：

"携带密钥的表达式"指**路径根为 `secret.` 的表达式**，例如 `{{ secret.TOKEN }}`。`{{ request.body | hmac_sha256_hex secret.KEY }}` 的路径根是 `request.body`，`secret.KEY` 只是签名函数的参数，输出的是签名而不是密钥本身，因此**不算**携带密钥的表达式。

"敏感名称"由 `domain.IsSensitiveName` 判定（与配置校验共用同一规则）：`Authorization`、`Proxy-Authorization`、`Cookie`、`Set-Cookie`、`key`，以及包含 `token`、`secret`、`password`、`passwd`、`signature`、`credential`、`api-key`/`apikey`、`access-key`、`private-key`，或以 `-key` 结尾的名称（不区分大小写，`_` 视同 `-`）。含 `idempotency` 的名称除外。

| 位置 | 记为 `***` 的部分 |
|---|---|
| Header | 名称为敏感名称的 Header，整个值；值中含有携带密钥的表达式的 Header，整个值 |
| URL | 携带密钥的表达式的输出；名称为敏感名称的查询参数的值 |
| 请求体 | 所在 key 为敏感名称的叶子值；含有携带密钥的表达式的叶子值（整个叶子） |
| OAuth2 | 自动附加的 `Authorization: Bearer <token>` 按 Header 规则脱敏；获取 token 的请求与响应**不写入** attempt 记录 |

- 脱敏在**截断之前**完成，保证截断不会留下部分密钥
- 响应体不做密钥脱敏（Hub 无法知道供应商会回显什么），只截断；这是已知的残余风险，记录在 ADR-0008
- 字段级的业务数据脱敏（如 Body 中的邮箱）属于 Phase 6

**截断**：请求体与响应体各截断到 16 KiB（16384 字节），截断时记 `truncated: true`。

---

## 11. 配置模型（ADR-0005）

配置文件为 YAML，结构由 `schemas/config.schema.json` 定义，不允许未知字段。

```yaml
version: 1

principals:
  - id: order-service              # role=source 时即为事件的 source
    role: source                   # source | viewer | operator
    api_key_sha256_ref: env:HUB_KEY_ORDER_SERVICE_SHA256

policies:
  - id: default
    timeout: 5s                    # 单次 HTTP 调用超时
    retry:
      max_attempts: 8              # 含首次投递，1–50
      backoff: { initial: 5s, multiplier: 2.0, max: 1h, jitter: 0.2 }
      retry_on: { status: [408, 425, 429, "5xx"] }
    success:
      status: ["2xx"]
      body: [ { path: code, op: eq, value: 0 } ]     # 可选
    ordering: { mode: none }       # none | per_key
    # mode: per_key 时可写 on_predecessor_dead: block | skip（默认 block）

destinations:
  - id: crm
    policy: default
    max_in_flight: 10              # 可选，默认 10
    secrets: { CRM_TOKEN: env:CRM_TOKEN }
    auth: { type: none }           # none | oauth2_client_credentials
    request:
      method: POST                 # POST | PUT | PATCH
      url: "https://crm.example.com/contacts/{{event.payload.contact_id}}/status"
      headers:
        Content-Type: application/json
        Authorization: "Bearer {{secret.CRM_TOKEN}}"
      body:                        # 任意 YAML 值；叶子字符串可以是模板
        status: paid
        paid_at: "{{event.occurred_at}}"

routes:
  - id: order-paid
    event_type: order.paid
    sources: [order-service]       # 可选
    match: [ { path: payload.channel, op: eq, value: ads } ]   # 可选
    destinations: [crm]
```

`auth.type: oauth2_client_credentials` 时的字段：`token_url`、`client_id_ref`、`client_secret_ref`、`scopes`（可选，字符串数组）。获取到的 token 以 `Authorization: Bearer <token>` 形式附加到请求上，并在过期前缓存复用（Phase 4 实现）。

**格式约束**：

| 项 | 规则 |
|---|---|
| 各类 id | `^[a-z][a-z0-9-]{1,62}$`，同类内唯一 |
| duration | `^[0-9]+(ms\|s\|m\|h)$` |
| 引用 | `^env:[A-Z][A-Z0-9_]*$` |
| `secrets` 的 key | `^[A-Z][A-Z0-9_]*$` |
| `jitter` | `[0, 1)` |
| `multiplier` | `≥ 1` |
| `max_attempts` | `1–50` |
| `max_in_flight` | `1–1000` |
| `retry_on.status` / `success.status` 元素 | 100–599 的整数，或 `^[1-5]xx$` 字符串 |

**凭据字段**（ADR-0008，加载时执行）：
- 模板中引用的每个 `secret.NAME`，都必须在该 Destination 的 `secrets` 中声明；每个 Destination 只能访问自己声明的 secret
- **名称为敏感名称**（规则见 §10）的 Header、URL 查询参数、Body 字段，其值必须含有引用 `secret.*` 的表达式（直接取值或作为 `hmac_*` 的参数均可），**不允许写字面量**
- 确实需要字面量的字段（例如 `X-Signature-Version: v1`、`token_type=bearer`），必须显式列入该 Destination 的 `allow_literal`，格式为 `headers.<Header 名>`（不区分大小写）、`url.query.<参数名>`、`body.<点分路径>`（数组用数字下标，如 `body.auth.0.token_type`）
- `allow_literal` 中没有对应任何"敏感名称且为字面量"字段的条目视为过期，加载失败，保证允许列表不会悄悄变宽
- 配置校验是第一道防线；仓库的密钥扫描（gitleaks 等）是第二道防线（ADR-0008）

```yaml
destinations:
  - id: vendor-x
    secrets: { API_KEY: env:VENDOR_X_API_KEY }
    allow_literal: [headers.X-Signature-Version]
    request:
      url: "https://x.example.com/hook?api_key={{ secret.API_KEY }}"   # 合法：引用 secret
      headers:
        X-Signature-Version: v1                                         # 合法：已列入 allow_literal
        # X-Api-Key: sk_live_abc                                        # 非法：敏感名称写字面量
```

**语义校验**（加载时执行，错误一次性全部返回）：
- route 引用的 destination、sources 引用的 principal（且角色必须为 `source`）都存在
- destination 引用的 policy 存在
- `event_type` 符合 §2 的格式
- `timeout` 小于 `retry.backoff.max`
- 同一 `event_type` 下多个 route 指向同一 destination → 加载成功，但返回 warning

**ConfigHash**：对加载后的配置做规范化 JSON 序列化（键排序），取 SHA-256 的小写 hex。

---

## 12. API 概览

完整定义见 `api/openapi.yaml`。所有响应使用统一信封：

```json
{ "success": true, "data": { }, "error": null, "meta": { } }
{ "success": false, "data": null, "error": { "code": "validation_failed", "message": "...", "details": [ { "field": "event_type", "reason": "..." } ] }, "meta": { } }
```

| Method | Path | 角色 | 说明 |
|---|---|---|---|
| POST | `/v1/events` | source | 受理（202 新事件 / 200 重复） |
| GET | `/v1/events/{event_id}` | 全部 | 事件、payload、deliveries 摘要（含 `blocked_by`） |
| GET | `/v1/events` | 全部 | 列表，支持过滤与游标分页 |
| GET | `/v1/deliveries/{delivery_id}` | 全部 | Delivery 与 attempts |
| GET | `/v1/deliveries` | 全部 | 列表，支持过滤与游标分页 |
| POST | `/v1/deliveries/{delivery_id}/replay` | operator | 重放（202；状态不允许时 409） |
| POST | `/v1/deliveries/{delivery_id}/skip` | operator | 跳过（200；只允许 DEAD，否则 409） |
| GET | `/healthz`、`/readyz` | 无需认证 | 存活与就绪 |

| HTTP | error.code |
|---|---|
| 400 | `validation_failed` |
| 401 | `unauthenticated` |
| 403 | `forbidden` |
| 404 | `not_found` |
| 409 | `invalid_state` |
| 413 | `payload_too_large` |
| 429 | `rate_limited`（预留，Phase 6） |
| 500 | `internal` |

---

## 13. 补充约定

> 以下约定在 Phase 2 编写契约文件时确定，用于消除前文未覆盖的细节。契约文件与测试已按这些约定实现。

### 13.1 受理

| 项 | 约定 |
|---|---|
| payload 大小 | 按**请求中 `payload` 字段的原始字节数**计算（含空白），≤ 262144 字节；不重新序列化后再计算 |
| 重复的 JSON 键 | 当前实现以最后出现的值为准。Phase 3 在 HTTP 层加上严格解析，拒绝重复键（返回 400） |
| 日期合法性 | 以 Go `time.Parse(time.RFC3339)` 为准；`2026-02-31` 这类不存在的日期返回 400（OpenAPI 的格式正则无法表达，属于服务端规则） |
| metadata key 的格式 | 由服务端校验（OpenAPI 3.0 无法表达 key 的约束） |
| 空字符串 | 可选字符串字段不允许为空字符串（长度至少为 1）；数据库中"缺省"一律存为 NULL |

### 13.2 模板

| 项 | 约定 |
|---|---|
| URL 中的转义 | `?` 之前的表达式结果按**路径段**转义（`url.PathEscape`，`/` 也转义）；`?` 之后的表达式结果按**查询参数**转义（`url.QueryEscape`）。因此 URL 中**不需要也不应使用** `urlencode`，否则会被转义两次 |
| `format_time unix` / `unix_ms` 的类型 | Body 中单表达式时结果为 **number**；嵌入文本或用在 Header、URL 中时为字符串 |
| `format_time rfc3339` | 转为 UTC，秒级精度（舍弃小数部分），与时间的默认字符串化相同 |
| 紧凑 JSON 的键顺序 | object 序列化时**按键排序**（与 Go `encoding/json` 对 map 的处理一致） |
| `request.body` 用在非 Header 位置 | `syntax` 错误（可在加载配置时静态检测） |
| 任何模板（含 `hmac_*` 的参数）引用未在 `secrets` 中声明的名字 | **加载配置时即失败**（`references undeclared secret`，见 §11 凭据字段）；引擎在渲染时若仍遇到此情况，按 `bad_argument` 处理 |
| 可选事件字段缺省（如 `event.idempotency_key` 未提供） | `missing_path`，需要用 `default` 兜底 |

### 13.3 成功判定与重试

| 项 | 约定 |
|---|---|
| `eq` / `in` 比较 | 类型不同即不相等，例如 `"0"` 与 `0` 不相等 |
| Content-Type | 判定不看 Content-Type，只看响应体是否为合法 JSON；空响应体（如 204）不是 JSON |
| Retry-After | 支持秒数与 HTTP 日期两种格式；HTTP 日期以收到响应的时刻为基准换算；负数、无法解析的值忽略 |

### 13.4 API

| 项 | 约定 |
|---|---|
| `/readyz` 未就绪 | 返回 503，`error.code = internal` |
| 时间范围过滤 | 事件用 `received_from`（含）/ `received_to`（不含）；投递用 `created_from`（含）/ `created_to`（不含） |
| Delivery 详情的 attempts 分页 | 复用 `cursor` / `limit`，`meta.next_cursor` 指向 attempts 的下一页 |
| replay / skip 请求体 | `reason` 必填，1–500 字符；响应 `data` 为 OperatorAction 记录 |
| 读取响应中的可选字段 | 缺省的可选字段返回 `null`；`metadata` 缺省时返回 `{}` |
| 事件列表 | 列表项不包含 payload 与 deliveries，需要时调用详情接口 |
| 路径参数格式错误 | 非法 UUID 返回 400 `validation_failed` |

### 13.5 配置

| 项 | 约定 |
|---|---|
| Principal 的认证 | 配置中只有 `env:` 引用；运行时读取环境变量得到 key 的 SHA-256，再构建"哈希 → Principal"查找表（Phase 3） |
| 重复的 `api_key_sha256_ref` | 加载失败，因为认证会产生歧义 |
| 密钥值的来源 | 只来自进程环境变量；当前阶段由 Kubernetes Secret 注入（`docs/deploy/k8s-secrets.md`）；仓库中禁止出现 `.env` 文件（ADR-0008） |
| 引用前缀 | 目前只支持 `env:`；`config.RequiredEnv()` 列出全部被引用的变量名。新前缀（如 `vault:`、`file:`）在解析器实现后才加入 schema |
| 密钥轮换与 ConfigHash | ConfigHash 只基于引用名计算，轮换密钥值不改变哈希；需要追溯时以部署记录为准 |
| ConfigHash 的计算范围 | 对**填充默认值之后**的配置计算；修改注释、显式写出默认值不改变哈希；duration 按原文参与计算（`5s` 与 `5000ms` 哈希不同） |
| 重复路由的 warning | 同一 `event_type` 的两个 route 指向同一 destination 即给出 warning，不分析 `sources` / `match` 条件是否重叠 |

### 13.6 数据库

| 项 | 约定 |
|---|---|
| OperatorAction 的外键 | **不级联删除**，审计记录不会被静默删除；因此删除带有 replay / skip 历史的事件前，需要先按保留策略处理审计记录（Phase 5） |
| `deliveries.ingest_seq` | 从所属事件复制而来，不建外键；用于有序领取时避免关联查询 |
| 待领取索引 | Phase 2 为 `(scheduled_at) WHERE status IN (...)`；Phase 4 实现按目标公平领取时，若 EXPLAIN 显示需要，再改为 `(destination_id, scheduled_at)` |
