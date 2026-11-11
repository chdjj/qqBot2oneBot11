# QQBot → OneBot 11 适配器

把 QQ 官方机器人平台（[QQ 开放平台 API v2](https://bot.q.qq.com/wiki/develop/api-v2/)）桥接到 [OneBot 11](https://github.com/botuniverse/onebot-11) 协议，让原本只支持 OneBot 的框架（NoneBot2、Koishi、HoshinoBot 等）可以直接接管 QQ 官方群机器人与单聊机器人。

> **本项目由 AI 编写。** 代码、界面、文档与测试全部由 AI（ds4.1f）在没有人工编写代码的情况下生成，人工只提供了需求与验收反馈。请在生产使用前自行审查代码。

## 它做什么

```
QQ 用户 ──@──▶ QQ 官方服务器 ──WebSocket──▶ 本适配器 ──OneBot 11──▶ 你的机器人框架
                                              ▲
                                      Web 控制台（配置 / 测试 / 日志）
```

- **上游**：QQ 官方机器人 v2 网关（WebSocket 长连接，op 2 鉴权、op 1 心跳、op 6 断线恢复）
- **下游**：OneBot 11 五种通信方式，事件格式为 v11 标准 JSON（消息段数组 + CQ 码字符串各一份）
- **控制台**：添加 AppID/AppSecret、启停机器人、配置监听、查看群 openid、发主动测试消息、看实时日志

## 快速开始

```bash
# 需要 Go 1.22+（本项目在 Go 1.27 上构建）
go build -o qqbot-onebot.exe ./cmd/qqbot-onebot

# 启动（Windows）
qqbot-onebot.exe -addr 127.0.0.1:8080
# Linux/macOS
./qqbot-onebot -addr 127.0.0.1:8080
```

打开 <http://127.0.0.1:8080> 即可使用控制台。数据库默认落在 `./data/qqbot-onebot.db`（SQLite 单文件，纯 Go 驱动，无需 CGO）。

命令行参数：

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `-addr` | `127.0.0.1:8080` | 控制台监听地址；对外网开放时务必加防火墙或反向代理 |

## 安全说明

控制台 API **本身不带登录**，默认只监听 `127.0.0.1`。它能够修改机器人凭据、读取群 openid、发送消息，因此：

- 不要把 `-addr` 改成 `0.0.0.0` 后直接暴露到公网。若必须远程访问，请用反向代理加 HTTP 基本认证，或通过 SSH 隧道。
- AppSecret 只写入、不读出：`/api/bots` 响应里的 `secret` 恒为空，并给出 `has_secret` 标记；编辑时留空表示保持原值。
- 数据库文件 `data/qqbot-onebot.db` 含明文 AppSecret，请限制其文件权限并排除在备份/版本库之外（`.gitignore` 已排除 `data/`）。
- OneBot 监听请设置 Token；反向 WS 与 HTTP 上报都会带上它。
| `-db` | `data/qqbot-onebot.db` | SQLite 数据库路径 |
| `-debug` | `false` | 输出调试日志 |

## 使用流程

1. **添加机器人**：在 QQ 开放平台创建机器人，把 AppID 与 AppSecret 填进「官方机器人」页。开发阶段可勾选「使用沙箱环境」。
2. **启动**：保存后勾选「保存后立即启动」，或回到列表点「启动」。状态变为「已连接」表示网关鉴权成功。
3. **发现群 openid**：QQ 官方不提供列群接口，群 openid 与机器人绑定。让机器人进群后，**在群里 @ 它说一句话**，适配器会自动记录该群 openid 与其数字 ID。
4. **配置 OneBot 监听**：在「OneBot 监听」页添加一种或多种通信方式，指向你的机器人框架。
5. **验证**：在「发送测试消息」页选群发送一条主动消息；或直接让框架回复群 @ 消息。

### 关于主动消息与被动回复

QQ 对主动消息有严格频控（单群每天 1000 条、未认证约 30 条/分钟）。适配器在发送 OneBot 消息时会**优先使用被动回复**：如果该群在最近 4 分钟内收到过消息，就把上一条消息的 `event_id` 作为 `msg_id` 带上，这样不占用主动消息额度。控制台的测试按钮则故意走主动消息，用来验证链路与权限。

主动消息失败返回 `40034102` 时，说明当前无主动消息权限：让群成员先 @ 机器人互动，或确认机器人认证与频控状态。

## OneBot 11 支持范围

### 通信方式

| 类型 | 行为 | 配置项 |
| --- | --- | --- |
| **HTTP 服务器** | 监听端口，`POST /` 收动作；事件以 HTTP POST 推送到「上报地址」 | 监听地址、端口、路径、Token、上报地址 |
| **HTTP + SSE** | 监听端口，`POST /` 收动作；事件从 `GET /_events` 以 `text/event-stream` 下发 | 监听地址、端口、路径、Token |
| **HTTP 客户端** | 不监听端口；事件 POST 到指定 URL | 上报地址、Token |
| **WebSocket 服务器** | 正向 WS：客户端连 `ws://host:port/`，同连接收发 | 监听地址、端口、路径、Token |
| **WebSocket 客户端** | 反向 WS：适配器主动连接指定地址，带 `X-Self-ID`、`X-Client-Role: Universal` | 连接地址、Token |

Token 校验接受 `Authorization: Bearer <token>`、`Authorization: Token <token>` 与 `?access_token=`。反向 WS 同时带 `Sec-WebSocket-Protocol: onebot.v11`。

### 事件

| OneBot 事件 | 来源 |
| --- | --- |
| `message` / `group` / `normal` | `GROUP_AT_MESSAGE_CREATE`（群内 @ 机器人） |
| `message` / `private` / `friend` | `C2C_MESSAGE_CREATE`（单聊） |
| `notice` / `group_increase` | `GROUP_ADD_ROBOT`（机器人入群） |
| `notice` / `group_decrease` / `kick_me` | `GROUP_DEL_ROBOT`（机器人被移出） |
| `meta_event` / `lifecycle` | 监听建立时下发（WS 与 SSE） |

消息同时提供 `message`（消息段数组）与 `raw_message`（CQ 码字符串），兼容两类插件写法。

### API（动作）

| 动作 | 状态 |
| --- | --- |
| `send_msg` | ✅ 按 `message_type` 路由 |
| `send_group_msg` | ✅ |
| `send_private_msg` | ✅ |
| `delete_msg` | ✅ 仅限本适配器发出的消息 |
| `get_login_info` | ✅ |
| `get_status` | ✅ |
| `get_version_info` | ✅ |
| `get_group_info` / `get_group_list` | ✅（基于本地已记录群） |
| `get_group_member_list` / `get_group_member_info` | ⚠️ 返回空（QQ 未开放） |
| `can_send_image` / `can_send_record` | ✅ |
| 其余 | 返回 `retcode 1404` |

### 消息段

- `text`、`at`（`@全体成员` 不支持）、`image`、`record`、`video`、`file` → 转换为 QQ v2 消息
- `markdown` → 以 QQ 的 Markdown 消息（`msg_type=2`）发送，需带 `content`
- 媒体段需要**可访问的 URL**（QQ 服务端会自行下载转存）；一次只能发一个媒体
- `json`、`xml`、`share`、`music` 等段不支持发送，返回明确错误
- 纯文本超过 **4000 字符**时自动按行分块续发，返回最后一块的 `message_id`

### 平台限制（影响使用）

- 撤回仅支持 **2 分钟内**发出的消息，超出返回错误码 `40064004`
- 群消息中的 `@` 在纯文本下把对方渲染为 `@昵称`；`@全体成员` 被 QQ 禁止
- 被动回复有效期 5 分钟、同一条消息最多回复 5 次；适配器取 4 分钟窗口以留余量

## ID 映射

OneBot 要求 `group_id` / `user_id` 为整数，而 QQ 官方使用字符串 openid，且**同一群/用户在不同机器人下的 openid 不同**。

- 群：适配器为每个 `(机器人, group_openid)` 维护一个稳定的数字 ID。若 openid 本身是纯数字则直接使用它，否则分配递增数字（从 100000 起）。
- 用户：同样映射（群成员从 200000 起，单聊用户单独计数）。
- 这些数字 ID 持久化在 SQLite，重启后不变；`self_id` 使用机器人真实的 QQ 号，不可用时回退为稳定负数。

所以：**在框架里用适配器给你的数字 ID 即可**，适配器发送时自动换回 openid。若换绑机器人，数字 ID 会变。

## 架构

```
cmd/qqbot-onebot/main.go      启动、命令行、内嵌控制台
internal/qq/client.go          access_token 缓存、OpenAPI 调用、媒体上传
internal/qq/gateway.go        WebSocket 网关：鉴权/心跳/重连/恢复/状态
internal/onebot/types.go      消息段、CQ 码互转、事件与动作结构
internal/bridge/app.go        多机器人运行时、控制台日志与状态
internal/bridge/events.go     QQ 事件 → OneBot 事件
internal/bridge/actions.go    OneBot 动作 → QQ API
internal/bridge/listener.go   五种传输实现
internal/store/store.go       SQLite 持久化（机器人、监听、群、ID 映射）
internal/api/*                控制台 REST API、实时 WS、内嵌前端
```

## 开发与测试

```bash
go test ./...          # 单元测试（协议转换、事件映射）
go vet ./...
```

集成测试在 `hack/` 下，用一个假 QQ 上游模拟网关与 OpenAPI：

```bash
# 终端 1：启动模拟上游
go run ./hack/mockupstream

# 终端 2：带测试环境变量启动适配器
QQBOT_API_BASE=http://127.0.0.1:19090 \
QQBOT_TOKEN_URL=http://127.0.0.1:19090/app/getAppAccessToken \
QQBOT_TEST_INJECT=1 \
go run ./cmd/qqbot-onebot

# 终端 3：先创建机器人，再运行传输测试与界面测试（需要 npm i ws jsdom）
node hack/node-test.mjs
node hack/ui-test.mjs
```

`QQBOT_API_BASE` / `QQBOT_TOKEN_URL` / `QQBOT_TEST_INJECT` 仅供测试：生产环境不要设置。`/api/__test/inject` 只在 `QQBOT_TEST_INJECT=1` 时注册。

## 已知限制

- 无 WebHook 回调接入（只实现 WebSocket 网关）。
- 不转发 QQ 频道（guild）事件；如需请扩展 intents 与事件映射（注意无权限的 intents 会导致网关拒绝连接）。
- 不支持流式消息、Markdown 自定义模板、内嵌键盘、按钮、引用回复。
- 群成员列表/成员资料需要白名单权限，当前返回空。
- 撤回仅支持本适配器发出且仍在内存索引中的消息（索引保留 1 小时）。

## 参考

- [QQ 机器人官方文档 v2](https://bot.q.qq.com/wiki/develop/api-v2/)：鉴权、网关、事件、发送消息、富媒体
- [OneBot 11 标准](https://github.com/botuniverse/onebot-11)：通信方式、事件与 API 结构

## 许可证

MIT
