# Implementation Report: 共享运行底座（Phase 2b）

## Summary
实现了 Phase 3 / Phase 4 共用的运行底座：
- 单一可执行文件 `messagehub`，子命令为 `config check` / `config env` / `migrate up` / `keygen` / `serve --role=api|worker|all`
- `HUB_*` 进程参数，日志中的连接串会脱敏
- `ConfigHolder`：原子持有当前配置，收到 SIGHUP 时热重载，重载失败则保留旧配置
- `env:` 引用解析，启动时检查缺失变量
- 入站 API Key：生成、解析双哈希、常量时间比对；启动时校验哈希格式
- 通用规则求值 `internal/rules`
- PostgreSQL 连接池、直连连接、内嵌迁移
- 集成测试支架 `pgtest`
- `api.Run`、`worker.Run` 为占位实现，由 Phase 3 / Phase 4 替换

## Assessment vs Reality

| Metric | Predicted (Plan) | Actual |
|---|---|---|
| Complexity | Medium | Medium |
| Confidence | 8/10 | 一次完成，无返工 |
| Files Changed | 约 24 | 33（新增 26 个 Go 文件，修改 7 个工程与文档文件） |

## Tasks Completed

| # | Task | Status | Notes |
|---|---|---|---|
| 1 | 进程参数 `internal/app/settings.go` | ✅ | 另加 `RequireDatabase()`，只有 serve / migrate 要求数据库连接串 |
| 2 | 数据库连接 `internal/platform/pg` | ✅ | 有偏离：改用 goose Provider，`MigrateUp` 返回本次执行的迁移数（见下） |
| 3 | 集成测试支架 `pgtest` | ✅ | `StartShared(ctx)` 供 `TestMain` 共享容器；`Start(t)` 每个测试单独一个容器；`Reset(t, pool)` |
| 4 | Secret 解析 `internal/secrets` | ✅ | 缺失项聚合为 `*secrets.MissingError{Names}` |
| 5 | API Key `internal/apikey` | ✅ | 新增导出常量 `KeyLength`（47）、`MaxHashes`（2）与哨兵错误 `ErrInvalidHash` |
| 6 | 规则求值 `internal/rules` | ✅ | 另导出 `Equal`；数值用 `big.Rat` 精确比较；与 success 黄金用例交叉验证 |
| 7 | 配置持有 `ConfigHolder` | ✅ | 用 `reloadMu` 串行化 Reload，保证日志里的新旧 hash 准确 |
| 8 | Runtime 与角色占位 | ✅ | |
| 9 | 可执行文件 `cmd/messagehub` | ✅ | 退出码另加 `64`（用法错误，EX_USAGE） |
| 10 | 工程与文档 | ✅ | 另外更新了 ADR-0008 实施表与 coding-conventions §1 |

## Validation Results

| Level | Status | Notes |
|---|---|---|
| Static Analysis | ✅ | `make lint`（gofmt + go vet）无输出 |
| Unit Tests | ✅ | `make test`（-race）全部通过 |
| Build | ✅ | `make build` 产出 `bin/messagehub` |
| Integration | ✅ | `make test-integration`：迁移幂等、连接池、直连、`Reset`、`migrate up` 与 `buildRuntime` 连接真实数据库 |
| Edge Cases | ✅ | 见下方手工验证 |

**手工验证（计划 Manual Validation）**
- `config env` 输出 10 行 ✅
- 不设环境变量时 `config check` 退出码为 2，列出全部 10 个变量名 ✅
- `keygen` 输出的 sha256 与 `shasum -a 256` 一致，key 长度 47 ✅
- 用 Docker 中的 PG 验证 `migrate up`：第一次执行 1 个迁移，第二次执行 0 个 ✅
- `serve --role=api` 启动日志里的连接串不含密码；占位角色以 `role not implemented yet` 退出，退出码 1 ✅

**覆盖率**（计划要求 4 个包各 ≥ 90%）
- 满足要求：`internal/app` 98.9%、`internal/secrets` 100%、`internal/apikey` 100%、`internal/rules` 100%
- 其他包：`internal/platform/pg` 81.5%（成功路径由集成测试覆盖）、`cmd/messagehub` 86.8%
- 全仓库 96.1%。19 个新增未覆盖块已逐条归类，记入 `docs/testing/coverage-gaps.md`（#23–#41，新增分类 D8–D10）

## Files Changed

| File | Action |
|---|---|
| `cmd/messagehub/main.go`, `serve.go`, `main_test.go`, `main_integration_test.go` | CREATED |
| `internal/app/settings.go`, `config_holder.go`, `principals.go`, `runtime.go` 及测试 | CREATED |
| `internal/secrets/resolver.go` 及测试 | CREATED |
| `internal/apikey/apikey.go` 及测试 | CREATED |
| `internal/rules/rules.go` 及测试 | CREATED |
| `internal/platform/pg/pg.go`, `pg_test.go`, `pg_integration_test.go` | CREATED |
| `internal/testsupport/pgtest/pgtest.go` | CREATED |
| `internal/api/run.go`, `internal/worker/run.go` | CREATED（占位） |
| `Makefile`, `.gitignore` | UPDATED |
| `docs/testing/coverage-gaps.md`, `docs/deploy/k8s-secrets.md`, `docs/adr/0008-secrets-management.md`, `docs/design/coding-conventions.md` | UPDATED |

## Deviations from Plan

1. **新增 `app.CheckPrincipalKeys`**
   - 改了什么：启动、SIGHUP 重载和 `config check` 时，都会校验每个 principal 的哈希变量是否为 1–2 个 64 位小写 hex
   - 为什么：ADR-0008 D4 要求格式不符时拒绝启动。原计划只提供 `ParseHashes`，由 Phase 3 在认证时调用；但 Phase 3 不能修改 `ConfigHolder`，留到那边只能在请求时才失败，无法拒绝启动
2. **迁移改用 goose Provider**
   - 改了什么：`pg.MigrateUp` 不再调用 `goose.SetBaseFS` / `SetDialect`，并且返回本次执行的迁移数
   - 为什么：Provider 没有包级全局状态，从根上消除了计划中"并行测试相互影响"这一风险，不再需要 `sync.Once`；迁移数用于 CLI 输出和幂等测试
3. **`pgtest` 多一个 `StartShared(ctx)`**
   - 为什么：计划要求每个包在 `TestMain` 中共享一个容器，但 `TestMain` 里拿不到 `*testing.T`
4. **测试中的假密钥改为 64 个 `a`**
   - 为什么：这个值既是明显的假值，又是格式合法的哈希，可以同时通过"变量存在"和"哈希格式"两项检查

## Issues Encountered
无阻塞问题。在 `TestRunRoles` 中，原先有一条子测试断言占位角色返回 `ErrRoleNotImplemented`。Phase 3 / 4 替换角色后这条测试会失败，并迫使他们修改 `cmd`，与文件归属约定冲突，所以已删除；占位行为改由手工验证覆盖。

## Code Review（ecc:go-reviewer）

审查结果：没有 CRITICAL；1 个 HIGH、3 个 MEDIUM、6 个 LOW。已修复的项：

| # | 级别 | 问题 | 处理 |
|---|---|---|---|
| 1 | HIGH | `RedactDSN` 只去掉了 userinfo 中的密码，查询参数里的 `?password=` / `sslpassword=` 仍会进入启动日志 | ✅ 脱敏时去掉整个查询串，补了测试 |
| 2 | MEDIUM | 用 `slog.Any` 记录 `Settings` 时会输出原始 DSN | ✅ 实现 `LogValue()`，补了测试 |
| 3 | MEDIUM | 关闭超时后，`pool.Close()` 会一直等到连接全部归还，超时形同虚设 | ✅ 超时时跳过 `Close`，进程按时退出 |
| 4 | MEDIUM | `big.Rat` 解析指数无上限，几个字节的 payload 就能消耗数十毫秒 CPU | ✅ 字面量长度上限 64，指数绝对值上限 400，超出视为不相等；补了测试 |
| 5 | LOW | 角色在没有关闭请求时自行返回 nil 或 `Canceled`，进程却以 0 退出 | ✅ 改为返回 `errRoleStopped`，补了测试 |
| 6 | LOW | 启动期间（连接数据库最多 30 秒）收到 SIGHUP，会按默认行为杀掉进程 | ✅ 在 `buildRuntime` 之前就注册 SIGHUP |
| 8 | LOW | 设置了但值为空的环境变量被当作有效值 | ✅ 空值视为未设置，与 `EnvResolver` 一致；补了测试 |
| 9 | LOW | `float64(0.1)` 与 `json.Number("0.1")` 比较结果为不相等 | ✅ float64 先转成最短十进制形式再比较；补了测试 |

未处理的项：
- **#7 第二次 SIGINT/SIGTERM 不能强制退出**：关闭等待已经有 `HUB_SHUTDOWN_TIMEOUT` 兜底，K8s 也会在宽限期后发 SIGKILL，暂不处理
- **#10 `ParseHashes` 不接受大写 hex，也不拒绝重复的哈希**：ADR-0008 D4 规定只接受小写，错误信息已经说明；重复的哈希不影响认证结果

## Tests Written

| Test File | Coverage |
|---|---|
| `internal/app/settings_test.go` | 默认值、显式值、聚合错误、边界、DSN 脱敏 |
| `internal/app/config_holder_test.go` | 首次加载失败（配置非法、变量缺失、哈希格式错误）、重载成功、三种重载失败时保留旧配置、`-race` 并发 |
| `internal/app/principals_test.go` | 合法哈希、轮换期间两个哈希、格式错误、变量缺失 |
| `internal/secrets/resolver_test.go` | 已设置、未设置、空值、未知前缀；`CheckRequired` 聚合缺失项且不泄露值 |
| `internal/apikey/apikey_test.go` | 生成格式、哈希一致性、`ParseHashes` 的 9 种输入、新旧 key 都能匹配 |
| `internal/rules/rules_test.go` | 路径取值 11 种情况、5 个算子 31 种情况、数值精确比较、success 黄金用例交叉验证 |
| `internal/platform/pg/pg_test.go`, `pg_integration_test.go` | 连接失败（单元测试）；迁移幂等、连接池、直连、`Reset`（集成测试） |
| `cmd/messagehub/main_test.go`, `main_integration_test.go` | 11 种用法错误、help、config check / env、keygen、migrate、serve 启动失败且不泄露密钥、`runRoles` 关闭与超时、`watchReload` |

## Next Steps
- [ ] Code review
- [ ] 提交（等你确认）
- [ ] 并行执行 Phase 3（`ingest-and-query.plan.md`）与 Phase 4（`delivery-engine.plan.md`）
