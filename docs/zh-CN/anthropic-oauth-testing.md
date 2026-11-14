# Anthropic OAuth 实现测试与验收清单

更新日期：2026-11-14。适用分支：`feat/anthropic-oauth-parity`，基于 `origin/feat/develop` 的 `8c057df`；参考 sub2api `530c5e3a4`。使用和配置说明见 [providers.md](providers.md#11-anthropic-oauth-兼容配置)。

本文集中记录本次已执行的检查，以及在具备条件的环境中还需要完成的测试。**代码提交不代表真实 Anthropic 登录、订阅额度归类或完整市场结算已经通过验收。**

## 1. 当前交付范围

已实现：模型兜底与参数校验、每 provider 代理、独立 token/推理 TLS 配置、账号 UUID 与稳定设备身份、inference-only 登录、sessionKey 组织选择登录、跨进程刷新去重、完整 OAuth 请求配置、缓存断点、显式会话、思考签名往返、输入计费调整、429 冷却、CLI/本机 API/桌面配置入口。

默认仍为 `standard` 请求配置、原生 TLS。`claude-code` 和浏览器 TLS 均需要显式选择。兼容配置采用参考项目的请求结构和版本基线，通用扩充提示词由本项目独立编写。

本次不包含原生买方 `/v1/messages`、本机多账号池、手动导入任意 token、完整 thinking/effort 参数接口或新的缓存差异定价；这些是独立功能，不属于本次测试通过后即可宣称支持的能力。

## 2. 已完成检查

| 检查 | 结果 | 说明 |
|---|---|---|
| `go test ./internal/provider ./internal/node ./internal/protocol ./internal/buyer ./cmd/sharetoken` | 通过 | buyer 与 CLI 包暂无独立测试；其编译通过。其余包含既有及新增回归测试 |
| `TestProviderConfigurationAPI` 和不依赖数据库的选定 client 测试 | 通过 | 覆盖配置更新、代理密码保留/清除、登录 scope、脱敏返回，以及既有配置/来源校验 |
| `go test -run '^$' ./...` | 通过 | 编译全仓库及测试代码，不代表运行了数据库集成测试 |
| Windows amd64 CLI 交叉编译 | 通过 | 文件锁和浏览器 TLS 依赖可编译；未在 Windows 实机运行 |
| macOS arm64 CLI 交叉编译 | 通过 | 未在 macOS 实机运行 |
| 桌面 `app.js` JavaScript 语法检查 | 通过 | 未启动真实浏览器执行界面交互 |
| `git diff --check` | 通过 | 提交前还会检查文档引用和最终补丁 |
| race detector | 未执行成功 | 当前环境未启用 cgo，也未发现 C 编译器 |
| `scripts/test/test.sh ./...` | 未执行成功 | 当前环境没有 Docker/MySQL 8；之前的 client 集成测试因缺少 MySQL 直接退出 |
| 真实 OAuth / 推理 / 额度 / 市场结算 | 未执行 | 本次没有使用真实账号或生产凭据 |

本地工具链放在 `/tmp`，不属于仓库内容。在其他环境使用正常安装的 Go 即可。

## 3. 自动化回归

### 环境要求与命令

- Go 1.26.6 或满足 `go.mod` 要求的版本。
- race 检查需要目标平台支持的 cgo 与 C 编译器。
- 完整测试需要 MySQL 8。优先使用项目的 Docker 测试脚本；已有专用测试库服务时可以设置 `SHARETOKEN_TEST_MYSQL`，不要指向生产数据库。

```bash
go test ./internal/provider ./internal/node ./internal/protocol ./internal/buyer ./cmd/sharetoken
go test ./internal/client -run '^TestProviderConfigurationAPI$'

CGO_ENABLED=1 go test -race ./internal/provider ./internal/node ./internal/protocol
scripts/test/test.sh ./...

# 如已有专用 MySQL 8 服务，设置连接信息后运行
# export SHARETOKEN_TEST_MYSQL='mysql://user@host:3306/'
go test ./...

GOOS=windows GOARCH=amd64 go build -o /tmp/sharetoken-test.exe ./cmd/sharetoken
GOOS=darwin GOARCH=arm64 go build -o /tmp/sharetoken-test-macos ./cmd/sharetoken
```

### 已添加的测试与必须保持的断言

| 测试 | 主要断言 |
|---|---|
| `TestAnthropicModelCapabilities` | 受限模型的采样边界正确；强制工具不被静默降为 auto；Haiku 5.5 与未知模型不被错误限制 |
| `TestClaudeFingerprintUTF16` | 空串、短串、中文和 emoji 使用与 JavaScript UTF-16 一致的结果 |
| `TestClaudeProfileIdentityAndRequest` | 头体版本一致、九个 beta、账号 UUID 不误用组织 UUID；Fable 两块 system；请求 ID 更新、会话稳定且隔离买方 |
| `TestAnthropicCacheConversion` | content/tools 缓存标记保留、数量上限、TTL 顺序和非法 TTL 校验 |
| `TestAnthropicProfileHTTPAndBilling` | 实际 HTTP 请求带兼容配置；注入 system 的输入成本不使短请求被判为虚报；输出预算不增大 |
| `TestAnthropicThinkingHistoryRoundTrip` | 思考文本和签名能回传至下一轮；签名负载不重复计数；拒绝缺签名的历史 |
| `TestAnthropicDiscoveryDistinguishesFallback` | 兜底含 5.2 模型且标记未验证；401/403 不被兜底目录掩盖 |
| `TestProviderProxyAndIdentityPersistence` | 不同 provider 使用各自代理；身份随 token 轮换保持；拒绝重复账号；代理摘要无密码 |
| `TestTokensConcurrentRejectedAndReplacement` | 并发 401 只刷新一次；刷新不覆盖新登录；无 refresh token 的凭据可用至真实过期 |
| `TestTokenRefreshAcrossProcesses` | 两个真实子进程同时处理同一个失效 token，只有一次刷新 |
| `TestProviderLockCancellation` | 等待锁时按 context 结束，不无限等待 |
| `TestInferenceLoginOptionsAndTokenMerge` | inference-only scope 正确；其他厂商不能使用；刷新元数据按键合并而非覆盖 |
| `TestBrowserTLSAndIndependentTokenTransport` | 使用本地 TLS 服务完成证书验证；传输层不覆盖适配器的 UA/Accept；token 与推理 TLS 独立 |
| `TestTransportPatchesPreserveProxySecrets` | 只改 TLS 不抹掉代理认证；改为直连可清除代理信息 |
| `TestProviderCooldownAndRecovery` | 解析 Retry-After/request ID；冷却期间不请求上游；恢复后解除；参数错误不触发冷却 |
| `TestSessionLoginSelectsOrganizationAndKeepsCookieEphemeral` | 必须选择有权限的组织；PKCE/state 对应；Cookie 不发送给 token 端点且不持久化 |
| `TestProviderConfigurationAPI` | 本机 API 传递配置和 scope；脱敏 View 不包含 token、Cookie 或代理密码 |
| `TestPrepareUpstreamBodyEnforcesNodePolicy` | `session_id` 不泄露为普通上游字段；模型、并发扩展和预算的节点约束保持 |

对应源码位于 [provider 测试目录](../../internal/provider)、[本机 API 测试](../../internal/client/providers_test.go)、[节点请求测试](../../internal/node/request_test.go)。

## 4. 需要补跑的集成测试

以下项目需要数据库、浏览器或多平台运行环境；本次尚未验证。

| 优先级 | 场景 | 验收条件 |
|---|---|---|
| P0 | 完整 Go 测试与既有 provider 端到端测试 | MySQL 8 环境下 `scripts/test/test.sh ./...` 全部通过；其他厂商登录、API key、报价和收据行为无回归 |
| P0 | 短请求 + 注入 system + 市场结算 | 买方 → 协调器 → 卖方完整链路返回有效收据，买方确认且结算成功；SSE usage、收据和费用一致 |
| P0 | 同时裁剪输入和隐藏输出 | 两种裁剪共同发生时 `total_tokens` 正确，实报字段保留，不超预留费用 |
| P0 | 中途取消和中途上游失败 | 沿用取消/失败结算规则，无重复交付或重放推理；取消路径与最终收据口径没有异常差异 |
| P0 | 429 撤回报价与到期恢复 | 部分来源冷却时只保留可用模型；全部冷却时撤下报价；旧 Ticket 返回 busy 且不发上游请求；到期恢复 |
| P0 | OAuth 刷新与配置修改竞态 | CLI 与 daemon 并发使用时不重复轮换；重新登录/删除/改代理期间，旧请求不覆盖新凭据或配置 |
| P1 | OAuth 短模型名 + 卖方输出上限 | 短名映射为带日期 ID 后仍遵守该 provider 的 `max_output`，以及 400 学到的上限 |
| P1 | 代理、TLS 与长流 | HTTP CONNECT、SOCKS5、环境代理、NO_PROXY、直连及 token/推理 TLS 组合均按配置；取消长 SSE 及时关闭连接 |
| P1 | Windows/macOS 实机并发刷新 | 文件锁互斥生效、进程退出后锁释放、凭据文件仍是私有权限 |
| P1 | 其他上游收到带会话标记的买方请求 | OpenAI/custom 上游不收到 `session_id`；没有引入未知顶层字段导致的 400 |

测试应保留精简的脱敏失败信息和请求 ID。完整请求内容、Cookie、token、代理密码不放进提交或测试报告。

## 5. 桌面与本机 API 验收

用项目的 [gui_stack.sh](../../scripts/test/gui_stack.sh) 启动测试栈，再按 [gui_ui_test.mjs](../../scripts/test/gui_ui_test.mjs) 和 [i18n_ui_test.mjs](../../scripts/test/i18n_ui_test.mjs) 开头的环境要求运行。补充手工检查：

1. 新建 API key/OAuth provider，分别验证标准与兼容请求配置；其他厂商不显示 Anthropic 专属选项。
2. “连接设置”只修改 TLS 时，既有代理密码仍生效且未显示在界面；改为直连后请求绕过环境代理。
3. 浏览器授权的 full/inference scope 正确；重试、关闭弹窗、切换厂商和替换已有凭据正常。
4. sessionKey 登录能列组织并选择；无 Cookie、失效 Cookie、无组织、选错组织或 state 不符都给出错误；失败不覆盖既有凭据。
5. 无 refresh token 的凭据显示“需重新登录续期”，不能显示“会自动续期”。
6. 模型目录来自 fallback 时显示警告，不显示“推理已验证”；401/403 及手动指定模型测试可区分。
7. 429 冷却状态显示合理，到期和后续成功后恢复；新增文字在中文/英文界面均正确。
8. 本机 API 的 Origin/本机认证规则保持；请求和响应中的所有密码字段按既有规则处理。

新增本机 API 数据格式及路径见 [providers.md 第 6 节](providers.md#6-本机-api)。

## 6. 真实 Anthropic 验收

由账号持有者在可访问上游的测试环境执行。先记录实际生效的 profile、TLS、代理、账号 scope、模型 ID；每个场景独立标记通过/失败。

```bash
sharetoken provider login anthropic -name claude-test -profile claude-code -token-tls-profile chrome
sharetoken provider models claude-test
sharetoken provider test claude-test -model claude-sonnet-4-6
```

| 场景 | 操作与验收条件 |
|---|---|
| 浏览器 full / inference-only 登录 | 分别授权、保存、重启、推理；未授予列模型权限时，可以区分模型目录错误与推理权限 |
| sessionKey 登录 | 个人/团队组织分别选择；确认请求作用于选定组织，Cookie 不持久化 |
| 提前刷新与 401 | 临近过期时刷新；上游先拒旧 token 时只重试一次；刷新后的 account/organization/device 信息正确 |
| 模型与采样 | 对可用的 Sonnet/Haiku 5.5、Fable/Mythos 5.2 检查默认值、边界值、冲突参数、强制工具选择；验证规则是否仍符合实际服务 |
| system 与 JSON 输出 | 标准/兼容配置对比：原系统指令、JSON/schema 输出要求和工具约束是否仍满足任务 |
| 工具两轮及以上 | 请求 → tool_use → tool_result → 最终回答；完整保留 `anthropic_thinking` 后继续成功；丢弃签名的客户端记录为不支持该组合 |
| 缓存 | 使用稳定前缀重复调用，检查 cache read/write 用量和实际延迟；5m/1h 组合不被上游拒绝 |
| 会话 | 显式 session_id 下追加轮次和裁剪首句仍稳定；不同买方/不同显式 ID 分离；重启卖方后设备身份不变 |
| 额度归类 | 结合账号实际用量记录确认归类；HTTP 200、CLI 标识头或 sub2api 注释均不能替代此验证 |
| 市场交易 | 经买方本机 `/v1/chat/completions` 完成流式/非流式、工具和短 prompt 请求；验证收据、余额变化及卖方获得的 credit |

真实工具测试必须使用实际可用的账号/模型；模型存在于兜底表并不证明账号有权限。记录规则变化时，同时更新能力表和对应回归用例。

## 7. 测试记录与上线条件

每次补测记录：代码提交、操作系统、Go 版本、测试命令或场景、实际配置、结果、脱敏 request ID 和失败原因。真实上游结果需附模型和测试日期，避免长期沿用旧结论。

提交前已通过的 mock/编译检查可以支持代码审查。将兼容配置作为默认值或承诺真实订阅可用前，至少完成：完整回归、市场收据/结算、登录/刷新、工具多轮、system 语义及实际额度归类的验收。保留 `standard` 和 `native` 配置用于回退。
