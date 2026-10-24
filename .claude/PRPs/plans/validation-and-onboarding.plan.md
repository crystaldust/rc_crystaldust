# Plan: 验证与首批接入（Message Hub Phase 7）

## Summary
用证据验证核心假设：压测确定容量基线并校准 Phase 4–6 留下的初始参数；故障注入证明"零静默丢失"与 P99 < 1s；补齐上线所需的工程设施（CI、Kubernetes 清单、接入与部署手册）；最后按接入清单接入首个真实业务。除"首个真实业务接入"需要业务方提供供应商信息外，其余任务都可在本仓库内完成。

## User Story
As a Message Hub 的技术负责人,
I want 知道系统在什么负载下仍满足 SLO、在各种故障下不丢事件，并有可重复的部署与接入流程,
So that 可以放心地接入第一个真实业务，并知道何时需要扩容或迁移到 MQ（ADR-0009）。

## Problem → Solution
所有阈值（fillfactor、autovacuum、批大小、轮询间隔、lease 余量、告警阈值、保留期）都是"初始值，Phase 7 校准"；没有压测与故障注入证据；没有 CI 与部署清单 → 压测报告 + 故障注入测试 + 校准后的参数 + CI + 部署清单 + 接入手册 + 首个业务上线。

## Metadata
- **Complexity**: Large
- **Source PRD**: `.claude/PRPs/prds/message-hub.prd.md`
- **PRD Phase**: Phase 7 — 验证与首批接入
- **Depends on**: Phase 5（指标、对账用于判定结果）、Phase 6（限流 / 熔断参与压测与故障场景）
- **Estimated Files**: 约 30 个（以工具、测试、清单、文档为主）

### 开始前需要用户提供（PRD Open Questions）
| 输入 | 用于 | 缺失时的做法 |
|---|---|---|
| 预期业务规模：峰值 QPS、日事件量、目标数、平均扇出 | Task 3 压测档位 | 用默认档位（见 Task 3），报告中标注"假设值" |
| 首个接入业务与供应商：接口文档、认证方式、限流规则、幂等支持 | Task 9 | Task 9 停在"接入清单"，等用户提供后再执行 |
| 部署环境：Kubernetes 版本、Ingress、出口方式（是否需要固定出口 IP） | Task 7 | 清单按通用 Kubernetes 编写，在 kind 中验证 |

---

## UX Design

### After（负责人与业务方视角）
```
docs/testing/capacity-baseline.md   在 N 个 worker、M 个目标下：受理 / 投递吞吐、P99、PG 负载
docs/testing/fault-injection.md     每个故障场景：注入方式、观察结果、对账结论（0 丢失）
docs/deploy/onboarding.md           新目标接入 = 写配置 + 配 Secret + config check + 灰度验证（< 1 天）
CI                                  每个 PR：lint、单元、集成、gitleaks、cover-gaps 一致性
```

### Interaction Changes
| Touchpoint | Before | After | Notes |
|---|---|---|---|
| 参数 | 初始值 | 压测后的校准值，附依据 | 改动集中在常量与迁移 |
| 部署 | 无清单 | `deploy/k8s/`（api、worker、迁移 Job、Secret 模板） | 关闭 B12 |
| 质量门禁 | 本机手动 | CI 自动 | 关闭 B6、B11 |

---

## Mandatory Reading

| Priority | File | Lines / Section | Why |
|---|---|---|---|
| P0 | `.claude/PRPs/prds/message-hub.prd.md` | Success Metrics、Open Questions | 验收目标与缺失输入 |
| P0 | `docs/adr/0009-pg-to-mq-migration-path.md` | 触发迁移的条件 | 压测报告要给出这些指标的实测值 |
| P0 | `docs/adr/0010-max-in-flight-soft-limit.md`、ADR-0011（Phase 6） | Revisit When | 压测后决定是否改为严格上限、限流开销是否可接受 |
| P0 | `docs/testing/coverage-gaps.md` | D11、B6、B11、B12、B16、B20 | 故障注入可覆盖 D11 的一部分 |
| P0 | `docs/design/observability.md`、`docs/deploy/runbook.md`（Phase 5） | 指标目录、告警 | 判定压测与故障结果 |
| P1 | `spike/mock-vendor/`、`spike/scenarios/` | all | PoC 场景与模拟供应商接口约定 |
| P1 | `docs/deploy/k8s-secrets.md` | all | Secret 注入规范（ADR-0008 D3） |
| P1 | `Makefile`、`scripts/cover-gaps.sh` | all | CI 直接调用现有目标 |
| P1 | `db/migrations/00002_deliveries_tuning.sql` | 头部注释 | 待校准参数 |
| P2 | `internal/worker/run.go`（`claimBatchSize`）、`internal/dispatch/{pg,wakeup,targets}.go`（`orphanCheckInterval`、`PollInterval`、`LeaseMargin`） | 常量 | 待校准参数 |

## External Documentation

| Topic | Source | Key Takeaway |
|---|---|---|
| testcontainers toxiproxy 模块 | `github.com/testcontainers/testcontainers-go/modules/toxiproxy`（版本与现有 v0.44.0 对齐） | 在 Hub 与 PostgreSQL / 供应商之间注入延迟、断连 |
| kind | https://kind.sigs.k8s.io/ | 本地 Kubernetes 验证清单 |
| GitHub Actions | GitHub 文档 | testcontainers 可直接在 ubuntu runner 上使用 Docker |
| gitleaks | https://github.com/gitleaks/gitleaks | CI 中固定版本运行（ADR-0008 D1 第二道防线） |

新增依赖同样遵循"发布至少两周"，在报告中写明版本与日期。

```
KEY_INSIGHT: "零静默丢失"的判定 = 每个已受理事件都有可查询的终态或进行中状态，由 Phase 5 的对账任务与一条独立的校验 SQL 共同给出
APPLIES_TO: Task 4、5
GOTCHA: 故障期间对账可能短暂报告 stale_due（这是延迟，不是丢失）；结论以故障恢复后、全部 delivery 结清时的状态为准

KEY_INSIGHT: 压测分别测"受理"与"投递"两条链路，并与 ADR-0009 的迁移阈值对照
APPLIES_TO: Task 3
GOTCHA: 本机 Colima 上的数字只能作相对比较；报告写明硬件、PostgreSQL 配置与数据量，正式基线在目标环境复测
```

---

## Patterns to Mirror

### TOOL_LAYOUT
新增 `cmd/loadgen`、`cmd/mockvendor`（只用于测试与压测，不进生产镜像）。沿用 `cmd/messagehub/main.go` 的结构：`run(args, lookup, stdout, stderr) int`，参数用 `flag`，可单元测试。

### MOCK_VENDOR_CONTRACT
// SOURCE: spike/mock-vendor/README.md
`POST /_control/<vendor>` 设置 `{status, delay_ms, body, fail_first_n}`；`GET /_requests/<vendor>`；`DELETE /_requests`。Go 版保持同一接口，另加按比例出错（`error_rate`）、Retry-After 与到达时间统计。

### INTEGRATION_TEST
// SOURCE: internal/worker/e2e_integration_test.go（真实 `worker.Run`、`pgtest`、httptest 供应商、`waitStatus`）；Phase 5 的 `cmd/messagehub/serve_integration_test.go`（子进程启动 `serve`）

### DOC_STYLE
// SOURCE: docs/spike/04-poc-results.md（场景表 + 结论 + 证据）、docs/testing/coverage-gaps.md

---

## Files to Change

| File | Action | Justification |
|---|---|---|
| `cmd/mockvendor/main.go` + test | CREATE | 高吞吐的 Go 模拟供应商 |
| `cmd/loadgen/main.go` + test | CREATE | 按目标速率提交事件、统计受理延迟 |
| `scripts/loadtest.sh`、`deploy/loadtest/docker-compose.yaml` | CREATE | 一键压测环境（PG、多个 Hub 实例、mockvendor、Phase 5 可观测性栈） |
| `scripts/verify-no-loss.sql` | CREATE | 零静默丢失校验 |
| `internal/chaos/*_test.go`（`//go:build chaos`） | CREATE | 故障注入测试（toxiproxy） |
| `Makefile` | UPDATE（底座，单独提交） | `test-chaos`、`loadtest` 目标 |
| `.github/workflows/ci.yaml`、`scripts/check-cover-gaps.sh` | CREATE | CI 与 coverage-gaps 一致性检查（B6、B11） |
| `deploy/k8s/{namespace,api,worker,migrate-job,secret.example,configmap,pdb,kustomization}.yaml` | CREATE | 部署清单（B12） |
| 被校准的常量与新迁移（编号接在 Phase 6 之后） | UPDATE / CREATE | Task 6 |
| `docs/testing/{capacity-baseline,fault-injection}.md` | CREATE | 证据 |
| `docs/deploy/{onboarding,deployment}.md` | CREATE | 接入与部署手册 |
| `docs/testing/coverage-gaps.md`、ADR-0009 / 0010 / 0011、PRD | UPDATE | 结论回写 |

## NOT Building

- 迁移到 MQ（ADR-0009；本阶段只给出是否触发的数据）
- 多区域 / 多活部署
- 生产监控平台的选型与接入（Phase 5 已提供 OTLP 与 Prometheus）
- 首个业务以外的批量接入
- 管理后台 UI

---

## Step-by-Step Tasks

### Task 1: Go 模拟供应商 `cmd/mockvendor`
- **ACTION**: 与 `spike/mock-vendor` 接口兼容，性能足以支撑压测
- **IMPLEMENT**: 每个 vendor 的行为：status、delay_ms（固定或区间）、error_rate、fail_first_n、Retry-After；记录到达时间与请求头摘要（不存 body）；`/_stats/<vendor>` 返回计数与到达速率
- **MIRROR**: TOOL_LAYOUT、MOCK_VENDOR_CONTRACT
- **VALIDATE**: 单元测试；与 Python 版跑同一组 curl 示例结果一致

### Task 2: 负载生成 `cmd/loadgen`
- **ACTION**: 以固定或阶梯速率调用 `POST /v1/events`
- **IMPLEMENT**: 参数：速率、时长、并发、event_type、payload 大小、ordering_key 比例、重复提交比例；输出受理延迟分位数与错误码分布；把 202 / 200 返回的 `event_id` 写入文件，供 Task 5 校验
- **GOTCHA**: 开环发压（按时间表发送，不等响应），避免协调遗漏导致低估延迟
- **VALIDATE**: 单元测试（速率控制误差 < 5%、统计正确）

### Task 3: 压测与容量基线
- **ACTION**: `scripts/loadtest.sh` 跑三档，结果写入 `docs/testing/capacity-baseline.md`
- **IMPLEMENT**:
  - 默认档位（无用户输入时）：50 / 200 / 1000 events/s，扇出 2，5 个目标，持续 15 分钟；worker 1 / 2 / 4 个实例
  - 记录：受理 P50 / P99、首次投递延迟 P99（Phase 5 指标）、投递吞吐、领取查询 P99、PG CPU / 死元组 / 表膨胀、`max_in_flight` 实际超出量（ADR-0010）、限流准确度与目标状态行锁等待（Phase 6）
  - 与 ADR-0009 迁移阈值对照，给出"在本环境下何时需要迁移"的结论
- **GOTCHA**: 报告写明环境与数据量；本机数字只作相对比较
- **VALIDATE**: 报告中每个数字都能由脚本复现（附命令与原始数据路径）

### Task 4: 故障注入 `internal/chaos`
- **ACTION**: `//go:build chaos` 测试，toxiproxy 置于 Hub ↔ PG、Hub ↔ 供应商之间
- **IMPLEMENT**（每个场景：持续负载中注入 → 恢复 → 等全部结清 → 断言）：
  | 场景 | 注入 | 断言 |
  |---|---|---|
  | 供应商宕机 | 断开供应商 | 熔断生效、无多余 DEAD；恢复后全部 SUCCEEDED |
  | 供应商慢 | 延迟 5s | 其他目标首次投递延迟不受影响（§8） |
  | worker 崩溃 | `kill -9` 子进程 | lease 过期后接管，0 丢失；接管 attempt 计数符合 §5.3 |
  | PG 断连 / 重启 | 断开 PG 30s；容器重启 | 受理返回 5xx（不返回 202）；恢复后投递继续；LISTEN 重连 |
  | PG 慢 | 延迟 500ms | 无重复投递；Complete 重试生效 |
  | LISTEN 连接丢失 | 只断 listen DSN 的代理 | 轮询兜底，延迟 ≤ 轮询间隔 + 处理时间 |
  - 统一断言：每个得到 202 的事件都在库中，其每个 delivery 处于终态（或等待熔断恢复的待投递状态）；Phase 5 对账 `routed_without_deliveries = 0`、`missing_payload = 0`
- **GOTCHA**: 故障注入测试慢，不进 `make test-integration`，单独 `make test-chaos`；CI 中手动触发或每日运行
- **VALIDATE**: `make test-chaos` 通过；结果写入 `docs/testing/fault-injection.md`；coverage-gaps 中被覆盖的 D11 条目改为指向测试

### Task 5: 零静默丢失校验
- **ACTION**: `scripts/verify-no-loss.sql`（或 `loadgen verify` 子命令）：给定 loadgen 输出的 id 文件，校验每个已受理事件的存在性与 delivery 状态
- **VALIDATE**: 压测与故障注入结束后都运行，报告中附结果

### Task 6: 参数校准
- **ACTION**: 依据 Task 3、4 的数据调整，并在代码注释、迁移、ADR 中写明依据
- **IMPLEMENT**: fillfactor / autovacuum（新迁移）、`claimBatchSize`、`PollInterval`、`LeaseMargin`、`orphanCheckInterval`、Phase 5 告警阈值与保留期默认值、Phase 6 限流 / 熔断推荐值
- **GOTCHA**: 每项只在有数据支撑时修改；证据不足的保持原值并在报告中说明
- **VALIDATE**: 校准后重跑一档压测，确认没有回退

### Task 7: Kubernetes 清单（B12）
- **ACTION**: `deploy/k8s/`：api Deployment（readiness `/readyz`、liveness `/healthz`）、worker Deployment、迁移 Job（`messagehub migrate up`）、Secret 模板（只有键名）、ConfigMap（hub.yaml）、PodDisruptionBudget、`/metrics` 抓取注解；kustomization
- **GOTCHA**: worker 的 `terminationGracePeriodSeconds` 大于 `HUB_SHUTDOWN_TIMEOUT`；`HUB_DATABASE_LISTEN_URL` 指向直连 PG（不经 transaction 模式 pgbouncer，ADR-0009 P4）；镜像不含 `cmd/loadgen`、`cmd/mockvendor`
- **VALIDATE**: kind 中部署：Secret 缺失时 Pod 起不来；滚动更新期间无事件丢失（loadgen 持续发压 + Task 5 校验）

### Task 8: CI（B6、B11）
- **ACTION**: `.github/workflows/ci.yaml`：`make lint`、`make test`、`make test-integration`、gitleaks（固定版本）、`scripts/check-cover-gaps.sh`（`make cover-gaps-all` 输出与 coverage-gaps.md 第 2 节逐条比对）、`make build`
- **GOTCHA**: Actions 固定到提交 SHA；安装 gitleaks 后 `make check-secrets` 不再跳过
- **VALIDATE**: 在分支上触发一次并全部通过；故意改坏 coverage-gaps 时检查失败

### Task 9: 首个真实业务接入（需要用户输入）
- **ACTION**: 先写 `docs/deploy/onboarding.md`（接入清单），再按清单接入用户指定的业务
- **IMPLEMENT**（清单）：供应商接口与认证 → 写 destination / policy / route（模板、成功判定、重试、限流、熔断、脱敏）→ 配 Secret → `messagehub config check` → 预发环境对 mock 与供应商沙箱验证 → 灰度（`sources` 限定）→ 观察看板与告警 → 全量；记录接入耗时（PRD 指标：< 1 天、只改配置）
- **GOTCHA**: 供应商格式无法用现有模板表达时，按 ADR-0006 评估扩展模板能力，不引入脚本
- **VALIDATE**: 接入过程无代码改动；接入记录写入 onboarding.md

### Task 10: 结论回写
- **ACTION**: 更新 ADR-0009（实测值、是否触发迁移）、ADR-0010 / 0011（是否需要严格上限、限流开销）、coverage-gaps（B6、B11、B12 关闭；D11 中被覆盖的条目）、PRD（Success Metrics 实测值、Phase 7 complete、已获答复的 Open Questions）
- **VALIDATE**: 文档之间无矛盾；PRD 每个 Success Metric 都有实测值或说明

---

## Testing Strategy

### Unit Tests
| Test | Input | Expected Output | Edge Case? |
|---|---|---|---|
| mockvendor 行为 | error_rate、fail_first_n、delay 区间 | 统计与配置一致 | 是 |
| loadgen 速率 | 目标 rps、时长 | 误差 < 5% | 是 |

### Fault Injection
见 Task 4 场景表。

### Edge Cases Checklist
- [ ] 空输入：零负载下告警不误报
- [ ] 最大输入：最大档位与 256 KiB payload
- [ ] 并发：多 worker、多 api 实例
- [ ] 网络故障：PG 与供应商的断连、延迟
- [ ] 进程崩溃：kill -9
- [ ] 权限：Secret 缺失时拒绝启动

---

## Validation Commands

```bash
make lint && make test && make test-integration
make test-chaos          # EXPECT: 全部场景通过，0 静默丢失
scripts/loadtest.sh      # EXPECT: 生成 capacity-baseline 原始数据
kind create cluster && kubectl apply -k deploy/k8s   # EXPECT: api / worker Ready
```

### Manual Validation
- [ ] 压测期间 Grafana 看板与告警符合预期
- [ ] kind 中滚动更新无丢失
- [ ] 首个业务灰度期间无告警、无 DEAD（或 DEAD 均为对端永久错误并已处理）

---

## Acceptance Criteria
- [ ] 满足 Success Metrics 的延迟与零静默丢失目标（PRD success signal），或明确记录未满足的条件
- [ ] 新目标接入只改配置（首个业务验证）
- [ ] B6、B11、B12 关闭；参数校准有数据依据
- [ ] ADR-0009 / 0010 / 0011 的 Revisit 结论已回写

## Completion Checklist
- [ ] 压测与故障注入可复现（脚本 + 命令 + 环境说明）
- [ ] 工具代码不进生产镜像
- [ ] 清单中无密钥值
- [ ] 底座改动（Makefile）单独提交

## Risks
| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| 本机环境不能代表生产 | H | M | 报告写明环境；正式基线在目标环境复测 |
| 故障注入测试不稳定 | M | M | 只断言最终状态；等待条件而非固定 sleep |
| 缺少业务规模与供应商信息 | H | M | 默认档位 + 接入清单先行；Task 9 等用户输入 |
| 压测暴露需要迁移 MQ | L | H | ADR-0009 已有分步路径；本阶段只给结论 |

## Notes
- Task 1、2、7、8 不依赖 Phase 5 / 6，可以提前开始
- 首个业务建议从示例配置的三个场景中选（广告回传、CRM、库存），分别覆盖 HMAC 签名、OAuth2 与有序投递
