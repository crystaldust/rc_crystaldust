# ADR-0002: 实现语言采用 Go

- **Status**: Accepted
- **Date**: 2026-10-24
- **Deciders**: Message Hub 技术负责人（用户确认，2026-10-24）
- **Related**: ADR-0001（自建）；`docs/spike/05-stack-evaluation.md` §A

## Context

ADR-0001 决定自建，需要选择实现语言。PRD 的选型原则是：性能、可维护性、开发速度、水平扩展、混合环境运维、AI 编码友好度（交付方式为 AI Vibe Coding，尽快上线）。负载特征是大量并发的出站 HTTP 请求，每个请求等待各自的响应。

## Options Considered

| Option | 加权得分（满分 85） | 主要优势 | 主要劣势 |
|---|---|---|---|
| **Go** | **76** | 并发出站 HTTP 模型自然；单一静态二进制；客户端库成熟；AI 生成代码可预期、易审查；与 Outpost 借鉴代码同语言 | 泛型和错误处理较啰嗦 |
| Java（Spring Boot 3 / 虚拟线程） | 66 | 生态最完整；虚拟线程 | JVM 镜像与内存基线高；框架魔法降低可审查性 |
| TypeScript（Node.js） | 61 | 开发最快 | 单线程下 CPU 操作会影响延迟；Kafka 客户端生态有过维护断档 |
| Rust | 61 | 性能与产物体积最优 | 开发速度和团队可维护性偏低 |

## Decision

**使用 Go 实现 Message Hub 的全部服务。**

## Rationale

- 加权得分领先第二名 10 分（`docs/spike/05-stack-evaluation.md` §A）
- 产物是单一静态二进制，非常适合混合环境（IDC 虚拟机与容器平台共存）的分发和运维
- AI 编码友好：语言特性少、gofmt 统一风格、编译器严格（未使用的变量和导入直接报错）、标准库测试工具与表驱动测试惯例统一
- ADR-0001 借鉴清单中的 Outpost 模块都是 Go 代码，可以直接对照阅读

## Consequences

- **正面**：部署简单，组件少；代码风格统一；可以用 `go test -race`、`go vet` 和静态检查工具构成严格的质量门禁
- **负面**：团队如无 Go 经验，需要建立编码规范（错误处理、context 传递、并发模式）。Phase 2 输出编码约定
- **未验证项**：团队 Go 能力未评估（N4 在 02/03 中均标注为待定）

## Revisit When

- 团队已有成熟的 JVM 技术栈与统一的 JVM 运维平台，且 Go 的维护成本成为实际障碍
