# Qoder / WorkBuddy 内置反代

参考实现：`../CLIProxyAPI-freetier2api-plugin`，移植时源码版本为 `e90609d`。
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

WorkBuddy 新账号默认生成独立的 `machine_salt`，用于稳定派生未显式配置的机器和会话标识。
现有账号不会因为编辑表单、令牌刷新或重新授权而重新生成盐值。普通对话按来源的 CLI
头矩阵省略设备头；计费、模型探测与各类任务使用对应的头矩阵和账号参数。

敏感字段不回传明文。编辑时留空保留已有值，Qoder 可显式清除已保存的机器身份。
重新授权、自动刷新和手动刷新合并凭据时保留机器参数。网页登录、模型调用、额度查询与任务
均使用账号所选代理；创建账号前的登录使用表单中所选代理。

## 文本指纹净化

WorkBuddy 的 `extra.reverse_sanitize_fingerprints` 默认启用，每账号可关闭。
Qoder 不增加文本指纹净化。Qoder 的消息只取自客户端，即使没有 system 或只有 developer 消息，
也不补入官方 CLI 提示词或中性提示词；保留协议封装和客户端实际提供的工具。
该行为适合由外部 Agent 管理身份、工具约定和运行环境，避免捕获模板中的 Qoder 身份与固定 Windows 路径污染请求。
上游服务端是否额外添加提示词无法从本地转发代码确认。

规则文件为 `internal/reverse/workbuddy/assets/sanitize_rules.json`，与来源一致。
净化位于请求规范化最后一步，覆盖所有角色的文本内容、文本 parts、`reasoning` /
`reasoning_content` 和工具调用参数，包含 `content: null` 的工具调用消息。
图片数据、工具函数名和调用 ID 保留。

完整的工具参数 JSON 先解析，再净化字符串值，避免移除 header 文本时破坏引号、转义和
JSON 结构，同时保留数值精度。未完成的 JSON 片段沿用来源的文本净化路径。
规则命中的客户端固定模板会发生改写；账号可关闭此功能以透传原文本。

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
模型能力在同步时映射为本项目的上下文、模态与推理字段，完整反代目录和积分倍率另存于账号快照。
11102 / 6004 使用本项目已有的模型级限流与故障切换，不影响该账号其他模型。

上游积分倍率作为模型元数据保存，不自动换算成美元或覆盖管理员已有的定价。

## 额度、签到与任务

账号操作菜单的「额度与任务管理」打开编辑页中的管理入口。
查询上游额度与本项目的消费限额独立：Qoder 展示套餐 / 拓展包，WorkBuddy 展示
个人资源包或企业用量，保留企业额度的小数精度。
账号列表的「用量」列直接展示这些上游额度，包括已用、总额、剩余和到期时间。
优先读取已保存快照；缺失或超过 5 分钟时自动查询，可通过单行按钮或列表刷新强制更新。
同一账号查询合并，页面最多并行查询 4 个反代账号，刷新失败保留上次额度并显示错误。
Free 套餐的全零额度仍显示，不将总额为 0 的数据计算成百分比；Qoder 到期时间兼容秒与毫秒。

签到状态按实际上游结果记录，未领取、未确认和无活动不会写成成功。
Qoder 按 UTC+8 每天 10:00 的活动窗口判断归属；自动签到自 10:05 后运行。
WorkBuddy CN 使用每日签到接口；Global 走活动信标与热力图链路，明确提示奖励次日到账。
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
