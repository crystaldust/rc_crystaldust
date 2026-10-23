# ADR-0005: 配置以 YAML 文件形式存放在 Git 中管理

- **Status**: Accepted
- **Date**: 2026-10-24
- **Deciders**: Message Hub 技术负责人（用户确认，2026-10-24）
- **Related**: PRD `message-hub` Open Question"配置变更的管理方式"；ADR-0003；`docs/design/domain-model.md`

## Context

Destination、Route、Delivery Policy、Principal 的配置决定了 Hub 的全部路由与投递行为。PRD 的目标是"接入新外部目标只改配置，少于 1 天"。MVP 阶段没有管理界面，配置变更频率预计较低（每周数次以内），但每次变更都直接影响对外请求，需要评审和审计。

## Options Considered

| Option | Pros | Cons |
|---|---|---|
| **YAML 文件 + Git** | 变更自带评审（PR）与审计（提交历史）；CI 可做 schema 与引用校验；实现最简单 | 变更需要触发重新加载；多实例一致性依赖发布流程 |
| 数据库 + Admin API | 运行时修改、多实例实时一致；便于以后做管理界面 | MVP 要多写一套 CRUD、校验、审计与权限；变更缺少代码评审环节 |
| 两者都做（DB 为唯一数据源，提供 YAML apply CLI） | 最灵活 | 工作量最大，MVP 不需要 |

## Decision

**配置以 YAML 文件存放在 Git 仓库中**：
- 由 `config.Load` 加载，加载过程依次做严格 YAML 解码、JSON Schema 校验（`schemas/config.schema.json`）和语义校验
- CI 对每次配置变更执行同样的校验
- 服务启动时加载；运行中收到 SIGHUP 时重新加载（Phase 3/4 实现），新配置校验失败时保留旧配置并报警
- 配置中**不出现任何凭据明文**，只写 `env:NAME` 引用，由运行时解析

## Rationale

- 配置变更直接影响对外请求，"先评审后生效"比"即时生效"更重要
- Git 历史天然满足审计需求，不需要另建审计表
- 加载器与 schema 同时服务于 CI 校验和运行时，规则只有一份

## Consequences

- **正面**：评审、审计、回滚（`git revert`）都是现成的；配置可以在本地和 CI 中完整校验
- **负面**：
  - 多实例之间切换配置存在短暂的不一致窗口
  - 每次 attempt 记录 `config_hash`，事件记录 `routing_config_hash`，用于追溯当时生效的版本
- **约束**：凭据只能通过 `env:` 引用；schema 拒绝任何未知字段

## Revisit When

- 需要运营人员自助配置，或配置变更频率超过每天多次
- 需要秒级的多实例配置一致性
