# 测试覆盖缺口记录

> 用途：记录**哪些代码没有被测试执行、为什么、何时处理**，便于随时回溯。
> 维护规则：`make cover-gaps-all` 的输出发生变化（新增或消失条目）时，必须在同一次提交中更新本文件。行号会随代码变化，所以每一条都同时记录**函数名**，以函数名为准。

## 如何重新生成

```bash
make cover-gaps-all    # 含集成测试（需要 Docker，见 coding-conventions.md §10）；第 2 节以它为准
make cover-gaps        # 只跑单元测试，不需要 Docker
```

脚本对全部包执行 `go test -coverpkg=./...`，合并各个测试二进制的结果（任一测试执行过即视为已覆盖），然后逐条列出从未执行的代码块（`文件:起止行  首行代码`），最后输出总覆盖率。

从 Phase 3 起，第 2 节以 `make cover-gaps-all` 的输出为准：`internal/store` 这类只与数据库交互的包只能由集成测试覆盖，只看单元测试会把它们整体列为缺口。

## 当前快照

| 项 | 值 |
|---|---|
| 日期 | 2026-10-24 |
| 阶段 | Phase 4 投递引擎完成后 |
| 语句覆盖率（`make cover-gaps-all`，含集成测试） | **96.1%** |
| 语句覆盖率（`make cover-gaps`，只含单元测试） | 82.0%（`internal/store`、`internal/dispatch` 主要由集成测试覆盖，单元测试下大部分未覆盖） |
| 未覆盖代码块 | 105 个，全部属于第 3 节"接受不覆盖"；其中 42 个为 D11（数据库调用失败） |
| 各包覆盖率（单元测试） | `internal/template` 98.3%、`internal/outcome` 100%、`internal/backoff` 100%、`internal/outbound` 98.8%、`internal/worker` 89.9%、`internal/api` 97.5%、`internal/routing` 100%、`internal/domain` 99.2%、`internal/config` 97.1%、`internal/contracts` 96.4%、`internal/app` 98.9%、`internal/secrets` 100%、`internal/apikey` 100%、`internal/rules` 100%、`cmd/messagehub` 88.3% |
| 各包覆盖率（含集成测试） | `internal/dispatch` 87.9%、`internal/worker` 98.4%、`internal/store` 91.3%、`internal/api` 98.4%、`cmd/messagehub` 90.7% |

## 1. 历史：已补齐的缺口

Phase 2 完成时的审查中发现，以下 6 处缺口（原"第一类：值得补测试的逻辑分支"）已补上测试。

| 位置（函数） | 原缺口 | 补充的测试 | 日期 |
|---|---|---|---|
| `internal/config/validate.go` `checkRoute` | route 的 `sources` 引用了不存在的 principal | `TestInvalidConfigs/unknown_source_principal`（fixture `testdata/invalid/unknown_source_principal.yaml`） | 2026-10-24 |
| `internal/config/clone.go` `cloneJSONValue` | Body 模板中数组的深拷贝 | `TestAccessorsReturnCopiesOfArraysInBody` | 2026-10-24 |
| `internal/config/load.go` `applyDefaults` | 策略未写 `ordering` 时默认为 `mode: none` | `TestOmittedOrderingDefaultsToModeNone` | 2026-10-24 |
| `internal/config/load.go` `toJSONValue` | YAML 值无法表示为 JSON（如 `.inf`） | `TestInvalidConfigs/unsupported_yaml_value`（fixture `testdata/invalid/unsupported_yaml_value.yaml`） | 2026-10-24 |
| `internal/config/values.go` `StatusMatcher.String` | 精确状态码的字符串形式 | `TestStatusMatcherString` | 2026-10-24 |
| `api/openapi_test.go` `TestServerOnlyRules` | 不存在的日期（`2026-02-31`）：OpenAPI 格式正则接受，服务端必须拒绝（domain-model.md §13.1） | `TestServerOnlyRules/impossible_calendar_date` | 2026-10-24 |

另外，原先归为"值得补测试"的 `internal/config/schema.go` `closestBranch` 兜底分支，经核对源码后**改归第 3 节**：`closestBranch` 只在存在子错误时被调用，而每个子错误都至少产生一条问题，所以该分支在现有调用路径下不可达。

## 2. 未覆盖条目总表（与 `make cover-gaps-all` 输出逐条对应）

| # | 位置 | 函数 | 分类 |
|---|---|---|---|
| 1 | `cmd/messagehub/main.go:46-48` | `main` | D10 |
| 2 | `cmd/messagehub/main.go:147-148` | `configCheck` | D4 |
| 3 | `cmd/messagehub/main.go:185-187` | `keygen` | D7 |
| 4 | `cmd/messagehub/serve.go:67-67` | `serve` | D9 |
| 5 | `cmd/messagehub/serve.go:79-81` | `runServe` | D4 |
| 6 | `cmd/messagehub/serve.go:92-103` | `runServe` | D8 |
| 7 | `cmd/messagehub/serve.go:103-105` | `runServe` | D8 |
| 8 | `cmd/messagehub/serve.go:106-107` | `runServe` | D8 |
| 9 | `cmd/messagehub/serve.go:112-114` | `newLogger` | D4 |
| 10 | `internal/api/ingest.go:33-35` | `ingest` | D4 |
| 11 | `internal/api/ingest.go:39-42` | `ingest` | D4 |
| 12 | `internal/api/ingest.go:84-86` | `ingestInput` | D4 |
| 13 | `internal/api/ingest.go:90-92` | `ingestInput` | D4 |
| 14 | `internal/api/run.go:54-56` | `serve` | D4 |
| 15 | `internal/app/settings.go:182-184` | `defaultWorkerID` | D7 |
| 16 | `internal/config/config.go:25-27` | `newConfig` | D1 |
| 17 | `internal/config/config.go:56-58` | `computeHash` | D1 |
| 18 | `internal/config/config.go:62-64` | `computeHash` | D1 |
| 19 | `internal/config/config.go:66-68` | `computeHash` | D1 |
| 20 | `internal/config/load.go:117-119` | `toJSONValue` | D1 |
| 21 | `internal/config/load.go:153-155` | `decodeTyped` | D1 |
| 22 | `internal/config/schema.go:29-31` | `compiledSchema` | D2 |
| 23 | `internal/config/schema.go:34-36` | `compiledSchema` | D2 |
| 24 | `internal/config/schema.go:44-46` | `validateSchema` | D2 |
| 25 | `internal/config/schema.go:52-54` | `validateSchema` | D3 |
| 26 | `internal/config/schema.go:92-94` | `closestBranch` | D4 |
| 27 | `internal/config/schema.go:140-142` | `leafMessage` | D3 |
| 28 | `internal/config/schema.go:178-180` | `joinIndex` | D4 |
| 29 | `internal/config/schema.go:200-202` | `isPlainKey` | D5 |
| 30 | `internal/contracts/cases.go:104-106` | `jsonValue` | D6 |
| 31 | `internal/contracts/cases.go:108-110` | `jsonValue` | D6 |
| 32 | `internal/contracts/cases.go:112-114` | `jsonValue` | D1 |
| 33 | `internal/contracts/success.go:199-201` | `convertBodyRule` | D6 |
| 34 | `internal/contracts/template.go:110-112` | `convertTemplateCase` | D6 |
| 35 | `internal/contracts/template.go:119-121` | `convertTemplateCase` | D6 |
| 36 | `internal/contracts/template.go:192-194` | `convertEvent` | D6 |
| 37 | `internal/dispatch/pg.go:67-69` | `Claim` | D11 |
| 38 | `internal/dispatch/pg.go:74-79` | `Claim` | D11 |
| 39 | `internal/dispatch/pg.go:80-82` | `Claim` | D11 |
| 40 | `internal/dispatch/pg.go:135-137` | `claim` | D11 |
| 41 | `internal/dispatch/pg.go:139-141` | `claim` | D11 |
| 42 | `internal/dispatch/pg.go:182-184` | `reclaim` | D11 |
| 43 | `internal/dispatch/pg.go:185-187` | `reclaim` | D11 |
| 44 | `internal/dispatch/pg.go:189-191` | `reclaim` | D11 |
| 45 | `internal/dispatch/pg.go:195-197` | `reclaim` | D11 |
| 46 | `internal/dispatch/pg.go:256-258` | `Complete` | D11 |
| 47 | `internal/dispatch/pg.go:287-289` | `insertAttempt` | D1 |
| 48 | `internal/dispatch/pg.go:291-293` | `insertAttempt` | D1 |
| 49 | `internal/dispatch/pg.go:297-299` | `insertAttempt` | D11 |
| 50 | `internal/dispatch/pg.go:309-311` | `jsonOrNil` | D1 |
| 51 | `internal/dispatch/pg.go:335-338` | `warnOrphans` | D11 |
| 52 | `internal/dispatch/pg.go:340-343` | `warnOrphans` | D11 |
| 53 | `internal/dispatch/targets.go:29-30` | `targetsOf` | D4 |
| 54 | `internal/dispatch/wakeup.go:87-89` | `listenOnce` | D11 |
| 55 | `internal/domain/ids.go:13-14` | `NewID` | D7 |
| 56 | `internal/outbound/record.go:61-64` | `maskedBodyText` | D1 |
| 57 | `internal/outbound/record.go:80-82` | `maskResponseBody` | D1 |
| 58 | `internal/platform/pg/pg.go:64-66` | `migrate` | D2 |
| 59 | `internal/platform/pg/pg.go:68-70` | `migrate` | D2 |
| 60 | `internal/store/blocked.go:53-55` | `blockedBy` | D11 |
| 61 | `internal/store/blocked.go:65-67` | `blockedBy` | D11 |
| 62 | `internal/store/ingest.go:123-125` | `insertChildren` | D11 |
| 63 | `internal/store/ingest.go:126-128` | `insertChildren` | D11 |
| 64 | `internal/store/ingest.go:145-147` | `loadDuplicate` | D11 |
| 65 | `internal/store/ingest.go:151-153` | `loadDuplicate` | D11 |
| 66 | `internal/store/ingest.go:161-163` | `loadDuplicate` | D11 |
| 67 | `internal/store/ingest.go:170-172` | `notifyReady` | D11 |
| 68 | `internal/store/operator.go:80-82` | `operate` | D11 |
| 69 | `internal/store/operator.go:83-85` | `operate` | D4 |
| 70 | `internal/store/operator.go:87-89` | `operate` | D11 |
| 71 | `internal/store/operator.go:104-106` | `lockStatus` | D11 |
| 72 | `internal/store/operator.go:117-119` | `insertAction` | D11 |
| 73 | `internal/store/query.go:112-114` | `GetEvent` | D11 |
| 74 | `internal/store/query.go:118-120` | `GetEvent` | D11 |
| 75 | `internal/store/query.go:122-124` | `GetEvent` | D11 |
| 76 | `internal/store/query.go:138-140` | `scanEventWithPayload` | D4 |
| 77 | `internal/store/query.go:148-150` | `ListEvents` | D11 |
| 78 | `internal/store/query.go:152-154` | `ListEvents` | D11 |
| 79 | `internal/store/query.go:188-190` | `ListDeliveries` | D11 |
| 80 | `internal/store/query.go:192-194` | `ListDeliveries` | D11 |
| 81 | `internal/store/query.go:231-233` | `GetDelivery` | D11 |
| 82 | `internal/store/query.go:235-237` | `GetDelivery` | D11 |
| 83 | `internal/store/query.go:242-244` | `GetDelivery` | D11 |
| 84 | `internal/store/query.go:259-261` | `listAttempts` | D11 |
| 85 | `internal/store/query.go:263-265` | `listAttempts` | D11 |
| 86 | `internal/store/query.go:286-288` | `collectDeliveryViews` | D11 |
| 87 | `internal/store/query.go:290-292` | `collectDeliveryViews` | D11 |
| 88 | `internal/store/store.go:143-145` | `scanDelivery` | D11 |
| 89 | `internal/template/funcs.go:59-61` | `stringFn` | D1 |
| 90 | `internal/template/funcs.go:74-76` | `hmacFn` | D1 |
| 91 | `internal/template/funcs.go:124-126` | `applyJSON` | D1 |
| 92 | `internal/template/lexer.go:91-93` | `lexString` | D4 |
| 93 | `internal/template/lexer.go:116-116` | `findStringEnd` | D4 |
| 94 | `internal/template/render.go:125-127` | `Render` | D1 |
| 95 | `internal/template/render.go:128-130` | `Render` | D1 |
| 96 | `internal/testsupport/pgtest/pgtest.go:53-55` | `StartShared` | D6 |
| 97 | `internal/testsupport/pgtest/pgtest.go:56-58` | `StartShared` | D6 |
| 98 | `internal/testsupport/pgtest/pgtest.go:60-63` | `StartShared` | D6 |
| 99 | `internal/testsupport/pgtest/pgtest.go:65-68` | `StartShared` | D6 |
| 100 | `internal/testsupport/pgtest/pgtest.go:69-72` | `StartShared` | D6 |
| 101 | `internal/testsupport/pgtest/pgtest.go:80-82` | `Start` | D6 |
| 102 | `internal/testsupport/pgtest/pgtest.go:91-93` | `Reset` | D6 |
| 103 | `internal/worker/events.go:42-44` | `LoadEvent` | D11 |
| 104 | `internal/worker/events.go:46-48` | `LoadEvent` | D1 |
| 105 | `internal/worker/events.go:57-59` | `decodePayload` | D1 |

## 3. 接受不覆盖：分类与理由

| 分类 | 说明 | 为什么不测 | 什么情况下要重新评估 |
|---|---|---|---|
| **D1** 序列化往返失败 | 对刚由 `encoding/json` 解码得到、或已通过 schema 校验的数据再次 `json.Marshal` / `Decode` 时出错。Phase 4：模板引擎对渲染结果做紧凑 JSON 序列化（`stringFn`、`hmacFn`、`applyJSON`、`Render` 的 Body）；记录脱敏后重新序列化（`maskedBodyText`、`maskResponseBody`）；attempt 记录写入 jsonb 前的 `json.Marshal`（`insertAttempt`、`jsonOrNil`）；worker 解码 jsonb 中的 payload（数据库保证它是合法的 JSON 对象） | 输入只含 JSON 原生类型（map、slice、string、json.Number、bool、nil），标准库对这些类型不会失败；只能通过注入故障触发 | 数据来源改为非 JSON 原生类型（例如直接接收 YAML 节点、自定义类型）时 |
| **D2** 内嵌资源损坏 | `schemas/config.schema.json` 无法解析或编译；内嵌迁移目录无法打开或 goose 无法识别其中的文件 | 资源在编译期内嵌，内容固定；任何一个加载配置的测试、任何一次迁移集成测试都会在资源损坏时立刻失败，等价于已被间接覆盖 | 改为运行时从外部加载 schema 或迁移时 |
| **D3** 第三方库的异常返回 | jsonschema 库返回非 `*ValidationError` 的错误，或叶子错误没有 BasicOutput | 依赖库内部行为，正常输入不会出现；保留分支只是为了不吞错误 | 升级 `santhosh-tekuri/jsonschema` 主版本时 |
| **D4** 当前调用路径下不可达 | `closestBranch` 的空结果兜底（见第 1 节说明）；`joinIndex` 处理根路径（配置的根是对象，数组索引不可能出现在根上）；`newLogger` 的日志级别错误（级别已由 `LoadSettings` 校验）；`configCheck` 中 `CheckRequired` 返回「缺失」以外的错误（`EnvResolver` 对 `env:` 引用只会返回 `ErrNotSet`）；`api` 受理中 `ParseEnvelope` 返回约定之外的错误、对已校验为对象的 payload 路由解码失败、route 引用了不存在的 destination（配置加载时已校验）、`Shutdown` 之后监听返回 `ErrServerClosed` 以外的错误；`store` 在 `FOR UPDATE` 锁住之后条件更新到 0 行（条件更新按 ADR-0009 M2 保留，用于日后去掉行锁或改为 MQ 派发）；详情查询中 payload 为 NULL（只有 Phase 5 的保留期清理才会删除 payload）；`dispatch.targetsOf` 遇到引用不存在策略的 destination（配置加载时已校验）；模板词法 `lexString` / `findStringEnd` 的未闭合字符串（`findClose` 已先以 unterminated expression 拒绝） | 没有任何合法或非法输入能走到 | 修改 schema 错误转换逻辑，或配置根改为数组时 |
| **D5** 可达但只影响报错文案 | `isPlainKey("")`：配置中出现空字符串 key（`"": 1`）时，字段路径会以引号形式显示 | 只影响错误信息里路径的显示格式，不影响校验结果 | 有用户反馈报错路径难以理解时 |
| **D6** 测试数据与测试支架自身出错 | 黄金用例 YAML 中出现无法表示为 JSON 的值（如 `.inf`）；`internal/testsupport/pgtest` 启动容器、迁移、清表失败 | 这些是**维护者编写的测试数据与测试工具**的失败路径，不是产品代码；它们出错时测试本身会失败并报告原因 | 黄金用例改由外部系统生成，或开放给其他团队编辑时 |
| **D7** 操作系统能力失效 | `uuid.NewV7()` 失败时 panic；`keygen` 读取 `crypto/rand` 失败；`os.Hostname()` 失败时 worker id 退回 `worker-<pid>` | 只有操作系统随机数源或主机名不可用时才会发生，此时进程无法正常工作 | 不需要 |
| **D8** 完整的 `serve` 进程 | `runServe` 在 Runtime 建好之后的部分（SIGHUP 监听、启动日志、角色编排、正常关闭时释放连接池、超时时跳过释放） | 组成部分分别由 `TestWatchReload`、`TestRunRoles`、`buildRuntime` 的集成测试和 `api.Run` 的集成测试覆盖；整体由各阶段计划中的手工验证覆盖（Phase 3：`serve --role=api` + curl）。Phase 4 的 worker 角色由 `internal/worker` 的端到端集成测试直接调用 `worker.Run` 覆盖 | 原定 Phase 4 完成后以子进程启动完整的 `serve` 并发送信号；Phase 4 计划未包含这一项，仍待处理（可在 Phase 5 可观测性阶段一并完成） |
| **D9** 角色正常退出 | `serve` 正常退出时的 `return exitOK`（单元测试中角色总是返回错误，走不到） | `api.Run`（Phase 3）与 `worker.Run`（Phase 4）均已实现并由集成测试覆盖；只剩完整进程收到信号后的正常退出，与 D8 一起处理 | 与 D8 相同 |
| **D11** 数据库调用失败 | `internal/store` 中事务内语句失败、查询或扫描失败、NOTIFY 失败；`internal/dispatch` 的领取、接管、完成、写 attempt、未配置目标检查失败（接管成功后领取失败时，仍交出已接管的 delivery），以及连接成功后 `LISTEN` 语句失败；`internal/worker` 读取事件失败；`api.serve` 的优雅关闭超时（已由 `TestServeCutsOffRequestsAfterShutdownTimeout` 覆盖） | 集成测试使用健康的数据库；这些分支只做"包装错误并返回"，事务由 `pgx.BeginFunc` 回滚，handler 对任何 store 错误返回 500（`TestIngestErrors`、`TestQueryStoreErrors` 用假 store 覆盖了映射）。要触发需要故障注入（中途断开连接、注入语句错误） | 出现数据库故障导致数据不一致或错误码不正确的线上问题时；或引入故障注入工具（如 toxiproxy）后 |
| **D10** 进程入口 | `main()` 只把 `os.Args`、`os.LookupEnv` 与标准输出交给 `run` 并退出 | `run` 的全部分支由 `cmd/messagehub/main_test.go` 测试 | 不需要 |

## 4. 覆盖率数字之外的行为缺口

这些缺口不会出现在 `make cover-gaps-all` 中：相关代码尚未编写，或属于行为与环境层面的要求。每一项都标明了负责的阶段和完成标准。

| # | 缺口 | 现状 | 负责阶段 | 完成标准 |
|---|---|---|---|---|
| B1 | 黄金用例只校验了结构，没有被引擎实际执行 | ✅ Phase 4：`internal/template/golden_test.go`（56 条，另有 `TestGoldenTemplatesPassStaticValidation` 用同一批用例检查加载时的静态校验）、`internal/outcome/golden_test.go`（32 条）、`internal/backoff/golden_test.go`（15 条）全部通过 | Phase 4 | 已覆盖 |
| B2 | OpenAPI 只做了文档与示例校验，没有针对运行中服务的契约测试 | ✅ Phase 3：`internal/api/contract_integration_test.go` 的 `TestContract` 对 8 个端点的每个请求与响应用 openapi3filter 校验（含 `IncludeResponseStatus`）；故意改坏一个字段名时测试会失败并指出字段 | Phase 3 | 已覆盖 |
| B3 | 受理请求中有重复 JSON 键时，以最后一个值为准，没有测试 | ✅ Phase 3：`api.CheckStrictJSON` 在 `ParseEnvelope` 之前拒绝任意层级的重复键（400，字段为重复键路径）；`TestCheckStrictJSON`、`TestIngestErrors/duplicate_key` | Phase 3 | 已覆盖 |
| B4 | 配置热重载（SIGHUP）与运行时解析 `env:` 引用 | ✅ Phase 2b 已实现：`secrets.EnvResolver`、`secrets.CheckRequired`、`app.ConfigHolder`（`TestReloadSwitchesToNewConfig`、`TestReloadFailureKeepsOldConfig`、`TestNewConfigHolderRejectsInvalidStart`、`TestConfigHolderConcurrentAccess`）；SIGHUP 接线见 `TestWatchReload` | Phase 2b | 已覆盖 |
| B5 | 并发：领取、lease、Reclaim（ADR-0009 M2、P5） | ✅ Phase 4（`-race`）：`internal/dispatch/pg_integration_test.go` 的 `TestConcurrentClaimsTakeEachDeliveryOnce`（4 个 dispatcher 领取 1000 条，每条恰好一次）、`TestConcurrentCompleteAppliesOnce`、`TestExpiredLeaseIsReclaimed`、`TestSameOwnerCannotCompleteAfterReclaimingItself`（lease 到期时间作为 fencing token）、`TestMaxInFlightIs*`、`TestOrdered*`、`TestDispatchQueriesUseIndexes`；`wakeup_integration_test.go` 覆盖 NOTIFY 延迟、`pg_terminate_backend` 后重连、LISTEN 不可用时轮询兜底；`internal/worker/e2e_integration_test.go` 覆盖 worker 停止后由另一个 worker 接管 | Phase 4 | 已覆盖 |
| B6 | 数据库迁移的集成测试不在 `make test` 中 | 需要 Docker；在 Colima 环境下需设置环境变量（见 `docs/design/coding-conventions.md` §10） | CI 建设时 | CI 中单独运行 `make test-integration` |
| B7 | `api`、`db`、`schemas` 三个包几乎没有 Go 语句，覆盖率统计对其不适用 | `db` 由集成测试覆盖；`schemas` 由 `internal/config` 的测试间接覆盖 | — | 不需要单独处理 |
| B9 | attempt 记录脱敏（ADR-0008 D2）：Header、URL、Body 中的敏感名称字段与携带密钥的表达式输出记为 `***`，且在截断前完成 | ✅ Phase 4：`internal/outbound/record_test.go`（三个位置 × 两类规则、`X-Hub-Signature` 按名称脱敏、截断边界上不残留部分密钥、UTF-8 边界）、`internal/template/render_test.go`（双输出：hmac 签名在 masked 中保持原值）、`TestE2ESuccessIsRecordedWithoutSecrets`（数据库中的记录不含密钥）。响应体为合法 JSON 时按名称规则脱敏叶子值（`TestBuildResponseRecord`）；剩余风险见 B18 | Phase 4 | 已覆盖 |
| B10 | API Key 生成工具；启动时校验哈希格式；双哈希轮换（ADR-0008 D4） | Phase 2b 已实现并测试：`messagehub keygen`、`apikey.ParseHashes`、`apikey.Matches`（新旧两个哈希都能匹配）、`app.CheckPrincipalKeys`（格式错误时拒绝启动与重载，`config check` 退出码 1）。**剩余**：HTTP 认证中间件使用这些函数 | Phase 3 | 认证测试覆盖新旧 key 并存期间都能通过 |
| B11 | 仓库密钥扫描（ADR-0008 D1 第二道防线） | `make check-secrets` 已检查被跟踪的 `.env` 文件；gitleaks 未安装时跳过扫描并提示 | CI 建设时 | CI 安装固定版本的 gitleaks，`make check-secrets` 在 CI 中必跑 |
| B12 | Kubernetes 清单（Secret 模板、Deployment 的 `envFrom`）尚未落地到仓库 | 规范与示例见 `docs/deploy/k8s-secrets.md` | 部署方案阶段 | `deploy/k8s/` 下有清单，并在 kind/minikube 等测试集群中验证 Secret 缺失时 Pod 无法启动 |
| B14 | 不同来源使用相同的 `ordering_key` 会进入同一有序范围、互相阻塞，系统不做强制隔离 | 用户确认维持现状（ADR-0007 方案 A）：domain-model.md §6 约定业务方给 key 加来源前缀；source 角色查询时其他来源的阻塞前序不出现在 `blocked_by` 中（`TestBlockedByHidesOtherSourcesFromScopedCallers`）。被阻塞方因此只能看到自己的投递处于 PENDING，需要 viewer / operator 排查 | 按 ADR-0007 Revisit When | 出现误阻塞时评估 ADR-0007 方案 B / C；在此之前，Phase 5 的阻塞告警应带上阻塞者的来源，便于运维定位 |
| B15 | `ingest_seq` 在 INSERT 时分配、在提交时才可见：同一 key 并发提交时，较小的 `ingest_seq` 可能晚于较大的可见 | ✅ Phase 4：领取只检查已提交的前序，不假设"可见即完整"；`TestOrderingCannotSeeUncommittedPredecessors` 记录违反约定时的行为（后提交的较小序号排在已提交的较大序号之后投递）。约定本身仍是业务方义务（domain-model.md §6） | Phase 4 | 已覆盖（行为已记录，不作保证） |
| B13 | 不带过滤条件、或只按 `destination_id` 过滤的 `GET /v1/deliveries` 没有可用的 `created_at` 排序索引，数据量大时走顺序扫描 + 排序 | `TestListQueriesUseIndexes` 已断言主要查询（事件列表、按 event_type、unrouted、按 destination+status、按 event_id）不走顺序扫描；这两种查询未纳入 | Phase 4 之后 | `00002_deliveries_tuning.sql` 已由 Phase 4 占用；新增 `deliveries (created_at DESC)` 索引的迁移编号应为 `00003`，并加入 EXPLAIN 断言 |
| B16 | `max_in_flight` 是软上限（domain-model.md §8 已写明）：多个 worker 同时领取时各自只看到已提交的在途数 | 超出量不超过 (worker 数 − 1) × min(批大小, `max_in_flight`)，`TestMaxInFlightIsASoftLimitAcrossWorkers` 断言该上界；单 worker 时严格遵守（`TestMaxInFlightIsRespected`） | ADR-0010 Revisit When | 用户确认暂不做严格上限（2026-10-24），设计预期日后修订；出现 ADR-0010 列出的触发条件时，按其方案 B（advisory lock）/ C（计数表）/ E（Phase 6 限流）重新评估 |
| B17 | InfraRetry 不计入预算，也不增加退避：`attempt_count` 不变，延迟始终按当前重试序号计算 | OAuth2 token 端点、出口代理或 secret 长期不可用时，每个受影响的 delivery 以固定间隔无限重试（`TestE2EOAuthTokenFailureIsInfraRetry`） | Phase 5 | infra_error 的指标与告警；如需要，增加"连续 infra 失败次数"以拉长间隔 |
| B18 | 响应体不是合法 JSON，或超过 1 MiB 读取上限而不完整时，不做名称脱敏，只截断 | ADR-0008 残余风险收窄为"非 JSON 响应体"；source 角色仍可通过 `GET /v1/deliveries/{id}` 读到这类响应体 | Phase 6 | 评估对 source 角色隐藏 `response.body`，或按供应商配置响应体脱敏规则 |
| B19 | 配置中删除某个 destination 后，它尚未完成的 delivery 不会被领取，只每分钟输出一次 `dispatch.unknown_destinations` warn 日志 | 零静默丢失：数据保留，恢复配置后继续投递（`TestUnknownDestinationIsNotClaimedAndIsReported`） | Phase 5 | 指标与告警；运维文档说明删除目标前先处理其在途投递 |
| B20 | 代码评审（2026-10-24）提出、本阶段未处理的项 | (1) 领取语句提交后、worker 开始处理前进程退出或连接断开，这些 delivery 要等 lease 过期才被接管，接管时计入一次实际没有发出的 attempt；(2) 供应商返回 401 时不会丢弃缓存的 OAuth2 token，直到 token 过期；(3) 全局并发槽位按到期时间分配，`HUB_WORKER_CONCURRENCY` 小于各目标 `max_in_flight` 之和时，一个慢目标可能占满全部槽位；(4) 已删除目标中仍为 IN_FLIGHT 的 delivery 不会被接管；(5) infra 重试的 attempt 序号重复（`number` 不唯一） | Phase 5 / 6 | 逐项评估：(1) 缩短首次 lease 或加心跳；(2) 401 时失效 token；(3) 按目标的 worker 内并发计数；(4) 孤儿检查覆盖 IN_FLIGHT；(5) 记录 infra 重试序号或在 UI 中区分 |
| B8 | 只由服务端校验的规则：metadata key 的格式、payload 大小、不存在的日期 | 由 `api/openapi_test.go` 的 `TestServerOnlyRules` 守护：测试断言 OpenAPI 接受、Go 拒绝；一旦 OpenAPI 也开始拒绝，测试会失败并提示迁移用例 | — | 已覆盖 |

## 5. 变更记录

| 日期 | 变更 |
|---|---|
| 2026-10-24 | 初始记录。补齐 6 处缺口；`closestBranch` 兜底分支改归 D4；覆盖率从 96.1% 提升到 97.2% |
| 2026-10-24 | ADR-0008：新增凭据校验（100% 覆盖，未覆盖列表不变）；覆盖率 97.4%；新增行为缺口 B9–B11 |
| 2026-10-24 | ADR-0008 D3 定案为 Kubernetes Secret：新增 `config.RequiredEnv()`（100% 覆盖）；更新 B4；新增 B12 |
| 2026-10-24 | Phase 3 受理与查询：第 2 节改以 `make cover-gaps-all`（含集成测试）为准并整体重新生成（76 条）；新增 D11（数据库调用失败），D6 扩展到测试支架，D8、D9 随 `api.Run` 实现收窄；B2、B3 完成；新增 B13 |
| 2026-10-24 | Phase 2b 共享运行底座：新增 19 个未覆盖条目（#23–#41），新增分类 D8–D10，扩充 D2、D4、D7；B4 完成；B10 只剩认证中间件；覆盖率 96.1% |
| 2026-10-24 | Phase 4 投递引擎：第 2 节重新生成（105 条，新增 30 条，`worker.Run` 占位条目消失）；扩充 D1、D4、D11，D8 记录未完成的子进程测试，D9 收窄为 `serve` 正常退出；B1、B5、B9、B15 完成；B13 改为编号 00003；新增 B16–B20；覆盖率 96.1% |
