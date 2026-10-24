# ADR-0008: 密钥管理——配置只写引用、运行时注入、记录脱敏、API Key 强度约束

- **Status**: Accepted
- **Date**: 2026-10-24
- **Deciders**: Message Hub 技术负责人（用户确认，2026-10-24）
- **Related**: ADR-0005（配置以 YAML + Git 管理）；`docs/design/domain-model.md` §9、§10、§11、§13.5

## Context

Message Hub 持有两类密钥：
1. **入站**：业务系统、运维调用 Hub 时使用的 API Key
2. **出站**：Hub 调用各供应商时使用的凭据（API Key、Bearer Token、OAuth2 client secret、签名密钥）

ADR-0005 决定配置存放在 Git 中，配置文件因此会被广泛阅读、评审和复制，绝不能包含密钥。Phase 2 已实现"凭据字段只能写 `env:` 引用"，但审查发现 4 处缺口：

| # | 缺口 |
|---|---|
| G1 | 模板（Header、Body、URL）中仍可以直接写字面量密钥，schema 管不到模板内容 |
| G2 | attempt 记录只对 Header 脱敏；放在 Body 或 URL 查询参数中的密钥会以明文写入数据库，并通过查询 API 返回 |
| G3 | 密钥如何注入运行环境没有约定，可能被写进部署脚本或 `.env` 文件 |
| G4 | 入站 API Key 的强度没有约束；以 SHA-256 保存哈希的前提是 key 为高熵随机值 |

## Decision

### D1 配置中不出现密钥（针对 G1）
- 凭据类字段（`*_ref`、`secrets` 的值）只允许 `env:NAME` 引用（Phase 2 已实现）
- 模板中引用的 `secret.NAME` 必须在本 Destination 的 `secrets` 中声明；每个 Destination 只能使用自己声明的 secret
- **名称为敏感名称**的 Header、URL 查询参数、Body 字段，值必须来自 `secret.*`，不允许写字面量；确需字面量时，必须显式列入 `allow_literal`，过期的条目会导致加载失败
- 敏感名称的判定集中在 `domain.IsSensitiveName`，配置校验与记录脱敏共用这一条规则
- 第二道防线：仓库启用密钥扫描（gitleaks 或同类工具，版本固定），在 CI 中对每次提交执行（CI 建设时落地，见"实施"）

### D2 记录中不出现密钥（针对 G2）
attempt 记录在截断前完成脱敏，覆盖 Header、URL、Body 三个位置（规则见 `domain-model.md` §10）：
- 名称为敏感名称的字段
- 含有路径根为 `secret.` 的表达式的字段

OAuth2 获取 token 的请求与响应不写入 attempt 记录。

### D3 密钥只在运行时注入（针对 G3）
- 密钥值只来自 **Hub 进程的环境变量**，配置中以 `env:NAME` 引用
- **当前阶段：Kubernetes Secret**（用户确认，2026-10-24）。Secret 通过 `envFrom` / `secretKeyRef` 注入为环境变量，Hub 代码不依赖 Kubernetes API。清单示例、集群侧安全前提（etcd 加密、RBAC、命名空间隔离等）与轮换流程见 `docs/deploy/k8s-secrets.md`
- **后续接入其他 secret manager**：
  - Kubernetes 环境：通过 External Secrets Operator 或 Vault Agent Injector 把 Vault、云厂商 secret 服务中的密钥同步为 Kubernetes Secret，Hub 不改代码
  - 非 Kubernetes 环境（虚拟机等）：按引用前缀扩展解析器（如 `vault:`、`file:`）。新前缀在解析器实现并测试完成后才加入 schema，schema 不接受尚未实现的前缀
- `config.RequiredEnv()` 列出配置引用的全部环境变量名，用于生成 Secret 的键清单和启动时的存在性检查
- 本地开发：开发者在自己的 shell 中设置环境变量，使用开发专用的假密钥
- 仓库中**禁止**出现 `.env`、`.env.*` 文件：已加入 `.gitignore`（`.env.example` 除外，且只能包含变量名、不含值）；`make check-secrets` 在发现被 Git 跟踪的 `.env` 文件时失败
- 密钥轮换不需要修改配置文件；ConfigHash 只基于引用名计算，因此轮换不改变哈希

### D4 入站 API Key 的强度与轮换（针对 G4）
- 格式：`mhk_` + 32 字节密码学随机数的 base64url 编码（无填充）
- 只能由 Hub 提供的工具生成（Phase 3），工具输出 key（只显示一次）和它的 SHA-256 hex
- 由于 key 为 256 位随机值，使用不加盐的 SHA-256 保存哈希即可抵御暴力破解（不适用于人工设定的密码，这也是禁止人工设定 key 的原因）
- 无中断轮换：环境变量中最多放两个以逗号分隔的哈希，新旧 key 并存期间都能认证，完成后删除旧哈希
- Hub 启动时校验哈希格式（1–2 个 64 位小写 hex），不符则拒绝启动

## Rationale

- 配置校验在 CI 和服务启动时都会执行，把"禁止字面量密钥"做成加载失败，比依赖评审者肉眼发现更可靠
- 配置校验与记录脱敏共用一条敏感名称规则，避免"校验放行了、脱敏却漏掉"的不一致
- `allow_literal` 要求显式声明，并且过期即失败，既能处理误报，又不会让例外越积越多
- 环境变量是 Go 服务、各类部署平台和各家 secret manager 的公共接口，不绑定具体产品

## Consequences

- **正面**：密钥不会出现在 Git、CI 日志、数据库 attempt 记录或查询 API 的响应中；接入新的 secret manager 不需要改代码
- **负面**：
  - 敏感名称规则基于命名约定，可能出现误报（已提供 `allow_literal`）或漏报（例如供应商使用 `X-Auth: ...` 这类不含关键词的名称）。漏报由密钥扫描兜底
  - 名称包含 `idempotency` 的字段被固定排除（`Idempotency-Key` 是幂等标识，不是凭据）
- **残余风险**：
  - 供应商在响应体中回显密钥时，Hub 不会脱敏（无法预知回显内容），只做截断
  - 进程环境变量对同一主机上有足够权限的用户可见，依赖容器或主机隔离来防护

## 实施

| 项 | 阶段 | 状态 |
|---|---|---|
| 凭据字段只允许 `env:` 引用 | Phase 2 | ✅ 已完成 |
| 模板引用的 secret 必须声明；敏感名称禁止字面量；`allow_literal` | Phase 2 | ✅ 已完成（`internal/config/secrets.go`） |
| `domain.IsSensitiveName` | Phase 2 | ✅ 已完成 |
| `.gitignore` 忽略 `.env*`；`make check-secrets` | Phase 2 | ✅ 已完成 |
| attempt 记录脱敏（D2） | Phase 4 | 待实现（`docs/testing/coverage-gaps.md` B9） |
| API Key 生成工具、启动时校验哈希格式、双哈希轮换（D4） | Phase 2b / 3 | ✅ 生成工具（`messagehub keygen`）、哈希解析与常量时间比对、启动与重载时校验格式已实现；认证中间件在 Phase 3（B10） |
| CI 中运行密钥扫描（D1 第二道防线） | CI 建设时 | 待实现（B11） |
| 当前阶段采用 Kubernetes Secret（D3） | Phase 2 | ✅ 已决定；部署文档 `docs/deploy/k8s-secrets.md`；`config.RequiredEnv()` 已实现 |
| Kubernetes 清单（Secret 模板、Deployment）落地到 `deploy/k8s/` | 部署方案阶段 | 待实现（B12） |
| 启动时检查 `RequiredEnv()` 中的变量全部存在 | Phase 2b | ✅ 已实现：`secrets.CheckRequired`，启动、SIGHUP 重载与 `messagehub config check` 都会执行（B4） |
| 其他 secret manager（Vault、云 KMS 等）与非 Kubernetes 环境的注入 | 按需 | 待决定（PRD Open Questions） |

## Revision History

| 日期 | 变更 |
|---|---|
| 2026-10-24 | 初版：secret manager 待定 |
| 2026-10-24 | D3 定案：当前阶段采用 Kubernetes Secret；其他 secret manager 后续通过 External Secrets Operator（K8s）或引用前缀扩展（非 K8s）接入 |

## Revisit When

- 需要支持非 Kubernetes 部署，或需要不重启即生效的密钥轮换
- 出现敏感名称规则无法覆盖、且 `allow_literal` 难以管理的供应商命名方式
- 需要对响应体做密钥脱敏（例如某个供应商确认会回显凭据）
