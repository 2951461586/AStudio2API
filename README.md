# AStudio2API

把本机 **AStudio**（讯飞星辰 / Astron Studio）的上游模型服务，逆向复刻成一个
**OpenAI / Anthropic 兼容的 Go 网关**。

复用桌面端已登录的会话凭据，无需另申请密钥；纯 Go 标准库实现，单文件约 9 MB，
零外部依赖。

```text
┌──────────────┐   /v1/chat/completions   ┌─────────────┐   Bearer <model key>   ┌──────────────────────────────┐
│ 客户端        │   /v1/responses          │ AStudio2API │  ───────────────────▶  │ maas-api.cn-huabei-1.xf-yun  │
│ NewAPI / pi  │ ───────────────────────▶ │  (本网关)    │  ◀───────────────────  │ /v1/chat/completions         │
│ Claude Code  │   /v1/messages           │             │      SSE 流式          │ /v1/responses                │
└──────────────┘                          └──────┬──────┘                        └──────────────────────────────┘
                                                 │ 自动读取
                                                 ▼
                                    AStudio Data/userdata/astron-session.json
                                    AStudio Data/runtime/acode-home/config.toml
```

## 一、特性

### 1.1 协议

- `/v1/chat/completions`、`/v1/responses`（Codex）原生透传
- `/v1/messages` 完整 Anthropic 双向转换，含 `tool_use` 流式 `input_json_delta`
- 模型支持别名、slug、目录 ID 三种写法，可自定义别名

### 1.2 账号

- 手机号验证码登录（GeeTest 由浏览器解题）
- 桌面端会话一键导入 / 手动录入凭据
- 多账号轮询，按积分余额降权，401 / 429 / 5xx 有界重试换号
- Bearer 凭据定时保鲜，失效即时重取重放

### 1.3 权益

- 积分、会员、待领弹窗总览
- 代领每日签到、运营弹窗、客户端下载奖励、Beta 资格，支持兑换码

### 1.4 可观测

- 真实 token 用量，含缓存命中与思考 token
- 请求日志只记元信息，不存对话内容
- 单页控制台：账号 / 密钥 / 模型 / 统计 / 日志 / 权益 / 设置

### 1.5 部署

- `docker compose up -d` 即可运行
- 状态单文件持久化，配置项可用环境变量覆盖
- 无密钥时拒绝服务，CORS 默认仅放行本机，登录带指数退避限流

### 1.6 上游错误原样透传

网关不替上游做裁决。上游返回什么状态码、什么错误体，就原样回给客户端：

```jsonc
// 账号没有该模型的权益时，网关原样回传 HTTP 403
{"error":{"code":11200,
  "message":"no valid authorization: the account lacks an active order, has insufficient quota, or the order is invalid",
  "type":"permission_error"}}
```

> 代价是错误文案保持上游原样（包括英文），换取的是“所见即上游”的可诊断性。

---

## 二、逆向结论：可以，且链路很短

AStudio 本身就是一个"**Electron 外壳 + 本地 Go/Node 服务 + Acode 内核**"的三层结构。
真正调用大模型的是最内层的 Acode 内核（`@iflytek/astron-code-prod`，一个 Codex 分支），
它只是往一个标准的 OpenAI 兼容上游发请求。

因此**不需要伪造签名、不需要破解加密、不需要驱动桌面客户端**——
只要拿到那一个 `Bearer` 凭据（桌面应用本来就以明文存在磁盘上），就能直接调用上游。

### 逆向产物对照表

| 环节 | 逆向结果 |
| --- | --- |
| 登录 / 鉴权 | `POST https://agent.xfyun.cn/xingchen-studio/...`（短信 / 密码 / 微信 SSO） |
| 会话落盘 | `<AStudio Data>/userdata/astron-session.json`（**明文 JSON**） |
| 模型凭据换取 | `GET {workspace}/bot/models/configs` + Cookie → `data[].api_key` |
| 凭据格式 | `<32位hex>:<base64>`，即 `modelBearerToken` |
| 凭据投影 | 写入 `<AStudio Data>/runtime/acode-home/config.toml` 的 `[model_providers.astron-spark]` |
| 模型目录 | `GET {modelsBase}/models` → slug / 上下文窗口 / 思考档位 |
| **推理上游** | `https://maas-api.cn-huabei-1.xf-yun.com/v1` |
| **推理协议** | `wire_api = "responses"`，同时兼容 `/chat/completions` |
| 客户端标识 | `clientType: 21`(Windows) / `22`(macOS)、`studioVersion: 3.4.4` |

### 关键代码位置（`app.asar` 解包后）

| 事实 | 文件 | 说明 |
| --- | --- | --- |
| 上游 Base URL | `apps/server/dist/index.mjs` | `const ASTRON_MAAS_BASE_URL = "https://maas-api.cn-huabei-1.xf-yun.com/v1"` |
| 凭据换取 | 同上 | `fetchAstronModelCredential()` → `bot/models/configs` |
| Cookie 构造 | 同上 | `cookieHeader()` → `ssoSessionId` / `sso_sessionid` / `account_id` / `token` |
| 内核注入 | 同上 | `model_providers.astron-spark.*` 一串 `configOverrides` |
| 客户端标识 | 同上 | `resolveAstronStudioClientType()` |

> 上游 `/v1/chat/completions`、`/v1/responses`、SSE 流式、`tools`、`reasoning_effort`、
> `stream_options.include_usage` 全部已实测可用。`/v1/models` 为空，模型清单要走
> `bot/models/configs` 或 `model-manager/models`。

---

## 三、支持的模型

模型目录由上游动态返回，**随账号权益变化**，下表为实测快照。

### 3.1 实测可用

| 显示名 | Slug | 上下文 | 思考档位 | 来源 |
| --- | --- | ---: | --- | --- |
| Auto | `astronclaw-auto` | 1M | `none` / `high` | 桌面端目录 |
| Spark-X2.5 | `spark-x2.5` | 256K | `none` / `high` | 桌面端目录 |
| GLM-5.2 | `xopglm52` | 1M | `none` / `high` / `max` | 桌面端目录 |
| DeepSeek-V4-Pro | `xopdeepseekv4pro0813` | 1M | `none` / `high` / `max` | 桌面端目录 |
| DeepSeek-V4-Flash | `xopdsv4flash0731in` | 1M | `none` / `high` / `max` | 桌面端目录 |
| DeepSeek-V4-Pro | `xopdeepseekv4pro` | — | — | 账号配置 |

> 前五个来自 `model-manager`（桌面端实际展示的模型），第六个来自 `bot/models/configs`。
> 同一模型在两处 slug 不同，网关都接受。

### 3.2 目录可见但需权益

以下模型在 `bot/models/configs` 里可见，但账号缺少对应权益，上游直接拒绝：

| 显示名 | Slug | 倍率 | 上游响应 |
| --- | --- | ---: | --- |
| GLM-5.1 | `xopglm51` | ×2.0 | `403` 账号无有效订单/额度 |
| Kimi-K2.6 | `xopkimik26` | ×2.0 | `403` |
| MiniMax-M2.5 | `xminimaxm25` | ×1.0 | `403` |
| Qwen3.6-35B-A3B | `xopqwen36v35b` | ×1.0 | `403` |
| Spark-X2-Agent | `xsparkx2agent` | ×2.0 | `403` |
| Spark-X2-Flash | `spark-x` | ×0.5 | `400` no category route found |

网关不替上游裁决，原样透传状态码与错误体，并在 `/v1/models` 用 `astron.source`
字段区分来源（目录类模型排序靠后）。购买套餐后即可变为可用。

### 3.3 模型命名

同一个模型有三种等价写法，`/v1/chat/completions` 的 `model` 字段填任意一种都能路由：

| 写法 | 示例 | 来源 |
| --- | --- | --- |
| 显示名 | `GLM-5.2` | 桌面端 UI 上看到的名字 |
| Slug | `xopglm52` | 上游推理接口的模型 ID |
| 目录 ID | `lm_glm52` | `bot/models/configs` 的稳定 id |

还可在控制台「设置 → 模型别名」里自定义，格式 `别名=目标`，例如 `glm=xopglm52`。

---

## 四、签到与多账号保活

### 4.1 关于「签到」：形态和 Qoder 不一样

星辰侧**没有** Qoder 那种 `GET /sash/api/v1/me/campaigns` → `claim` 的签到接口。
它的「每日签到」是**运营弹窗下发**的，官方客户端在用户关闭弹窗时领取：

```text
GET  {workspace}/client-popups/pending
  -> [{ componentType: "DAILY_REWARD_DIALOG", popupId, instanceKey, payload, ... }]
POST {workspace}/client-popups/complete   {popupId, instanceKey}
```

`componentType` 只有三种会被客户端处理（对齐 bundle 里的 `SUPPORTED_COMPONENT_TYPES`）：

| componentType | 含义 |
| --- | --- |
| `DAILY_REWARD_DIALOG` | **每日签到奖励** |
| `NEW_USER_DIALOG` | 新人奖励 |
| `CLIENT_DOWNLOAD_REWARD_DIALOG` | 客户端下载奖励（对应 `client-download-reward/claim`） |

网关的代领逻辑就是复刻官方客户端的同一动作，因此**不需要伪造任何签名**。

### 4.2 可用的权益接口（全部实测通过）

| 接口 | 方法 | 用途 |
| --- | --- | --- |
| `points/balance` | GET | 积分余额（通用 + Spark，含各类来源与到期时间） |
| `points/summary` | GET | 积分摘要 |
| `membership/me` | GET | 会员套餐（`TRIAL`/`体验版` 等） |
| `client-popups/pending` | GET | 待领取弹窗 |
| `client-popups/complete` | POST | 完成/领取弹窗 |
| `client-download-reward/claim` | POST | 领取下载奖励 |
| `beta/claim/eligibility` | GET | Beta 资格 |
| `beta/claim` | POST | 领取 Beta（需域账号） |
| `redeem-codes/redeem` | POST | 兑换码兑换 |

> 实测数据：`points/balance` 返回 `totalBalance` / `sparkTotalBalance` / `activityNextExpireTime` 等；
> `membership/me` 的 `uid` 是**数字**而非字符串（网关已用宽松标量类型兼容）。

### 4.3 诚实记账：报告真实增量

`client-download-reward/claim` 之类的接口即使**今日已领**也可能返回成功。
网关因此会在领取前后各取一次余额，用 **`points_delta`** 报告真实到账的积分：

- 有增量 → `签到成功：+N 积分`
- 无增量但调用成功 → `调用成功但积分无变化（可能今日已领）`

不会把「接口返回 200」谎报成「签到成功」。

### 4.4 多账号轮转保活

| 机制 | 说明 |
| --- | --- |
| **凭据刷新** | 用会话 Cookie 重新换取 `modelBearerToken`，401 时即时刷新并重放 |
| **状态保活** | 定时拉取积分/会员/弹窗，同时让 Cookie 保持活跃 |
| **余额感知轮转** | 积分与 Spark 均为 0 的账号自动降权；两轮回退策略保证不会硬阻塞请求 |
| **失败转移** | 401 / 429 / 5xx 自动换号重试（有界 3 次），失败账号进入短冷却 |
| **自动签到** | 每日指定小时运行（默认关闭），按账号分别记录结果 |

调度器每分钟读一次设置，**面板改动无需重启即时生效**；所有后台任务挂在
一个可取消的根 context 上，关机时立即中止在途请求。

### 4.5 相关设置

| 键 | 默认 | 说明 |
| --- | --- | --- |
| `auto_checkin` | `false` | 每日自动签到 |
| `checkin_hour` | `9` | 自动签到时间（0-23） |
| `keepalive_minutes` | `0` | 保活间隔分钟数，0 = 关闭 |
| `balance_aware_rotation` | `true` | 余额感知轮转 |
| `checkin_complete_popups` | `true` | 代领运营弹窗（含每日签到） |
| `checkin_claim_download_reward` | `true` | 领取客户端下载奖励 |
| `checkin_claim_beta` | `true` | 领取 Beta 资格（需填域账号，否则自动跳过） |

---

## 五、快速开始

### 5.1 本机运行

```bash
go build -ldflags="-s -w" -o astudio2api .
./astudio2api
```

默认监听 `0.0.0.0:10086`，控制台在 <http://127.0.0.1:10086/>，默认密码 `admin`。

启动日志会打印自动导入结果：

```text
2026/01/01 09:00:00 imported AStudio account 12345678901 (12345678901)
2026/01/01 09:00:00 AStudio2API listening on http://0.0.0.0:10086
```

### 5.2 Docker 部署

镜像只打包一个静态 Go 二进制（≈9 MB），构建阶段不需要联网拉依赖
（纯标准库，`go.mod` 无 require）。

```bash
# 1) 启动
ASTUDIO_ADMIN_PASSWORD='换成强密码' docker compose up -d --build

# 2) 看日志
docker compose logs -f

# 3) 打开控制台
#    http://<服务器IP>:10086/
```

也可以直接 `docker run`：

```bash
docker build -t astudio2api:latest .

docker run -d --name astudio2api --restart unless-stopped \
  -p 127.0.0.1:10086:10086 \
  -v "$PWD/data:/data" \
  -e ASTUDIO_ADMIN_PASSWORD='换成强密码' \
  -e TZ=Asia/Shanghai \
  astudio2api:latest
```

要点：

| 项 | 说明 |
| --- | --- |
| 状态卷 | `./data:/data`，`ASTUDIO_DATA_PATH=/data/astudio2api-data.json` |
| **卷里含凭据** | `astudio2api-data.json` 等同账号密码，**不要提交或分享** |
| 端口 | compose 默认绑 `127.0.0.1`；要局域网访问改成 `"10086:10086"` |
| 时区 | `TZ=Asia/Shanghai`，否则自动签到的「小时」会按 UTC 算 |
| 出网 | 容器需能访问 `agent.xfyun.cn` 与 `maas-api.cn-huabei-1.xf-yun.com` |
| 健康检查 | `GET /ping`（免鉴权，不查账号池） |
| 浏览器侧 | GeeTest 脚本由**浏览器**加载，容器本身不需要访问 `static.geetest.com` |
| 桌面导入 | 容器里没有 AStudio 桌面端，**请用手机号验证码登录**添加账号 |

> 在容器内运行时，`ASTUDIO_ADMIN_PASSWORD` 每次启动都会覆盖控制台里保存的密码。

### 5.3 添加账号

两种方式，按场景选：

#### 方式一：手机号验证码登录（推荐，Docker 下唯一可用）

控制台 → 账号 → 「手机号验证码登录」：填手机号 → 点「发送验证码」
（弹出 GeeTest 滑块，在本页浏览器完成）→ 填短信码 → 「登录并添加账号」。

背后走的是与官方客户端完全一致的链路：

```text
GET  {authBase}chat/gee-captcha                取 GeeTest v3 配置
POST {authBase}login/mobile/send-verify-code   multipart，需已解题的验证码
POST {authBase}login/phone-quick-login         multipart，不需要验证码
POST {authBase}tenant-app/v2/init-app          Cookie，返回 banned
GET  {authBase}userInfo                        Cookie，补齐 uid / mobile
GET  {workspace}bot/models/configs              Cookie，换取 modelBearerToken
```

**关于安全验证的实话**：`send-verify-code` 由服务端**强制**校验 GeeTest v3——
空验证码会返回 `code=20009 geetest verify failed`（已实测）。
所以验证码**必须由浏览器解题**后回传；网关不做也不应做自动解题。
面板镜像了官方客户端的初始化参数（`product:'bind'`、`https:true`、
`offline:!success`、`onReady→verify()`、30s 超时），
SDK 与官方客户端同源：`https://static.geetest.com/static/tools/gt.js`。

#### 方式二：导入本机 AStudio 登录态

控制台 → 账号 → 「一键导入」，留空自动探测，或手填数据目录
（如 `F:\IDE\AStudio Data`）。仅适用于本机有桌面端的场景。

### 5.4 调用

```bash
# 先在控制台「API 密钥」建一个 sk-...
curl http://127.0.0.1:10086/v1/chat/completions \
  -H "Authorization: Bearer sk-xxxxxxxx" \
  -H "Content-Type: application/json" \
  -d '{"model":"GLM-5.2","messages":[{"role":"user","content":"你好"}],"stream":true}'
```

### 5.5 客户端接入

| 客户端 | Base URL | 备注 |
| --- | --- | --- |
| OpenAI SDK / NewAPI | `http://127.0.0.1:10086/v1` | 模型填 `xopglm52` 或 `GLM-5.2` |
| Codex / pi | `http://127.0.0.1:10086/v1` | 走 `/v1/responses`，原生支持 |
| Claude Code | `http://127.0.0.1:10086` | `ANTHROPIC_BASE_URL`，走 `/v1/messages` |

---

## 六、端点

### 6.1 推理接口

鉴权：`Authorization: Bearer <key>` 或 `x-api-key`。未创建任何密钥时，控制台密码可临时充当密钥。

| 端点 | 方法 | 说明 |
| --- | --- | --- |
| `/v1/chat/completions` | POST | OpenAI Chat Completions，流式/非流式 |
| `/v1/responses` | POST | OpenAI Responses API（Codex） |
| `/v1/messages` | POST | Anthropic Messages API |
| `/v1/models` | GET | 模型清单（含 `astron` 扩展：显示名、上下文、思考档位、来源） |

### 6.2 探活接口

| 端点 | 方法 | 说明 |
| --- | --- | --- |
| `/health` `/healthz` | GET | 健康检查，含账号/模型就绪计数（免鉴权） |
| `/ping` | GET | 纯文本 `pong`，不查账号池（免鉴权，供容器探针） |
| `/` | GET | 控制台页面 |

### 6.3 控制台接口

除 `login` 外均需面板登录会话。

| 端点 | 方法 | 说明 |
| --- | --- | --- |
| `/admin/api/login` | POST | 面板密码登录 |
| `/admin/api/logout` | POST | 退出登录 |
| `/admin/api/state` | GET | 全量状态（账号 / 密钥 / 模型 / 统计 / 日志 / 权益） |
| `/admin/api/keys` | GET / POST / DELETE | API 密钥增删与启停 |
| `/admin/api/accounts` | POST | 账号管理：`import` / `manual` / `refresh` / `toggle` / `domain` / `remove` |
| `/admin/api/login/geetest` | GET | 取 GeeTest 配置（添加账号用） |
| `/admin/api/login/sms` | POST | `{mobile, geetest_challenge, geetest_validate, geetest_seccode}` |
| `/admin/api/login/verify` | POST | `{mobile, verify_code}` 登录并加入账号池 |
| `/admin/api/checkin` | POST | 签到；`{id}` 指定单账号，空对象则全部账号 |
| `/admin/api/keepalive` | POST | 保活；同上 |
| `/admin/api/redeem` | POST | `{id?, code}` 兑换码 |
| `/admin/api/benefits` | GET / DELETE | 权益事件日志 / 清空 |
| `/admin/api/models` | GET / POST | 查看 / 刷新模型目录 |
| `/admin/api/logs` | GET | 请求日志（`?limit=`） |
| `/admin/api/stats` | GET / DELETE | 统计 / 清空 |
| `/admin/api/settings` | POST | 更新设置 |
| `/admin/api/password` | POST | 修改面板密码 |

---

## 七、配置

优先级：**命令行参数 > 环境变量 > 控制台设置 > 默认值**。

| 参数 | 环境变量 | 默认 | 说明 |
| --- | --- | --- | --- |
| `-host` | `ASTUDIO_HOST` | `0.0.0.0` | 监听地址 |
| `-port` | `ASTUDIO_PORT` | `10086` | 监听端口 |
| `-data` | `ASTUDIO_DATA_PATH` | 可执行文件同目录 `astudio2api-data.json` | 状态文件 |
| `-password` | `ASTUDIO_ADMIN_PASSWORD` | `admin` | 控制台密码 |
| `-astron-dir` | `ASTUDIO_DATA_DIR` | 自动探测 | AStudio 数据目录 |
| `-sync-once` | — | — | 只刷新一次模型目录后退出 |

控制台内还可调：上游 Base URL、模型目录 API、工作区 API、`studioVersion`、
上游并发上限、响应超时、流空闲超时、日志保留天数/条数、CORS 来源、模型别名。

### 7.1 状态文件与脱敏

所有状态（账号凭据、API 密钥、统计、日志、权益事件）存在单文件里，
路径由 `ASTUDIO_DATA_PATH` 决定：

```text
data/astudio2api-data.json      # 含账号 token / Bearer / API 密钥
```

> ⚠️ **这个文件等同于账号密码。** 它已写入 `.gitignore`，请勿提交、勿分享。

仓库里提供了一个结构相同、凭据全部为占位值的样例：

```text
astudio2api-data.example.json
```

它只是格式参考 —— 正常使用无需手工创建，首次启动会自动生成。

泄露后的处置：删掉 `data/` 重新用手机号登录，并在控制台删掉旧 API 密钥。

### 7.2 仓库里没有什么

| 不包含 | 原因 |
| --- | --- |
| `extract/` | AStudio `app.asar` 解包产物（~148 MB），含第三方版权内容，仅作本地协议比对 |
| `data/` | 真实账号凭据 |
| `astudio2api.exe` 等二进制 | 构建产物 |

---

## 八、与参考项目的对应关系

参考的三个 qoder2api 项目（Python hub / Go 桥 / Go 重写版）解决的是
「Qoder 需要 COSY 签名 + 设备指纹」这类**主动伪造**问题；
AStudio 这条链路**不需要签名伪造**，所以本项目的复杂度集中在别处：

| 维度 | qoder2api 系列 | 本项目 |
| --- | --- | --- |
| 上游鉴权 | COSY 签名 + RSA/AES 包裹 | 直接 `Bearer`，凭据桌面端已落盘 |
| 设备指纹 | 必须派生稳定指纹防关联 | 无需 |
| 凭据来源 | OAuth 设备流 / PAT | 直接读桌面端会话文件 |
| 协议转换 | Qoder 私有信封 → OpenAI | OpenAI 原生 → OpenAI 透传（+ Anthropic 转换） |
| 多账号 | 有 | 有 |
| 控制台 | 有 | 有 |

---

## 九、注意事项

- **本项目仅供本机 / 内网自用与协议研究。** 上游服务的使用受讯飞星辰服务条款约束，
  请勿公开暴露到公网或用于商业转售。
- 凭据即账号。`astron-session.json` 与 `config.toml` 里的 `modelBearerToken`
  等同于账号密码，**不要把状态文件 `astudio2api-data.json` 提交到仓库或分享**。
- 上游模型清单里会包含**当前账号无权调用**的模型（存在于 `bot/models/configs`
  但不在权益内），调用时上游会返回 403 + 业务码。网关已用 `astron.source` 字段
  区分来源，目录类模型排序靠后。
- 首次部署请立刻修改默认密码，并按需在设置里收紧 CORS 来源。

---

## 十、项目结构

```text
astudio2api/
├── main.go                       # 入口：配置、启动、自动导入、定时维护
├── internal/
│   ├── astron/
│   │   ├── session.go            # 会话文件定位与解析、Cookie 构造
│   │   ├── client.go             # 上游 HTTP 客户端（凭据换取 / 模型目录 / userInfo）
│   │   ├── account.go            # 权益接口（积分/会员/弹窗/兑换码/Beta）
│   │   └── login.go              # 手机号验证码登录（GeeTest + 短信 + 换凭据）
│   ├── registry/registry.go      # 模型目录合并、别名解析
│   ├── server/
│   │   ├── server.go             # 路由、鉴权、CORS、账号选择
│   │   ├── handlers.go           # /v1/chat/completions、/v1/responses
│   │   ├── proxy.go              # 上游转发、SSE 中继、用量提取、空闲超时
│   │   ├── messages.go           # Anthropic /v1/messages 双向转换
│   │   ├── benefits.go           # 签到 / 权益 / 多账号保活
│   │   ├── login.go              # 手机号登录的 HTTP 面
│   │   └── admin.go              # 控制台 API
│   ├── store/store.go            # 状态持久化、账号池、统计、日志、权益事件
│   └── web/panel.go              # 单文件控制台页面（含 GeeTest 登录）
├── Dockerfile                    # 多阶段构建，静态二进制 + alpine 运行时
├── docker-compose.yml
├── .dockerignore / .gitignore
├── astudio2api-data.example.json # 脱敏状态文件样例
├── LICENSE
└── README.md
```
