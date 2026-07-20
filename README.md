# cpa-codex-auto-reset
`cpa-codex-auto-reset` 是一个使用 Go 编写的 CLIProxyAPI 原生插件。它能够在CPA中自动重置额度达到阈值的Codex账号，以便让用户蹬Token蹬的爽，无需在任务进行途中去手动重置额度。

## 推荐安装方法：添加插件商店源

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
| `reset_thresh` | `95` | 使用率阈值，范围 80–100；配置低于 80 时自动回退为 95。 |

## 管理页面与 API

插件注册浏览器资源：

```text
GET /v0/resource/plugins/cpa-codex-auto-reset/status
```

页面默认中文，支持账号搜索、参与筛选、分页、批量参与/退出、立即扫描和重置日志，并在运行摘要卡片中显示当前生效的重置阈值。每轮扫描会对 CPA 发现的所有 Codex 账号刷新只读 usage；页面显示“已用量”，与 CPA 页面的“剩余量”口径相反。页面从同源 CPA 管理中心的已保存登录状态读取 Management Key，不显示密钥输入框，也不会从插件配置或接口回显密钥。请在 CPA 管理中心连接时选择记住密钥，以便嵌入的插件页面复用认证状态。

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

## 许可证

[MIT](LICENSE)

## 致谢
感谢 [linux.do](https://linux.do) 社区的讨论、反馈与支持。