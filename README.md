# cpa-codex-auto-reset

`cpa-codex-auto-reset` 是一个使用 Go 编写的 CLIProxyAPI 原生插件。它发现文件型 Codex 账号和官方重置机会，仅为用户明确选择参与的账号，在机会清单完整、额度达到阈值且所有幂等与冷却条件满足时消费一次机会。

当前版本：`v0.1.0`。

## 安全边界

- 新发现账号默认不参与；必须在管理页逐个或批量开启。
- 不接管账号调度，不拦截、修改或阻断模型请求。
- 不刷新或写回 Codex OAuth 凭据，不制造额度消耗。
- 写操作前持久化精确机会 ID、UUID v4 幂等键和 attempt 阶段。
- 同账号使用进程内互斥和跨进程文件锁；每轮最多发起一次新的消费请求。
- 超时、连接中断或 5xx 可能意味着请求已送达，会进入 `ambiguous`，后续只复用原幂等键核验。
- 成功或 `already_redeemed` 后立即进入持久冷却；本地 quota 清理失败只重试清理，不再次消费机会。
- 状态损坏、清单不完整、身份不确定或日志无法可靠落盘时停止写操作，但不影响 CLIProxyAPI 正常代理服务。
- 页面和日志不返回 Management Key、token、完整机会 ID或幂等键。

## 推荐安装：添加插件商店源

CLIProxyAPI 必须启用插件和 Management API。将以下内容合并到 CLIProxyAPI 配置：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  store-sources:
    - "https://raw.githubusercontent.com/vrxiaojie/cpa-codex-auto-reset/main/registry.json"
  configs:
    cpa-codex-auto-reset:
      enabled: true
      priority: 100
      management-url: "http://127.0.0.1:8317"
      management-key-env: "CPA_MANAGEMENT_KEY"
      reset_thresh: 95
```

然后在 CLIProxyAPI 进程环境中设置：

```bash
export CPA_MANAGEMENT_KEY='your-management-key'
```

也可以在插件管理页直接填写 `management-url` 和 `management-key`。插件自己的账号页面会复用 CPA 管理中心已保存的登录凭据，不再要求重复输入 Management Key；页面只显示插件配置中的密钥是否已配置，不会回显密钥内容。

商店安装要求 CLIProxyAPI 服务端能够访问商店源、GitHub API 和 GitHub Release 下载地址。手动安装时请从 Release 下载对应平台 ZIP，只解压其中的动态库，并保持 basename 为 `cpa-codex-auto-reset`。

## 配置

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `management-url` | `http://127.0.0.1:8317` | CLIProxyAPI Management API 根地址；远程地址会在页面显示安全警告。 |
| `management-key` | 空 | Management Key，非空时优先使用，只保存在插件内存中。 |
| `management-key-env` | `CPA_MANAGEMENT_KEY` | 从环境变量读取 Management Key。 |
| `scan-interval-seconds` | `300` | 后台扫描周期，范围 60 秒到 7 天。 |
| `post-reset-cooldown-seconds` | `1800` | 成功或已兑换后的账号冷却。 |
| `failure-backoff-seconds` | `300` | 临时错误初始指数退避；`nothing_to_reset`、`no_credit` 等逻辑失败至少退避 30 分钟。 |
| `state-dir` | 用户缓存目录下插件专用目录 | 私有持久状态目录；热更新不能迁移，修改后需重启宿主。 |
| `default-participation` | `false` | 新发现账号是否默认参与；不建议改为 `true`。 |
| `reset_thresh` | `95` | 使用率阈值，范围 80–100。 |

短横线与下划线配置名均可解析；对外字段以表格中的名称为准。

## 决策策略

插件只选择 `reset_type=codex_rate_limits`、`status=available` 且具有未来 RFC3339 到期时间的机会，并要求 `available_count` 与详细可用记录一致。多个机会按到期时间排序。

- 到期前 6 小时进入候选窗口。
- quota 已阻塞或最高 usage 达到 `reset_thresh` 才允许重置。
- 到期前 30 分钟进入保护窗口，但仍会重新读取 usage；usage 为 0 时不会为了避免过期而连续重置。
- POST 前重新读取完整机会清单，并确认选中的仍是最早可用机会。
- `nothing_to_reset`、`no_credit`、核验失败或清单陈旧会保存指纹并抑制同一逻辑尝试，直到 usage 窗口或机会清单发生可验证变化。

## 管理页面与 API

插件注册浏览器资源：

```text
GET /v0/resource/plugins/cpa-codex-auto-reset/status
```

页面默认中文，支持账号搜索、参与筛选、分页、批量参与/退出、立即扫描和重置日志。页面从同源 CPA 管理中心的已保存登录状态读取 Management Key，不显示密钥输入框，也不会从插件配置或接口回显密钥。请在 CPA 管理中心连接时选择记住密钥，以便嵌入的插件页面复用认证状态。

受 CLIProxyAPI Management 鉴权保护的接口：

```text
GET  /v0/management/plugins/cpa-codex-auto-reset/status
GET  /v0/management/plugins/cpa-codex-auto-reset/accounts
PUT  /v0/management/plugins/cpa-codex-auto-reset/accounts/participation
GET  /v0/management/plugins/cpa-codex-auto-reset/logs
POST /v0/management/plugins/cpa-codex-auto-reset/scan
```

参与更新示例：

```json
{
  "auth_ids": ["脱敏账号引用"],
  "participating": true
}
```

“立即扫描”不会绕过参与设置、机会完整性、阈值、冷却、退避或幂等保护。

## 本地开发

要求 Go 1.26+ 和可用的 C 编译器。

```bash
gofmt -w .
go vet ./...
go test ./...
go test -race ./...
make build
```

本机构建产物写入 `build/`。Release 使用 `CGO_ENABLED=1` 和 `-buildmode=c-shared`，自动生成的 `.h` 文件不会进入 ZIP。

## Release 资产

Git tag 使用 `vX.Y.Z`。v0.1.0 发布五个 ZIP：

```text
cpa-codex-auto-reset_0.1.0_linux_amd64.zip
cpa-codex-auto-reset_0.1.0_linux_arm64.zip
cpa-codex-auto-reset_0.1.0_darwin_amd64.zip
cpa-codex-auto-reset_0.1.0_darwin_arm64.zip
cpa-codex-auto-reset_0.1.0_windows_amd64.zip
```

每个 ZIP 根目录只有一个 `.so`、`.dylib` 或 `.dll`，Release 同时包含覆盖全部 ZIP 的 `checksums.txt`。

## 许可证

[MIT](LICENSE)
