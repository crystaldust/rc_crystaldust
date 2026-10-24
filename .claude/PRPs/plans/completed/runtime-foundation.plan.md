# Plan: 共享运行底座（Message Hub Phase 2b）

## Summary
为 Phase 3（受理与查询）和 Phase 4（投递引擎）提供共用的运行底座：单一可执行文件与子命令、进程参数、数据库连接池与迁移、配置持有与 SIGHUP 热重载、secret 解析与启动校验、入站 API Key 的生成与哈希、通用规则求值（`internal/rules`）、日志、优雅退出，以及集成测试共用的 PostgreSQL 测试支架。完成后 Phase 3 与 Phase 4 可以完全并行，互不修改对方的文件。

## User Story
As a Phase 3 / Phase 4 的开发者,
I want 一套已经接好配置、数据库、密钥、日志与进程生命周期的运行底座,
So that 两个阶段只需各自实现 `api.Run` 与 `worker.Run`，不必重复搭建、也不会在同一批文件上冲突。

## Problem → Solution
Phase 2 只有契约与库代码，没有可运行的进程 → 有一个 `messagehub` 可执行文件：能校验配置、列出所需环境变量、执行迁移、生成 API Key，并以 `api` / `worker` / `all` 角色启动（两个角色的实现由 Phase 3 / 4 填充）。

## Metadata
- **Complexity**: Medium
- **Source PRD**: `.claude/PRPs/prds/message-hub.prd.md`
- **PRD Phase**: Phase 2b — 共享运行底座（新增，Phase 3 / 4 的前置）
- **Estimated Files**: 约 24 个

---

## UX Design

### Before
N/A — 尚无可执行程序。

### After（运维视角）
```
messagehub config check   --config hub.yaml   # 校验配置；列出 warning 与所需环境变量；缺失变量时退出码非 0
messagehub config env     --config hub.yaml   # 每行输出一个所需环境变量名（用于生成 K8s Secret 键清单）
messagehub migrate up                          # 执行数据库迁移（HUB_DATABASE_URL）
messagehub keygen                              # 生成入站 API Key，输出 key 与 sha256
messagehub serve --role=api|worker|all         # 启动服务；SIGHUP 重载配置；SIGTERM 优雅退出
```

### Interaction Changes
| Touchpoint | Before | After | Notes |
|---|---|---|---|
| 配置校验 | 只能跑 Go 测试 | `messagehub config check`，CI 可直接调用 | 退出码：0 通过，1 配置错误，2 环境变量缺失 |
| K8s Secret 键清单 | 手工整理 | `messagehub config env` | 与 `docs/deploy/k8s-secrets.md` 衔接 |
| 生成 API Key | 无 | `messagehub keygen` | key 只输出一次 |

---

## Mandatory Reading

| Priority | File | Lines | Why |
|---|---|---|---|
| P0 | `docs/design/coding-conventions.md` | all | 包结构、错误、不可变、日志、测试、密钥约定 |
| P0 | `internal/config/load.go` | 21–80 | `Load`、`Parse`、`Option`、`WithTemplateValidator` 的签名与加载流程 |
| P0 | `internal/config/env_refs.go` | all | `RequiredEnv()`、`EnvRefPrefix` |
| P0 | `docs/adr/0008-secrets-management.md` | D3、D4 | 密钥只来自环境变量；API Key 格式、双哈希轮换、启动校验 |
| P0 | `docs/design/domain-model.md` | §7.2、§9、§13.5 | 规则算子语义；API Key 要求；引用前缀 |
| P1 | `docs/adr/0009-pg-to-mq-migration-path.md` | P4、P5 | LISTEN 需要直连数据库的专用连接；轮询兜底 |
| P1 | `db/embed.go`、`db/migrations_integration_test.go` | all | 迁移内嵌方式；testcontainers + goose 的用法 |
| P1 | `internal/domain/errors.go` | 1–42 | `FieldError`、`ValidationError` |
| P1 | `internal/config/types.go` | `Rule`、`RuleOp` | `rules` 包的输入类型 |
| P2 | `docs/deploy/k8s-secrets.md` | §2–§5 | 进程参数中的数据库连接串同样来自 Secret |

## External Documentation

| Topic | Source | Key Takeaway |
|---|---|---|
| pgxpool | https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool | `pgxpool.New(ctx, dsn)`；`pool.Ping(ctx)` 用于就绪检查 |
| goose 内嵌迁移 | https://github.com/pressly/goose | `goose.SetBaseFS(db.Migrations)`、`goose.SetDialect("postgres")`、`goose.UpContext(ctx, sqlDB, db.MigrationsDir)` |
| os/signal | https://pkg.go.dev/os/signal | `signal.NotifyContext` 处理 SIGTERM/SIGINT；SIGHUP 单独用 `signal.Notify` 的 channel |
| log/slog | https://pkg.go.dev/log/slog | `slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl}))` |

```
KEY_INSIGHT: 本阶段不新增第三方依赖；pgxpool、goose、testcontainers 已在 go.mod 中
APPLIES_TO: 全部任务
GOTCHA: 不得运行 go get 升级既有依赖；需要新增依赖时必须满足"发布至少两周"

KEY_INSIGHT: LISTEN 不能经过事务模式的 pgbouncer
APPLIES_TO: Task 1（Settings 中的 HUB_DATABASE_LISTEN_URL）
GOTCHA: 底座只提供配置项与连接函数；LISTEN 循环由 Phase 4 实现
```

---

## Patterns to Mirror

### NAMING_CONVENTION / IMMUTABILITY
// SOURCE: internal/domain/delivery_state.go:6-46
```go
type Trigger string
const ( TriggerClaim Trigger = "Claim" /* ... */ )
var transitions = map[DeliveryStatus]map[Trigger]DeliveryStatus{ /* read-only table */ }
func Transition(from DeliveryStatus, trigger Trigger) (DeliveryStatus, error) {
	if to, ok := transitions[from][trigger]; ok { return to, nil }
	return "", fmt.Errorf("%w: %s on %s", ErrInvalidTransition, trigger, from)
}
```

### ERROR_HANDLING（聚合校验错误）
// SOURCE: internal/domain/errors.go:15-32
```go
type FieldError struct { Field string; Reason string }
type ValidationError struct { Problems []FieldError }
func (e *ValidationError) Error() string { /* "validation failed:\n" + 每行一个 field: reason */ }
```

### CONFIG_LOADING
// SOURCE: internal/config/load.go:51-80
```go
func Load(path string, opts ...Option) (*Config, error)   // 读取文件后调用 Parse，并以 fmt.Errorf("load config %q: %w") 包装
func Parse(data []byte, opts ...Option) (*Config, error)  // 严格 YAML → schema → 语义校验 → 只读 *Config
func WithTemplateValidator(v TemplateValidator) Option
```

### TEST_STRUCTURE
// SOURCE: internal/config/config_test.go:15-22、internal/domain/delivery_state_test.go
```go
func mustLoad(t *testing.T, path string, opts ...Option) *Config {
	t.Helper()
	cfg, err := Load(path, opts...)
	if err != nil { t.Fatalf("Load(%q) error: %v", path, err) }
	return cfg
}
// 表驱动 + t.Run；注释标注 Arrange / Act / Assert
```

### INTEGRATION_TEST
// SOURCE: db/migrations_integration_test.go（//go:build integration）
testcontainers `postgres.Run(ctx, "postgres:16-alpine", postgres.BasicWaitStrategies())` + goose；断言用 `errors.As(err, &*pgconn.PgError)` 检查 SQLSTATE。Colima 环境需要 `DOCKER_HOST` 与 `TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE`（coding-conventions.md §10）。

### LOGGING_PATTERN
本阶段首次引入运行时日志：`log/slog` JSON；字段名小写下划线；禁止记录密钥、Authorization、payload 原文（coding-conventions.md §7、§7a）。

---

## Files to Change

| File | Action | Justification |
|---|---|---|
| `cmd/messagehub/main.go` | CREATE | 子命令分发（config / migrate / keygen / serve） |
| `cmd/messagehub/serve.go` | CREATE | 角色启动、信号处理、优雅退出 |
| `cmd/messagehub/main_test.go` | CREATE | 子命令参数解析与退出码 |
| `internal/app/settings.go` + `_test.go` | CREATE | 进程参数（环境变量）解析与校验 |
| `internal/app/runtime.go` | CREATE | `Runtime`：Phase 3/4 共用的依赖集合 |
| `internal/app/config_holder.go` + `_test.go` | CREATE | 原子持有当前配置；Reload 失败保留旧配置 |
| `internal/secrets/resolver.go` + `_test.go` | CREATE | `Resolver` 接口、`EnvResolver`、`CheckRequired` |
| `internal/apikey/apikey.go` + `_test.go` | CREATE | 生成 key、计算哈希、解析环境变量中的 1–2 个哈希 |
| `internal/rules/rules.go` + `_test.go` | CREATE | 点分路径取值与 eq/ne/in/exists/not_exists 求值（Route.match 与成功判定共用） |
| `internal/platform/pg/pg.go` | CREATE | 连接池创建、直连连接、迁移执行 |
| `internal/platform/pg/pg_integration_test.go` | CREATE | 连接与迁移的集成测试 |
| `internal/testsupport/pgtest/pgtest.go` | CREATE | 集成测试共用：启动 PG 容器并迁移（`//go:build integration`） |
| `internal/api/run.go` | CREATE | `api.Run(ctx, *app.Runtime) error` 占位，返回 `app.ErrRoleNotImplemented`。**Phase 3 拥有并替换** |
| `internal/worker/run.go` | CREATE | `worker.Run(ctx, *app.Runtime) error` 占位。**Phase 4 拥有并替换** |
| `Makefile` | UPDATE | `build` 产出 `bin/messagehub`；新增 `config-check` |
| `.gitignore` | UPDATE | 忽略 `bin/` |
| `docs/deploy/k8s-secrets.md` | UPDATE | 进程参数中的 `HUB_DATABASE_URL` 等同样来自 Secret |
| `docs/testing/coverage-gaps.md` | UPDATE | B4、B10 的状态 |

### 文件归属（保证 Phase 3 / 4 并行不冲突）
- 本计划完成后：`internal/api/**`、`internal/store/**` 只由 Phase 3 修改；`internal/worker/**`、`internal/dispatch/**`、`internal/template/**`、`internal/outcome/**`、`internal/backoff/**`、`internal/outbound/**` 只由 Phase 4 修改
- `cmd/messagehub/**` 在 Phase 3 中**不修改**；Phase 4 只允许在 `serve.go` 中接入模板校验器（一处改动，见 Phase 4 计划 Task 10）
- 两个阶段都可以**只读使用**本计划产出的包；如需修改底座，先单独提交到底座，再各自同步

## NOT Building

- HTTP 路由、受理与查询逻辑（Phase 3）
- 领取、投递、模板引擎（Phase 4）
- `env:` 以外的引用前缀（ADR-0008 D3）
- 指标与追踪（Phase 5）；本阶段只有日志
- Kubernetes 清单（B12）

---

## Step-by-Step Tasks

### Task 1: 进程参数 `internal/app/settings.go`
- **ACTION**: 定义 `Settings` 与 `LoadSettings(lookup func(string) (string, bool)) (Settings, error)`
- **IMPLEMENT**:

  | 环境变量 | 必填 | 默认 | 说明 |
  |---|---|---|---|
  | `HUB_CONFIG_PATH` | 是 | — | hub.yaml 路径 |
  | `HUB_DATABASE_URL` | serve / migrate 时必填 | — | 业务连接（可经 pgbouncer）；**属于密钥**，来自 K8s Secret |
  | `HUB_DATABASE_LISTEN_URL` | 否 | 同 `HUB_DATABASE_URL` | LISTEN 专用的直连连接串（ADR-0009 P4） |
  | `HUB_HTTP_ADDR` | 否 | `:8080` | api 角色监听地址 |
  | `HUB_WORKER_ID` | 否 | `<hostname>-<pid>` | lease_owner |
  | `HUB_WORKER_CONCURRENCY` | 否 | `64` | worker 同时处理的投递数上限（1–4096） |
  | `HUB_LOG_LEVEL` | 否 | `info` | debug / info / warn / error |
  | `HUB_SHUTDOWN_TIMEOUT` | 否 | `30s` | 优雅退出等待时长 |

  - 所有默认值、上下限定义为命名常量
  - 校验错误聚合为 `*domain.ValidationError`（Field 为环境变量名）
  - 注入 `lookup` 便于测试，生产传 `os.LookupEnv`
- **MIRROR**: ERROR_HANDLING
- **GOTCHA**: `Settings` 的 `String()` / 日志输出中，数据库连接串必须去掉密码（`url.Parse` 后 `User = url.User(username)`）
- **VALIDATE**: 表驱动测试覆盖默认值、非法值、缺失必填、连接串脱敏

### Task 2: 数据库连接 `internal/platform/pg`
- **ACTION**: `NewPool(ctx, dsn) (*pgxpool.Pool, error)`、`ConnectDirect(ctx, dsn) (*pgx.Conn, error)`、`MigrateUp(ctx, dsn) error`
- **IMPLEMENT**:
  - `MigrateUp` 用 `pgx/v5/stdlib` 打开 `*sql.DB`，`goose.SetBaseFS(db.Migrations)`、`SetDialect("postgres")`、`UpContext`
  - 连接池参数取 pgxpool 默认值，不在 MVP 引入调优参数
- **GOTCHA**: goose 的 `SetBaseFS` 与 `SetDialect` 是包级全局状态；用 `sync.Once` 包住，避免并发测试互相影响
- **VALIDATE**: 集成测试：对容器执行 `MigrateUp` 两次（幂等），`NewPool` 后 `Ping` 成功

### Task 3: 集成测试支架 `internal/testsupport/pgtest`
- **ACTION**: `func Start(t *testing.T) (dsn string)`，启动 `postgres:16-alpine`、执行 `MigrateUp`、`t.Cleanup` 终止容器
- **IMPLEMENT**: 每个测试包共享一个容器（`TestMain` 中调用）；提供 `Reset(t, pool)` 用 `TRUNCATE … RESTART IDENTITY CASCADE` 清空 5 张业务表
- **GOTCHA**: 文件带 `//go:build integration`，避免普通 `go test` 引入 Docker 依赖
- **VALIDATE**: Task 2 的集成测试使用该支架

### Task 4: Secret 解析 `internal/secrets`
- **ACTION**: 定义 `Resolver` 接口与 `EnvResolver`
- **IMPLEMENT**:
  ```go
  type Resolver interface {
      Resolve(ref string) (string, error)   // ref 形如 "env:NAME"
  }
  var ErrUnsupportedScheme = errors.New("unsupported secret reference scheme")
  var ErrNotSet = errors.New("secret reference is not set")
  type EnvResolver struct{ lookup func(string) (string, bool) }
  func NewEnvResolver(lookup func(string) (string, bool)) EnvResolver
  func CheckRequired(cfg *config.Config, r Resolver) error // 解析 cfg.RequiredEnv() 中的全部引用，缺失或为空的一次性聚合返回
  ```
  - 只支持 `config.EnvRefPrefix`；其他前缀返回 `ErrUnsupportedScheme`（为 ADR-0008 后续扩展预留）
  - 值为空字符串视为未设置
- **GOTCHA**: 任何错误信息只包含变量**名**，绝不包含值
- **VALIDATE**: 表驱动：已设置、未设置、空值、未知前缀；`CheckRequired` 对示例配置在只设置部分变量时列出全部缺失项

### Task 5: 入站 API Key `internal/apikey`
- **ACTION**: 按 ADR-0008 D4 实现
- **IMPLEMENT**:
  ```go
  const Prefix = "mhk_"
  const randomBytes = 32
  func Generate(rand io.Reader) (key, hashHex string, err error) // mhk_ + base64.RawURLEncoding(32 字节)
  func Hash(key string) [32]byte                                    // sha256
  func ParseHashes(value string) ([][32]byte, error)                // 1–2 个逗号分隔的 64 位小写 hex
  func Matches(key string, hashes [][32]byte) bool                  // subtle.ConstantTimeCompare
  ```
- **GOTCHA**: 比较必须常量时间；`ParseHashes` 拒绝大写 hex、空项、超过 2 项、长度不对
- **VALIDATE**: 生成的 key 长度为 47 且以 `mhk_` 开头；Hash 与 `sha256.Sum256` 一致；ParseHashes 的合法与非法用例；Matches 对两个哈希都能匹配

### Task 6: 规则求值 `internal/rules`
- **ACTION**: 实现 domain-model.md §7.2 的路径取值与 5 个算子，供 Phase 3（Route.match）与 Phase 4（成功判定的 body 规则）共用
- **IMPLEMENT**:
  ```go
  func Lookup(doc any, path string) (value any, found bool) // 点分路径；数组用数字下标；中途遇到非容器即 not found
  func Eval(rule config.Rule, doc any) bool
  func EvalAll(rules []config.Rule, doc any) bool           // 空规则列表返回 true
  ```
  - 数字比较：`json.Number`、`float64`、`int` 统一按数值比较（`0` 等于 `0.0`）
  - 类型不同即不相等（`"0"` ≠ `0`，§13.3）
  - `exists`：路径存在即可（值可为 null）；`ne`：路径不存在也算满足
- **MIRROR**: TEST_STRUCTURE
- **GOTCHA**: 配置中的 rule `Value` 来自 `json.Number`（internal/config 解码时使用 UseNumber），响应体也应以 `json.Decoder.UseNumber()` 解码；比较前统一转换
- **VALIDATE**: 表驱动覆盖 5 个算子 × 存在 / 不存在 / null / 类型不同 / 数值等价 / 数组下标；并用 `internal/contracts` 的 success 黄金用例中**只涉及 body 规则**的部分做交叉验证

### Task 7: 配置持有 `internal/app/config_holder.go`
- **ACTION**: `ConfigHolder` 原子持有当前 `*config.Config`
- **IMPLEMENT**:
  ```go
  type ConfigHolder struct { cur atomic.Pointer[config.Config]; path string; opts []config.Option; resolver secrets.Resolver; log *slog.Logger }
  func NewConfigHolder(path string, r secrets.Resolver, log *slog.Logger, opts ...config.Option) (*ConfigHolder, error) // 首次加载失败即返回错误（拒绝启动）
  func (h *ConfigHolder) Current() *config.Config
  func (h *ConfigHolder) Reload() error // 加载 + CheckRequired；失败时保留旧配置并以 error 级别记录，成功时记录新旧 ConfigHash
  ```
- **GOTCHA**: 调用方每次处理请求或投递时调用 `Current()`，**不得缓存** `*config.Config`（避免 PoC S2 中 Svix 的配置缓存不失效问题）
- **VALIDATE**: 测试：首次加载失败返回错误；Reload 成功切换；Reload 配置非法或缺少环境变量时保留旧配置；并发 `Current()` 与 `Reload()` 在 `-race` 下通过

### Task 8: 运行时与角色占位
- **ACTION**: `internal/app/runtime.go`、`internal/api/run.go`、`internal/worker/run.go`
- **IMPLEMENT**:
  ```go
  type Runtime struct {
      Settings Settings
      Config   *ConfigHolder
      Secrets  secrets.Resolver
      Pool     *pgxpool.Pool
      Log      *slog.Logger
      Now      func() time.Time
  }
  var ErrRoleNotImplemented = errors.New("role not implemented yet")
  // internal/api/run.go:    func Run(ctx context.Context, rt *app.Runtime) error { return app.ErrRoleNotImplemented }
  // internal/worker/run.go: func Run(ctx context.Context, rt *app.Runtime) error { return app.ErrRoleNotImplemented }
  ```
- **GOTCHA**: `Runtime` 只读；不在其中放可变状态
- **VALIDATE**: `go build ./...`

### Task 9: 可执行文件 `cmd/messagehub`
- **ACTION**: 子命令与 serve
- **IMPLEMENT**:
  - 只用标准库 `flag`，每个子命令一个 `flag.FlagSet`
  - `config check`：`Load` + `CheckRequired`；输出 warning；退出码 0 / 1（配置错误）/ 2（环境变量缺失）
  - `config env`：每行一个 `RequiredEnv()` 名称
  - `migrate up`：`pg.MigrateUp`
  - `keygen`：输出两行 `key: …`、`sha256: …`，并提示 key 只显示一次
  - `serve --role=api|worker|all`：构建 Runtime → 启动对应角色（`all` 时并发运行两者，任一返回错误即整体退出）；`signal.NotifyContext(SIGINT, SIGTERM)`；SIGHUP 调用 `ConfigHolder.Reload()`；收到退出信号后在 `HUB_SHUTDOWN_TIMEOUT` 内等待角色返回
  - 配置加载选项集中在 `serve.go` 的 `configOptions()` 函数里，Phase 4 只在这里加入模板校验器
  - 业务逻辑放在可测试的函数里（`run(args []string, lookup func(string) (string, bool), stdout, stderr io.Writer) int`），`main` 只调用它并 `os.Exit`
- **GOTCHA**: `keygen` 的输出不得写入日志；serve 启动日志只记录脱敏后的 Settings 与 ConfigHash
- **VALIDATE**: `main_test.go` 表驱动：未知子命令、缺参数、`config check` 对示例配置在缺环境变量时退出码 2、`config env` 输出与 `RequiredEnv()` 一致、`keygen` 输出格式

### Task 10: 工程与文档更新
- **ACTION**: Makefile、.gitignore、文档
- **IMPLEMENT**:
  - Makefile：`build` 改为 `go build -o bin/messagehub ./cmd/messagehub`；新增 `config-check`（`bin/messagehub config check --config $(CONFIG)`，默认示例配置）
  - `.gitignore` 加 `bin/`
  - `docs/deploy/k8s-secrets.md`：第 2 节补充 `HUB_DATABASE_URL`、`HUB_DATABASE_LISTEN_URL` 也放入 Secret；介绍 `messagehub config env`
  - `docs/testing/coverage-gaps.md`：B4（解析、缺失检查与热重载已实现）、B10（生成与哈希解析已实现）更新状态；`make cover-gaps` 新出现的条目写入第 2、3 节
- **VALIDATE**: `make build && bin/messagehub config env --config config/examples/hub.example.yaml` 输出 10 行

---

## Testing Strategy

### Unit Tests
| Test | Input | Expected Output | Edge Case? |
|---|---|---|---|
| Settings 默认值 | 只设置必填项 | 默认值生效 | 否 |
| Settings 非法值 | 并发 0、超时 `abc`、级别 `verbose` | 一次返回全部问题 | 是 |
| 连接串脱敏 | 带密码的 DSN | 输出不含密码 | 是 |
| EnvResolver | 已设置 / 未设置 / 空值 / `vault:` | 值 / ErrNotSet / ErrNotSet / ErrUnsupportedScheme | 是 |
| CheckRequired | 示例配置缺 3 个变量 | 错误中列出 3 个变量名，不含任何值 | 是 |
| apikey.Generate | 固定随机源 | 格式正确、哈希一致 | 否 |
| apikey.ParseHashes | 1 个 / 2 个 / 3 个 / 大写 / 空项 | 成功 / 成功 / 失败 / 失败 / 失败 | 是 |
| rules | 5 个算子的组合 | 符合 §7.2 与 §13.3 | 是 |
| ConfigHolder | 加载、重载成功、重载失败 | 失败保留旧配置 | 是 |
| CLI | 各子命令 | 退出码与输出 | 是 |

### Integration Tests（`-tags integration`）
| Test | Expected |
|---|---|
| MigrateUp 两次 | 第二次无变更、无错误 |
| NewPool + Ping | 成功 |

### Edge Cases Checklist
- [x] Empty input（空环境、空配置路径）
- [x] Invalid types（非法 duration、并发数）
- [x] Concurrent access（ConfigHolder 的 `-race` 测试）
- [x] Permission denied（N/A，底座不做鉴权）
- [x] Network failure（数据库不可达：NewPool / Ping 返回错误，serve 退出码非 0）

---

## Validation Commands

```bash
make lint              # EXPECT: 无输出
make test              # EXPECT: 全部通过
make test-integration  # EXPECT: 全部通过（Colima 需先设置环境变量）
make build && bin/messagehub config env --config config/examples/hub.example.yaml   # EXPECT: 10 行
make check-secrets     # EXPECT: 通过
make cover-gaps        # EXPECT: 新增未覆盖条目已写入 docs/testing/coverage-gaps.md
```

### Manual Validation
- [ ] 不设置任何环境变量执行 `bin/messagehub config check --config config/examples/hub.example.yaml`，退出码为 2，输出列出 10 个变量名
- [ ] `bin/messagehub keygen` 输出的 sha256 与 `printf '%s' <key> | shasum -a 256` 一致
- [ ] `serve --role=api` 在占位实现下立即以"role not implemented yet"退出，退出码非 0

---

## Acceptance Criteria
- [ ] 所有任务完成，验证命令通过
- [ ] 覆盖率：`internal/app`、`internal/secrets`、`internal/apikey`、`internal/rules` 各 ≥ 90%
- [ ] Phase 3 / Phase 4 只需替换 `internal/api/run.go`、`internal/worker/run.go` 即可接入

## Completion Checklist
- [ ] 遵循 coding-conventions.md
- [ ] 错误信息与日志中不出现密钥或连接串密码
- [ ] 默认值、上下限都是命名常量
- [ ] 未超出 NOT Building

## Risks
| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| goose 全局状态在并行测试中冲突 | M | L | `sync.Once` 初始化；集成测试共享一个容器 |
| `rules` 的数值比较与黄金用例不一致 | M | M | 用 success 黄金用例交叉验证 |
| 角色占位被误用于生产 | L | L | 返回明确错误并非 0 退出 |

## Notes
- 本计划是 PRD 新增的 Phase 2b。完成后 Phase 3、Phase 4 并行执行
- `internal/rules` 放在底座而不是 Phase 3 或 Phase 4，是因为路由匹配与成功判定共用同一套算子语义，必须只有一份实现
