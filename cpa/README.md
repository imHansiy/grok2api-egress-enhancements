# CPA — CLIProxyAPI Egress Quality Guard Plugin

CPA（Circuit-Protection Agent）是一个用于 [CLIProxyAPI](https://help.router-for.me/cn/plugin/development.html) 的插件，
把 [grok2api-egress-enhancements](https://github.com/lij768423-svg/grok2api-egress-enhancements)
里的「出口质量守护」设计移植到 CLIProxyAPI 插件体系：

> 按 grok2api 面板同口径计算每个凭证的吐字速度（output tokens per second），
> 超过硬阈值（吐字异常快，说明被降智/缓存/异常缓冲）立即隔离；低于软阈值触发固定 Prompt 主动复测，
> 连续命中后隔离；隔离到期后自动复测，健康即恢复。同时提供最低健康凭证数保护，避免把服务饿死。

## 设计对齐

| grok2api 补丁设计 | CPA 插件实现 |
| --- | --- |
| 被动审计：`输出 Token / (总耗时 - 首字耗时)`，输出 Token 含推理 Token | `usage.handle` 按 `outputTokensPerSecond` 同口径计算 |
| 被动硬阈值立即隔离（默认 1000 TPS） | `stateStore.observePassive` 硬阈值分支 |
| 软阈值触发固定 Prompt 主动复测 | `probe.go` 用 `host.model.execute_stream` 发固定 Prompt |
| 连续探测错误可隔离 | `stateStore.markError` / `recordProbeResult` |
| 隔离只禁用不删除 | `scheduler.pick` 从候选剔除隔离凭证 |
| 恢复前真实模型探测 | `probeCredential` 检查 expected marker + TPS |
| 最低健康节点保护（默认 3） | `pickAuth` 全部隔离时委托内置调度器 |
| 管理端质量守护页面、手动诊断、策略热加载 | `management_api` 资源页 + 管理路由 |

## 能力声明

插件声明以下能力：

```json
{
  "scheduler": true,
  "usage_plugin": true,
  "management_api": true
}
```

## 工作原理

1. **被动测速**：每个完成的流式请求到达 `usage.handle`，携带 `AuthID`、`OutputTokens`（含
   `ReasoningTokens`）、`TTFT`、`Latency`。插件按面板口径计算 TPS 并更新该凭证的健康状态。
   - `tps >= hard_tps` → 立即隔离
   - `tps < soft_tps` → 标记 suspect
   - 健康观测 → 清除 suspect
2. **主动复测**：隔离到期或被管理端触发时，插件设置一次性 scheduler override 固定目标凭证，
   通过 `host.model.execute_stream` 发送固定 Prompt，校验 expected marker 与 TPS。
   - marker 命中且 TPS 低于 hard 阈值 → 恢复
   - 否则保持隔离并累计错误
3. **调度隔离**：`scheduler.pick` 从候选凭证中剔除隔离中的凭证，从健康候选里显式挑选；
   若全部候选都被隔离，委托内置 round-robin 保证服务可用（最低健康保护）。
4. **管理面板**：`/v0/resource/plugins/cpa/` 提供仪表盘；`/v0/management/plugins/cpa/*`
   提供状态查询、手动隔离/释放、触发探测、策略热加载。

## 构建

需要 Go 1.26+ 与 CGO（macOS 构建 .dylib）。

```bash
cd cpa
export CGO_ENABLED=1
go build -buildmode=c-shared -o cpa.dylib .
```

产物：`cpa.dylib`（macOS arm64/amd64）、`cpa.h`。

或直接使用 GitHub Actions 自动构建（见 `.github/workflows/build-cpa.yml`），
推送 tag `v*` 时自动产出多平台动态库并发布 Release。

## 安装到 CLIProxyAPI

1. 将 `cpa.dylib` 放入宿主插件目录：`plugins/darwin/arm64/cpa.dylib`（或 `plugins/`）。
2. 在 `config.yaml` 开启插件：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa:
      enabled: true
      priority: 1
      mode: "active"
      hard_tps: 1000
      soft_tps: 500
      soft_hits_before_quarantine: 2
      max_consecutive_errors: 5
      quarantine_duration: "30m"
      recovery_delay: "2m"
      min_healthy: 3
      probe_model: "grok-3"
      probe_prompt: "Reply with the exact marker QUALITY_OK and nothing else."
      probe_expected: "QUALITY_OK"
      probe_max_output_tokens: 64
      probe_timeout: "60s"
      override_ttl: "30s"
      include_reasoning_tokens: true
```

3. 启动 CLIProxyAPI 后请求：

```bash
curl -H "Authorization: Bearer <management-key>" \
  http://localhost:8317/v0/management/plugins | jq
```

确认 `cpa` 的 `registered: true`、`effective_enabled: true`。

## 验证

```bash
go test ./...       # 单元测试（TPS 公式对齐面板 + 状态机 + 调度过滤）
```

生产验证清单：

- [ ] 被动硬阈值命中后，对应凭证从调度候选中消失
- [ ] 手动 `POST /v0/management/plugins/cpa/release` 后凭证恢复可用
- [ ] 隔离到期后主动复测通过（marker 命中 + TPS 正常）自动恢复
- [ ] 全部候选被隔离时委托内置调度器，服务不中断
- [ ] 管理 API 不返回上游 token、代理 URL、探测 Prompt 或响应正文

## 安全与隐私

- 插件不保存上游凭证；状态只含 AuthID、时间、TPS、Token 指标。
- 管理面板为受信任同源资源页，管理密钥从 `localStorage` 读取，敏感操作走
  `/v0/management/...`（需要管理密钥）。
- 日志不输出密钥、token、探测 Prompt 或请求体。

## 配置字段

| 字段 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `enabled` | bool | true | 总开关 |
| `mode` | enum | active | `passive` 仅被动审计；`active` 软阈值触发主动复测 |
| `passive_only` | bool | false | 完全禁用主动复测 |
| `hard_tps` | number | 1000 | 被动硬阈值，超过立即隔离 |
| `soft_tps` | number | 500 | 被动软阈值，低于触发复测 |
| `soft_hits_before_quarantine` | number | 2 | 复测失败后连续软命中次数 |
| `max_consecutive_errors` | number | 5 | 连续错误隔离阈值，0 禁用 |
| `quarantine_duration` | duration | 30m | 隔离时长 |
| `recovery_delay` | duration | 2m | 健康复测后额外恢复延迟 |
| `min_healthy` | number | 3 | 最低健康凭证数保护 |
| `probe_model` | string | grok-3 | 复测模型 |
| `probe_prompt` | string | 固定 | 复测 Prompt |
| `probe_expected` | string | QUALITY_OK | 复测 expected marker |
| `probe_max_output_tokens` | number | 64 | 复测输出上限 |
| `probe_hard_tps` | number | 1000 | 复测硬阈值 |
| `probe_soft_tps` | number | 500 | 复测软阈值 |
| `probe_timeout` | duration | 60s | 单次复测超时 |
| `override_ttl` | duration | 30s | scheduler override 一次性 TTL |
| `monitor_interval` | duration | 10s | 审计窗口（信息性） |
| `include_reasoning_tokens` | bool | true | 输出 Token 含推理 Token（面板同口径） |

## 已知限制

- 被动审计依赖 `usage.handle` 上报的 `TTFT` 与 `Latency`；非流式请求的 TTFT 语义由宿主决定。
- 主动复测通过 scheduler override 绑定凭证；若宿主在 `host.model.*` 回调路径中不经过
  scheduler，override 不会生效，此时需依赖被动审计 + 管理端手动探测。
- 固定代理与代理池：插件按 AuthID 隔离，不区分出口节点；同一凭证若共享出口，隔离粒度
  为凭证级（与 grok2api 补丁的节点级隔离略有差异）。

## 许可

MIT。本插件移植自 [grok2api-egress-enhancements](https://github.com/lij768423-svg/grok2api-egress-enhancements)
的设计思路，非 grok2api 官方发行版。
