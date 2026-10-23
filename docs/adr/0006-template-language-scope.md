# ADR-0006: 请求模板只支持字段路径取值与固定的内置函数

- **Status**: Accepted
- **Date**: 2026-10-24
- **Deciders**: Message Hub 技术负责人（用户确认，2026-10-24）
- **Related**: PRD Decisions Log"转换层"；`docs/design/domain-model.md` §6；`testdata/contracts/template/cases.yaml`

## Context

各供应商的 URL、Header、Body 格式不同，Hub 需要按目标构造请求。PRD 明确不引入脚本或通用 DSL（Won't），只要求"定义好数据格式 + 简单模板映射"。实际需求包括：从事件中取字段并重新组织结构；对邮箱等字段做哈希（广告平台常见要求）；按供应商要求对请求体签名。

## Options Considered

| Option | Pros | Cons |
|---|---|---|
| **字段路径 + 固定内置函数** | 能覆盖重组结构、哈希、签名等常见需求；没有控制流，行为可预测、易审查；单表达式可以保留类型，输出合法 JSON | 遇到需要条件分支的格式时无法表达 |
| 只做字段映射，不提供函数 | 最简单 | 哈希、签名都要业务方预先算好，签名甚至无法预先计算（依赖最终请求体） |
| Go `text/template` | 最灵活 | 输出是字符串，容易生成非法 JSON；带控制流，接近 PRD 排除的"通用 DSL" |

## Decision

模板表达式为 `{{ path [| func [arg]]... }}`：
- **路径根**：`event.`、`secret.<NAME>`、`delivery.id`、`delivery.attempt`、`now`；Header 模板中还可以引用 `request.body`
- **内置函数固定为 12 个**：`lower`、`upper`、`trim`、`sha256`、`base64`、`urlencode`、`default`、`format_time`、`to_string`、`json`、`hmac_sha256_hex`、`hmac_sha256_base64`
- **不支持**条件、循环、变量赋值和自定义函数
- **渲染错误**（路径不存在且没有 `default`、函数未知、参数错误）按永久失败处理，Delivery 进入 DEAD，修复配置后可以重放

完整规格见 `docs/design/domain-model.md` §6，黄金用例见 `testdata/contracts/template/cases.yaml`。

## Rationale

- 覆盖了原始需求中三个场景的全部格式要求（见 `config/examples/hub.example.yaml`）
- 没有控制流，配置评审时可以直接读出请求的样子
- 类型保留规则保证 Body 输出是合法 JSON，避免 `text/template` 的字符串拼接问题

## Consequences

- **正面**：模板引擎小而可穷举测试；配置评审成本低
- **负面**：需要条件逻辑的供应商格式要么由业务方在 payload 中预处理，要么拆成多个 Route（用 `match` 区分）
- **约束**：新增内置函数需要修改本 ADR 和黄金用例

## Revisit When

- 出现无法用"路径 + 函数"表达、也无法要求业务方预处理的供应商格式
