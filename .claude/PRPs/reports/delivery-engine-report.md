# Implementation Report: 投递引擎（Message Hub Phase 4）

## Summary

实现了 `worker` 角色：

- **模板引擎** `internal/template`：词法、语法、12 个内置函数、URL 路径段 / 查询参数转义、加载时静态校验（`ValidateField` 接入 `config.WithTemplateValidator`）；一次求值同时产出实际值与脱敏值（hmac 签名按实际 Body 计算）
- **成功判定与结果分类** `internal/outcome`、**退避** `internal/backoff`（含 Retry-After 秒数与 HTTP 日期）
- **出站调用** `internal/outbound`：共享 Transport 的 HTTP 客户端（超时、不跟随重定向、1 MiB 读取上限、网络 / 出口代理 / 非法请求三类错误）、OAuth2 client credentials token 缓存、attempt 记录的脱敏与截断
- **派发器** `internal/dispatch`：`Dispatcher` 接口（M1）与 PostgreSQL 实现——按目标领取（`FOR UPDATE SKIP LOCKED` 只找候选，状态迁移全部是条件更新，M2）、有序检查（block / skip）、`max_in_flight`、lease 过期接管、完成时状态与 attempt 同一事务写入；LISTEN 直连 + 1s 轮询兜底 + 断线重连（P4、P5）
- **worker** `internal/worker`：每个 delivery 一个 goroutine 的流水线（配置快照 → 事件 → secret → 渲染 → 认证 → 发送 → 判定 → 退避 → 完成），全局并发上限、优雅退出（等待在途投递，超时后放弃，由 lease 过期后接管）
- 迁移 `00002`：deliveries 的 `fillfactor` / autovacuum 参数（P2），并把待领取索引换成 `(destination_id, scheduled_at)`

## Assessment vs Reality

| Metric | Predicted (Plan) | Actual |
|---|---|---|
| Complexity | XL | XL |
| Confidence | — | 高：三组黄金用例全部通过，并发 / 有序 / 端到端集成测试通过，手工验证通过 |
| Files Changed | 约 40 个 | 新建 32 个、修改 8 个（另有 1 个不属于本阶段的 README 改动，见 Issues） |

## Tasks Completed

| # | Task | Status | Notes |
|---|---|---|---|
| 1 | 模板引擎 `internal/template` | [done] Complete | 56 条黄金用例通过；偏离见 D-6 |
| 2 | 成功判定与结果分类 `internal/outcome` | [done] Complete | 32 条黄金用例通过 |
| 3 | 退避 `internal/backoff` | [done] Complete | 15 条黄金用例通过，另测 jitter 上下界与 HTTP 日期 |
| 4 | attempt 记录 `internal/outbound/record.go`（B9） | [done] Complete | 响应体按名称脱敏（计划给出的选项之一），见 D-4 |
| 5 | 出站认证 `internal/outbound/auth.go` | [done] Complete | 错误中不带 token 端点响应体，见 D-5 |
| 6 | HTTP 发送 `internal/outbound/client.go` | [done] Complete | 新增 `ErrInvalidRequest`，见 D-7 |
| 7 | 迁移 `00002_deliveries_tuning.sql` | [done] Complete | 同时替换待领取索引，见 D-1 |
| 8 | 派发器 `internal/dispatch` | [done] Complete | 按目标领取、fencing token、数据库时钟，见 D-1、D-2、D-3 |
| 9 | 唤醒 `internal/dispatch/wakeup.go` | [done] Complete | NOTIFY 延迟约 1ms；`pg_terminate_backend` 后约 0.5s 重连 |
| 10 | worker 流水线 `internal/worker` | [done] Complete | 事件读取放在 `worker/events.go`，见 D-8 |
| 11 | 接入、并发与端到端测试（B5） | [done] Complete | `serve.go` 一处接入；端到端测试的两处简化见 D-10 |
| 12 | 文档 `docs/testing/coverage-gaps.md` | [done] Complete | 同时更新 domain-model.md §10、§13.3、§13.6 与 ADR-0008 |

## Validation Results

| Level | Status | Notes |
|---|---|---|
| Static Analysis | [done] Pass | `make lint`（gofmt + go vet）无输出；新文件尚未被 git 跟踪，另以 `gofmt -l` 检查全部新文件 |
| Unit Tests | [done] Pass | `make test`（`-race`）全部通过 |
| Build | [done] Pass | `make build` |
| Integration | [done] Pass | `make test-integration`（`-race`）全部通过 |
| Edge Cases | [done] Pass | 见下方清单 |
| Coverage | [done] Pass | `make cover-gaps-all` 96.1%；template 98.3%、outcome 100%、backoff 100%、outbound 98.8%（目标 ≥ 90%）；dispatch 87.9%、worker 98.4%（含集成测试，目标 ≥ 80%） |
| Secrets | [done] Pass（部分） | `make check-secrets` 通过；本机未安装 gitleaks，只检查了被跟踪的 `.env` 文件 |
| Manual | [done] Pass | 见下方"手工验证" |

### 边界情况

- 空输入：空 Body 模板（无 body 时 `BodyBytes` 为 nil、记录为空）；空响应体有 body 规则时不满足（黄金用例）
- 超大输入：响应超过 1 MiB 时截断并视为不完整；记录截断到 16 KiB，回退到 UTF-8 边界，截断处不残留部分密钥
- 非法类型：模板参数类型错误（bad_argument）、Retry-After 非法或为过去时间时忽略
- 并发：4 个 dispatcher 并发领取 1000 条每条恰好一次；同一 delivery 并发 Complete 只生效一次、两条 attempt 都在；接管
- 网络故障：超时、连接拒绝、出口代理不可用（infra）、LISTEN 连接被终止
- 401 / 403：默认 permanent

### 手工验证

本地 PostgreSQL 16（临时容器）+ `spike/mock-vendor` + `serve --role=all`，通过 Phase 3 的 API 提交一个扇出到 3 个目标的事件：

| 目标 | mock 行为 | 结果 |
|---|---|---|
| vendor-a | 200 + `{"code":0}` | SUCCEEDED，1 次 attempt |
| vendor-b | 前 2 次 500 | SUCCEEDED，3 次 attempt，`next_attempt_at` 间隔约 1s、2s |
| vendor-c | 200 + `{"code":1001}` | DEAD，permanent（PoC S3） |

- 查询 API 返回的 attempt 记录中，`Authorization`、`X-Signature`、`api_key` 查询参数均为 `***`；mock 收到的是真实 token，`X-Signature` 与实际 Body 的 HMAC 一致
- 服务日志共 5 条 `delivery.attempted`，日志中没有出现 token
- SIGTERM 后服务正常退出（`hub.stopped`）

## Files Changed

| File | Action | Lines |
|---|---|---|
| `internal/template/lexer.go` | CREATED | +163 |
| `internal/template/parser.go` | CREATED | +275 |
| `internal/template/eval.go` | CREATED | +163 |
| `internal/template/funcs.go` | CREATED | +128 |
| `internal/template/render.go` | CREATED | +306 |
| `internal/template/golden_test.go` | CREATED | +128 |
| `internal/template/parser_test.go` | CREATED | +161 |
| `internal/template/render_test.go` | CREATED | +293 |
| `internal/outcome/outcome.go` | CREATED | +92 |
| `internal/outcome/golden_test.go` | CREATED | +142 |
| `internal/backoff/backoff.go` | CREATED | +76 |
| `internal/backoff/golden_test.go` | CREATED | +122 |
| `internal/outbound/client.go` | CREATED | +158 |
| `internal/outbound/auth.go` | CREATED | +116 |
| `internal/outbound/record.go` | CREATED | +154 |
| `internal/outbound/client_test.go` | CREATED | +171 |
| `internal/outbound/auth_test.go` | CREATED | +219 |
| `internal/outbound/record_test.go` | CREATED | +197 |
| `internal/dispatch/dispatcher.go` | CREATED | +61 |
| `internal/dispatch/targets.go` | CREATED | +69 |
| `internal/dispatch/pg.go` | CREATED | +341 |
| `internal/dispatch/wakeup.go` | CREATED | +106 |
| `internal/dispatch/targets_test.go` | CREATED | +127 |
| `internal/dispatch/main_integration_test.go` | CREATED | +196 |
| `internal/dispatch/pg_integration_test.go` | CREATED | +517 |
| `internal/dispatch/wakeup_integration_test.go` | CREATED | +141 |
| `internal/worker/run.go` | UPDATED | +149 / -6 |
| `internal/worker/process.go` | CREATED | +297 |
| `internal/worker/events.go` | CREATED | +61 |
| `internal/worker/process_test.go` | CREATED | +460 |
| `internal/worker/run_test.go` | CREATED | +193 |
| `internal/worker/e2e_integration_test.go` | CREATED | +495 |
| `db/migrations/00002_deliveries_tuning.sql` | CREATED | +34 |
| `db/migrations_integration_test.go` | UPDATED | +49 / -2 |
| `cmd/messagehub/serve.go` | UPDATED | +2 / -2 |
| `go.mod` / `go.sum` | UPDATED | +1 / +4（`golang.org/x/oauth2 v0.37.0`，发布于 2026-08-25） |
| `docs/testing/coverage-gaps.md` | UPDATED | 第 2 节重新生成（105 条）；B1、B5、B9、B15 完成；新增 B16–B20 |
| `docs/design/domain-model.md` | UPDATED | §10 记录与脱敏、§13.3 调度时钟等 6 条约定、§13.6 待领取索引 |
| `docs/adr/0008-secrets-management.md` | UPDATED | D2 实施完成；残余风险收窄 |

## Deviations from Plan

| # | WHAT | WHY |
|---|---|---|
| D-1 | **领取改为按目标进行**：先算出每个目标的空闲槽位，再用 `LATERAL` 对每个目标取 `LIMIT least(空闲槽位, 批大小)` 条，而不是计划中"全局按 `scheduled_at` 取一批再按目标截断"。迁移 `00002` 同时把待领取索引从 `(scheduled_at)` 换成 `(destination_id, scheduled_at)` | 计划的写法下，一个积压的目标（`max_in_flight` 小、到期任务多）会占满整批候选，其他目标在这一轮完全领不到，违反 §8 的按目标隔离（PoC S6 的问题）。`TestBusyDestinationDoesNotStarveOthers` 覆盖该场景。§13.6 原本就预留了"按目标公平领取时改为 `(destination_id, scheduled_at)`"；旧索引没有其他查询使用，因此替换而不是并存。EXPLAIN 测试确认领取、接管、完成都走索引 |
| D-2 | **`Completion` 携带 `RetryDelay`（时长），不携带 `NextAttemptAt`（绝对时间）**；`scheduled_at` 与 attempt 的 `next_attempt_at` 在 SQL 中以 `now() + 延迟` 计算 | 端到端测试中出现过一次"重试间隔 5ms"（预期 ≥ 100ms）的失败：`scheduled_at` 用 worker 时钟计算，领取却用数据库时钟比较，Colima 虚拟机与宿主机的时钟偏差足以让重试提前触发。统一用数据库时钟后（lease 本来就如此），偏差不再影响调度。已写入 domain-model.md §13.3 |
| D-3 | **完成时以 lease 到期时间作为 fencing token**（`WHERE … AND lease_owner = $owner AND lease_expires_at = $lease`），`Completion` 携带整个 `Claimed` | 只比较 `lease_owner` 时，同一个 worker 接管了自己过期的 lease 后，迟到的旧结果会覆盖新一轮的状态。`TestSameOwnerCannotCompleteAfterReclaimingItself` 覆盖 |
| D-4 | **合法 JSON 响应体按名称规则脱敏**；请求未发出时（渲染失败、infra 错误）不记录 request | 计划 Task 4 的 GOTCHA 给了两个选项，选了"脱敏"而不是"对 source 角色隐藏"，不需要改 Phase 3 的 API。domain-model.md §10 与 ADR-0008 已同步；非 JSON 响应体的残余风险记为 B18 |
| D-5 | **错误文本不含 URL 与 token 端点响应体**：去掉 `*url.Error` 包装；`oauth2.RetrieveError` 只保留状态码与 OAuth2 错误码 | Go 的 `*url.Error` 会引用完整 URL，查询参数中可能有密钥（如 `?api_key={{secret.X}}`）；`RetrieveError` 会带上 token 端点的响应体。两者都会进入 attempt 的 `error` 和日志。各有回归测试 |
| D-6 | 模板：路径根未知或 `event.<未知字段>` 归为 `syntax`（可在加载时静态检出）；公开 API 为 `RenderURL` / `RenderHeader` / `RenderBody` / `Render`，`Rendered{Actual, Masked template.Request}` | 规格只定义了 `missing_path` 为"路径不存在"；未知的根或字段在任何事件下都不可能存在，按语法错误在加载时拒绝更有用。拆分的渲染函数便于黄金用例按位置执行 |
| D-7 | 新增 `outbound.ErrInvalidRequest`：渲染出的 URL 无法解析或协议不是 http / https 时，outcome 为 `render_error` | 计划只要求"只允许 http / https"，没有说明归类；这是配置问题，重试无意义，修复后 replay |
| D-8 | 事件读取放在 `internal/worker/events.go`（`EventLoader` 接口 + `pgEvents`） | 计划没有指定位置；`internal/store` 属于 Phase 3，不能修改 |
| D-9 | 已删除目标的 warn 日志每分钟最多一次（计划为"每轮一次"）；reclaim 后预算已用尽时直接 GiveUp、不再发送（`Completion.Attempt` 可为 nil） | 每轮（约 1s）一次会刷屏，且每次都要扫描；后者是计划未覆盖的边界：接管计入的那次 attempt 已经用完预算 |
| D-10 | 端到端测试：慢目标为每次 2s（计划 5s）；接管场景用 `UPDATE` 把 lease 提前到期，而不是等待 31s | 缩短测试时间，验证的行为不变 |
| D-11 | 派发器类型命名为 `dispatch.Postgres`（计划为 `pgDispatcher`），由 `dispatch.NewPostgres` 构造 | worker 的 `Run` 在另一个包中构造它，需要导出 |
| D-12 | `db/migrations_integration_test.go` 的 "down removes all tables" 改用 `goose.DownTo(…, 0)`，并新增 reloptions 的 up / down 子测试 | 原测试只回退一个版本；有两个迁移后会失败 |

## Issues Encountered

| 问题 | 处理 |
|---|---|
| `go get golang.org/x/oauth2` 经本机代理（127.0.0.1:7890）多次返回 EOF / 超时 | 重试 `go mod download` 后成功；`go.sum` 来自官方代理 |
| 端到端测试一次性出现重试间隔过短 | 定位为应用时钟与数据库时钟混用，改为数据库时钟（D-2） |
| `make lint` 的 gofmt 只检查被 git 跟踪的文件，新文件不在其中 | 另以 `gofmt -l` 检查全部新文件；提交后 `make lint` 会自然覆盖 |
| 工作区中的 `README.md` 有一处与本阶段无关的改动（架构图改为 mermaid），本会话没有修改它 | 未触碰；提交 Phase 4 时不应包含该文件 |
| `app.ErrRoleNotImplemented` 在 `worker.Run` 实现后已无引用 | 属于底座文件（`internal/app`），按文件归属未删除；建议另行清理 |

## Code Review

独立评审（ecc:go-reviewer，只读）提出 3 个 HIGH、7 个 MEDIUM、8 个 LOW。处理结果：

| 项 | 结论 | 处理 |
|---|---|---|
| H1 Header 中 `{{ request.body }}` 会把 Body 里的密钥原样复制进脱敏记录 | 属实 | 已修复：引用 `request.body` 且管道最后不是 `sha256` / `hmac_*` 的表达式视为携带密钥（`TestHeaderCopyingTheBodyIsMasked`）；domain-model.md §10 已同步 |
| H2 `max_in_flight` 为软上限，与 §8"不得超过"不符 | 计划中的既定取舍（KEY_INSIGHT） | §8 改为写明软上限及其上界；是否改为严格上限见 B16，需要决定 |
| H3 接管已提交、随后领取失败时，已接管的 delivery 被丢弃并再次计费 | 属实 | 已修复：领取失败时仍返回已接管的 delivery 并记录错误（属 D11，无故障注入测试） |
| M2 `Complete` 失败不重试，会导致重复发送 | 属实 | 已修复：最多 3 次、退避 100ms 起翻倍；attempt id 不变，避免重复记录（`TestProcessRetriesATransientCompleteFailure`） |
| M3 Header 值含 CR/LF 等控制字符被当作网络错误 | 属实 | 已修复：归为 `ErrInvalidRequest` → render_error |
| M6 单个 delivery panic 会使进程退出 | 属实 | 已修复：`recover` 并记录，delivery 由 lease 过期后接管（`TestLoopSurvivesAPanickingDelivery`） |
| L1 `RetrieveError` 无 Response 时可能带出响应体 | 属实 | 已修复 |
| L4 配置中小写 `authorization` 与 OAuth2 的 `Authorization` 并存 | 属实 | 已修复：先不区分大小写删除 |
| M1、M5、M7、L7、L8 | 属实，非本阶段范围 | 记入 coverage-gaps B20 |
| M4、L2 | 已在 B17、B18 记录 | — |
| L3（非 JSON 请求体不按名称脱敏）、L5、L6 | 影响很小 | 未处理；请求体只能是 JSON（配置中的 body 为 JSON 值） |

## Tests Written

| Test File | Tests | Coverage |
|---|---|---|
| `internal/template/golden_test.go` | 2（56 + 静态校验子用例） | 黄金用例、加载时校验 |
| `internal/template/render_test.go` | 11 | 双输出、渲染顺序、错误位置、边界值、嵌套上限 |
| `internal/template/parser_test.go` | 6 | 位置推断、语法 / 函数 / 参数错误、示例配置加载 |
| `internal/outcome/golden_test.go` | 3（32 条黄金用例） | 结果分类、判定顺序、不完整 / 多值 JSON |
| `internal/backoff/golden_test.go` | 5（15 条黄金用例） | 退避、jitter 上下界、Retry-After |
| `internal/outbound/record_test.go` | 7 | 三个位置 × 两类规则、先脱敏后截断、响应体脱敏、UTF-8 边界 |
| `internal/outbound/auth_test.go` | 8 | 缓存、过期刷新、配置变化、infra 错误、错误文本不含响应体 |
| `internal/outbound/client_test.go` | 6 | 成功、重定向、读取上限、超时 / 拒绝 / 代理 / 非法请求、URL 不进错误文本 |
| `internal/dispatch/targets_test.go` | 6 | 配置到领取参数、状态列表与状态机一致 |
| `internal/dispatch/pg_integration_test.go` | 17 | 并发领取、并发完成、接管、fencing、软上限、隔离、有序、B15、EXPLAIN |
| `internal/dispatch/wakeup_integration_test.go` | 5 | NOTIFY 延迟、合并、重连、轮询兜底、关闭 |
| `internal/worker/process_test.go` | 10 | 每条触发器路径、脱敏、OAuth2、配置缺失、预算用尽、replay |
| `internal/worker/run_test.go` | 4 | 并发上限、优雅退出、超时放弃、领取失败后继续 |
| `internal/worker/e2e_integration_test.go` | 7 | 成功、退避重试、DEAD、业务码、慢目标隔离、OAuth2 infra 重试与恢复、停机接管 |
| `db/migrations_integration_test.go` | +2 子测试 | reloptions up / down |

## Next Steps

- [x] 代码评审（ecc:go-reviewer，见上节）
- [ ] 提交：Phase 4 的改动不包含 `README.md`；`cmd/messagehub/serve.go` 的接入属于计划允许的一处改动
- [ ] 创建 PR（`/prp-pr`）
- [ ] 后续：coverage-gaps D8（子进程启动完整 `serve`）、B13（迁移 `00003`）、B16–B20；B16 是否改为严格上限（advisory lock）需要决定
