# 本地测试指南

> 适用对象：想在自己的机器上验证 Message Hub 基本功能的开发者与评审者。
> 分两层：**自动化测试**（快、全面，日常开发用）和 **本地端到端演示**（起真实进程、走真实 HTTP，用来亲眼看到投递结果）。
> 质量门禁的完整要求见 `docs/design/coding-conventions.md` §10；覆盖率的衡量方式见 `docs/testing/coverage-gaps.md`。

## 0. 前置条件

| 工具 | 用途 | 说明 |
|---|---|---|
| Go 1.26 | 构建与测试 | |
| Docker | 集成测试（testcontainers）、演示中的 PostgreSQL | macOS 上用 Colima 即可，见下文 |
| Python 3 | 演示中的模拟供应商 `spike/mock-vendor` | 只用标准库 |
| curl | 手工调用 API | |

**本机设置了 HTTP 代理时**（`http_proxy` / `https_proxy`），访问 localhost 的请求也会被代理而失败，需要：

```bash
export NO_PROXY='*' no_proxy='*'          # bash / zsh
```
```fish
set -x NO_PROXY '*'; set -x no_proxy '*'  # fish
```

**Docker 由 Colima 提供时**（`/var/run/docker.sock` 不存在），集成测试还需要：

```bash
export DOCKER_HOST=unix://$HOME/.colima/default/docker.sock
export TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock
```
```fish
set -x DOCKER_HOST unix://$HOME/.colima/default/docker.sock
set -x TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE /var/run/docker.sock
```

`scripts/demo.sh` 会自动处理这两点，手工步骤需要自己设置。

---

## 1. 自动化测试

| 命令 | 需要 Docker | 覆盖内容 | 耗时（参考） |
|---|---|---|---|
| `make lint` | 否 | gofmt（只检查被 git 跟踪的文件）+ `go vet` | 秒级 |
| `make test` | 否 | 全部单元测试（`-race`），含模板 / 成功判定 / 退避三组黄金用例 | 约 1 分钟 |
| `make test-integration` | 是 | 单元测试 + 集成测试：迁移、受理与查询、replay / skip、并发领取、有序投递、LISTEN 唤醒、worker 端到端、OpenAPI 契约 | 数分钟 |
| `make cover-gaps-all` | 是 | 含集成测试的覆盖率与未覆盖代码块清单，应与 `coverage-gaps.md` 第 2 节一致 | 数分钟 |
| `make check-secrets` | 否 | 禁止跟踪 `.env` 文件；安装了 gitleaks 时扫描仓库 | 秒级 |

只看某一部分：

```bash
go test -race ./internal/template/ -run Golden -v                     # 模板黄金用例
go test -race -tags integration ./internal/dispatch/ -v               # 派发器：并发、有序、接管、EXPLAIN
go test -race -tags integration ./internal/worker/ -run E2E -v        # worker 端到端（真实 PG + 模拟供应商）
go test -race -tags integration ./internal/api/ -run Contract -v      # OpenAPI 契约测试
```

worker 端到端测试（`internal/worker/e2e_integration_test.go`）覆盖的场景：一次成功且记录已脱敏、5xx 退避重试后成功、持续失败进入 DEAD、HTTP 200 加业务错误码进入 DEAD、慢目标不拖慢其他目标、OAuth2 token 端点故障时 InfraRetry 不消耗预算、worker 停止后由另一个 worker 接管。

---

## 2. 一键端到端演示

```bash
make demo                 # 等价于 bash scripts/demo.sh
KEEP=1 make demo          # 演示结束后保留全部进程，便于继续手工探索
```

脚本会依次：构建 → 用 Docker 启动 PostgreSQL（端口 55432，容器名 `mh-demo-pg`）→ 启动模拟供应商（127.0.0.1:18080）→ 生成两把一次性 API Key（source 与 operator 各一把）→ `config check` 与 `migrate up` → 以 `config/examples/local-demo.yaml` 启动 `serve --role=all`（127.0.0.1:18090）→ 提交一个扇出到三个目标的事件 → 打印结果 → 重放 DEAD 的投递 → 结束时清理全部资源。

可通过环境变量调整：`PG_PORT`（默认 55432）、`HUB_ADDR`（默认 127.0.0.1:18090）。模拟供应商的端口固定为 18080（配置中的 URL 写死了这个端口）。

### 预期输出

| 目标 | 模拟供应商的行为 | 预期结果 | 验证的能力 |
|---|---|---|---|
| vendor-a | 200 | SUCCEEDED，1 次 attempt | 模板渲染、secret 注入、HMAC 签名、记录脱敏 |
| vendor-b | 前 2 次 500，之后 200 | SUCCEEDED，3 次 attempt（间隔约 1s、2s） | 可重试失败、指数退避 |
| vendor-c | 200 + `{"code":1001}` | DEAD，1 次 attempt（permanent） | 按响应体判定成功（PoC S3） |
| vendor-c（重放后） | 改为 `{"code":0}` | SUCCEEDED | operator 重放、审计记录 |

脚本还会打印 vendor-a 的 attempt 记录，其中 `api_key` 查询参数、`Authorization` 与 `X-Signature` 都应为 `***`，Body 保持原样。

重放后 vendor-c 的 attempts 中会出现两条 `#1`：重放会让 `attempt_count` 归零、进入新一轮预算，attempt 序号在每一轮内从 1 开始（`domain-model.md` §5.3、§10），这是预期行为。

### 失败时

| 现象 | 原因与处理 |
|---|---|
| `docker is not running` | 启动 Docker（Colima：`colima start`） |
| `port 18080 is in use` | 之前的模拟供应商还在运行：`lsof -i :18080` 找到后结束它 |
| `timed out waiting for hub /readyz` | 用 `KEEP=1` 重跑，查看脚本打印的临时目录中的 `serve.log` |
| curl 返回代理错误 | 当前 shell 的代理设置影响了 curl，见第 0 节 |

---

## 3. 手工探索（在演示环境上）

用 `KEEP=1 make demo` 保留环境后，按脚本最后的提示加载环境变量（bash / zsh：`. <临时目录>/demo.env`；fish：`bash -c '. <临时目录>/demo.env; exec fish'`）。`demo.env` 中有 `SOURCE_KEY`、`OPERATOR_KEY` 以及 hub 所需的全部变量，文件权限为 600，key 只在本机、只对这一轮演示有效。

### 3.1 提交事件

```bash
curl -s -X POST http://127.0.0.1:18090/v1/events \
  -H "Authorization: Bearer $SOURCE_KEY" -H 'Content-Type: application/json' \
  -d '{"source_event_id":"manual-1","event_type":"order.paid","occurred_at":"2026-10-24T08:00:00Z","payload":{"order_id":"o-2","amount":10}}'
```

- 返回 202 与 `event_id`、每个目标的 `delivery_id`
- 用同一个 `source_event_id` 再提交一次：返回 200、`duplicate: true`，不会产生新的投递
- `event_type` 改为没有路由的值（如 `order.refunded`）：返回 202、`routing_status: unrouted`

### 3.2 查询

```bash
curl -s http://127.0.0.1:18090/v1/events/<event_id>          -H "Authorization: Bearer $SOURCE_KEY"
curl -s http://127.0.0.1:18090/v1/deliveries/<delivery_id>   -H "Authorization: Bearer $SOURCE_KEY"
curl -s 'http://127.0.0.1:18090/v1/deliveries?status=DEAD'   -H "Authorization: Bearer $SOURCE_KEY"
```

事件详情中每个投递的 id 字段名为 `id`；受理响应中为 `delivery_id`。

### 3.3 改变模拟供应商的行为

```bash
curl -s -X POST localhost:18080/_control/vendor-a -d '{"status":503}'                    # 一直失败 → 重试 3 次后 DEAD
curl -s -X POST localhost:18080/_control/vendor-a -d '{"status":200,"delay_ms":5000}'    # 超时（策略 timeout 2s）→ 可重试
curl -s -X POST localhost:18080/_control/vendor-b -d '{"fail_first_n":1}'                # 失败 1 次后成功
curl -s localhost:18080/_requests/vendor-a                                               # 供应商实际收到的请求（含真实 Header）
```

接口说明见 `spike/mock-vendor/README.md`。对比 `_requests` 中供应商收到的请求（真实 token）与查询 API 中的 attempt 记录（`***`），可以确认脱敏只发生在记录中。

### 3.4 运维操作（operator）

```bash
curl -s -X POST http://127.0.0.1:18090/v1/deliveries/<delivery_id>/replay \
  -H "Authorization: Bearer $OPERATOR_KEY" -H 'Content-Type: application/json' -d '{"reason":"vendor fixed"}'
curl -s -X POST http://127.0.0.1:18090/v1/deliveries/<delivery_id>/skip \
  -H "Authorization: Bearer $OPERATOR_KEY" -H 'Content-Type: application/json' -d '{"reason":"not needed"}'
```

- replay 只允许终态（DEAD / SUCCEEDED / SKIPPED），skip 只允许 DEAD，否则返回 409
- 用 `SOURCE_KEY` 调用返回 403

### 3.5 配置热重载

修改 `config/examples/local-demo.yaml`（例如把 `demo` 策略的 `max_attempts` 改为 5），然后 `kill -HUP <hub pid>`；日志中出现 `config.reloaded`，新投递按新配置执行。配置有误时日志为 `config.reload_failed`，旧配置继续生效。

---

## 4. 不用脚本、从零手工启动

与 `scripts/demo.sh` 的步骤相同，适合需要自定义配置时参考：

1. `docker run -d --rm --name mh-demo-pg -e POSTGRES_USER=hub -e POSTGRES_PASSWORD=hub -e POSTGRES_DB=messagehub -p 55432:5432 postgres:16-alpine`
2. `python3 spike/mock-vendor/mock_vendor.py --host 127.0.0.1 --port 18080 &`
3. `make build`，然后运行两次 `bin/messagehub keygen`：输出中的 `key:` 交给调用方，`sha256:` 放进配置引用的环境变量（`HUB_KEY_DEMO_SOURCE_SHA256`、`HUB_KEY_DEMO_OPERATOR_SHA256`）
4. 设置环境变量：`HUB_CONFIG_PATH`（配置路径）、`HUB_DATABASE_URL`（`postgres://hub:hub@127.0.0.1:55432/messagehub?sslmode=disable`）、`HUB_HTTP_ADDR`（如 `127.0.0.1:18090`），以及配置中引用的 secret（`DEMO_VENDOR_TOKEN`）
5. `bin/messagehub config check`（缺少环境变量时退出码为 2，并列出缺失项）
6. `bin/messagehub migrate up`（注意必须带 `up`）
7. `bin/messagehub serve --role=all`（也可以分别以 `--role=api` 和 `--role=worker` 启动两个进程）

全部进程参数见 `internal/app/settings.go`（`HUB_*`）；配置格式见 `docs/design/domain-model.md` §11 与 `config/examples/hub.example.yaml`。
