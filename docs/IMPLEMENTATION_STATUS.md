# Alice 实现状态与路线图

基线版本：Alice Core v0.2.1  
基线提交：`99b0fdf init`  
整理日期：2026-08-08

## 1. 当前结论

Alice 已经是一个能通过 Docker 启动、能保存数据、能运行 Blueprint、能编译动态 Go 组件的技术原型。核心骨架真实可用，但距离“能够自主完成通用家庭任务的产品”仍有明显差距。

状态定义：

- 已实现：代码、API 或页面已经存在，并经过测试或本地运行验证。
- 基础实现：核心原语存在，但自动化、产品体验或边界处理还不完整。
- 未实现：仅有设计，没有可用实现。

## 2. 已实现

### 2.1 部署与基础服务

- Docker Compose 启动 Alice Core、PostgreSQL、Qdrant、Ollama 和 Component Host。
- PostgreSQL、Qdrant、Ollama 和组件构建数据使用独立 Docker named volume。
- 管理页只映射到 `127.0.0.1:8080`，内部服务默认不暴露宿主机端口。
- 提供 Windows 启动脚本、Go 工具链脚本和发布打包脚本。
- 提供 Go 单元测试；当前 `go test ./...` 通过。

### 2.2 DeepSeek Runtime

- 语言模型只接受 DeepSeek provider 和官方 Base URL。
- 只接受 `deepseek-v4-flash`、`deepseek-v4-pro`。
- 支持 thinking enabled/disabled 和 reasoning effort high/max。
- 普通对话、事实判断、Core Operator 和组件生成共用同一个 Runtime。
- JSON 模式会添加明确格式要求和 `max_tokens`，DeepSeek 空内容时重试一次。
- 解析输入、输出、推理和缓存命中 token，管理页显示进程内累计命中率。
- DeepSeek API Key 经 AES-GCM 加密后保存，API 不返回明文。

### 2.3 原始消息与事实记忆

- 用户和 Alice 原始消息写入 PostgreSQL。
- 事实包含 subject、predicate、object、来源、置信度、时间、敏感度、状态和标签。
- 保存事实来源关系。
- 基础显式规则可识别“我喜欢”“我不喜欢”“我叫”“记住”等陈述。
- 配置 DeepSeek 后，由模型输出 commit/ignore/ask 和 replace/coexist/ask 决策。
- 事实与 vector outbox 在 PostgreSQL 事务内提交。
- 支持旧 `facts.json` 幂等导入。

### 2.4 检索和向量

- 本地 Ollama 运行 `qwen3-embedding:0.6b`。
- Qdrant 保存向量索引，PostgreSQL 保存权威事实。
- PostgreSQL 文本检索与 Qdrant 语义检索并行执行。
- 使用 RRF 融合结果。
- Qdrant 写入失败时 outbox 后台重试。
- Embedding 模型指纹变化时重算向量并原子切换 collection alias。

### 2.5 Blueprint 和 Execution

- Blueprint 不可变、版本化并支持活动版本。
- Blueprint 校验节点、边和环；当前只允许 DAG。
- 每次运行复制 Blueprint 节点和边，形成私有 Execution。
- 节点支持 timeout 和 max_attempts。
- 保存每个节点的输入、输出、状态、组件版本、尝试次数和错误。
- 支持取消 Execution。
- 支持对尚未运行的节点执行 replace_component、set_config、skip_node。
- 支持将 Execution 私有图发布为新的 Blueprint 版本。
- Execution 快照保存到 PostgreSQL，并记录事件时间线。

### 2.6 Task

- Task 保存 Blueprint、输入、触发器、状态、结果和 Execution ID。
- 支持 manual 和指定时间 at 触发。
- Task 异步启动 Execution。
- 支持 waiting、running、completed、failed 状态。
- Alice 重启时将未完成 running Task 恢复为 waiting。
- 完成信息交给可替换的 `task.result.fact` 组件。

### 2.7 动态 Go 组件

- 稳定的 Go Component、Descriptor、Envelope 协议。
- Registry 保存多个组件版本并维护 active 指针。
- Component Host 在独立容器中编译和执行 Go stdio 组件。
- 管理 API 可以从源码目录编译、注册和激活组件。
- Core Operator 可以根据明确的“创建组件”请求调用 DeepSeek 生成 manifest 和 Go 源码。
- 编译失败会反馈给模型，最多自动修复三次。
- 动态组件成功后热激活，不重启 Alice Core。

### 2.8 管理页面和 API

- DeepSeek 模型、thinking、API Key 和 Embedding 设置。
- DeepSeek/Ollama 连接测试。
- 与 Alice 对话、与 Core Operator 对话。
- 创建基础 Task、手动构建 Go 组件。
- 查看服务、组件、Blueprint、Execution、Task、事实和事件 JSON 状态。
- 提供 Blueprint 发布/激活、Execution patch/cancel/promote、组件激活等 API。

## 3. 基础实现但尚不完整

| 能力 | 当前基础 | 主要缺口 |
|---|---|---|
| 多任务编排 | DAG 和 Task/Execution 已有 | DAG 节点仍按拓扑顺序执行，没有真正并行调度、队列和资源预算 |
| Alice 自主接管 | Patch、重试、取消和 promote 原语已有 | Core Operator 不会持续观察并自主决定何时修改、降级或发布流程 |
| Task 管理服务 | 内部 Manager 和状态持久化已有 | 不是独立服务；只有 manual/at；缺少 Cron、事件和条件触发 |
| Task 结果记忆 | 完成信息会交给管理组件 | 当前主要依赖 remember_result，不是完整模型判断和确认流程 |
| 组件生命周期 | Descriptor 可声明 singleton/per_call | 缺少统一实例管理、资源池、自动休眠、并发限制和 Alice 自动选择 |
| Core Operator | 能查询状态和生成组件 | 自然语言管理命令覆盖有限，规则查询与模型回答尚未形成完整控制循环 |
| 记忆冲突 | replace/coexist/ask 基础策略 | 缺少事实合并、时间衰减、版本审阅、人工确认中心和自动失效 |
| DeepSeek 缓存 | 自动前缀缓存和命中统计 | 缺少确定性 Prompt 编译、计划缓存、组件输出缓存和费用趋势分析 |
| 管理页面 | 设置、聊天和 JSON 状态可用 | 不是可视化 Blueprint/Execution 管理产品 |
| 组件隔离 | Component Host 独立容器 | 尚无细粒度网络、文件、CPU、内存和系统调用权限控制 |

## 4. 尚未实现

### 4.1 完整 Tool Calling

- DeepSeek `tool_calls` 请求与响应协议；
- 工具目录和动态发现；
- JSON Schema 参数验证；
- reasoning_content 在工具调用轮次中的正确回传；
- 多轮调用、并行工具调用和工具结果组装；
- 幂等、重试、副作用分类和未来审批。

当前“组件可由 Blueprint 调用”不等于“模型已经能够自主使用所有组件作为工具”。

### 4.2 Alice 自主编排控制器

- 根据目标自动创建 Blueprint；
- 运行时持续观察 Execution；
- 自动插入、删除和重新连接节点；
- 自动选择备用组件、调整预算和降级；
- 需要时询问用户并在回答后继续；
- 完成后判断是否保存为新 Blueprint；
- 根据历史 Execution 主动优化流程。

### 4.3 并行、异步和恢复

- DAG 中无依赖节点的真正并行执行；
- 持久化异步工作队列；
- Core 崩溃后的 Execution 断点恢复；
- 长任务暂停和恢复；
- 全局并发、Token、费用、时间和工具预算；
- 多实例 Alice Core 协调。

### 4.4 输入与输出组件

- 飞书适配器；
- 语音识别和语音合成；
- 手机、桌面端和智能音箱；
- 邮件、日历、浏览器和家庭设备服务；
- 流式输出、生成中取消和跨设备回复路由。

当前真正可用的用户渠道只有 Web 管理页面和 HTTP API。

### 4.5 管理产品

- Blueprint 可视化有状态流程图；
- 拖拽编辑、版本比较和回滚；
- Execution 实时节点动画、接管和重放；
- Task 详情、过滤、历史和批量管理；
- 事实搜索、编辑、确认、删除和来源追踪界面；
- 组件源码、版本、构建日志、激活和回滚界面；
- Token、缓存、延迟、错误和费用图表。

### 4.6 记忆增强

- 长对话总结和分层摘要；
- 上下文压缩与 Token 预算；
- 事实 valid_to、自动过期和时间推理；
- 事实关系图和多跳检索；
- 模型主动多轮事实查询；
- 图片、音频、文件等多模态原始记录；
- 记忆删除、遗忘、审计和数据保留策略。

### 4.7 权限和安全

该部分按此前决定暂缓，目前未实现：

- 管理页面登录；
- 多用户和设备身份；
- 事实可见范围；
- 组件和工具权限；
- 敏感操作确认；
- 细粒度组件沙箱；
- 审计、撤销和密钥轮换流程。

### 4.8 生产运维

- PostgreSQL/Qdrant 自动备份恢复；
- 指标、日志采集和告警；
- 数据迁移版本管理增强；
- 高并发和长时间稳定性测试；
- 组件签名和可信来源；
- 发布升级、兼容性检查和自动回滚。

## 5. 建议开发顺序

### 阶段 A：让 Alice 真正能够“使用能力”

1. DeepSeek Tool Calling Runtime。
2. 将组件 Descriptor 转换为工具 Schema。
3. 工具结果回传和多轮模型循环。
4. 工具幂等、副作用和预算基础。

### 阶段 B：让 Core 真正自主编排

1. Execution Supervisor 组件。
2. 并行 DAG 调度和持久化队列。
3. 模型生成 Blueprint Candidate。
4. 运行中补丁、降级、暂停询问和继续。
5. 完成后 promote/ignore 决策。

### 阶段 C：完善记忆和 Task

1. 对话摘要与上下文预算。
2. 事实管理和冲突确认界面。
3. Cron、事件和设备状态触发器。
4. Task 结果的模型化事实决策。

### 阶段 D：渠道和管理体验

1. Blueprint/Execution 可视化。
2. 飞书输入输出。
3. 语音输入输出。
4. 家庭服务和设备组件。

### 阶段 E：权限与生产化

在 Alice 需要被其他人或设备访问、或需要执行高风险副作用前，必须优先实现身份、权限、审批和审计。

## 6. 当前使用前提

- Docker 服务目前可以独立运行。
- 必须在管理页面填写有效 DeepSeek API Key，真正的对话、事实模型判断和组件生成才可用。
- 没有 Key 时，本地 Embedding 和基础显式事实规则仍能运行，但这不代表 DeepSeek 已连接。
- 当前管理页面没有登录，只适合本机开发环境。

