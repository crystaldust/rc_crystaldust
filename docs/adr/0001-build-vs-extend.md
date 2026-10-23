# ADR-0001: 基于开源组件自建，不在 Outpost / Svix 上二次开发

- **Status**: Proposed
- **Date**: 2026-10-24
- **Deciders**: Message Hub 技术负责人（待评审）
- **Related**: PRD `message-hub` Phase 1；`docs/spike/01`–`04`；ADR-0002、0003、0004

## Context

PRD 要求 Message Hub 把内部事件可靠地投递到多个异构的供应商 API。核心差异化能力是：
- 按目标构造请求（C3）
- 按目标认证（C4）
- 可配置成功判定（C5）
- 按目标的重试策略、隔离、限流（C6、C9、C12）

开源候选 Hookdeck Outpost（v1.7.0）与 Svix server（server-v1.102.1）面向"SaaS 推送给客户 endpoint"模型。需要判断在它们之上二次开发是否划算。

决策规则在打分前锁定并单独提交（`docs/spike/01-evaluation-checklist.md`，commit `f4bc44f`）。选择"二次开发"需同时满足：
1. 加权得分 ≥ 101 / 144
2. C3、C4、C5 中没有 0 分
3. 所有 1 分项的改动集中在 ≤ 3 个核心模块

## Options Considered

| Option | 加权得分 | 条件 1 | 条件 2 | 条件 3 | 结果 |
|---|---|---|---|---|---|
| 基于 Outpost 二次开发 | 82 / 144（56.9%） | ✗ | ✓ | ✗（涉及 8 个模块） | 不满足 |
| 基于 Svix 二次开发 | 88 / 144（61.1%） | ✗ | ✓ | ✗（涉及 5 个模块） | 不满足 |
| **基于开源组件自建** | — | — | — | — | 规则下的默认选项 |

## Decision

**基于开源组件自建 Message Hub**。不 fork Outpost 或 Svix，系统地借鉴两者已验证的设计（见下方借鉴清单）。

## Rationale

1. **两个候选都不满足条件 1 和条件 3**，结论可以由规则机械推导出来
   - Outpost 评估：`docs/spike/02-outpost-evaluation.md` §1、§4
   - Svix 评估：`docs/spike/03-svix-evaluation.md` §1、§4
2. **核心差异化能力都被硬编码**，C3、C4、C5 两者都只得 1 分。主线程已抽查确认：
   - Outpost 用 `StatusCode >= 400` 判失败
   - Svix 用 `is_success()` 判成功
   - 两者的 Body 都只能原样发送，不能重构
   - 证据见 `docs/spike/04-poc-results.md` 抽查记录
3. **两者都存在与"零静默丢失"目标冲突的设计**
   - Outpost：`/publish` 只写 MQ、不写 DB；目标自动禁用后消息被丢弃且不记录
   - Svix：先落库后入队，两步之间崩溃则永不投递；禁用目标的消息被跳过且无记录；扇出中任一目标出现内部错误，会导致整条消息重投
   - 改掉这些需要修改核心数据流，而不只是加扩展
4. **fork 后跟进上游的成本高（N3）**：需要改动的热点文件在近 6 个月频繁变更
   - Outpost：`config.go` 25 次提交，`services/builder.go` 15 次
   - Svix：`core` 14 次，`v1` 18 次
   - 两个项目都主要由 2–3 名厂商员工维护，上游演进方向以其托管产品为准
5. **PoC 实测印证了源码结论**（S1–S8，`docs/spike/04-poc-results.md`），分数无调整：
   - S3：两者都把"HTTP 200 + 业务错误码"判为成功
   - S6：Outpost 中一个慢供应商把健康供应商的投递 P50 拉高到 4.9s
   - S7：两者都不保证顺序
   - 另新发现 Svix 修改 Header 后要约 30s 才生效
6. **模型错位**：两者的租户 / 应用模型以"客户"为中心。套用到我们的场景只能用"单租户 + filter"，多租户、portal、JWT 能力用不上，按来源系统的权限控制需要另做（02 评估 §7）

## 借鉴清单

自建时参考以下设计。源码版本以评估文档中钉死的版本为准。

| 主题 | 来源 | 借鉴什么 | 用于 |
|---|---|---|---|
| 供应商适配器边界 | Outpost `internal/destregistry/registry.go:L41-L85` | Provider（校验、展示、创建 Publisher）+ Publisher（Publish 返回 Delivery 和分类错误）两层接口；成功判定与认证收敛在 Publisher 内 | Phase 4 Destination 适配层 |
| 投递错误三分类 | Outpost `internal/deliverymq/messagehandler.go:L30-L65` | 投递前错误、对端失败、投递后错误，分别决定确认、重排程或重投；基础设施故障不消耗重试预算 | Phase 4 |
| 出口代理故障不计入供应商失败 | Outpost `destwebhook/httphelper.go:L37-L52` | 固定出口 IP 场景下，代理故障不算作对端失败 | Phase 4 / 6 |
| 退避与 jitter | Svix `worker.rs:L500-L545` | 表驱动退避加 ±20% jitter；429 和超时设最低罚时；对 Retry-After 设上下界 | Phase 4 |
| 每次尝试一行 attempt 记录 | Svix `worker.rs:L333-L445` | prepare → call → 成功 / 失败三段式；记录状态码、截断后的响应、耗时、触发类型 | Phase 4 |
| 受理去重 | Svix `migrations/..._initial.up.sql:L74` | 部分唯一索引 `(scope, uid) WHERE uid IS NOT NULL`，冲突时返回 409。**必须与 Delivery 写入放在同一事务** | Phase 3 |
| 可排序 ID 与范围查询 | Svix `core/types/mod.rs:L587`、`v1/endpoints/attempt.rs:L186-L237` | 带前缀、按时间排序的 ID 作主键，时间范围查询改写为主键范围 | Phase 2 / 3 |
| 声明式 JSON 匹配 | Outpost `internal/simplejsonmatch/` | `$eq/$in/$gte/...` 匹配，用于路由条件和 C5 的响应体字段判定，不引入脚本 | Phase 2 / 4 |
| 配置缓存与主动失效 | Svix `core/message_app.rs:L21-L122` | 按作用域缓存目标配置（TTL），写配置时主动失效，热路径不查库 | Phase 4 |
| 告警评估器分层 | Outpost `internal/alert/evaluator.go` | 评估器只产出信号，副作用由调用方执行。**不沿用"禁用后丢弃"**；禁用期间的消息必须记为 SKIPPED，并可补投 | Phase 5 / 6 |
| Attempt 异步批写与分区表 | Outpost `logmq/batchprocessor.go`、`migrator/migrations/postgres/000001_init.up.sql` | 批量插入加 ON CONFLICT；按时间分区 | Phase 3 / 4 |
| 响应体大小上限 | Outpost `httphelper.go:L173-L201`、Svix 20KB 截断 | 防止日志过大导致写入失败或无限重试 | Phase 4 |
| SSRF 防护与出站代理 | Svix `core/webhook_http_client.rs:L135-L193` | 在 DNS 解析层拦截内网地址，支持白名单与代理 | Phase 4 / 6 |
| 敏感 Header 脱敏展示 | Svix `v1/endpoints/endpoint/mod.rs:L655-L678` | 读取接口只返回 Header 的键；我们还需要加密存储 | Phase 6 |
| 过期清理 | Svix `expired_message_cleaner.rs:L41-L93` | payload 单独存放，按到期时间批量 `DELETE ... SKIP LOCKED` | Phase 5 |
| 角色拆分 | Svix `lib.rs:L235-L299` | 同一二进制通过开关切换 api、worker、cleaner 角色，便于独立扩缩容 | Phase 2 架构 |
| 宽事件日志 | Outpost `publishmq/eventhandler.go:L84-L120` | 每次受理、每次 attempt 输出一条字段齐全的结构化日志 | Phase 5 |

## Consequences

- **正面**
  - 数据模型直接按 PRD 设计（Event、Destination、Route、Delivery Policy、Delivery、Attempt）
  - 受理与入队原子化，零静默丢失可以从架构层面保证
  - 不背 fork 分叉的维护负担
- **负面 / 成本**
  - 投递引擎、重试调度、查询 API 都需要自行实现和测试。借鉴清单可以降低设计风险，但不减少编码量
  - 没有现成的管理界面
- **未验证项**
  - 自建实现的延迟和吞吐尚未验证，需要在 Phase 7 实测。候选方案 S8 的实测值可作参照：P99 57ms（Outpost）/ 8.5ms（Svix）

## Revisit When

- Outpost 或 Svix 的开源版本原生支持按目标的请求构造、可配置成功判定和按目标的重试策略（例如 Svix 托管版的 transformations、OAuth 下放到开源版）
- 或 PRD 范围收窄到"统一格式、2xx 即成功"的简单投递场景
