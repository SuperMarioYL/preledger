[English](./README.en.md) | **简体中文**

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="./assets/hero-dark.svg">
  <img src="./assets/hero-light.svg" alt="Preledger —— 编辑时记录假设 · 评审时逐条证伪 · 证伪即测试" width="960">
</picture>

**面向代码评审者的 Coding Agent 未验证假设台账**

Preledger 记录 Coding Agent 每一次编辑背后的未验证假设，生成随仓库走的假设台账；评审者在本地界面逐条确认或证伪，每条被证伪的假设自动编译成一个失败的测试。

[![ci](https://img.shields.io/github/actions/workflow/status/SuperMarioYL/preledger/ci.yml?label=CI&logo=githubactions&logoColor=white)](https://github.com/SuperMarioYL/preledger/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/SuperMarioYL/preledger?logo=github&logoColor=white)](https://github.com/SuperMarioYL/preledger/releases)
[![go](https://img.shields.io/badge/Go-1.24%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![license](https://img.shields.io/github/license/SuperMarioYL/preledger?color=3fb950)](./LICENSE)

<img src="https://readme-typing-svg.demolab.com?font=Noto+Sans+SC&weight=600&size=18&pause=1400&color=6A8BE7&center=true&vCenter=true&random=false&width=620&lines=%E8%AE%A9%E6%AF%8F%E6%AC%A1%E7%BC%96%E8%BE%91%E4%BA%A4%E4%BB%A3%E5%AE%83%E7%9A%84%E5%81%87%E8%AE%BE%EF%BC%8C%E5%9C%A8%E5%AE%83%E5%8F%91%E7%94%9F%E7%9A%84%E9%82%A3%E4%B8%80%E5%88%BB" alt="让每次编辑交代它的假设，在它发生的那一刻">

**免费、开源 —— v0.1 不含任何付费功能。**

</div>

## 为什么需要它

合并一个 AI 写的 diff 时，评审者看得到「改了什么」，看不到它「想当然认为什么」。会话转录记录的是动作和事后的完成声明（「我修好了 X」），从不记录每条编辑背后的前提：那个配置文件真的存在吗？那个依赖真的在 go.mod 里吗？PR 描述则是工作完成之后、由同一个想被合并的 agent 写出来的，它天然只会说「做了什么」。

Preledger 把「前提」变成一等公民，并且在**编辑发生的那一刻**记录它：

- **捕获不打断编辑**。`preledger install` 一次性把 PostToolUse 钩子接进 Claude Code；之后 agent 照常工作，每次 Edit/Write 都会被记录。
- **台账随仓库走**。`.preledger/ledger.jsonl` 是一个纯 append-only JSONL 文件，每条记录带编辑引用（会话、tool_use、文件、diff hunk）和依据原文。没有守护进程、没有数据库、没有云端。
- **证伪编译成测试**。评审者点「证伪」，Preledger 在仓库里写出一个断言该前提的失败测试——前提确实为假时套件变红，修复代码后转绿。红色是证据，绿色是闭环。

捕获是启发式优先、完全离线的：规则找的是「编辑对仓库做出的、仓库还回答不了的前提」，而不是猜测意图；LLM 分诊是可选项，关掉也不影响任何功能。

## 演示

30 秒看完完整闭环：接入 → 三次编辑自动入账 → 本地界面证伪 → 套件变红 → 修复转绿。

<p align="center">
  <img src="./assets/demo.gif" alt="Preledger 演示：install 接入 hook，三次编辑后台账出现六条假设；评审界面证伪「config.yaml 存在且含 server.port」，go test 变红；补上 config.yaml 后转绿" width="960">
</p>

上图为真实录制（`vhs docs/demo.tape`）。样例仓库由 `examples/demo/setup.sh` 现场生成；为了在录制里复现，agent 的三次编辑以与 Claude Code 完全同构的 stdin 载荷直接喂给 `preledger hook`。每次运行的实际命令与输出记录在 [`docs/demo-results.json`](./docs/demo-results.json)。

## 快速开始

前置：Go 1.24+，一个使用 Claude Code 的仓库。约 1 分钟。

```bash
# 1. 安装单一二进制
go install github.com/SuperMarioYL/preledger@latest

# 2. 在仓库根目录接线（写入 .claude/settings.json，搭建 .preledger/）
cd your-repo
preledger install

# 3. 让 Claude Code 正常编辑——假设会自动记入 .preledger/ledger.jsonl

# 4. 评审
preledger up          # 浏览器自动打开 http://127.0.0.1:7830
```

评审界面里：按状态筛选、逐条查看每条假设与它的转录依据原文，点 **确认** 或 **证伪**。证伪会立即在仓库里生成测试文件（Go 仓库生成 `preledger_refute_<id>_test.go`，package.json 仓库生成 vitest 测试），然后：

```bash
go test ./...         # 被证伪且确实为假的前提 → 套件变红
# 修复代码（或让 agent 修），再跑一次 → 转绿
```

**没有 Claude Code 也想先试试？** 样例输入随仓库发布：

```bash
git clone https://github.com/SuperMarioYL/preledger && cd preledger
go build -o bin/preledger .
bash examples/demo/setup.sh          # 生成演示仓库与三段 hook 载荷
```

之后按脚本输出的提示操作即可，与上面的快速开始完全同构。

## 工作原理

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./assets/architecture-dark.svg">
    <img src="./assets/architecture-light.svg" alt="架构：Claude Code 经 PostToolUse 钩子调用 preledger hook，内部按机械前提、转录引文、启发式三步提取，写入 .preledger/ledger.jsonl；preledger up 以 go:embed 提供本地中文界面，fsnotify 保持实时；证伪经 internal/refute 编译为仓库内的 Go/vitest 测试" width="960">
  </picture>
</p>

一个二进制、三个子命令：

| 命令 | 生命周期 | 职责 |
| --- | --- | --- |
| `preledger install` | 一次性 | 把 PostToolUse 钩子写入 `.claude/settings.json`，搭建 `.preledger/` |
| `preledger hook` | 每次编辑一个子进程 | 读 stdin 载荷，解析转录，提取假设，追加台账；失败也以 0 退出，绝不阻塞编辑 |
| `preledger up` | 唯一的常驻进程 | 本地评审界面（127.0.0.1:7830），fsnotify 保持实时，证伪写回仓库 |

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./assets/process-dark.svg">
    <img src="./assets/process-light.svg" alt="流程：agent 侧一次性 install 后照常编辑，PostToolUse 自动记录；台账作为共同事实源；评审者合并前 preledger up 逐条确认或证伪，证伪生成失败测试，修复后转绿再合并" width="960">
  </picture>
</p>

### 捕获了什么

每次编辑都会产生一条**机械前提**（Edit 假设被替换片段存在且唯一，Write 假设整文件重写）。在此之上，启发式规则寻找三类「编辑做出、而仓库还回答不了」的前提：

| 启发式 | 触发 | 记录为 |
| --- | --- | --- |
| 新依赖 | 编辑文本引入了 go.mod / package.json 未声明的模块 | `go.mod 存在且含 <module>` |
| 缺失的配置 / 环境变量 | 编辑读取的配置文件在仓库中不存在；引用的键在树中搜不到 | `config.yaml 存在且含 server.port`、`.env 存在且含 <KEY>` |
| 仓库中不存在的新路径 | Write 落在 git 判定为新文件的路径上 | `<path> 存在` |

依据（basis）优先取 agent 在编辑前说出的原话（来自转录），没有转录时退回触发该前提的代码行——两者都是转录原文，不是重建的转述。台账按 `hash(编辑引用 + 前提)` 去重，同一次编辑重复捕获不会产生重复条目。真实的捕获记录：

```json
{"id":"d0cb1f3e9e449e41","edit_ref":{"session_id":"s1-demo-session","transcript_path":"…/transcript-s1.jsonl","tool_use_id":"toolu_a1","tool":"Edit","file":"main.go","hunk":"@@ -1,7 +1,22 @@"},"statement":"config.yaml 存在且含 server.port","basis":"配置文件还不存在，我会假定 config.yaml 存在且含 server.port，先写读取逻辑。","status":"open"}
```

确认或证伪都通过追加新记录来更新状态，文件永远 append-only、可以在 git 里 diff。

### 证伪编译

被证伪的前提按语法编译成断言；无法机械化验证的前提编译成 TODO 占位（`t.Skip` / `it.todo`），把前提留作注释：

| 前提语句 | 生成的断言 |
| --- | --- |
| `<path> 存在` | `os.Stat(path)` / `existsSync(path)` |
| `<path> 存在且含 <key>` | 读文件并检查字面子串（「含」按字面匹配） |
| `<file> 的现有内容中待替换片段存在且唯一：<片段>` | `strings.Count(data, 片段) == 1` |
| `<file> 的现有内容包含待替换片段：<片段>` | `strings.Contains(data, 片段)` |

## 与其他环节的分工

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./assets/integrations-dark.svg">
    <img src="./assets/integrations-light.svg" alt="集成面：以 .preledger/ledger.jsonl 为中心，实线连接 Claude Code hook、本地评审界面、Go 测试与 vitest；虚线为可选的 DeepSeek/GLM 分诊与随仓库走的 git" width="960">
  </picture>
</p>

Preledger 不替代任何现有环节，它补上「前提」这个没人记录的位置：

| 环节 | 回答的问题 | 时机 |
| --- | --- | --- |
| agent 写的 PR 描述 | 做了什么 | 工作完成之后 |
| 会话回放 / 完成声明校验 | agent 说「修好了」是否属实 | 会话结束之后 |
| PR 评审机器人 | 对 diff 的通用改进意见 | 推送之后 |
| **Preledger** | **这次编辑想当然认为什么** | **编辑发生的那一刻** |

## 配置

全部可选，默认零配置即可运行：

| 环境变量 | 默认 | 说明 |
| --- | --- | --- |
| `PRELEDGER_PORT` | `7830` | 评审界面端口（`up --port` 可覆盖） |
| `PRELEDGER_TRIAGE_PROVIDER` | 未设置（离线） | `deepseek` 或 `glm`，启用可选的 LLM 分诊 |
| `PRELEDGER_TRIAGE_API_KEY` | 未设置 | 对应服务商的 API Key（DeepSeek / 智谱开放平台，均为 OpenAI 兼容端点，无需 OpenAI Key） |
| `PRELEDGER_NO_OPEN` | 未设置 | 设为任意值时 `up` 不自动打开浏览器 |

LLM 分诊只做一件事：把启发式候选交给模型判断哪些是承重假设，过滤掉明显琐碎的条目。调用失败、超时（5 秒）或未配置时保留全部候选——离线启发式永远可用。

## 路线图

**v0.1（已实现）**

- PostToolUse 捕获 + 三类启发式前提 + 可选 DeepSeek/GLM 分诊
- 中文本地评审界面：状态筛选、逐条确认 / 证伪、实时刷新
- 证伪编译：Go 仓库 → `go test`，package.json 仓库 → vitest；不可机械化验证的前提 → TODO 占位

**接下来（按反馈排序）**

- 更多启发式模式（函数签名变更、被跳过的既有测试）
- 从台账条目直接跳转到 diff hunk 与文件
- 把台账格式发布为独立规范，附 JSON Schema 校验
- vitest 仓库的目录级测试放置策略

边界同样明确：v0.1 只支持 Claude Code 的钩子与转录格式；不做 IDE 插件、PR 机器人、云端服务与自动修复。

## 本地开发

```bash
git clone https://github.com/SuperMarioYL/preledger && cd preledger
go build ./... && go vet ./... && go test ./... -count=1
```

提交信息里的 `docs/demo-results.json` 是演示流程的真实运行记录；`docs/demo.tape` 可用 [vhs](https://github.com/charmbracelet/vhs) 重新渲染 `assets/demo.gif`。

## License

MIT © 2026 SuperMarioYL
