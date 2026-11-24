# Qoder / WorkBuddy 内置反代

参考实现：`../CLIProxyAPI-freetier2api-plugin`（<https://github.com/cxy2730/CLIProxyAPI-freetier2api-plugin>），
首次移植的源码版本为 `e90609d`。

原地址 `jiandanc/CLIProxyAPI-freetier2api-plugin` 已随作者账号从 GitHub 消失（账号与仓库均 404）。
当前维护方 `cxy2730` 自 v0.1.29 起接手，该仓库与旧仓库同源、完整历史保留（`e90609d`、`f128066`
等移植点在历史中均可查），插件 ID 仍为 `freetier`。

最近一次同步对照到 `df2812d`（v0.1.67）：移植了签到事件复用对话活跃上报并在计费域
统一剥离 `knowledgeId` / `knowledgeName`（`5d7faaf`、`7efe1cf`）、WorkBuddy 目录过滤
视频/音频生成模型与 11102 模型 24 小时隔离（`e884b56`）、请求体解码保留数字原文
（`c2c1e66`）。更早的 `c202dfd`（v0.1.56）移植过「国际版签到事件补全必填字段」，
参考方在当天数小时后即证伪：`knowledge*` 恰恰是 format validation 拒绝的对象，本仓库
因此按最新实现改为在共享上报出口剥离。

`c2c1e66` 的 SSE 心跳注释行修复本仓库无需移植——`stream.go` 本来就丢弃所有非
`data:` 行。Qoder 排队返回 503 + Retry-After 并同号等待，本仓库已在 `qoder/retry.go`
实现；参考方强调的「排队不换号」是否在网关层完全等价取决于账号 failover 配置，
未另作改动。trae 续期修复（`62c4f9f`）不适用（本仓库没有 trae 反代）。内嵌
`runtime-info` 二进制（`387c658`）、插件宿主自己的控制台 / CI / ABI 契约
（`53e6c33` 等）不在移植范围内，理由见下文。

协议适配位于 `internal/reverse/`，保留来源的 MIT 许可与说明。

## 接入与账号参数

在「添加账号」的反代区域选择 Qoder CN / Global 或 WorkBuddy CN / Global。
四个平台使用 `reverse` 账号类型；区域决定上游域名、模型目录、活动接口，凭据不跨区使用。

创建和编辑账号均提供「模型限制」，可同步上游模型、选择白名单、手动添加模型或设置请求名到上游名的映射。
白名单与映射按本项目既有规则合并保存为 `credentials.model_mapping`；全部清空则允许所有模型。
反代平台不预填其他供应商的静态模型。创建时手动凭据和网页登录票据均支持模型预览，使用所选代理，
预览不会消耗登录票据或写入临时账号；创建成功后按已有机制保存同步的模型能力。

可通过浏览器授权、手动令牌或「导入凭据 JSON」创建账号。JSON 导入兼容插件的
扁平字段、WorkBuddy 的 `auth` / `account` 对象和 Qoder 导出条目的 `secret`。
已标注供应商或区域的导入条目会校验是否与所选平台相符。也可通过管理 API
传入对应结构，保存时归一为本项目的账号凭据字段。

| 平台 | 每账号字段 | 用途 |
| --- | --- | --- |
| 通用 | `access_token`、`refresh_token`、`uid` | 上游鉴权与用户身份 |
| Qoder | `machine_token`、`machine_type`、`machine_code`、`machine_id`、`user_type`、`desktop_version` | 活动机器身份、会话参数和签到客户端版本 |
| WorkBuddy | `device_token`、`machine_id`、`session_id`、`machine_salt` | 计费、活动与事件链的设备参数 |
| WorkBuddy | `domain`、`enterprise_id`、`nickname` | 账号所属域与企业额度 |
| WorkBuddy | `user_agent`、`client_version`、`cli_version`、`client_name`、`desktop_version` | 客户端请求头与桌面事件版本 |

Qoder 的 `qoder_machine_token` / `qoder_machine_type` / `qoder_machine_code` /
`qoder_desktop_version` 等插件字段名可作为导入别名。`machine_token` 与
`machine_type` 必须成对配置。签到仅发送账号提供的原始配对；未配置时不发送机器身份头。
对话协议在无覆盖值时沿用来源的稳定设备标识派生，不能用它替代签到所需的原始配对。

登录、重新授权和刷新都不会覆盖已保存的机器参数：登录只回传令牌与账号资料，
刷新只回传令牌对，两者都经合并写入，已有字段原样保留。

插件 v0.1.29 起内嵌了官方 `runtime-info` 二进制（三平台各约 650 KB），在没有安装
官方客户端的机器上现场签发 `machine_token` / `machine_type`，登录时一并落盘。本项目
**不移植**这条路径：随服务端分发不透明的第三方可执行文件，并且要在服务器上运行它
以读取本机指纹，与本项目的依赖与信任模型不符。需要活动资格的账号仍按上面的方式
手工填写机器身份（见 `checkin.go` 的错误提示）。

WorkBuddy 新账号默认生成独立的 `machine_salt`，用于稳定派生未显式配置的机器和会话标识。
现有账号不会因为编辑表单、令牌刷新或重新授权而重新生成盐值。普通对话按来源的 CLI
头矩阵省略设备头；计费、模型探测与各类任务使用对应的头矩阵和账号参数。

出站客户端画像按区域取值，不再共用一套常量：CN 为 5.8.3 / CLI 2.179.0、UA 平台段
`WorkBuddy`、`X-Private-Data: false`；Global 为 5.8.4 / CLI 2.161.0、UA 平台段
`workbuddy-ai`、`X-Private-Data: true`。CN 的对话域 origin 是 `workbuddy.cn`，计费域
仍是 `codebuddy.cn`。账号可在凭据里覆盖版本号。

若某区域的具体取值被上游换掉而出现 11128，第一手段是**在账号凭据里填 `user_agent`**：
它完整覆盖出站 UA（含平台段与两个版本号），无需改代码或重新发版。取值应以当时的
官方客户端抓包为准；本仓库记录的值来自参考实现的实测结论，没有在本环境复验过。

对话请求保留 `Origin` / `Referer` / `Accept-Language`，只剔除设备派生标识
（`X-Device-Token` / `X-Machine-ID` / `X-Session-ID`）。会话头族要求
`X-Conversation-ID` 为带连字符的标准 UUID，调用方提供的其它形态会被替换而不是透传；
`X-Root-Request-ID` 与当轮 `X-Conversation-Request-ID` 同值。链路头 `b3` 为三段式，
不发送 `X-B3-ParentSpanId`。以上都是上游 11128 渠道校验的输入。

敏感字段不回传明文。编辑时留空保留已有值，Qoder 可显式清除已保存的机器身份。
重新授权、自动刷新和手动刷新合并凭据时保留机器参数。网页登录、模型调用、额度查询与任务
均使用账号所选代理；创建账号前的登录使用表单中所选代理。

## 会话与出站身份

Qoder 的 `session_id` 取自请求体里的会话标识：`metadata.conversation_id`、顶层
`conversation_id`、`prompt_cache_key`，最后退化为 system + 首条 user 的哈希。
同一对话的连续请求因此落在同一个会话里，`request_id`、`chat_record_id` 与
`request_set_id` 仍是每请求独立。会话 id 掺入账号身份：账号池在对话中途换账号后
是另一个会话，同一个会话不会出现在两个账号下。拿不到会话标识（正文全空）时退回
每请求随机。
派生的代价要记住：客户端改写 system prompt 就会换会话，需要稳定分组时应显式传
`metadata.conversation_id`。

`user_agent` 可在账号凭据里覆盖，对话、模型目录、额度、用户信息、jobToken 交换与签到
同时生效；只有登录流程（此时还没有账号凭据）沿用各自默认值。Qoder 表单在「高级」区
提供该输入框，也可通过导入凭据 JSON 或管理 API 设置。含控制字符或超长的值视为不可用，
退回默认值，避免一个填错的 UA 让账号连 bridge 都建不起来。不填时对话、目录、额度、用户信息与
jobToken 交换沿用 `Go-http-client/2.0`、活动接口沿用 `Qoder`——两者并不一致，
这是已知的画像差异，抓到官方客户端取值后再改默认值，而不是先猜一个。

## 文本指纹净化

WorkBuddy 与 Qoder 都支持 `extra.reverse_sanitize_fingerprints`，默认启用、每账号可关闭。
规则命中的是 harness 模板句、与渠道无关，两条反代线共用同一份规则表。
Qoder 仍然不补入官方 CLI 提示词或中性提示词：消息只取自客户端，由外部 Agent 管理身份、
工具约定和运行环境；净化只改文本，不改变这个取舍。上游服务端是否额外添加提示词无法从
本地转发代码确认。

规则文件为 `internal/reverse/textsanitize/sanitize_rules.json`，与来源逐字节一致，
WorkBuddy 与 Qoder 共用。WorkBuddy 的净化位于请求规范化最后一步；Qoder 的净化位于
`buildEnvelope` 之前、作用于源消息——envelope 会把 prompt 再派生到
`chat_context.text.text`、`chat_context.extra.originalContent.text` 和 `business.name`，
先洗源消息，这三处自然干净。两者都覆盖所有角色的文本内容、文本 parts、`reasoning` /
`reasoning_content` 和工具调用参数，包含 `content: null` 的工具调用消息。
图片数据、工具函数名和调用 ID 保留；工具定义（`tools[].description`）与 WorkBuddy 一致不做净化。

Qoder 还有一步自己的收尾：通用规则把 harness 声明改写成本渠道官方名，那对 WorkBuddy
是官方客户端、对 Qoder 只是另一个第三方 agent 名，声明结构原样保留照样会命中判定。
因此 Qoder 在通用净化**之前**先把 system / developer 消息里的 agent 声明句换成通用身份
`You are a helpful assistant.`：只改声明句（到第一个句号为止），句号之后的指令保留，
markdown 标题前缀保留；用户消息不动，因为那可能是用户自己的内容。开关仍是同一个
`reverse_sanitize_fingerprints`。实测依据来自外部反馈：删掉声明句或把 agent 改成中性
名字后，omp / kimi code 等 agent 不再被判成第三方 agent。

完整的工具参数 JSON 先解析，再净化字符串值，避免移除 header 文本时破坏引号、转义和
JSON 结构，同时保留数值精度。未完成的 JSON 片段沿用来源的文本净化路径。
规则命中的客户端固定模板会发生改写；账号可关闭此功能以透传原文本。

规则表覆盖 Claude Code、Codex CLI 与 DeepSeek Harness（DSH）三类 harness 的模板句：
上游对 system/assistant 正文做逐字匹配，命中 DSH 内核身份句即返回 11128。部分 DSH
构建会在 `DeepSeek` 与 `Harness` 之间插入零宽字符（U+200B）规避字面量改写，因此
指纹预检命中后会先清理零宽字符再套用改写表；未命中特征的正文原样透传。剥离型正则
只吃掉水平空白、不吃换行，避免 system prompt 被压成一行。

品牌改写的目标是本渠道官方名 `WorkBuddy`。仅改字符分隔（`DeepSeek-Harness`）不够：
上游归一化后仍按短语匹配。除品牌句外，DSH 的 `@` 路径工作区约定也是独立触发点——
实测只替换品牌后仍返回 11128，直到把 `workspace root` 改写成 `workspace` 才通过。
改写表因此同时包含这一条；规则整体幂等，重复净化不会继续改动文本。

WorkBuddy 提示词支持透传、替换与追加；上游内容策略拒绝后，替换/追加模式按账号隔离保存运行期
的降级标记，后续请求使用来源的简短提示词。渠道校验错误 11128 保留上游说明并作为
请求错误返回，不冒充官方客户端证明。

## 网关与模型

复用本项目的 `/v1/chat/completions`、`/v1/responses`、`/v1/messages` 转换链，支持
流式和非流式响应、工具调用、推理内容及 token/cache usage 的提取。
用户限额、订阅、分组调度、计费与请求日志仍由现有网关负责。

Qoder 从区域模型目录建立公开模型名到上游 key 的索引，并在首段内容发出前处理
排队提示和瞬时错误。排队默认最多等待两次，每次最多 90 秒；取消请求会终止等待。
已经返回内容的流不会重放，避免重复输出。

WorkBuddy 合并 IDE UA、CLI UA 和企业目录三路模型探测，补齐来源的静态能力表，
保存上下文、输出上限、推理档位、图片/工具能力与积分倍率。缓存按账号凭据和代理隔离。
Qoder 兼容 `context_config` 的默认配置及 `max_input_tokens` 回退，按来源补齐默认输出上限。
Qoder 的目录按上游 `model/list` 全部分类读取：`assistant` / `developer` / `chat` 为
主流分类，全量保留（不受 `enable` 影响，该标记在这些分类上并不表示不可调用）；
`app` / `qwork` / `qwake` / `inline` / `quest` / `experts` / `nap` / `byok_*` 为产品线
投影，只补 `enable=true` 的条目——QoDuck 只存在于 `app` 且限免，漏读就永远拉不到。
投影里被跳过的场景专属 key 记为 `other_category`，不是 `disabled`。
模型能力在同步时映射为本项目的上下文、模态与推理字段，完整反代目录和积分倍率另存于账号快照。
11102 / 6004 使用本项目已有的模型级限流与故障切换，不影响该账号其他模型。

上游积分倍率作为模型元数据保存，不自动换算成美元或覆盖管理员已有的定价。

## 额度、签到与任务

账号操作菜单的「额度与任务管理」打开编辑页中的管理入口。
查询上游额度与本项目的消费限额独立：Qoder 展示套餐 / 拓展包 / 共享资源包，
WorkBuddy 展示个人资源包或企业用量，保留企业额度的小数精度。
账号列表的「用量」列直接展示这些上游额度，包括已用、总额、剩余和到期时间。
优先读取已保存快照；缺失或超过 5 分钟时自动查询，可通过单行按钮或列表刷新强制更新。
同一账号查询合并，页面最多并行查询 4 个反代账号，刷新失败保留上次额度并显示错误。
Qoder 按「谁还有余额」排定额度桶的输出顺序，摘要与分组同序：团队账号套餐席位用尽
（0/3000）时页头取仍有余额的共享资源包，而不是停在已用尽的桶上。全零额度桶会被过滤，
但所有桶都为空时保留一个，好让「已经用光」仍然可见；总额为 0 的数据不计成百分比。
Qoder 到期时间兼容秒与毫秒。

签到状态按实际上游结果记录，未领取、未确认和无活动不会写成成功。
Qoder 按 UTC+8 每天 10:00 的活动窗口判断归属；自动签到自 10:05 后运行。
可领但成就未完成的活动不会被选为当前轮——上游会拒绝领取，且会让同一列表中今天
已领的每日活动被覆盖成误报失败。
WorkBuddy CN 使用每日签到接口；Global 走活动信标与热力图链路，明确提示奖励次日到账。
Global 的连续点亮天数取自上游连登接口，查不到时保留上一次的值；今日是否已点亮按日期
匹配，不把热力图的最后一格当成今天。
企业 CN 账号没有签到收入路径，会显示不支持而不发起无效签到。

WorkBuddy 已移植的任务包括：成长任务列表/报名/领奖、小程序任务、真实桌面对话辅助、
专家/模板/技能/画布等事件链、旅行、活跃上报、夜间任务、开学季、补签与连登兑换/抽奖，
以及国际版每日活跃奖励。来源未提供自动完成动作的任务保留原限制。

新增自动任务与自动续期默认关闭。开启自动任务后，可按账号选择任务族，并配置
0–23 的 UTC+8 小时数；留空使用来源默认排程。签到、任务扫描和手动执行均独立可用。

| 任务族 | 默认执行时间（UTC+8） |
| --- | --- |
| 成长任务、旅行 | 09:00、21:00 |
| 活跃上报 | 10:00 |
| Token 保活 | 22:00 |
| 夜间任务 | 23:00 |
| 开学季、连登奖励 | 当日签到成功后 |
| Global 每日活跃奖励 | 08:00、12:00、16:00、20:00 |

自动续期也可单独启用，默认 22:00 执行。Qoder 的设备令牌沿用来源的有效性检查逻辑；
PAT / refresh-token 使用 jobToken 交换。WorkBuddy 使用 refresh token 续期。
失效且无法续期的账号仍需重新授权。

部分任务会发起真实模型请求，消耗该账号的上游额度。任务内部的模型调用用于完成账号
任务，不属于普通用户的网关请求，不计入普通用户账单。页面扫描不会自动执行任务。
手动与自动账号任务互斥，失败与部分完成结果持久化到账号 extra。每个排程时段最多执行一次，
调用上游前先持久化对应时段，失败后可手动重试，避免对已经产生副作用的步骤反复补跑。
使用项目已有的 Redis 锁与 PostgreSQL 锁回退协调多个实例；时段校验与执行共用账号操作锁。
夜间任务和补签固定采用 UTC+8。已达标的开学季任务直接领奖，不重复生成模型对话。
未确认计分显示 pending，网络错误、领奖失败和部分完成保留在结果中。服务停止会取消等待中的任务。

签到每个账号每天只跑一次，因此单次限流就会丢掉当天奖励：上游返回 429 / 502 / 503 / 504
时按指数退避最多重试 3 次，上游给了 `Retry-After` 就按它等待（有上限，避免超长提示把
账号挂起数小时）。重放是安全的——上游签到幂等，已领取会返回成功而不是重复发放。
400 与凭据类错误立即返回；401 仍交给调用方的刷新路径处理。

成长任务领奖前先查询再等待：任务在动作返回时多数已可领，此前每轮固定先睡 3 秒，
整族跑下来白等数十秒。任务尚未出现在列表里不算失败——上游列表最终一致，只有轮询次数
用尽才判失败，否则刚做完的任务会连奖励一起丢掉。

## 管理接口

沿用项目已有的管理员鉴权：

- `POST /api/v1/admin/accounts/reverse/login/start`：支持 `platform` / `account_id` / `proxy_id`。
- `GET /api/v1/admin/accounts/reverse/login/poll?session_id=...`：一次性登录票据或重新授权结果。
- `POST /api/v1/admin/accounts/:id/checkin`：手动签到。
- `POST /api/v1/admin/accounts/:id/refresh`：刷新凭据。
- `GET /api/v1/admin/accounts/:id/reverse/quota`：上游额度。
- `GET /api/v1/admin/accounts/:id/reverse/tasks`：WorkBuddy 任务目录、待办及上次执行结果。
- `POST /api/v1/admin/accounts/:id/reverse/tasks`：`action=run` + `code`、`action=all`，或 `action=scheduled` + `kind`。

运行态 extra（`reverse_checkin`、`reverse_model_catalog`、`reverse_quota`、`reverse_tasks`、
`reverse_task_schedule`）在表单更新时保留。任务自动执行配置为 `reverse_auto_tasks`、
`reverse_task_kinds` 和 `reverse_task_hours`；独立续期开关为 `reverse_auto_refresh`。
