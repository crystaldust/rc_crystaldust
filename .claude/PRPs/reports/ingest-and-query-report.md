# Implementation Report: 受理与查询（Phase 3）

## Summary
实现了 `api` 角色，即 `api/openapi.yaml` 中的全部 8 个端点：
- **认证**：`Bearer` key 与 SHA-256 哈希做常量时间比对。索引按 ConfigHash 缓存，配置热重载后立即生效。轮换期间新旧两个 key 都能通过。
- **受理**：处理顺序为请求体上限 → 拒绝重复 JSON 键 → 信封校验 → 路由 → 同一事务写入 events、event_payloads、deliveries → 发送一次无 payload 的 NOTIFY。
- **查询**：
  - 按角色限定范围，source 角色越权访问返回 404。
  - 游标分页，并推导 `blocked_by`（整页一次 LATERAL 查询）。
  - attempts 分页。
- **运维操作**：replay 和 skip 先经 `domain.Transition` 判断，再用 `FOR UPDATE` 加条件更新落库，同时写审计并发送 NOTIFY。
- **健康检查**：`/healthz`、`/readyz`。
- **观测**：每个请求一条访问日志，带 request id；panic 恢复为 500；宽事件日志 `event.received` / `event.unrouted` / `event.duplicate` / `delivery.operator_action`。

## Assessment vs Reality

| Metric | Predicted (Plan) | Actual |
|---|---|---|
| Complexity | Large | Large |
| Confidence | 7/10 | 一次完成；契约测试首次运行即通过（用故意改坏字段的方式确认校验有效） |
| Files Changed | 约 30 | 37 个 Go 文件（新增 36、更新 1），5 个文档 / 工程文件；另有 2 个底座提交 |

## Tasks Completed

| # | Task | Status | Notes |
|---|---|---|---|
| 1 | 路由 `internal/routing` | ✅ | 100% 覆盖 |
| 2 | 游标 `store/cursor.go` | ✅ | 拒绝未知字段；非法游标返回 400，字段为 `cursor` |
| 3 | 受理事务 `store/ingest.go` | ✅ | 100 个并发重复提交只产生 1 个事件；unrouted 不发 NOTIFY，routed 恰好发 1 次 |
| 4 | 查询 `store/query.go`、`blocked.go` | ✅ | 有偏离，见下 |
| 5 | 运维操作 `store/operator.go` | ✅ | 10 个并发 replay 只有 1 个成功 |
| 6 | 认证 `api/auth.go` | ✅ | |
| 7 | 严格 JSON 与请求体上限 | ✅ | 任意层级的重复键都返回 400，字段为重复键的路径 |
| 8 | HTTP 层 | ✅ | 另拆出 `params.go`（参数校验，聚合全部问题）和 `views.go`（OpenAPI 视图） |
| 9 | 契约与端到端测试（B2） | ✅ | 另加 `TestRunServesUntilCancelled`，用真实 Runtime 运行 `api.Run` |
| 10 | 文档 | ✅ | coverage-gaps 改为以含集成测试的结果为准，并整体重新生成 |

## Validation Results

| Level | Status | Notes |
|---|---|---|
| Static Analysis | ✅ | `make lint` |
| Unit Tests | ✅ | `make test`（-race） |
| Build | ✅ | `make build` |
| Integration | ✅ | `make test-integration`：store 的受理、查询、运维、EXPLAIN；api 的契约测试 |
| Edge Cases | ✅ | 见计划的 Edge Cases 清单；手工验证见下 |

**覆盖率**（计划要求 api、store、routing ≥ 80%）
- 单元测试：`internal/api` 97.5%，`internal/routing` 100%
- 含集成测试：`internal/store` 91.3%，`internal/api` 98.4%
- 全仓库：只含单元测试为 81.6%，含集成测试为 95.8%

**手工验证**：用 Docker 启动 PG，执行 `migrate up`，生成 5 个 key，再 `serve --role=api`，用 curl 依次验证：
- 受理新事件返回 202，重复提交返回 200，unrouted 事件返回 202
- 请求里有重复键时返回 400
- viewer 可以查询列表
- 对 PENDING 的 delivery 执行 replay 返回 409，对 DEAD 的返回 202
- 收到 SIGTERM 后进程优雅退出

日志检查：没有出现 API Key，也没有出现 payload 中的标记字符串；所有宽事件日志都齐全。

## Deviations from Plan

1. **`blocked_by` 只对 PENDING 和 RETRY_SCHEDULED 计算**
   - 计划原文："非终态"。
   - 原因：规格对 `blocked_by` 的定义是"cannot be claimed yet"。IN_FLIGHT 已经被领取，不应再显示阻塞。
   - 实现：这两个状态由 `domain.Transition(s, Claim)` 是否合法推导，不在代码里手写。
2. **`blocked_by` 整页一次查询**
   - 计划原文：每条 delivery 一条 SQL。
   - 原因：避免 N+1。
   - 实现：用 `unnest` 加 LATERAL；block 和 skip 两种模式的"结清状态"集合都由 `IsOrderingSettled` 推导后作为参数传入。
3. **键集分页条件改写**
   - 计划原文：`(t, id) < ($t, $id)`。
   - 改为：`t <= $t AND (t < $t OR id < $id)`。
   - 原因：这样首列时间可以约束索引扫描范围，EXPLAIN 断言能通过。
4. **两处修改先提交到底座分支，再快进同步**（遵循计划的文件归属规则）
   - `687796f`：`pg.DeliveryReadyChannel`。NOTIFY 和 Phase 4 的 LISTEN 共用这一个常量。
   - `5b37cef`：`make cover-gaps-all`（`COVER_TAGS=integration`）。原因：store 包只有集成测试，只统计单元测试会把它整体列为缺口；Phase 4 的 dispatch 包也会有同样的情况。
5. **coverage-gaps 改以 `make cover-gaps-all` 为准**
   - 第 2 节整体重新生成，共 76 条。
   - 新增 D11（数据库调用失败，需要故障注入才能触发）。
   - coding-conventions §8 同步修改。
6. **store 列表查询的 SQL 构造拆为 `eventsQuery` / `deliveriesQuery`**
   - 原因：让 EXPLAIN 测试检查的正好是生产代码执行的 SQL。

## Issues Encountered

- **`git stash -u` 失败**：之前用 `git add -N` 标记过的新文件让 stash 出错。命令在第一步就停了，工作区没有丢任何东西；改用临时 worktree 修改底座分支。
- **覆盖缺口表重新生成时分类出错**：脚本按 `(文件, 函数)` 查原有分类。同一个函数里有多个分类时（`validateSchema`、`jsonValue`）会被合并，已按"位置"逐条修正。

## Known Gaps
- **B14**：跨来源共用 `ordering_key` 时会互相阻塞（维持现状，见 ADR-0007）。
- **B15**：`ingest_seq` 的分配顺序和提交可见顺序可能不一致（Phase 4 负责）。
- **B9 补充**：attempt 的响应体只截断、不脱敏（Phase 4 负责）。
- **B13**：不带过滤、或只按 `destination_id` 过滤的 deliveries 列表，没有 `created_at` 排序索引可用。需要等 Phase 4 的 `00002` 迁移落地后，再新增迁移来补。
- **D11**：store 中的数据库失败分支需要故障注入才能测试；错误映射到 500 这一点已用假 store 测过。

## Code Review

两个审查代理并行审查：go-reviewer 看正确性和并发，security-reviewer 看安全面。结论：没有 CRITICAL，两份报告各有 1 个 HIGH 或 MEDIUM，并且指向同一个问题。

**已修复**

| 来源 | 级别 | 问题 | 处理 |
|---|---|---|---|
| Go 审查 H1、安全 M2 | HIGH | payload 或字符串字段里的 `\u0000`、非法 UTF-8、`1e999999999` 这类数字能通过校验，到 PG 才报错，返回 500（已在 postgres:16 上复现） | `CheckNoDuplicateKeys` 扩展为 `CheckStrictJSON`，拒绝 UTF-8 非法、NUL、超长数字或超大指数；查询参数同样拒绝 NUL 和非法 UTF-8；store 把 SQLSTATE 22 类错误映射为 `ErrInvalidInput`，HTTP 层返回 400 作为兜底 |
| 安全 M1 | MEDIUM | `blocked_by` 会把其他来源的 delivery id 暴露给 source 角色 | 查询同时返回前序所属的来源；调用方是 source 角色且前序属于其他来源时返回 null。规格 §6 补充了跨来源说明 |
| 安全 L1 | LOW | 嵌套超过 1000 层时整个检查被跳过，后面的重复键能绕过 | 超过深度直接拒绝，返回 400 |
| Go 审查 M1 | MEDIUM | `RouteIDs` 为 nil 时会写入 NULL | 写入前改成空数组 |
| Go 审查 M2 | MEDIUM | 关闭超时后没有强制关闭连接；`http.ErrAbortHandler` 被 recover 吞掉 | 超时后调用 `srv.Close()` 并等待监听结束；遇到 `ErrAbortHandler` 重新 panic |
| 安全 L2 | LOW | 多个 principal 共用同一个哈希时，配置中靠前的那个会静默胜出 | 共用同一哈希的所有 principal 都禁用，并记录 error 日志 |
| 安全 L3 | LOW | 401 和 403 没有安全日志 | 新增 `auth.rejected` 日志，包含 reason、route、remote_addr 和 principal_id，不含 key |
| 安全 L4 | LOW | 缺少 `nosniff`、`no-store` 响应头；没有设置 `MaxHeaderBytes` | 加上这两个响应头；`MaxHeaderBytes` 设为 16 KiB |
| 安全 L5 | LOW | 列表查询没有超时 | 每个请求设 10 秒超时，小于 WriteTimeout |
| Go 审查 L4 | LOW | 接受 `urn:uuid:` 等非规范 UUID 形式 | 只接受 36 字符的规范格式 |
| Go 审查 L3 | LOW | 翻页期间正在提交的事务可能被漏掉 | 写入规格 §13.4 |

每项修复都补了测试，包括：NUL / UTF-8 / 数字 / 深度（单元测试和 HTTP 测试）、store 层的 22 类错误兜底（集成测试）、跨来源的 `blocked_by`、哈希共用、拒绝日志、响应头、`ErrAbortHandler`、关闭超时后强制断开。

**未修改，已记录**
- **Go 审查 M3**：`ingest_seq` 的分配顺序和提交顺序可能不一致。已作为 GOTCHA 写入 Phase 4 计划 Task 8。
- **安全 L6**：attempt 的响应体只截断、不脱敏，source 角色能读到。已作为 GOTCHA 写入 Phase 4 计划 Task 4。panic 日志里原样记录了 panic 值；目前的 panic 来源都不会带 payload，所以保持不变。
- **Go 审查 L1**：`to_status` 不入库，表结构是固定的。
- **Go 审查 L2**：`blocked_by` 和 delivery 不在同一个快照里读取，作为推导值可以接受。
- **Go 审查 L5**：认证缓存不会重试缺失的变量。环境变量在进程生命周期内不会变化，所以不适用。
- **Go 审查 L6**：key 名里带 `.` 或 `[` 时，错误路径会有歧义，只影响报错文案。

**用户决定（2026-10-24）**
- 跨来源共用同一个 `ordering_key` 的问题先维持现状，即 ADR-0007 方案 A：规格中约定业务方按来源给 key 加前缀，source 角色看不到其他来源的阻塞前序。
- 替代方案 B（有序范围加入 source）和 C（由配置决定）记录在 ADR-0007；决策记入 PRD 的 Decisions Log；缺口记为 coverage-gaps 的 B14。

## Tests Written

| Test File | 内容 |
|---|---|
| `internal/routing/routing_test.go` | 无匹配、单 route、去重、sources 过滤、payload 和 metadata 规则、payload 损坏 |
| `internal/store/cursor_test.go` | 往返编解码、8 种非法游标 |
| `internal/store/ingest_integration_test.go` | 新事件写入、NULL 字段、重复、跨来源同 id、100 并发、NOTIFY |
| `internal/store/query_integration_test.go` | 详情与范围限定、9 种事件过滤、分页、7 种投递过滤、attempts 分页、blocked_by 5 种模式、已领取与终态不阻塞 |
| `internal/store/plan_integration_test.go` | 1 万行数据下 6 个主要查询的 EXPLAIN 不出现顺序扫描 |
| `internal/store/operator_integration_test.go` | 3 种 replay、skip、6 种非法状态、不存在的 delivery、10 并发 replay |
| `internal/api/*_test.go` | 认证（8 种情况、轮换、热重载、坏哈希）、角色检查、受理（9 种错误）、查询（9 种校验错误、范围、视图）、运维（11 种错误）、健康检查、未知路由、panic、访问日志、优雅关闭 |
| `internal/api/contract_integration_test.go` | 真实数据库加 HTTP，用 openapi3filter 校验全部端点的请求与响应；`api.Run` |

## Next Steps
- [x] 代码审查（go-reviewer、security-reviewer），CRITICAL 和 HIGH 已全部修复
- [ ] 提交（等你确认）
- [ ] Phase 4：`/ecc:prp-implement .claude/PRPs/plans/delivery-engine.plan.md`
