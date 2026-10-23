# Implementation Report: 领域模型与契约（Message Hub Phase 2）

## Summary

Phase 2 的产出是一套可以被测试验证的契约：
- 行为规格：`docs/design/domain-model.md`，共 13 节，其中 §13 为执行中补充的约定
- ADR-0005 / 0006 / 0007
- Go 工程骨架（module `messagehub`）
- 领域类型与 Delivery 状态机（`internal/domain`）
- 配置 JSON Schema 与加载器（`schemas/`、`internal/config`）
- 示例配置，覆盖原始需求的三个场景
- OpenAPI 3.0.3 契约及受理请求 fixture（`api/`）
- PostgreSQL 迁移（`db/`）
- 模板、成功判定、重试三组黄金用例及其加载器（`testdata/contracts/`、`internal/contracts`）
- Go 编码约定

Phase 3 与 Phase 4 现在可以基于这些契约并行开发。

## Assessment vs Reality

| Metric | Predicted (Plan) | Actual |
|---|---|---|
| Complexity | Large | Large |
| Confidence | 7/10 | 一次完成；没有任务返工，执行中补充了 §13 约定 |
| Files Changed | 约 40 | 约 100：新建 96（其中约 45 个为测试 fixture）、更新 4 |
| 黄金用例数 | 模板 ≥ 20、成功 ≥ 12、重试 ≥ 8 | 模板 56、成功 32、重试 15 |

## Tasks Completed

| # | Task | Status | Notes |
|---|---|---|---|
| 1 | Go 工程与工具链 | Complete | 依赖版本与计划一致；执行期间用临时 `tools_deps.go` 固定依赖，结束时删除 |
| 2 | ADR-0005 / 0006 / 0007 | Complete | README 索引共 7 条 |
| 3 | 领域规格 | Complete | 执行后新增 §13 补充约定（见 Deviations） |
| 4 | 配置模型、Schema、加载器 | Complete | 子 agent 完成；覆盖率 94.7%；无效用例 25 个 |
| 5 | 领域类型与状态机 | Complete | 48 个组合全表测试（合法 11 个，非法 37 个）；覆盖率 99.1% |
| 6 | OpenAPI 契约 | Complete | 子 agent 完成；fixture 有效 6 个、无效 16 个；OpenAPI 与 Go 校验结论一致 |
| 7 | 数据库迁移 | Complete | 子 agent 完成；集成测试 14 个子用例全部通过 |
| 8 | 黄金用例与加载器 | Complete | 子 agent 完成；哈希、HMAC、退避结果都在测试中复算；覆盖率 96.4% |
| 9 | 编码约定 | Complete | 增加了 Colima 环境下运行集成测试的说明 |
| 10 | 更新 PRD | Complete | 勾选 2 个 Open Questions，新增 1 个；Decisions Log 新增 4 行 |

## Validation Results

| Level | Status | Notes |
|---|---|---|
| Static Analysis | Pass | `make lint`（gofmt + go vet）无输出 |
| Unit Tests | Pass | `make test`（`-race`），5 个包全部通过 |
| Coverage | Pass | domain 99.1%、config 94.7%、contracts 96.4%，internal 总计 96.1% |
| Build | Pass | `go build ./...` |
| Integration | Pass | `make test-integration`：迁移 up → 约束 → down → up 全部通过（需要 Colima 环境变量，见 Issues） |
| 一致性核对 | Pass | 状态、outcome、trigger、routing_status 等枚举在 domain、SQL、OpenAPI、config schema 中逐一比对一致 |

## Files Changed

| 区域 | 文件 |
|---|---|
| 工程 | `go.mod`、`go.sum`、`Makefile`、`.gitignore`（更新） |
| 文档 | `docs/design/domain-model.md`、`docs/design/coding-conventions.md`、`docs/adr/0005`–`0007`、`docs/adr/README.md`（更新） |
| 领域 | `internal/domain/{errors,event,ids,delivery,delivery_state,attempt}.go` 及 3 个测试文件 |
| 配置 | `schemas/{config.schema.json,embed.go}`；`internal/config/` 下 8 个源文件、5 个测试文件、28 个 fixture；`config/examples/hub.example.yaml` |
| API | `api/openapi.yaml`、`api/openapi_test.go`、22 个事件 fixture |
| 数据库 | `db/migrations/00001_init.sql`、`db/embed.go`、`db/embed_test.go`、`db/migrations_integration_test.go` |
| 黄金用例 | `testdata/contracts/{template,success,retry}/cases.yaml`；`internal/contracts/` 下 4 个源文件、2 个测试文件 |
| PRD | `.claude/PRPs/prds/message-hub.prd.md`（更新） |

## Deviations from Plan

1. **校验函数名称**：计划中写的是 `domain.ValidateEnvelope(e)`，实现为 `domain.ParseEnvelope(raw []byte)`。要检查未知字段和字段类型，就必须从原始 JSON 开始校验，所以把解码和校验合并为一个函数。超过 256 KiB 时单独返回 `ErrPayloadTooLarge`，便于 HTTP 层映射为 413
2. **没有 `PrincipalByKeyHash` 查找表**：配置中只有 `env:` 引用，没有哈希值。运行时读取环境变量后再构建查找表（Phase 3），这一点已写入 §13.5
3. **规格新增 §13 补充约定**：子 agent 编写契约时发现 20 多处规格没有覆盖的细节，统一写回规格，保持规格是唯一权威。例如：
   - URL 中 `?` 之后的值按查询参数转义
   - `format_time unix` 在 Body 中作为单表达式时输出数字
   - 比较时类型不同即不相等
   - 时间范围的开闭区间
   - OperatorAction 不级联删除
   - 重复的 API Key 引用会导致加载失败

   为其中两条新增了黄金用例
4. **数据库约束比计划更严**：可选字段不允许空字符串（缺省一律为 NULL）；metadata 和 payload 必须是 JSON 对象；状态码限定在 100–599
5. **fixture 数量多于计划下限**（见 Assessment）

## Issues Encountered

- **Colima 环境下运行集成测试**：本机 Docker 由 Colima 提供，`/var/run/docker.sock` 指向一个不存在的 Docker Desktop socket，testcontainers 默认找不到 Docker。需要设置以下环境变量，已写入 `docs/design/coding-conventions.md` §10，没有修改用户的任何全局配置：
  ```bash
  DOCKER_HOST=unix://$HOME/.colima/default/docker.sock
  TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock
  ```
- **并行子 agent 与 go.mod**：为避免 4 个子 agent 同时修改 go.mod，先用临时 `tools_deps.go` 固定全部依赖并执行 `go mod tidy`，并要求子 agent 不得修改 go.mod。全部完成后删除该文件并重新 tidy，依赖版本没有变化
- **当前已知、留到 Phase 3 处理**：
  - 受理请求中的重复 JSON 键目前以最后出现的值为准，Phase 3 在 HTTP 层拒绝
  - Go 会拒绝 `2026-02-31` 这类不存在的日期，OpenAPI 的格式正则会接受。属于服务端规则，已在 §13.1 说明

## Tests Written

| Test File | 覆盖内容 |
|---|---|
| `internal/domain/delivery_state_test.go` | 状态机 48 个组合、IsTerminal、IsOrderingSettled、HasOrderingKey |
| `internal/domain/event_test.go` | 信封的有效 / 无效用例（共 28 个）、错误聚合、非对象请求体、payload 恰好到上限与超出 1 字节、RoutingStatusFor |
| `internal/domain/ids_test.go` | UUIDv7 有序性、ValidationError 格式 |
| `internal/config/*_test.go` | 98 个用例：示例配置、28 个 fixture、聚合错误、warning、模板校验钩子、ConfigHash 稳定性 |
| `api/openapi_test.go` | 文档与示例校验、fixture 校验、OpenAPI 与 Go 一致性、仅服务端校验的规则、请求 schema 不接受额外字段 |
| `db/embed_test.go`、`db/migrations_integration_test.go` | 迁移内嵌；表、约束、分区、up / down / up |
| `internal/contracts/*_test.go` | 黄金用例结构、覆盖面、哈希与 HMAC 复算、退避公式复算、约 45 个加载器错误路径 |

## Post-implementation: 覆盖缺口补齐（2026-10-24）

- 补齐 6 处缺口（详见 `docs/testing/coverage-gaps.md` 第 1 节），总覆盖率从 96.1% 提升到 97.2%，`internal/config` 从 94.7% 提升到 96.6%
- 剩余 22 个未覆盖代码块都是防御性分支，已按 D1–D7 分类记录理由与重新评估条件
- 覆盖率之外的行为缺口（B1–B8）已标注负责阶段与完成标准
- 新增 `make cover-gaps`（`scripts/cover-gaps.sh`），可随时重新生成未覆盖列表并与文档对照

## Next Steps

- [ ] 代码评审：`/code-review`
- [ ] Phase 3（受理与查询）与 Phase 4（投递引擎）可以并行规划：`/ecc:prp-plan .claude/PRPs/prds/message-hub.prd.md`
- [ ] Phase 4 实现模板引擎时，在 `config.Load` 中接入 `WithTemplateValidator`，并用黄金用例验收
