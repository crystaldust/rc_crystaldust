# PoC 场景执行步骤

> 状态：**已执行**（2026-10-24），结果见 `docs/spike/04-poc-results.md`，原始数据在 `spike/scenarios/results/`。
> 场景逻辑在 `spike/scenarios/scenarios.py` 中，两个候选共用；本文件只记录环境准备与复跑命令。

## 前置

```bash
# 若 shell 设置了代理，访问 localhost 时需要绕过（scenarios.py 内部已自动绕过）
export NO_PROXY=localhost,127.0.0.1 no_proxy=localhost,127.0.0.1

# 启动模拟供应商（宿主机，需要监听 0.0.0.0 以便容器访问）
python3 spike/mock-vendor/mock_vendor.py --port 18080 --log .spike/mock-requests.jsonl &
```

Docker daemon 需要能拉取 Docker Hub 镜像（如有需要，在 Docker Desktop 中配置代理）。

## Outpost v1.7.0

```bash
(cd spike/scenarios/outpost && docker compose up -d)
until [ "$(curl -s -o /dev/null -w '%{http_code}' localhost:3333/api/v1/healthz)" = 200 ]; do sleep 2; done

# 一次性准备：单一 hub 租户 + 两个 webhook 目标（映射方式见 02-outpost-evaluation.md §7）
AUTH="Authorization: Bearer spike-api-key"
curl -s -X PUT -H "$AUTH" localhost:3333/api/v1/tenants/hub
for v in vendor-a vendor-b; do
  curl -s -X POST -H "$AUTH" -H 'Content-Type: application/json' localhost:3333/api/v1/tenants/hub/destinations \
    -d "{\"type\":\"webhook\",\"topics\":[\"order.paid\"],\"config\":{\"url\":\"http://host.docker.internal:18080/$v/hook\"}}"
done

python3 spike/scenarios/scenarios.py outpost all
(cd spike/scenarios/outpost && docker compose down -v)
```

`compose.yml` 已设置：`TOPICS=order.paid`、`RETRY_SCHEDULE=1,2,4`、`LOG_BATCH_THRESHOLD_SECONDS=1`、`DELIVERY_MAX_CONCURRENCY=10`。

## Svix server-v1.102.1

使用官方镜像（上游 compose 从源码构建，且默认重试 schedule 长达数小时）：

```bash
(cd spike/scenarios/svix && docker compose up -d)
until [ "$(curl -s -o /dev/null -w '%{http_code}' localhost:8071/api/v1/health/)" = 200 ]; do sleep 2; done
docker compose -f spike/scenarios/svix/compose.yml exec -T backend svix-server jwt generate \
  | sed -n 's/^Token (Bearer): //p' > .spike/svix-token
AUTH="Authorization: Bearer $(cat .spike/svix-token)"

# 一次性准备：事件类型 + hub 应用 + 两个 endpoint
curl -s -X POST -H "$AUTH" -H 'Content-Type: application/json' localhost:8071/api/v1/event-type/ -d '{"name":"order.paid","description":"order paid"}'
curl -s -X POST -H "$AUTH" -H 'Content-Type: application/json' localhost:8071/api/v1/app/ -d '{"name":"hub","uid":"hub"}'
for v in vendor-a vendor-b; do
  curl -s -X POST -H "$AUTH" -H 'Content-Type: application/json' localhost:8071/api/v1/app/hub/endpoint/ \
    -d "{\"url\":\"http://host.docker.internal:18080/$v/hook\",\"filterTypes\":[\"order.paid\"]}"
done

SVIX_TOKEN=$(cat .spike/svix-token) python3 spike/scenarios/scenarios.py svix all
```

> `compose.yml` 用 `SVIX_WHITELIST_SUBNETS='["0.0.0.0/0"]'` 放开私网拦截，以便访问宿主机上的 mock。**仅限本次验证使用。**

**S6 变体（模拟槽位被占满）**：以 `worker_max_tasks=10` 单独起一个实例再跑 S6。

```bash
(cd spike/scenarios/svix && docker compose run -d --no-deps --name spike-svix-small \
   -e SVIX_WORKER_MAX_TASKS=10 -p 127.0.0.1:8072:8071 backend && docker compose stop backend)
SVIX_BASE=http://localhost:8072/api/v1 RESULT_SUFFIX=-slots10 SVIX_TOKEN=$(cat .spike/svix-token) \
  python3 spike/scenarios/scenarios.py svix S6
docker rm -f spike-svix-small; (cd spike/scenarios/svix && docker compose down -v)
```

## 场景定义

| 场景 | 做法 | 覆盖能力 |
|---|---|---|
| S1 | 同一 ID 发布两次；检查两端收到次数、幂等 Header、Body | C1、C2、C14 |
| S2 | 给 vendor-b 加 `X-Api-Key` Header；确认 Body 无法重构 | C3、C4 |
| S3 | vendor-a 返回 200 + `{"code":1001}`；查看 attempt 状态 | C5 |
| S4 | vendor-a 前 3 次返回 500；测量重试间隔 | C6 |
| S5 | vendor-a 持续返回 500 直到耗尽；查询失败 attempt；恢复后手动重放 | C7、C8 |
| S6 | vendor-a 每次延迟 10s，发布 20 个事件（不重试）；测量 vendor-b 延迟 | C9 |
| S7 | vendor-a 第 1 次失败；依次发布 created / paid / cancelled；查看送达顺序 | C13 |
| S8 | 50 events/s 持续 60s（不重试）；测量"发送 → mock 收到"的 P50 / P99 | C17、C18 |

## 清理

```bash
kill %1   # mock-vendor
```
