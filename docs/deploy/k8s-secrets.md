# 在 Kubernetes 中为 Message Hub 提供密钥

> 依据：ADR-0008 D3（当前阶段使用 Kubernetes Secret，后续接入其他 secret manager）。
> 适用范围：部署在 Kubernetes 上的 Hub 实例。非 Kubernetes 环境（虚拟机）的密钥注入尚未支持，见第 6 节。

## 1. 原理

```
Kubernetes Secret  ──(envFrom / secretKeyRef)──►  Hub 进程环境变量  ◄──(env:NAME 引用)──  hub.yaml（ConfigMap）
```

- 配置文件 `hub.yaml` 只包含 `env:NAME` 形式的引用，**可以**放在 ConfigMap 中，也可以放在 Git 中
- 密钥值只放在 Kubernetes Secret 中，由 kubelet 在 Pod 启动时注入为环境变量
- Hub 代码只读环境变量，不依赖 Kubernetes API

## 2. 需要哪些键

Secret 的键必须与配置中引用的环境变量名一一对应。获取完整列表：

```go
cfg, _ := config.Load("hub.yaml")
names := cfg.RequiredEnv() // 排序、去重后的变量名，只读引用，不读取值
```

Phase 3 会提供命令行工具直接输出该列表；Hub 启动时也会检查列表中的每个变量都存在且非空，缺失则拒绝启动（`docs/testing/coverage-gaps.md` B4）。

示例配置 `config/examples/hub.example.yaml` 需要的键（由 `TestRequiredEnvMatchesExampleConfig` 守护，配置变化时测试会失败并提示同步本文）：

| 键 | 用途 |
|---|---|
| `HUB_KEY_USER_SERVICE_SHA256` 等 `HUB_KEY_*_SHA256`（5 个） | 入站 API Key 的 SHA-256（每个 principal 一个；轮换期间可放两个，以逗号分隔） |
| `CRM_CLIENT_ID`、`CRM_CLIENT_SECRET` | CRM 的 OAuth2 client credentials |
| `INVENTORY_API_TOKEN` | 库存系统的 Bearer Token |
| `AD_PLATFORM_SIGNING_KEY` | 广告平台请求签名密钥 |
| `DWH_WEBHOOK_SIGNING_KEY` | 数据仓库 webhook 签名密钥 |

## 3. 清单示例

**Secret**（仓库中只保存模板，值由运维在集群中创建，**不提交真实值**）：

```yaml
# deploy/k8s/secret.template.yaml —— 只含占位符
apiVersion: v1
kind: Secret
metadata:
  name: message-hub-secrets
  namespace: message-hub
type: Opaque
stringData:
  HUB_KEY_ORDER_SERVICE_SHA256: "<sha256-hex>"
  CRM_CLIENT_ID: "<value>"
  CRM_CLIENT_SECRET: "<value>"
  # ……其余键见第 2 节
```

创建真实 Secret 时不要把值写进任何文件：

```bash
kubectl -n message-hub create secret generic message-hub-secrets \
  --from-literal=CRM_CLIENT_SECRET="$(vault-or-password-manager-output)" \
  --dry-run=client -o yaml | kubectl apply -f -
```

**Deployment** 片段：

```yaml
spec:
  template:
    spec:
      serviceAccountName: message-hub
      containers:
        - name: hub
          envFrom:
            - secretRef:
                name: message-hub-secrets
                optional: false          # Secret 不存在时 Pod 无法启动，而不是带着空值启动
          volumeMounts:
            - name: config
              mountPath: /etc/message-hub   # hub.yaml 来自 ConfigMap，只含引用
              readOnly: true
      volumes:
        - name: config
          configMap:
            name: message-hub-config
```

用 `envFrom` 一次注入 Secret 的全部键，新增 destination 时只需在 Secret 中加键。若希望只注入特定键，也可以对每个变量使用 `env[].valueFrom.secretKeyRef`。

## 4. 集群侧的安全前提

Kubernetes Secret 默认只做 base64 编码，**不是加密**。以下前提由集群管理员保证，部署前逐项确认：

| # | 要求 | 原因 |
|---|---|---|
| K1 | etcd 静态加密：配置 `EncryptionConfiguration`（推荐使用 KMS provider） | 否则拿到 etcd 备份即可读出全部密钥 |
| K2 | RBAC：只有 Hub 的部署流水线能 `create/update` 该 Secret；任何人（包括 Hub 的 ServiceAccount）都不需要 `get/list/watch` 权限，因为注入由 kubelet 完成 | 防止通过 API 读取密钥 |
| K3 | 独立命名空间 `message-hub`，限制可以在其中创建 Pod 的主体 | 能在同一命名空间创建 Pod 的人，就能挂载并读取该 Secret |
| K4 | 不把密钥放进 ConfigMap、镜像、Helm values 文件或 CI 日志 | 这些位置都没有 Secret 的访问控制 |
| K5 | 严格限制 `pods/exec`、`pods/attach` 与临时调试容器（ephemeral containers）的权限 | 能进入容器的人可以读取进程环境变量（ADR-0008 残余风险） |

## 5. 轮换

环境变量在 Pod 运行期间**不会**随 Secret 更新，轮换需要滚动重启：

1. **入站 API Key**：
   - 用工具生成新 key，把 Secret 中对应变量改为 `<旧哈希>,<新哈希>`
   - `kubectl -n message-hub rollout restart deployment/message-hub`
   - 调用方切换到新 key
   - 把变量改为只含 `<新哈希>`，再次滚动重启
2. **出站凭据**：
   - 先在供应商处签发新凭据（大多数供应商允许新旧凭据并存）
   - 更新 Secret 中的值并滚动重启
   - 确认投递正常后，在供应商处吊销旧凭据

轮换不修改 `hub.yaml`，ConfigHash 不变；需要追溯某个时间点使用的凭据版本时，以 Secret 的变更记录（或 secret manager 的审计日志）为准。

## 6. 后续接入其他 secret manager

| 场景 | 推荐方式 | Hub 是否需要改代码 |
|---|---|---|
| 部署在 Kubernetes 上，密钥存放在 Vault 或云厂商的 secret 服务中 | 使用 External Secrets Operator（或 Vault Agent Injector）把外部密钥同步为 Kubernetes Secret，Hub 侧保持不变 | **不需要** |
| 部署在虚拟机等非 Kubernetes 环境 | 在 Hub 中按引用前缀扩展解析器：保留 `env:`，新增如 `vault:<path>#<key>`、`file:<path>` | 需要（Phase 3 预留解析器接口，按需实现） |
| 需要不重启就生效的轮换 | 以文件形式挂载 Secret，配合 `file:` 前缀与配置重载 | 需要 |

扩展规则（ADR-0008 D3）：
- 新的引用前缀只有在解析器实现、测试完成后，才加入配置 schema；schema 不接受尚未实现的前缀，避免出现"配置写了却无法解析"
- 所有前缀共用同一套"启动时检查引用是否都能解析"的逻辑（`RequiredEnv` 将泛化为按前缀分组列出引用）
