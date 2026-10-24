# 测试覆盖缺口记录

> 用途：记录**哪些代码没有被测试执行、为什么、何时处理**，便于随时回溯。
> 维护规则：`make cover-gaps` 的输出发生变化（新增或消失条目）时，必须在同一次提交中更新本文件。行号会随代码变化，所以每一条都同时记录**函数名**，以函数名为准。

## 如何重新生成

```bash
make cover-gaps        # 等价于 bash scripts/cover-gaps.sh
```

脚本对全部包执行 `go test -coverpkg=./...`，合并各个测试二进制的结果（任一测试执行过即视为已覆盖），然后逐条列出从未执行的代码块（`文件:起止行  首行代码`），最后输出总覆盖率。集成测试（`-tags integration`）不计入，见第 4 节。

## 当前快照

| 项 | 值 |
|---|---|
| 日期 | 2026-10-24 |
| 阶段 | Phase 2b 共享运行底座完成后 |
| 语句覆盖率（`./...`，不含集成测试） | **96.1%**（下降来自只有连上数据库才会执行的启动代码，见 D8） |
| 未覆盖代码块 | 41 个，全部属于第 3 节"接受不覆盖" |
| 各包覆盖率 | `internal/domain` 99.2%、`internal/config` 97.1%、`internal/contracts` 96.4%、`internal/app` 98.9%、`internal/secrets` 100%、`internal/apikey` 100%、`internal/rules` 100%、`internal/platform/pg` 81.5%（集成测试另外覆盖成功路径）、`cmd/messagehub` 86.8% |

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

## 2. 未覆盖条目总表（与 `make cover-gaps` 输出逐条对应）

| # | 位置 | 函数 | 分类 |
|---|---|---|---|
| 1 | `internal/config/config.go:25-27` | `newConfig` | D1 |
| 2 | `internal/config/config.go:56-58` | `computeHash` | D1 |
| 3 | `internal/config/config.go:62-64` | `computeHash` | D1 |
| 4 | `internal/config/config.go:66-68` | `computeHash` | D1 |
| 5 | `internal/config/load.go:117-119` | `toJSONValue` | D1 |
| 6 | `internal/config/load.go:153-155` | `decodeTyped` | D1 |
| 7 | `internal/config/schema.go:29-31` | `compiledSchema` | D2 |
| 8 | `internal/config/schema.go:34-36` | `compiledSchema` | D2 |
| 9 | `internal/config/schema.go:44-46` | `validateSchema` | D2 |
| 10 | `internal/config/schema.go:52-54` | `validateSchema` | D3 |
| 11 | `internal/config/schema.go:92-94` | `closestBranch` | D4 |
| 12 | `internal/config/schema.go:140-142` | `leafMessage` | D3 |
| 13 | `internal/config/schema.go:178-180` | `joinIndex` | D4 |
| 14 | `internal/config/schema.go:200-202` | `isPlainKey` | D5 |
| 15 | `internal/contracts/cases.go:104-106` | `jsonValue` | D6 |
| 16 | `internal/contracts/cases.go:108-110` | `jsonValue` | D6 |
| 17 | `internal/contracts/cases.go:112-114` | `jsonValue` | D1 |
| 18 | `internal/contracts/success.go:199-201` | `convertBodyRule` | D6 |
| 19 | `internal/contracts/template.go:110-112` | `convertTemplateCase` | D6 |
| 20 | `internal/contracts/template.go:119-121` | `convertTemplateCase` | D6 |
| 21 | `internal/contracts/template.go:192-194` | `convertEvent` | D6 |
| 22 | `internal/domain/ids.go:13-14` | `NewID` | D7 |
| 23 | `cmd/messagehub/main.go:46-48` | `main` | D10 |
| 24 | `cmd/messagehub/main.go:147-148` | `configCheck` | D4 |
| 25 | `cmd/messagehub/main.go:174-175` | `migrate` | D8 |
| 26 | `cmd/messagehub/main.go:185-187` | `keygen` | D7 |
| 27 | `cmd/messagehub/serve.go:67-67` | `serve` | D9 |
| 28 | `cmd/messagehub/serve.go:79-81` | `runServe` | D4 |
| 29 | `cmd/messagehub/serve.go:92-103` | `runServe` | D8 |
| 30 | `cmd/messagehub/serve.go:103-105` | `runServe` | D8 |
| 31 | `cmd/messagehub/serve.go:106-107` | `runServe` | D8 |
| 32 | `cmd/messagehub/serve.go:112-114` | `newLogger` | D4 |
| 33 | `cmd/messagehub/serve.go:132-140` | `buildRuntime` | D8 |
| 34 | `internal/api/run.go:12-14` | `Run` | D9 |
| 35 | `internal/app/settings.go:182-184` | `defaultWorkerID` | D7 |
| 36 | `internal/platform/pg/pg.go:30-30` | `NewPool` | D8 |
| 37 | `internal/platform/pg/pg.go:40-40` | `ConnectDirect` | D8 |
| 38 | `internal/platform/pg/pg.go:58-60` | `migrate` | D2 |
| 39 | `internal/platform/pg/pg.go:62-64` | `migrate` | D2 |
| 40 | `internal/platform/pg/pg.go:69-69` | `migrate` | D8 |
| 41 | `internal/worker/run.go:12-14` | `Run` | D9 |

## 3. 接受不覆盖：分类与理由

| 分类 | 说明 | 为什么不测 | 什么情况下要重新评估 |
|---|---|---|---|
| **D1** 序列化往返失败 | 对刚由 `encoding/json` 解码得到、或已通过 schema 校验的数据再次 `json.Marshal` / `Decode` 时出错 | 输入只含 JSON 原生类型（map、slice、string、json.Number、bool、nil），标准库对这些类型不会失败；只能通过注入故障触发 | 数据来源改为非 JSON 原生类型（例如直接接收 YAML 节点、自定义类型）时 |
| **D2** 内嵌资源损坏 | `schemas/config.schema.json` 无法解析或编译；内嵌迁移目录无法打开或 goose 无法识别其中的文件 | 资源在编译期内嵌，内容固定；任何一个加载配置的测试、任何一次迁移集成测试都会在资源损坏时立刻失败，等价于已被间接覆盖 | 改为运行时从外部加载 schema 或迁移时 |
| **D3** 第三方库的异常返回 | jsonschema 库返回非 `*ValidationError` 的错误，或叶子错误没有 BasicOutput | 依赖库内部行为，正常输入不会出现；保留分支只是为了不吞错误 | 升级 `santhosh-tekuri/jsonschema` 主版本时 |
| **D4** 当前调用路径下不可达 | `closestBranch` 的空结果兜底（见第 1 节说明）；`joinIndex` 处理根路径（配置的根是对象，数组索引不可能出现在根上）；`newLogger` 的日志级别错误（级别已由 `LoadSettings` 校验）；`configCheck` 中 `CheckRequired` 返回「缺失」以外的错误（`EnvResolver` 对 `env:` 引用只会返回 `ErrNotSet`） | 没有任何合法或非法输入能走到 | 修改 schema 错误转换逻辑，或配置根改为数组时 |
| **D5** 可达但只影响报错文案 | `isPlainKey("")`：配置中出现空字符串 key（`"": 1`）时，字段路径会以引号形式显示 | 只影响错误信息里路径的显示格式，不影响校验结果 | 有用户反馈报错路径难以理解时 |
| **D6** 黄金用例文件本身写错 | 黄金用例 YAML 中出现无法表示为 JSON 的值（如 `.inf`） | 这些是**维护者编写的测试数据**的加载路径，不是产品代码；黄金用例的结构由 `internal/contracts` 的测试整体校验 | 黄金用例改由外部系统生成，或开放给其他团队编辑时 |
| **D7** 操作系统能力失效 | `uuid.NewV7()` 失败时 panic；`keygen` 读取 `crypto/rand` 失败；`os.Hostname()` 失败时 worker id 退回 `worker-<pid>` | 只有操作系统随机数源或主机名不可用时才会发生，此时进程无法正常工作 | 不需要 |
| **D8** 需要可用的数据库 | 连接池、直连、迁移的成功路径；`buildRuntime` 组装 Runtime；`runServe` 在 Runtime 建好之后的部分（含正常关闭时释放连接池、超时时跳过释放）；`migrate up` 成功后的输出 | 单元测试不连接数据库。`internal/platform/pg/pg_integration_test.go` 与 `cmd/messagehub/main_integration_test.go`（`-tags integration`）覆盖连接、迁移幂等与 `buildRuntime`；`runServe` 的后半段由 `TestWatchReload`、`TestRunRoles` 分别覆盖其组成部分，整体由计划中的手工验证覆盖 | Phase 3 / 4 的角色实现后，在集成测试中启动完整的 `serve` |
| **D9** 角色占位 | `api.Run`、`worker.Run` 的占位实现；`serve` 正常退出时的 `return exitOK`（占位角色总是返回错误，走不到） | 占位代码由 Phase 3 / Phase 4 整体替换 | Phase 3 / Phase 4 完成时这些条目应消失 |
| **D10** 进程入口 | `main()` 只把 `os.Args`、`os.LookupEnv` 与标准输出交给 `run` 并退出 | `run` 的全部分支由 `cmd/messagehub/main_test.go` 测试 | 不需要 |

## 4. 覆盖率数字之外的行为缺口

这些缺口不会出现在 `make cover-gaps` 中：相关代码要么尚未编写，要么不在单元测试中运行。每一项都标明了负责的阶段和完成标准。

| # | 缺口 | 现状 | 负责阶段 | 完成标准 |
|---|---|---|---|---|
| B1 | 黄金用例只校验了结构，没有被引擎实际执行 | 模板引擎、成功判定、退避计算尚未实现 | Phase 4 | 三组黄金用例全部由引擎执行并通过 |
| B2 | OpenAPI 只做了文档与示例校验，没有针对运行中服务的契约测试 | HTTP 服务尚未实现 | Phase 3 | 对每个端点的请求与响应做 schema 校验的契约测试 |
| B3 | 受理请求中有重复 JSON 键时，以最后一个值为准，没有测试 | 已在 domain-model.md §13.1 记录 | Phase 3 | HTTP 层拒绝重复键（400），并有测试 |
| B4 | 配置热重载（SIGHUP）与运行时解析 `env:` 引用 | ✅ Phase 2b 已实现：`secrets.EnvResolver`、`secrets.CheckRequired`、`app.ConfigHolder`（`TestReloadSwitchesToNewConfig`、`TestReloadFailureKeepsOldConfig`、`TestNewConfigHolderRejectsInvalidStart`、`TestConfigHolderConcurrentAccess`）；SIGHUP 接线见 `TestWatchReload` | Phase 2b | 已覆盖 |
| B5 | 并发：领取、lease、Reclaim（ADR-0009 M2、P5） | 本阶段没有并发代码 | Phase 4 | 集成测试（含 `-race`）覆盖：多 worker 竞争领取、重复领取同一 id 只成功一次、lease 过期接管、NOTIFY 丢失后轮询兜底 |
| B6 | 数据库迁移的集成测试不在 `make test` 中 | 需要 Docker；在 Colima 环境下需设置环境变量（见 `docs/design/coding-conventions.md` §10） | CI 建设时 | CI 中单独运行 `make test-integration` |
| B7 | `api`、`db`、`schemas` 三个包几乎没有 Go 语句，覆盖率统计对其不适用 | `db` 由集成测试覆盖；`schemas` 由 `internal/config` 的测试间接覆盖 | — | 不需要单独处理 |
| B9 | attempt 记录脱敏（ADR-0008 D2）：Header、URL、Body 中的敏感名称字段与携带密钥的表达式输出记为 `***`，且在截断前完成 | 规格见 domain-model.md §10；脱敏判定复用 `domain.IsSensitiveName` | Phase 4 | 黄金用例覆盖三个位置、hmac 签名不脱敏、截断边界上的密钥不残留 |
| B10 | API Key 生成工具；启动时校验哈希格式；双哈希轮换（ADR-0008 D4） | Phase 2b 已实现并测试：`messagehub keygen`、`apikey.ParseHashes`、`apikey.Matches`（新旧两个哈希都能匹配）、`app.CheckPrincipalKeys`（格式错误时拒绝启动与重载，`config check` 退出码 1）。**剩余**：HTTP 认证中间件使用这些函数 | Phase 3 | 认证测试覆盖新旧 key 并存期间都能通过 |
| B11 | 仓库密钥扫描（ADR-0008 D1 第二道防线） | `make check-secrets` 已检查被跟踪的 `.env` 文件；gitleaks 未安装时跳过扫描并提示 | CI 建设时 | CI 安装固定版本的 gitleaks，`make check-secrets` 在 CI 中必跑 |
| B12 | Kubernetes 清单（Secret 模板、Deployment 的 `envFrom`）尚未落地到仓库 | 规范与示例见 `docs/deploy/k8s-secrets.md` | 部署方案阶段 | `deploy/k8s/` 下有清单，并在 kind/minikube 等测试集群中验证 Secret 缺失时 Pod 无法启动 |
| B8 | 只由服务端校验的规则：metadata key 的格式、payload 大小、不存在的日期 | 由 `api/openapi_test.go` 的 `TestServerOnlyRules` 守护：测试断言 OpenAPI 接受、Go 拒绝；一旦 OpenAPI 也开始拒绝，测试会失败并提示迁移用例 | — | 已覆盖 |

## 5. 变更记录

| 日期 | 变更 |
|---|---|
| 2026-10-24 | 初始记录。补齐 6 处缺口；`closestBranch` 兜底分支改归 D4；覆盖率从 96.1% 提升到 97.2% |
| 2026-10-24 | ADR-0008：新增凭据校验（100% 覆盖，未覆盖列表不变）；覆盖率 97.4%；新增行为缺口 B9–B11 |
| 2026-10-24 | ADR-0008 D3 定案为 Kubernetes Secret：新增 `config.RequiredEnv()`（100% 覆盖）；更新 B4；新增 B12 |
| 2026-10-24 | Phase 2b 共享运行底座：新增 19 个未覆盖条目（#23–#41），新增分类 D8–D10，扩充 D2、D4、D7；B4 完成；B10 只剩认证中间件；覆盖率 96.1% |
