# 04 · PoC 实测结果

## 状态：已执行（2026-10-24）

Docker 配置代理后补跑，两个候选方案的 S1–S8 全部执行完成。

- **执行脚本**：`spike/scenarios/scenarios.py <outpost|svix> <Sx|all>`
- **原始结果**：`spike/scenarios/results/*.json`（Outpost S2 中的签名密钥已脱敏）
- **环境**：
  - Docker Desktop（macOS），候选方案在容器中运行
  - 模拟供应商 `spike/mock-vendor/` 在宿主机运行，容器经 `host.docker.internal` 访问
- **候选配置**：
  - Outpost：`spike/scenarios/outpost/compose.yml`（Redis + PG + RabbitMQ，api / delivery / log 三服务拆分，`DELIVERY_MAX_CONCURRENCY=10`）
  - Svix：`spike/scenarios/svix/compose.yml`（PG + Redis，官方镜像 `svix/svix-server:v1.102.1`，默认 `worker_max_tasks=500`）
  - 两者的重试 schedule 都设为 `1,2,4` 秒，便于观察
- **场景前置**：每个场景开始前清空 mock 记录并恢复默认行为。两个目标 vendor-a、vendor-b 都订阅 `order.paid`

## 总览

| 场景 | 覆盖能力 | Outpost | Svix | 与源码结论一致？ |
|---|---|---|---|---|
| S1 扇出与受理去重 | C1、C2、C14 | ✅ 两端各收 1 次；重复发布返回 `duplicate:true` | ✅ 两端各收 1 次；重复发布返回 409 | 一致 |
| S2 请求构造 | C3、C4 | ⚠️ 静态 Header 立即生效；Body 无法重构 | ⚠️ 静态 Header **约 30s 后才生效**；Body 无法重构 | 一致，Svix 有**新发现** |
| S3 成功判定 | C5 | ❌ 200 + `{"code":1001}` 判为成功 | ❌ 判为成功 | 一致 |
| S4 重试 | C6 | ✅ 间隔 1.02 / 2.14 / 3.97s，无 jitter | ✅ 间隔 1.75 / 3.01 / 4.95s，有 jitter | 一致 |
| S5 死信与重放 | C7、C8 | ✅ 4 次失败后停止；可查询；手动重放成功 | ✅ 同左 | 一致 |
| S6 隔离 | C9 | ❌ vendor-b P50 **4.9s**，最大 **9.8s** | 默认 500 槽位：P50 2ms、最大 8ms；**10 槽位时最大 10.0s** | 一致（Svix 需要槽位被占满才会出现） |
| S7 顺序 | C13 | ❌ 成功顺序 paid → cancelled → created | ❌ 同左 | 一致 |
| S8 首次投递延迟（50 events/s × 60s） | C17、C18 | ✅ P50 24ms、**P99 57ms** | ✅ P50 3.8ms、**P99 8.5ms** | 一致 |

**结论：没有任何场景推翻源码评估，两份评估的分数都不需要调整。**Outpost 维持 82/144，Svix 维持 88/144，ADR-0001 的结论不变。实测还新发现了一个 Svix 缺陷（见 S2）。

---

## 分场景记录

### S1 — 基线：扇出、受理去重、幂等 Header

| | Outpost | Svix |
|---|---|---|
| 首次发布 | 202，`duplicate:false`，匹配到 2 个目标 | 202，返回 `msg_...` |
| 重复发布（同一 ID） | 202，`duplicate:true`，**没有重复投递** | **409 conflict** |
| 两端收到次数 | 各 1 次 | 各 1 次 |
| 幂等 Header | `X-Outpost-Event-Id: <发布方 ID>` | `svix-id: msg_...`（**服务端生成**，不是发布方的 `eventId`） |
| Body | 原样 `{"uid":1,"amount":99}` | 原样（JSON 被重新序列化，字段间多了空格） |

印证 C1（两者 2 分）、C2（3 分）、C14（Outpost 2 分 / Svix 1 分）。

### S2 — 请求构造与认证 Header

| | Outpost | Svix |
|---|---|---|
| 给 vendor-b 加 `X-Api-Key` | PATCH 200，**下一条消息立即带上** | PATCH 204；**1s 后发出的消息不带；等待 35s 后才带上** |
| 读取 API 是否回显明文 | **回显明文**（`config.custom_headers` 中可见 `spike-key`） | 只返回 key（`"sensitive":["X-Api-Key"]`） |
| vendor-a Body 改成 `{uid, evt}` | 没有任何配置项可以做到 | 没有任何配置项可以做到 |

**新发现（Svix）**：修改 endpoint Header 不会让配置缓存失效。在 `v1/endpoints/endpoint/` 模块中，只有 `crud.rs`（L98/L174/L292）和 `secrets.rs`（L156）调用了 `CreateMessageApp::invalidate`，**`headers.rs` 没有调用**。因此新 Header 要等缓存 TTL（约 30s）过期后才生效。轮换供应商 API Key 时会出现最长约 30s 的窗口，期间请求仍带旧凭据。C3/C4 的分数（1 分）不变，此项作为负面证据补充到 03 评估中。

印证了 Outpost 评估中"API Key 放在 `custom_headers` 里以明文保存，API 读取时也不脱敏"的结论（C4 = 1）。

### S3 — 成功判定

vendor-a 返回 `200 {"code":1001,"msg":"business failure"}`：
- Outpost：attempt `status=success, code=200`
- Svix：attempt `status=0`（Success）、`responseStatusCode=200`

两者都按 HTTP 状态码判定，无法识别业务失败。印证 C5 = 1。

### S4 — 重试（前 3 次 500，第 4 次 200）

| | 请求次数 | 间隔（秒） | attempt 记录 |
|---|---|---|---|
| Outpost | 4 | 1.02 / 2.14 / 3.97 | 3 failed + 1 success |
| Svix | 4 | 1.75 / 3.01 / 4.95 | 3 failed + 1 success |

Outpost 的间隔几乎精确等于 schedule，说明投递重试**没有 jitter**；Svix 的间隔有偏移，与源码中 ±20% jitter 一致。两者都使用全局 schedule（compose 中只能全局配置）。印证 C6 = 1。

### S5 — 死信与人工重放（持续 500 直到耗尽）

| | 耗尽前请求数 | 可查询失败 attempt | 手动重放 | 重放后 |
|---|---|---|---|---|
| Outpost | 4（1 次首投 + 3 次重试） | ✅ 4 条 failed | `POST /retry` → 202 | 第 5 次请求成功，attempt 记录追加 1 条 success |
| Svix | 4 | ✅ 4 条 status=2（Fail） | `POST .../resend/` → 202 | 同左 |

两者都没有显式的"DEAD"状态，失败状态只能由"最新 attempt 失败且不再重试"推断。重放请求中没有记录操作人。印证 C7 = 2。

### S6 — 隔离（vendor-a 每次响应延迟 10s，vendor-b 立即响应，20 个事件，不重试）

| | 配置 | vendor-b 延迟 P50 | vendor-b 延迟最大值 |
|---|---|---|---|
| Outpost | `DELIVERY_MAX_CONCURRENCY=10` | **4.914s** | **9.838s** |
| Svix | 默认 `worker_max_tasks=500` | 0.002s | 0.008s |
| Svix | `worker_max_tasks=10`（模拟槽位被占满） | 0.011s | **10.039s** |

- **Outpost**：所有目标共用一个投递队列和全局并发上限。vendor-a 的慢请求（超时 5s）占满 10 个槽位后，vendor-b 的投递被排在后面，健康供应商的延迟被拉高到秒级
- **Svix**：默认 500 个槽位在 20 个事件下没有被占满，所以没有表现出问题。把槽位降到 10 后，后一半消息的 vendor-b 投递等待了约 10s，这正是源码评估指出的问题：同一消息的所有 endpoint 在一个任务里 `join_all`，任务要等最慢的 endpoint 完成才释放槽位。生产中慢供应商的请求只要累积到槽位上限（≤ 30s 超时 × 足够的流量），就会出现同样的情况

两者都缺少按目标的隔离。印证 C9 = 1。

### S7 — 顺序（同一订单 3 个事件，第 1 次请求失败）

发布顺序：created → paid → cancelled。两者在 vendor-a 的成功送达顺序都是 **paid → cancelled → created**：失败的 created 重试后最后到达。没有任何顺序保证。印证 C13（Outpost 0 分 / Svix 1 分）。

### S8 — 首次投递延迟（50 events/s，持续 60s，共 3000 个事件 × 2 个目标，不重试）

| | 发布错误 | 发布 API P99 | vendor-a P50 / P99 / 最大 | vendor-b P50 / P99 / 最大 |
|---|---|---|---|---|
| Outpost | 0 | 15.9ms | 24.1 / **56.2** / 131.1 ms | 24.5 / **57.0** / 129.8 ms |
| Svix | 0 | 7.8ms | 3.7 / **8.5** / 39.8 ms | 3.8 / **8.4** / 38.5 ms |

两者的 P99 都远低于 1s，印证 C18 = 3。所有组件都是自托管开源镜像，没有云依赖，印证 C17 = 3。

> 这只是本机低负载下的**量级验证**，不是容量基线。Outpost 多出来的约 20ms 来自 API → RabbitMQ → delivery 服务的额外跳转。

---

## 对决策的影响

- 所有场景与源码评估一致，**分数不调整**
- ADR-0001（自建）的结论不变，而且证据从"源码推断"升级为"源码 + 实测"
- 对自建方案的启示：
  - S6 说明**按目标隔离**必须是投递引擎的一等设计（ADR-0003 中按目标公平领取、限制每个目标的在途数量），不能只靠全局并发上限
  - S2 的 Svix 缺陷说明，配置变更后的**缓存失效**必须覆盖所有写路径，凭据轮换要立即生效
  - S8 中候选方案的延迟是实测参照：自建方案在 Phase 7 的目标应至少达到 P99 < 100ms（低负载），远高于 PRD 要求的 1s

## 抽查记录（源码证据核对）

| 项 | 引用 | 核对结果 |
|---|---|---|
| Outpost C5 | `outpost@v1.7.0:internal/destregistry/providers/destwebhook/httphelper.go:L89-L129` | 确认：`if resp.StatusCode >= 400` 判失败；S3 实测一致 |
| Svix C5 | `svix@server-v1.102.1:server/svix-server/src/worker.rs:L377-L427` | 确认：`res.status().is_success()`；S3 实测一致 |
| Svix C12 | `throttleRate` / `rate_limit` 的引用位置 | 确认：只出现在 `core/message_app.rs`、`db/models`、`v1/endpoints` 中，`worker.rs` 与 `queue/` 均不读取 |
| Outpost C9 | `outpost@v1.7.0:internal/consumer/consumer.go:L209-L236` | 确认：单一全局信号量；S6 实测一致 |
| Svix 缓存失效 | `svix@server-v1.102.1:server/svix-server/src/v1/endpoints/endpoint/*.rs` | 新增：`headers.rs` 不调用 `invalidate`；S2 实测一致 |
