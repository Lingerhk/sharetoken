# ShareToken 文档

[English](../README.md) | **简体中文**

面向最终用户的指南（安装应用、登录、共享模型）随门户发布在 <https://sharetoken.app/docs/>。这里的文档写给运行网络或参与代码开发的人。

| 文档 | 内容 |
|---|---|
| [architecture.md](architecture.md) | 四个角色、关键设计、数据库、诚实的边界 |
| [security.md](security.md) | 谁在什么事情上受信任、各项保证及其机制、边界、报告漏洞 |
| [guide.md](guide.md) | 参考：用户命令、运维命令、客户端的本机 API、门户的 HTTP API |
| [self-hosting.md](self-hosting.md) | 用 Docker Compose 运行完整的网络 |
| [configuration.md](configuration.md) | 中继、协调器和门户的配置文件 |
| [providers.md](providers.md) | 出售托管 API：厂商类型、API key 和 OAuth 登录 |
| [apis.md](apis.md) | 三种模型接口（chat completions、Messages、Responses）：透传、转换、匹配、计费 |
| [tailnet.md](tailnet.md) | NAT 后的买家和卖家之间的直连隧道：工作原理、设置、隧道里跑 QUIC 还是 TCP、实测结果、边界 |
| [desktop.md](desktop.md) | 桌面应用：结构、本机 API、托盘、打包 |
| [mail.md](mail.md) | 邮件服务：验证码、投递方式、DNS |
| [versioning.md](versioning.md) | 发布版本、接口版本、升级规则、升级提醒 |
| [i18n.md](i18n.md) | 官网、门户和客户端的语言 |
| [development.md](development.md) | 构建、测试、打包、约定 |

第一次阅读建议的顺序：先读 [architecture.md](architecture.md)，再读 [security.md](security.md)，然后按 [self-hosting.md](self-hosting.md) 把它跑起来。

每一页在[上一级目录](../README.md)都有同名的英文版。以英文版为准：两者不一致时，英文版是对的。
