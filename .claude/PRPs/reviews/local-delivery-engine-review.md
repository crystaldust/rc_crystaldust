# Local Review: Phase 4 投递引擎（未提交改动）

**Reviewed**: 2026-10-24
**Branch**: feat/delivery-engine（未提交）
**Decision**: APPROVE with comments（发现的 HIGH 已在本次评审中修复）

## Summary

本地评审覆盖全部未提交改动（新建 32 个、修改 9 个文件，另有不属于本阶段的 `README.md`）。机械检查全部通过；复查了第一轮独立评审（ecc:go-reviewer，结果见 `.claude/PRPs/reports/delivery-engine-report.md` 的 Code Review 一节）之后的修复代码，又发现 1 个 HIGH、1 个 MEDIUM，均已修复。

## Findings

### CRITICAL
None。无硬编码凭据（测试中只有明显的假值）；SQL 全部参数化，仅有的两处字符串拼接分别是由 domain 常量组成的状态列表（`dispatch.sqlList`）和经 `pgx.Identifier.Sanitize` 的 `LISTEN` 通道名。

### HIGH
- **[已修复] `internal/worker/process.go` `truncateText`**：错误文本中有来自外部的部分（如 token 端点返回的 OAuth2 错误码），含 U+0000 或非法 UTF-8 时 PostgreSQL text 列拒绝写入，`Complete` 反复失败，delivery 停在 IN_FLIGHT 并被反复接管、每次计费直到 DEAD。现在先替换为 U+FFFD 再截断；`TestTruncateText` 增加用例；domain-model.md §10 `error` 一行已写明。

### MEDIUM
- **[已修复] 规格与实现不一致**：§10 写"请求没有发出时 request 为空"，实现则在渲染与认证完成后、因出口代理不可用或请求非法而未发出时仍记录（`TestProcessEgressFailureIsInfra` 断言如此，便于排查）。改为修正规格措辞。

### LOW
- `completeWithRetry`：首次写入已提交但返回错误时，重试会因 attempt 主键冲突失败并输出 `delivery.complete_failed`，实际状态已正确，只是多一条误导性的错误日志。可接受，已在注释中说明。

## Validation Results

| Check | Result |
|---|---|
| Lint（`go vet ./...`、`gofmt -l`） | Pass |
| Tests（`go test -race ./...`） | Pass |
| Integration（worker、dispatch，`-race -tags integration`） | Pass |
| Build（`go build ./...`） | Pass |

## 机械检查

| 检查项 | 结果 |
|---|---|
| 文件 > 400 行（非测试） | 无（最大 `dispatch/pg.go` 约 350 行） |
| 函数 > 50 行 | 无 |
| 嵌套 > 4 层 | 无 |
| TODO / FIXME、调试输出 | 无 |
| 新代码缺测试 | 无；各包覆盖率见实施报告 |
