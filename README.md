# 需求 AI 自动化交付平台

一个 workspace 对应一个目标系统，可登记多个 GitHub 或 GitLab 仓库。需求选择 SOP 和仓库。Agent 节点与人工审核节点共同推进交付。

管理面使用 Go、Gin、GORM 和 SQLite。React 控制台使用 Meta Astryx 和 Neutral 主题，构建后嵌入同一个 `ai-dev` 二进制。该二进制通过子命令分别启动管理面和执行器。

## 构建与启动

Linux 构建需要 Go 1.24+、带静态链接工具链的 C 编译器、Node.js 22.13+、npm 和 Git。

```sh
npm ci
npm run build
npm test
```

把 `bin/ai-dev` 分发到管理面和执行器主机。运行管理面和执行器不需要安装 Python、Node.js 或 npm。为双方的进程注入同一个高熵 `DELIVERY_EXECUTOR_TOKEN`。

```sh
# 管理面与内嵌控制台
./bin/ai-dev serve --host 127.0.0.1 --port 8000 --root .delivery

# 独立进程，或另一台主机
./bin/ai-dev executor --url http://127.0.0.1:8000 --id local --name "Local executor" --root .executor
```

管理面使用 `.delivery/delivery.db`。升级时先停止旧进程并备份数据库，再用相同的 `--root` 启动 Go 服务。执行器使用 `.executor/` 保存身份、待回报结果及 Git worktree。要继续既有任务，请保留原执行器 ID 和执行目录。

当前控制台面向可信操作者。跨主机连接使用 HTTPS，并通过反向代理或私网保护管理界面和 API。workspace 是管理边界。

## Workspace 与执行

- 在 **Workspace 配置** 管理 Agent、Skill、MCP 和 SOP。Agent 可以选 Codex CLI 或 Claude Code CLI，引用本 workspace 的 Skill 文本和 stdio MCP 服务器。
- SOP 支持拖拽排序与设置依赖、AI 多轮对话生成草稿、Agent 和人工审核节点。保存时校验依赖与 workspace 归属。创建需求时保存 SOP 与资源配置快照。
- Agent 调用 `git worktree add` 创建工作目录。同一需求的所有仓库共用 `delivery/<需求 ID>` 这个分支名，各节点复用已有 worktree。节点存档输出、提交、PR／MR 链接或人工审核意见。
- 启用发布的节点由 Agent 调用二进制提供的发布命令。发布前运行仓库验证命令，随后推送分支、创建草稿 PR 或 MR。发布重试复用已经记录的评审链接。
- 执行器经 HTTP 注册、上报心跳并领取任务。停用后停止领取新节点，运行中的节点仍可回报。任务绑定保存其 worktree 的执行器。失联时先确认原进程已停止，再停用并恢复任务，保留原 ID 和执行目录以便重试。

真实执行需要在执行器主机安装 Git 和所选 AI CLI。发布时需要已认证的 `gh` 或 `glab`、Git 作者身份，以及目标仓库的读写和创建审核请求权限。当前支持 `github.com` 和 `gitlab.com` 的 HTTPS 仓库。

`npm test` 会构建单二进制并运行 Go 测试，覆盖 SQLite 业务状态、执行器鉴权、内嵌静态资源、真实 Git worktree、审核和跨平台发布链路。模型与托管平台调用由测试替身提供。

## SOP 发布与审核返工

在 Workspace 配置保存 SOP 草稿后，发布版本才能供新需求选择。每次发布会冻结节点、Agent、Skill 和 MCP 配置。修改草稿或发布新版本时，现有需求继续运行创建时的快照。升级已有数据库会为旧 SOP 回填首个已发布版本。

「待审核」汇集当前 workspace 的人工审核节点。审核人可以通过、驳回并终止，或填写意见后退回已完成的上游 Agent。返工沿用原执行器和 worktree，重跑该节点及其后继，保留之前的提交、审核意见和执行产物。

Agent 节点可以声明相对于仓库根目录的 UTF-8 文本产物，每项路径至少在一个目标仓库中存在。平台保存每次执行的输出、提交、评审链接和产物文本，供审核详情回看。单个文件上限 16 KiB，每次节点执行最多保存 24 个文件，编码后的文本总上限为 96 KiB。
