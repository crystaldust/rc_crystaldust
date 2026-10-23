# PoC 场景执行步骤

> 状态：**未执行**。执行环境的 Docker daemon 无法访问 Docker Hub，本地也没有 Redis、PostgreSQL、RabbitMQ，按决定跳过实测，以源码证据为准（见 `docs/spike/04-poc-results.md`）。
> 网络可用后，按以下步骤补跑，并把结果写回 `04-poc-results.md`。

## 前置

```bash
# 1. 启动模拟供应商（宿主机）
python3 spike/mock-vendor/mock_vendor.py --port 18080 --log .spike/mock-requests.jsonl &

# 2. 若 shell 设置了代理，所有访问 localhost 的 curl 都加 --noproxy '*'
alias lcurl="curl --noproxy '*' -s"
```

## Outpost v1.7.0

```bash
cd spike/scenarios/outpost && docker compose up -d
# 等待健康
until [ "$(lcurl -o /dev/null -w '%{http_code}' localhost:3333/api/v1/healthz)" = 200 ]; do sleep 2; done
AUTH="Authorization: Bearer spike-api-key"
```

`compose.yml` 已设置：`TOPICS=order.paid`、`RETRY_SCHEDULE=1,2,4`、`LOG_BATCH_THRESHOLD_SECONDS=1`。

**公共准备**：按"单一 hub 租户"映射方式（见 `docs/spike/02-outpost-evaluation.md` §7）

```bash
lcurl -X PUT -H "$AUTH" localhost:3333/api/v1/tenants/hub
for v in vendor-a vendor-b; do
  lcurl -X POST -H "$AUTH" -H 'Content-Type: application/json' localhost:3333/api/v1/tenants/hub/destinations \
    -d "{\"type\":\"webhook\",\"topics\":[\"order.paid\"],\"config\":{\"url\":\"http://host.docker.internal:18080/$v/hook\"}}"
done
publish() {  # $1 = event id, $2 = data JSON
  lcurl -X POST -H "$AUTH" -H 'Content-Type: application/json' localhost:3333/api/v1/publish \
    -d "{\"id\":\"$1\",\"tenant_id\":\"hub\",\"topic\":\"order.paid\",\"eligible_for_retry\":true,\"data\":$2}"
}
```

| 场景 | 步骤 | 预期（源码推断） |
|---|---|---|
| S1 | `publish order:1:paid '{"uid":1}'`，再执行一次相同的命令；查看 `GET /_requests/vendor-a`、`vendor-b` | 两端各收到 1 次；第二次 publish 返回 `duplicate: true`；Header 带 `x-outpost-event-id: order:1:paid` |
| S2 | 在 vendor-b 的 destination `config.custom_headers` 中加 `{"X-Api-Key":"k"}`；尝试让 vendor-a 的 Body 变成 `{"uid":...,"evt":...}` | Header 可以配置；Body 只能是原始 `data`，无法重构 |
| S3 | `POST /_control/vendor-a {"status":200,"body":{"code":1001}}`，然后 publish | 被判定为成功（`< 400` 即成功） |
| S4 | `POST /_control/vendor-a {"fail_first_n":3}`，然后 publish；观察 mock 记录中各次请求的时间间隔 | 重试间隔约为 1s、2s、4s，第 4 次成功；投递重试没有 jitter |
| S5 | vendor-a 持续返回 500，直到重试耗尽；`GET /api/v1/attempts?status=failed`；然后将 vendor-a 恢复为 200，`POST /api/v1/retry` | 能查到失败的 attempt；手动重放后送达 |
| S6 | vendor-a 设置 `delay_ms=10000`，连续 publish 20 个事件；比较 vendor-b 的 `received_at` 与 publish 时间 | 默认 `DELIVERY_MAX_CONCURRENCY=1` 时 vendor-b 被拖慢；compose 中设为 10 时，槽位被占满后同样会被拖慢 |
| S7 | vendor-a 设置 `fail_first_n=1`，依次 publish 同一订单的 3 个事件 | 后 2 个事件先于第 1 个送达（没有顺序保证） |
| S8 | 以 50 events/s 持续 60s 执行 publish（例如 `for` 循环加 `sleep 0.02`）；用 mock 的 `received_at` 减去发送时间，计算 P50/P99 | P99 < 1s（首次投递路径没有批处理或轮询） |

## Svix server-v1.102.1

使用上游仓库自带的 compose（只运行，不修改）：

```bash
cd .spike/src/svix/server && docker compose up -d   # 依赖 Postgres、Redis
# 按 server/README.md 生成 JWT：docker compose exec backend svix-server jwt generate
TOKEN=<上一步输出>
AUTH="Authorization: Bearer $TOKEN"
lcurl -X POST -H "$AUTH" -H 'Content-Type: application/json' localhost:8071/api/v1/app/ -d '{"name":"hub","uid":"hub"}'
for v in vendor-a vendor-b; do
  lcurl -X POST -H "$AUTH" -H 'Content-Type: application/json' localhost:8071/api/v1/app/hub/endpoint/ \
    -d "{\"url\":\"http://host.docker.internal:18080/$v/hook\",\"filterTypes\":[\"order.paid\"]}"
done
```

> Svix 默认在 DNS 层拦截私网地址（`webhook_http_client.rs`），需要在配置中把 `host.docker.internal` 解析到的网段加入白名单（`whitelist_subnets`），并在结果中记录这一点。

| 场景 | 步骤 | 预期（源码推断） |
|---|---|---|
| S1 | `POST /api/v1/app/hub/msg/`，Body 为 `{"eventType":"order.paid","eventId":"order:1:paid","payload":{...}}`，重复发送一次 | 第二次返回 409；两端各收到 1 次；Header 中的 `svix-id` 为服务端生成的 msg ID |
| S2 | 在 endpoint headers 中设置 `X-Api-Key` | Header 可以配置；Body 只能是原样 payload |
| S3 | mock 返回 200 加 `{"code":1001}` | 判定为成功（2xx 即成功） |
| S4 | `fail_first_n=3` | 按全局重试 schedule 退避，带 ±20% jitter |
| S5 | 持续失败直到耗尽 → `GET .../attempt/endpoint/{id}?status=...` → `POST .../resend/{endpoint}` | 能查到失败记录；resend 只尝试 1 次，不进入重试 |
| S6 | 同 Outpost 的 S6 | 全局 500 个并发槽位被慢供应商占满时，其他目标也会被拖慢 |
| S7 | 同 Outpost 的 S7 | 没有顺序保证 |
| S8 | 同 Outpost 的 S8 | P99 < 1s（立即入队，阻塞读） |

## 清理

```bash
(cd spike/scenarios/outpost && docker compose down -v)
(cd .spike/src/svix/server && docker compose down -v)
kill %1   # mock-vendor
```
