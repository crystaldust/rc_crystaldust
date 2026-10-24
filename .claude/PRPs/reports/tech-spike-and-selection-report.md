# Implementation Report: 技术验证与选型（Message Hub Phase 1）

## Summary

完成了 Phase 1 技术验证：
- 锁定评估清单与决策规则，在打分前单独提交
- 对 Hookdeck Outpost v1.7.0 与 Svix server-v1.102.1 做了源码级评估（两个子 agent 并行执行，主线程独立抽查证据）
- 编写了模拟供应商与 PoC 执行步骤
- 完成自建技术栈的对比评估
- 产出 ADR 0001–0004

结论：**自建，使用 Go + PostgreSQL**（MVP 只有两个组件），系统借鉴两个候选方案的 17 项设计。ADR 状态为 `Proposed`，待评审。

## Assessment vs Reality

| Metric | Predicted (Plan) | Actual |
|---|---|---|
| Complexity | Medium | Medium |
| Confidence | 7/10 | 一次完成；PoC 因环境原因跳过，不影响决策 |
| Files Changed | 约 16 | 19（新建 17、更新 2，另有 1 个归档移动） |
| Timebox | 2–3 个工作日 | 1 个会话 |

## Tasks Completed

| # | Task | Status | Notes |
|---|---|---|---|
| 1 | 初始化验证工作区 | Complete | 偏差：`.gitignore` 额外加入 `.DS_Store` 和 `__pycache__/` |
| 2 | 评估清单与决策规则 | Complete | 单独提交（`f4bc44f`），早于两份评估文档 |
| 3 | Outpost 源码级评估 | Complete | 子 agent 完成；82/144，不满足条件 1 和条件 3 |
| 4 | Svix 源码级评估 | Complete | 子 agent 完成；88/144，不满足条件 1 和条件 3 |
| 5 | 模拟供应商与 PoC 场景 | Complete | mock 自检 9/9；两个候选的 S1–S8 均已补跑，与源码一致；新发现 Svix 修改 Header 后需约 30s 才生效 |
| 6 | 自建技术栈评估 | Complete | Go 76/85；仅 PG 64/75；PostgreSQL 58/60 |
| 7 | ADR 与 PRD 更新 | Complete | ADR 为 Proposed；PRD Phase 1 保持 in-progress，待评审 |

## Validation Results

| Level | Status | Notes |
|---|---|---|
| Static Analysis | Pass | `py_compile`、`bash -n` |
| Unit Tests | Pass | `self_check.sh` 9 项全部 PASS（plan 中列了 6 项，另补 3 项边界用例） |
| Build | N/A | 本阶段不产出可构建的服务代码 |
| Integration（PoC） | Pass（补跑） | 起初因 Docker 网络问题跳过；配置代理后两个候选的 S1–S8 全部执行，结果与源码评估一致，分数无调整（`docs/spike/04-poc-results.md`） |
| Edge Cases | Pass | 空 Body、1MB Body、非法控制参数、并发隔离均已覆盖；"连接拒绝"属于 PoC 范围，未执行 |
| 文档完整性 | Pass | 无 MISSING；每个 ADR 都有 Decision 段；无 TBD |
| 证据抽查 | Pass | 4 条源码引用经主线程独立核对（见 `docs/spike/04-poc-results.md`） |

## Files Changed

| File | Action |
|---|---|
| `.gitignore` | CREATED |
| `docs/spike/README.md` | CREATED |
| `docs/spike/01-evaluation-checklist.md` | CREATED |
| `docs/spike/02-outpost-evaluation.md` | CREATED（子 agent） |
| `docs/spike/03-svix-evaluation.md` | CREATED（子 agent） |
| `docs/spike/04-poc-results.md` | CREATED |
| `docs/spike/05-stack-evaluation.md` | CREATED |
| `docs/adr/README.md`、`0001`–`0004` | CREATED（5 个文件） |
| `spike/mock-vendor/mock_vendor.py`、`self_check.sh`、`README.md` | CREATED |
| `spike/scenarios/run_scenarios.md`、`outpost/compose.yml` | CREATED |
| `.claude/PRPs/reports/tech-spike-and-selection-report.md` | CREATED（本文件） |
| `.claude/PRPs/prds/message-hub.prd.md` | UPDATED（Phase 1 状态、Decisions Log 新增 3 行并更新 1 行、Open Questions 勾选 2 项） |
| `.claude/PRPs/plans/tech-spike-and-selection.plan.md` | MOVED → `plans/completed/` |

## Deviations from Plan

1. **PoC 先跳过、后补跑**：Docker daemon 起初无法访问 Docker Hub；配置代理后补跑了全部场景。为便于复跑，场景逻辑改为 `spike/scenarios/scenarios.py` 脚本（plan 中只要求 Markdown 步骤）
2. **Svix 单独编写了 compose 文件**（`spike/scenarios/svix/compose.yml`）：上游 compose 从源码构建，且默认重试 schedule 长达数小时，因此改用官方镜像并缩短 schedule
3. **S6 增加 Svix 变体**（`worker_max_tasks=10`）：默认 500 个槽位在 20 个事件下不会被占满，变体用来证实共享槽位机制
3. **`UNVERIFIED` 的计分方式**：checklist 中明确写为"按 0 分计"。plan 只说"不得计入评分"，两者含义一致，checklist 的写法更明确
4. **PRD Phase 1 未标记为 `complete`**：plan 的 GOTCHA 要求 ADR 评审通过后才能标记完成，与 prp-implement 的默认步骤冲突，以 plan 为准
5. **`.gitignore`** 额外加入 `.DS_Store` 和 `__pycache__/`

## Issues Encountered

- **shell 代理**：shell 设置了 `http_proxy`，导致 `curl localhost` 返回 502。已在文档中注明访问 localhost 时加 `--noproxy '*'`
- **Docker 镜像拉取**：daemon 连接 Docker Hub 超时；之后 Docker daemon 也停止运行。没有遗留任何容器或数据
- **Svix 仓库状态变化**：Svix 评估子 agent 为统计 N3 执行了 `git fetch --unshallow`，`.spike/src/svix` 已不再是浅克隆（只是 git 操作，没有执行仓库代码；该目录在 `.gitignore` 中）
- **子 agent 产出的核验**：主线程独立抽查了 4 条引用，全部正确

## Tests Written

| Test File | Tests | Coverage |
|---|---|---|
| `spike/mock-vendor/self_check.sh` | 9 | mock 的全部控制接口与边界情况（只用于验证阶段的工具，不计入项目覆盖率） |

## Next Steps

- [ ] 评审 ADR 0001–0004（重点：ADR-0003"仅用 PG"的吞吐上限和公平领取 SQL 的实现风险；ADR-0002 中团队的 Go 能力）
- [ ] 评审通过后：ADR 状态改为 `Accepted`，PRD Phase 1 改为 `complete`
- [ ] 网络可用时可选择补跑 PoC，作为参考
- [ ] `/ecc:prp-plan .claude/PRPs/prds/message-hub.prd.md` → Phase 2 领域模型与契约
