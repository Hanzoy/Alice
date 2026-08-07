# Alice 家庭 Git 开发指南

适用版本：Alice Core v0.2.1  
远端仓库：`git@github.com:Hanzoy/Alice.git`

## 1. Git 会同步什么

Git 仓库同步源码、配置模板、文档、测试和启动脚本。

Git 不同步：

- `.env` 中的本地密钥；
- PostgreSQL 数据；
- Qdrant 向量；
- Ollama 模型文件；
- Docker named volume；
- `data/master.key` 和已加密的 DeepSeek Key；
- `dist`、编译缓存和本地工具链。

因此，在家中首次克隆后是一个新的本地运行环境。需要重新创建 `.env`、下载 Docker 镜像和 Ollama 模型，并在管理页面重新填写 DeepSeek API Key。

## 2. 家中电脑准备

建议安装：

- Git；
- Docker Desktop；
- PowerShell 5.1 或更高；
- 可选：Go。仓库也提供项目本地 Go 工具链下载脚本。

确认环境：

```powershell
git --version
docker version
docker compose version
```

## 3. 首次克隆和启动

```powershell
git clone git@github.com:Hanzoy/Alice.git
Set-Location Alice
Copy-Item .env.example .env
notepad .env
docker compose up -d --build
docker compose ps
```

也可以双击 `start-docker.cmd`，脚本会在不存在 `.env` 时从 `.env.example` 创建一份。

第一次启动会下载 PostgreSQL、Qdrant、Ollama 和 Go 构建镜像，并拉取 `qwen3-embedding:0.6b`。完成后打开：

```text
http://localhost:8080
```

在管理页面填写 DeepSeek API Key，选择 V4 Flash 或 V4 Pro，然后测试连接。不要把 DeepSeek Key 写进 Git 跟踪文件。

## 4. 日常 Git 工作流

开始开发前：

```powershell
git switch main
git pull --ff-only
git status
```

为每个独立功能创建分支：

```powershell
git switch -c feature/deepseek-tools
```

完成一小段可验证工作后：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\go.ps1 test ./...
git diff --check
git status
git add README.md docs internal
git commit -m "feat: add deepseek tool calling"
git push -u origin feature/deepseek-tools
```

不要长期直接在 `main` 上积累大量未提交修改。一个提交尽量只表达一个目的，文档和相应测试随功能一起提交。

## 5. 测试与本地验证

### 5.1 Go 测试

如果电脑没有 Go：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\bootstrap-go.ps1
```

运行测试：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\go.ps1 test ./...
```

### 5.2 Docker 验证

代码修改后重建：

```powershell
docker compose up -d --build
docker compose ps
docker compose logs --tail 100 alice-core
docker compose logs --tail 100 component-host
```

API 快速检查：

```powershell
Invoke-RestMethod http://127.0.0.1:8080/api/health
Invoke-RestMethod http://127.0.0.1:8080/api/snapshot
Invoke-RestMethod http://127.0.0.1:8080/api/settings/model
```

### 5.3 发布包

生成不包含 Go 工具链的 Windows 包：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\package.ps1 -WithoutToolchain
```

生成包含项目本地 Go 工具链的便携包：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\package.ps1
```

发布包位于 `dist`，该目录默认不提交 Git。

## 6. 目录说明

```text
cmd/                    Alice Core 与 Component Host 入口
components/             可动态编译的 Go 组件源码
docs/                   设计、状态和开发文档
internal/app/           应用装配和内置 Blueprint
internal/core/          Blueprint、Execution、Registry、事件
internal/builtin/       对话、记忆和 Core Operator 内置组件
internal/models/        DeepSeek Runtime
internal/facts/         PostgreSQL 事实模型和冲突处理
internal/memory/        混合召回
internal/vector/        Qdrant 与向量 outbox
internal/tasks/         Task 创建、触发和状态
internal/dynamic/       动态 Go 组件管理
internal/componenthost/ 组件编译与隔离执行服务
internal/httpapi/       管理 API 和 Web 页面
internal/storage/       PostgreSQL schema 和持久化
pkg/component/          动态组件可依赖的稳定公共协议
scripts/                Go 安装、测试和打包脚本
compose.yaml            推荐本地部署
```

## 7. 数据与备份

### 7.1 不要误删数据卷

安全停止：

```powershell
docker compose down
```

这会停止容器，但保留数据。

以下命令会删除 PostgreSQL、Qdrant、Ollama 模型、Core 密钥和组件构建数据，除非明确要重置环境，否则不要执行：

```powershell
docker compose down -v
```

### 7.2 Git 不是数据备份

重要事实和原始消息在 PostgreSQL named volume 中。Qdrant 可以从 PostgreSQL 事实重建，但 PostgreSQL 不能从 Qdrant 完整恢复。

准备跨电脑迁移真实 Alice 数据时，至少需要：

- PostgreSQL 备份；
- Alice Core 数据卷中的 `master.key`；
- 动态组件源码和必要的组件数据；
- 可选的 Qdrant 快照，或者在新环境重建向量。

数据库与 `master.key` 必须作为一组安全保存，否则数据库中加密的 DeepSeek API Key 无法解密。个人开发早期更简单的方式是在家中新建数据环境并重新输入 Key。

## 8. 密钥和安全规则

- `.env` 已被 `.gitignore` 排除，不要使用 `git add -f .env`。
- 不要在 Issue、提交信息、日志截图或测试代码中放 DeepSeek Key。
- `.env.example` 只能放占位值或本地开发默认值。
- 当前管理页面没有登录，只绑定本机；不要修改为 `0.0.0.0` 后直接暴露公网。
- 动态组件相当于可执行代码，合并前必须检查源码、网络访问和文件访问。

提交前检查：

```powershell
git status
git diff --cached
git grep -n "sk-"
```

如果密钥曾经被提交，即使后来删除，也应立即在服务端轮换；Git 历史中的旧内容不能依赖普通删除消失。

## 9. 常见问题

### 管理页面能打开，但 Alice 不聊天

检查管理页是否保存了 DeepSeek API Key，再点击“测试连接”。Ollama Embedding 正常不代表 DeepSeek 对话正常。

### Qdrant points 为 0

没有 active 事实时这是正常状态。先完成能够形成事实的对话，再观察 PostgreSQL facts、vector_pending 和 Qdrant points。

### 修改组件后为什么 Core 没重启

这是预期行为。动态组件由 Component Host 构建和热注册；只有修改 Alice Core、公共协议或容器镜像时才需要重建 Core。

### Git pull 后数据库为什么没有变化

Git 只同步代码。数据库迁移由 Core 启动时执行；实际用户数据保存在每台电脑自己的 Docker volume 中。

### 在两台电脑同时开发

每台电脑使用独立功能分支，推送前先提交，合并前从远端 main 变基或合并。不要依靠拷贝整个项目目录同步未提交工作。

