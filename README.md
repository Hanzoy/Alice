# Alice Core v0.2.1

Alice 是使用 Go 编写的组件化个人 AI 内核。所有输入、模型、记忆、Task、输出与动态能力均作为组件，由 Alice Core 根据不可变 Blueprint 创建私有 Execution 并编排运行。

## 项目文档

- [文档总览](docs/README.md)
- [架构与设计决策](docs/ARCHITECTURE_AND_DECISIONS.md)
- [当前实现状态与路线图](docs/IMPLEMENTATION_STATUS.md)
- [家庭 Git 开发指南](docs/HOME_DEVELOPMENT.md)

README 用于快速启动。产品边界、此前讨论形成的决策、已实现与未实现功能以 `docs` 中的文档为准。

## 默认架构

```text
Web / 飞书（未来）/ 语音（未来）
        │
    Alice Core ───── PostgreSQL（权威数据）
        │
        ├────────── Qdrant（事实向量索引）
        ├────────── DeepSeek V4（唯一语言模型服务）
        ├────────── Ollama + qwen3-embedding:0.6b（本地 Embedding）
        └────────── Component Host（Go 生成、编译和隔离执行）
```

- PostgreSQL 保存原始消息、事实、来源与版本、设置、Blueprint、Task 和完整 Execution 快照。
- Qdrant 只保存可重建的向量索引，payload 通过 `fact_id` 指回 PostgreSQL。
- 事实写入与 `vector_outbox` 在同一个 PostgreSQL 事务内完成；Qdrant 临时不可用不会丢事实。
- PostgreSQL 文本召回与 Qdrant 语义召回使用 RRF 融合，再从 PostgreSQL读取完整事实。
- Ollama 默认运行 `qwen3-embedding:0.6b`，模型约 639 MB、输出 1024 维向量，支持中文及多语言。
- 普通对话、事实提取、记忆决策、Core Operator 与动态组件生成共用一个 DeepSeek Runtime；Task 蓝图调用模型节点时也进入同一 Runtime，不存在第二套语言模型 provider。
- 动态 Go 代码只在 Component Host 中编译和执行；Alice Core 通过远程代理调用，组件故障不需要重启 Core。
- Core Operator 内置 Reasonix 式组件生成器：模型生成 manifest 与 Go 源码，Component Host 编译；编译错误会回传模型并最多自动修复三次，成功后热激活。

## Docker 一键启动（推荐）

需要 Docker Desktop 或兼容的 Docker Engine。

```powershell
Copy-Item .env.example .env
# 正式使用前修改 .env 中的三个本地服务密钥
docker compose up -d --build
```

也可以双击：

```text
start-docker.cmd
```

第一次启动会下载 PostgreSQL、Qdrant、Ollama 镜像以及 `qwen3-embedding:0.6b`。模型和数据库均使用 Docker named volume，后续启动不会重复下载。

打开 [http://localhost:8080](http://localhost:8080)。只有 Alice 管理页面绑定到 `127.0.0.1`；PostgreSQL、Qdrant、Ollama 和 Component Host 默认不对宿主机开放端口。

常用命令：

```powershell
docker compose ps
docker compose logs -f alice-core
docker compose down
```

`docker compose down` 不删除数据。只有明确执行 `docker compose down -v` 才会删除 Alice 的数据库、向量、模型及组件卷。

## 配置 DeepSeek 对话模型

Embedding 已默认使用本地 Ollama；对话、事实提取和动态组件生成默认适配 DeepSeek。打开管理页后：

```text
1. 使用 deepseek-v4-flash（速度/成本优先）或 deepseek-v4-pro（能力优先）
2. 选择是否启用 thinking，以及 high / max 思考强度
3. 填写 DeepSeek API Key，保存并测试连接
```

DeepSeek 官方 OpenAI-compatible Base URL 是 `https://api.deepseek.com`。`deepseek-chat` 与 `deepseek-reasoner` 已在 2026-07-24 退役，Alice 会拒绝保存这两个旧名称并给出迁移提示。

Embedding 和对话模型拥有独立 Base URL：DeepSeek 是 Alice 唯一适配的语言模型服务；本地 Docker 中的 Ollama `qwen3-embedding:0.6b` 只负责生成事实向量，Qdrant 负责向量检索。API Key 使用随机生成的 `data/master.key` 通过 AES-GCM 加密后保存在 PostgreSQL；管理 API 只返回 `has_api_key`，不回传明文。

DeepSeek 的磁盘上下文缓存默认自动开启，无需 Alice 创建私有缓存服务。Alice 固定系统指令和消息前缀，并读取每次响应中的 `prompt_cache_hit_tokens` / `prompt_cache_miss_tokens`。管理页会显示本次 token、推理 token、累计请求数和进程启动以来的前缀缓存命中率。缓存命中由 DeepSeek 按完全相同的输入前缀判定，首次请求或前缀变化时出现未命中是正常行为。

## 记忆工作流

默认对话蓝图：

```text
message.normalize
→ memory.fact.query
→ context.assemble
→ llm.chat
→ memory.fact.process
→ output.reply
```

一轮消息会：

1. 将用户原始消息写入 PostgreSQL。
2. 并行进行 PostgreSQL 文本检索和 Qdrant 向量检索。
3. 融合相关事实与最近的全局对话时间线。
4. 交给对话模型生成回复。
5. 由模型输出 `commit / ignore / ask` 事实决策以及 `replace / coexist / ask` 冲突策略。
6. 事实先写 PostgreSQL，再立即或通过 outbox 写入 Qdrant。
7. 保存助手原始回复和完整 Execution 快照。

没有配置对话模型时，Alice 会明确提示配置；基础显式事实规则仍可验证流程，本地 Embedding 与混合召回保持可用。

更换 Embedding 模型时，Alice 会：

- 为新的模型指纹创建 `alice_facts_<fingerprint>` collection。
- 将所有 active 事实重新放入 outbox。
- 后台重算向量。
- 完成后原子切换 `alice_facts_current` alias。

旧版 `data/facts.json` 会在首次启动时幂等导入 PostgreSQL，原文件保留不删除。

## 动态 Go 组件

组件源码位于 `components/<name>`，包含 `component.json` 与 `main.go`。公共协议是：

```go
type Component interface {
    Descriptor() Descriptor
    Execute(context.Context, Envelope) (Envelope, error)
}
```

管理页提交源码目录后，Core 把请求交给 Component Host；Host 编译、热注册并运行组件。构建后立即对新 Execution 生效，Alice Core 不重启。

## 本地开发

只启动依赖服务后，可以在宿主机运行 Core；需要相应设置数据库和 Qdrant/Ollama 地址。完整 Go 测试不要求外部数据库，PostgreSQL 集成测试通过 `ALICE_TEST_DATABASE_URL` 显式启用。

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\bootstrap-go.ps1
powershell -ExecutionPolicy Bypass -File .\scripts\go.ps1 test ./...
```

## 管理 API

```text
GET  /api/snapshot
GET  /api/messages
GET  /api/facts/search?q=...
GET  /api/settings/model
PUT  /api/settings/model
POST /api/settings/model/test
POST /api/chat
POST /api/core/chat
POST /api/tasks
POST /api/tasks/{id}/trigger
POST /api/executions/{id}/patch
POST /api/executions/{id}/cancel
POST /api/executions/{id}/promote
POST /api/blueprints
POST /api/blueprints/{id}/activate
POST /api/components/build
POST /api/components/{id}/activate
```

## 数据卷

```text
alice-postgres-data    PostgreSQL 权威数据
alice-qdrant-data      Qdrant 可重建向量索引
alice-ollama-data      本地模型
alice-core-data        Core 密钥和事件日志
alice-component-data   组件清单与构建产物
alice-component-cache  Go 编译缓存
```
