# 在编程助手中使用

Claude Code 和 Codex 可以替你操作客户端。安装一次客户端自带的 skill，之后直接用自然语言提要求即可，例如“ShareToken 在运行吗？”、“我还剩多少 credit？”、“通过 ShareToken 调用 qwen2.5:7b 总结这个文件”、“把我本地 Ollama 的模型共享出去”。

skill 就是一个 `SKILL.md` 文件，告诉助手有哪些命令、本地接口在哪里，以及哪些事不能自作主张。

## 安装 skill {#install}

```bash
{{exe}} skill install
```

客户端会在本机查找 Claude Code 和 Codex，给找到的每一个装上 skill：

::: macos linux
| 助手 | 文件 |
|---|---|
| Claude Code | `~/.claude/skills/sharetoken/SKILL.md`（设置了 `$CLAUDE_CONFIG_DIR` 时在它下面） |
| Codex | `~/.codex/skills/sharetoken/SKILL.md`（设置了 `$CODEX_HOME` 时在它下面） |
:::

::: windows
| 助手 | 文件 |
|---|---|
| Claude Code | `%USERPROFILE%\.claude\skills\sharetoken\SKILL.md`（设置了 `%CLAUDE_CONFIG_DIR%` 时在它下面） |
| Codex | `%USERPROFILE%\.codex\skills\sharetoken\SKILL.md`（设置了 `%CODEX_HOME%` 时在它下面） |
:::

如果某个助手从没在这台电脑上用过，它的目录不存在，就会被跳过。仍想给它装，就指明：

```bash
{{exe}} skill install -agent claude
{{exe}} skill install -agent codex
{{exe}} skill install -agent all
```

已经打开的助手会话需要重启：skill 在会话开始时读取。

## 检查 {#check}

```bash
{{exe}} skill
```

列出两个助手是否找到、skill 是否已装、是否最新。`{{exe}} skill show` 打印 skill 本身。

写给你的这份 skill 带有本客户端的命令、数据目录 `{{home}}` 和接口地址 `http://127.0.0.1:{{port}}/v1`。如果 `{{exe}}` 不在 PATH 中，写入的是程序的完整路径。

## 助手可以做什么 {#rules}

skill 允许助手随意查看：`status`、`balance`、`jobs`、`models`、`relays`、`share`、`pair`、`config`。同时要求助手：

- 修改任何东西之前先问你：退出登录、开关共享、添加或删除模型和 provider、配对。
- 把 credit 当钱看：请求要设 `max_tokens`，大批量或长篇生成之前先问你。
- 不在命令行里写 API key，不打印你的 token。
- 向你要邮箱地址和邮件里的登录验证码。没有你，它无法登录。
- 客户端没运行时，在后台启动它，或者请你启动。

助手会遵守这些规则，但不是强制的。请保留助手自身对执行命令的确认提示。

## 升级之后 {#update}

[升级客户端](/docs/update)后再运行一次 `{{exe}} skill install`。它会替换自己先前写的那份，已是最新的则不动。

## 卸载 {#remove}

```bash
{{exe}} skill uninstall
```

从两个助手中都删除 skill。加 `-agent claude` 或 `-agent codex` 只删一个。

同一位置上不是客户端写的 `SKILL.md`（比如你自己写的）不会被替换或删除。`{{exe}} skill install -force` 会替换它。
