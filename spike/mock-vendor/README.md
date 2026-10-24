# mock-vendor

可控的模拟供应商 HTTP 服务，用于 PoC 场景 S1–S8，后续可复用于 Phase 4 集成测试和 Phase 7 故障注入。只依赖 Python 3 标准库。

## 启动

```bash
python3 spike/mock-vendor/mock_vendor.py --port 18080 --log .spike/mock-requests.jsonl
```

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--host` | `0.0.0.0` | 监听地址；容器内访问宿主机时使用 `host.docker.internal` |
| `--port` | `18080` | 监听端口 |
| `--log` | 无 | 将收到的每个请求以 JSONL 格式追加写入该文件 |

## 接口

路径的第一段即供应商名，例如 `POST /vendor-a/hook` 对应供应商 `vendor-a`。保留前缀：

| 方法与路径 | 作用 |
|---|---|
| `POST /_control/<vendor>` | 设置行为：`{"status": 200, "delay_ms": 0, "body": {...}, "fail_first_n": 0}`。未提供的字段取默认值；设置后失败计数清零；参数非法时返回 400，原配置不变 |
| `GET /_control/<vendor>` | 查看当前行为 |
| `GET /_requests/<vendor>` | 已收到的请求列表（时间戳、方法、路径、Header、Body） |
| `DELETE /_requests` | 清空所有请求记录和失败计数 |

行为说明：
- `fail_first_n=N`：之后的前 N 个请求返回 500，然后按 `status` 返回
- `delay_ms`：响应前等待的毫秒数（上限 120000）；服务为多线程，慢供应商不会阻塞其他供应商
- Body 上限 10MB，超过返回 413

## 示例

```bash
# 模拟"HTTP 200 但业务失败"（S3）
curl --noproxy '*' -X POST localhost:18080/_control/vendor-a -d '{"status":200,"body":{"code":1001}}'

# 模拟前 3 次失败（S4）
curl --noproxy '*' -X POST localhost:18080/_control/vendor-a -d '{"fail_first_n":3}'
```

> 若 shell 中设置了 `http_proxy`，访问 localhost 时需要加 `--noproxy '*'`。

## Self-check

```bash
bash spike/mock-vendor/self_check.sh        # 默认端口 18099
```

覆盖：记录请求、注入状态码、前 N 次失败、慢供应商隔离、非法控制参数（400 且原配置不变）、清空记录、空 Body、1MB Body。全部通过时输出 `ALL PASS`，退出码为 0。
