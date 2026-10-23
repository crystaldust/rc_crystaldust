# 04 · PoC 实测结果

## 状态：未执行（UNVERIFIED）

**原因**：执行环境的 Docker daemon 无法连接 Docker Hub（`registry-1.docker.io` 超时；shell 设置了代理，但 daemon 没有配置代理或镜像源），本地没有缓存镜像，也没有安装 Redis、PostgreSQL、RabbitMQ。

**决定**（2026-10-24，用户确认）：跳过实测，以源码证据为准。计划的风险应对中已允许这种处理。复现步骤与 compose 文件已就绪，网络可用后可以补跑：
- 执行步骤：`spike/scenarios/run_scenarios.md`
- Outpost 栈：`spike/scenarios/outpost/compose.yml`
- 模拟供应商：`spike/mock-vendor/`（自检 9/9 通过）

## 各场景状态

| 场景 | 覆盖能力 | Outpost | Svix | 源码推断（详见 run_scenarios.md） |
|---|---|---|---|---|
| S1 | C1、C2、C14 | UNVERIFIED | UNVERIFIED | 两者都能扇出与受理去重；幂等 Header 的值，Outpost 来自发布方，Svix 由服务端生成 |
| S2 | C3、C4 | UNVERIFIED | UNVERIFIED | 两者都只能配置静态 Header，Body 无法重构 |
| S3 | C5 | UNVERIFIED | UNVERIFIED | 两者都会把"200 + 业务错误码"判为成功 |
| S4 | C6 | UNVERIFIED | UNVERIFIED | 全局重试策略；Svix 带 jitter，Outpost 的投递重试不带 |
| S5 | C7、C8 | UNVERIFIED | UNVERIFIED | 都能查询并手动重放 |
| S6 | C9 | UNVERIFIED | UNVERIFIED | 都是共享队列加全局并发，慢目标会拖慢其他目标 |
| S7 | C13 | UNVERIFIED | UNVERIFIED | 都没有顺序保证 |
| S8 | C17、C18 | UNVERIFIED | UNVERIFIED | 首次投递路径都没有批处理或轮询延迟 |

## 对决策的影响分析

两份评估中的分数都基于源码证据（已逐行核对，`UNVERIFIED` 数量为 0）。PoC 的作用是**用运行时行为确认或推翻源码结论**。下面分析如果 PoC 推翻源码结论，最多能改变多少：

- C3、C5、C6 等"硬编码"类结论（如 `StatusCode >= 400`、`is_success()`、全局重试配置）属于静态代码事实，运行时不可能表现出源码中不存在的能力，PoC 无法提高这些分数
- 运行时可能和源码推断不一致的只有 C9（隔离）、C13（顺序）、C18（延迟）。假设这三项**全部按满分 3 分计**：
  - Outpost：82 + (3−1)×3 [C9] + (3−0)×2 [C13] + 0 [C18 已是 3] = **94** < 101
  - Svix：88 + (3−1)×3 [C9] + (3−1)×2 [C13] + 0 [C18 已是 3] = **98** < 101

**结论**：即使 PoC 在所有可能被推翻的项上都对候选方案有利，两者仍达不到决策规则条件 1（≥ 101）。**跳过 PoC 不影响 ADR-0001 的结论**。

## 抽查记录（Manual Validation）

主线程对子评估中的证据做了独立抽查：

| 项 | 引用 | 核对结果 |
|---|---|---|
| Outpost C5 | `outpost@v1.7.0:internal/destregistry/providers/destwebhook/httphelper.go:L89-L129` | 确认：`if resp.StatusCode >= 400` 判失败 |
| Svix C5 | `svix@server-v1.102.1:server/svix-server/src/worker.rs:L377-L427` | 确认：`res.status().is_success()` |
| Svix C12 | `throttleRate` / `rate_limit` 的引用位置 | 确认：只出现在 `core/message_app.rs`、`db/models`、`v1/endpoints` 中，`worker.rs` 与 `queue/` 均不读取 |
| Outpost C9 | `outpost@v1.7.0:internal/consumer/consumer.go:L209-L236` | 确认：单一全局信号量 `sem := make(chan struct{}, c.concurrency)` |
