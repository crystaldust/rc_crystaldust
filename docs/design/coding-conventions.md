# Go 编码约定

> 适用于 Message Hub 全部 Go 代码（ADR-0002）。新代码应与已有代码风格一致，无法区分出自谁手。
> 范例代码：`internal/domain/`（命名、错误、不可变、测试）。

## 1. 包结构与依赖方向

```
internal/domain     核心类型与纯规则，不依赖任何内部包
internal/config     配置加载与校验，只依赖 domain 与 schemas
internal/contracts  黄金用例加载器，只依赖标准库与 YAML
schemas/            内嵌 JSON Schema
api/                OpenAPI 契约
db/                 内嵌 SQL 迁移
（Phase 3+）服务层  依赖 domain 与 config；domain 永远不反向依赖服务层
```

- 包名为小写单词，按领域划分；禁止 `utils`、`common`、`helpers` 这类包名
- 每个包在 `doc` 注释（首个文件的 `// Package xxx ...`）中写明职责与依赖边界

## 2. 命名

- 导出类型、函数使用 PascalCase；枚举使用**类型化常量**（`type DeliveryStatus string` + `const StatusPending DeliveryStatus = "PENDING"`）
- 枚举的字符串值即数据库与 API 中的值，修改它等于修改契约
- 布尔方法读起来像判断：`IsTerminal()`、`HasOrderingKey()`
- 文件名小写加下划线：`delivery_state.go`、`delivery_state_test.go`
- 限制值、默认值一律定义为命名常量（如 `MaxPayloadBytes`），禁止魔法数字

## 3. 错误处理

- 包级哨兵错误：`var ErrInvalidTransition = errors.New(...)`；调用方用 `errors.Is` 判断
- 包装底层错误时带上下文：`fmt.Errorf("load config %q: %w", path, err)`
- **校验错误聚合返回**：使用 `*domain.ValidationError{Problems []FieldError}`，一次列出全部问题；字段路径形如 `destinations[2].request.url`
- 不吞错误；不得以 `_ =` 丢弃可能失败的调用结果，除非加注释说明原因
- 面向 API 的错误映射到 `docs/design/domain-model.md` §12 的错误码；内部细节只进日志，不进响应

## 4. 不可变

- 纯函数优先：例如 `Transition(from, trigger) (to, error)` 不读也不改任何 Delivery
- 需要"修改"时返回新值，不就地修改入参
- 配置加载后只读；查找表在构建时一次生成，对外只提供查找方法或返回副本
- 包级 `var` 只允许：哨兵错误、编译后的正则、只读查找表

## 5. 函数与文件规模

- 函数 < 50 行；嵌套不超过 4 层，优先提前返回
- 文件 200–400 行为宜，800 行为上限（测试与生成文件除外）

## 6. context 与 I/O

- 所有执行 I/O（数据库、HTTP、文件之外的外部调用）的函数，第一个参数为 `ctx context.Context`
- 不在结构体中保存 context
- 外部调用必须设置超时（来自 Policy 或明确的命名常量）

## 7. 日志

- 使用标准库 `log/slog`，JSON handler
- **宽事件日志**：每次受理输出一条 `event.received`（无匹配路由时为 `event.unrouted`，级别 warn），每次 attempt 输出一条 `delivery.attempted`，字段齐全（event_id、delivery_id、destination_id、outcome、status_code、duration_ms、attempt、config_hash）
- **禁止进入日志**：API Key、secret 值、`Authorization` 头、payload 原文、响应体原文

## 7a. 密钥（ADR-0008）

- 配置、代码、测试数据、文档中**不得出现真实密钥**；测试中使用明显的假值（如 `abc123`、`spike-key`）
- 配置中的凭据只写 `env:NAME` 引用；模板中的凭据只能来自 `{{ secret.NAME }}`
- 判断"某个名称是否携带凭据"时，一律调用 `domain.IsSensitiveName`，不得另写一套规则
- 不提交 `.env` 文件；本地密钥只放在自己的 shell 环境中
- 提交前运行 `make check-secrets`

## 8. 测试

- 结构：Arrange-Act-Assert，表驱动，`t.Run` 子测试，用例名描述被测行为
- 状态类规则做**全组合**测试（参照 `delivery_state_test.go`），不只测合法路径
- 集成测试使用 build tag `//go:build integration`，通过 testcontainers 启动依赖
- 黄金用例（`testdata/contracts/`）是行为契约；修改黄金值必须同时修改规格文档
- 覆盖率 ≥ 80%（`internal/domain` ≥ 90%）
- 未覆盖的代码必须记录在 `docs/testing/coverage-gaps.md` 中（分类与理由）；`make cover-gaps` 的输出发生变化时，在同一次提交中更新该文件
- 测试中不得依赖真实外部服务或当前时间（时间通过参数或时钟接口注入）

## 9. 依赖

- 新增依赖前先评估标准库能否满足
- 只使用**发布至少两周**的版本，并在 PR 描述中写明版本与发布日期
- YAML 使用 `go.yaml.in/yaml/v3`（`gopkg.in/yaml.v3` 已归档）

## 10. 质量门禁

提交前必须通过：

```bash
make lint            # gofmt（只检查 git 跟踪的文件）+ go vet
make test            # go test -race ./...
make check-secrets   # 禁止跟踪 .env 文件；安装了 gitleaks 时同时扫描仓库
```

涉及数据库 schema 时还需：

```bash
make test-integration
```

集成测试通过 testcontainers 使用本机 Docker。Docker 由 Colima 提供时（`/var/run/docker.sock` 不可用），需要先设置：

```bash
export DOCKER_HOST=unix://$HOME/.colima/default/docker.sock
export TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock
```

也可以写入 `~/.testcontainers.properties`（`docker.host=...`）。
