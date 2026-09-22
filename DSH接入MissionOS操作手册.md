# DSH 接入 MissionOS 操作手册

> 适用对象：要在 MissionOS 里使用 DSH 的同事　｜　预计耗时 10 分钟

## 使用说明

- 写着「把下面这段复制给你的 AI」的步骤：把方框里的整段文字粘给 AI（Codex / DSH 等）执行，你不用自己敲命令。
- 其余步骤需要你本人操作，跟着编号做即可。
- 每台电脑各做一遍；换电脑或换登录用户都要重做。

## 第 1 步 安装 DSH

**把下面这段复制给你的 AI：**

```text
请在这台电脑上帮我安装 DSH（DeepSeek Harness）：

1. 检查 node -v（需要 v22.19 以上或 v24 以上，不满足先告诉我）和 pnpm -v
   （没有 pnpm 就执行 npm install -g pnpm）。
2. 执行 npm install -g @deepseek-ai/dsh，再执行 dsh --version 确认安装成功。
3. 安装会写入全局 npm 目录，超出常规工作目录范围。需要更高权限请直接申请，
   不要改安装路径，也不要手工复制文件绕过。
4. 任何一步失败，把完整报错原文回传给我，不要只给结论。
```

**完成标志：** AI 回复 `dsh --version` 有版本号（例如 `0.1.5-rc.2`）。失败就把报错原文发回给 AI。

## 第 2 步 启动 DSH 本地界面

**这一步必须你本人做：** AI 在自己的环境里跑命令，不会替你在电脑上弹出可见的终端窗口；而 `dsh web` 是常驻服务，AI 任务结束后可能被一起关掉。

1. 打开终端（macOS：聚焦搜索「终端」；Windows：开始菜单搜索「PowerShell」）。
2. 粘贴下面这行命令，回车：

```bash
dsh web
```

3. 浏览器打开终端打印的地址（形如 `http://127.0.0.1:3080`），能看到 DSH 聊天界面即成功。

> ⚠️ 这个终端窗口要一直开着，第 3、4 步都在这个界面里操作。全部做完后可以关掉，不影响 MissionOS 跑任务。

## 第 3 步 配置 BASE_URL 和 API Key

我们用的不是 DeepSeek 官方 API，而是公司自己的中转地址，所以这一步必须自己填。**首次打开如果弹出输入 API Key 的窗口，点「稍后配置」，不要在那里填。**

在 DSH 界面里依次点击：**设置（Settings）→ 模型（Models）→ DeepSeek 那一行 → 编辑**。

| 填什么 | 在哪填 | 填成什么 |
| --- | --- | --- |
| API Key | 「API 密钥」输入框 | 公司发给你的密钥（输入后不回显，属于正常现象） |
| BASE_URL | 展开「自定义设置」里的 `API 地址` | `https://sub2api.lechun.cc/v1` |

![截图：设置 → 模型，API 密钥与 API 地址的填写位置](/tmp/dsh-doc-build/user-shot.png)

点「保存」。**完成标志：** 该行的 API 密钥状态变成绿色实心点。

## 第 4 步 验证 DSH 能不能用

新建一个会话，发一句：`你好，请用一句话自我介绍。`

**能正常收到回复 = 通了**，继续第 5 步。密钥错、余额不足、地址写错，都会在这一步暴露出来。

## 第 5 步 把 DSH 接入 MissionOS

关键点：只装 `dsh` 是不够的，还必须给 DSH 装一个 MissionOS 专用桥接配置。

**把下面这段复制给你的 AI：**

```text
请把本机的 DSH 接入 MissionOS：

1. 执行：dsh plugin --profile multica add dsh-profile-multica
2. 执行：dsh --profile multica --probe
   - 成功判据：输出里必须同时包含 "type":"probe" 和 "protocol_version":1；
   - 若报 profile "multica" does not exist，说明第 1 条没成功，请重跑第 1 条。
3. 以上命令会写入 ~/.dsh，超出常规工作目录范围。需要更高权限请直接申请，
   不要手工复制文件或修改 DSH_HOME、安装路径来绕过。
4. 把两条命令的完整输出原样回传给我，不要总结、不要省略。
5. 不要改动 DSH 版本，不要读取、记录或传输任何 API 密钥，不要重启电脑。
```

**完成标志：** `--probe` 输出下面这样一行（字段顺序可能不同，关键是两个字段都在）：

```json
{"v":1,"type":"probe","runtime":"dsh","plugin_version":"0.1.0","protocol_version":1}
```

之后等 1～2 分钟，MissionOS 后台服务每 2 分钟自动扫描一次新装的工具，通常无需重启。

## 第 6 步 在 MissionOS 里用起来

1. 打开 MissionOS →「运行时」，应该能看到 **DeepSeek Harness (你的电脑名)**，状态**在线**。
2. 新建或编辑智能体时，在「运行时」里选择这一条。
3. 派一个任务试跑，能跑完就代表全部打通。

### 出问题了：把下面这段发给 AI

```text
我的 DSH 接入 MissionOS 没成功，请帮我排查并修复：

1. 现象是：<把看到的报错或截图内容写在这里>
2. 请依次执行并检查：node -v、dsh --version、dsh --profile multica --probe
3. 如果 --probe 报 profile "multica" does not exist，
   请重跑 dsh plugin --profile multica add dsh-profile-multica
4. 排查 MissionOS 后台服务日志：~/.multica/profiles/*/daemon.log，搜索 dsh
5. 不要改动 DSH 版本，不要读取或记录我的密钥，不要重启电脑
6. 把每一步的完整命令输出回传给我，不要只给结论
```

### 常见问题

| 现象 | 怎么处理 |
| --- | --- |
| DSH 里发消息报 401 / 认证失败 | 回第 3 步重填密钥（注意不要带 `NAME=` 和引号） |
| DSH 里发消息连不上 / 超时 | 回第 3 步核对 API 地址（要以 `/v1` 结尾） |
| 第 4 步通了，但「运行时」里没有 DSH | 检查第 5 步的 probe 是否输出了成功判据 |
| 运行时在线，但任务一跑就失败 | 回第 3 步；确认是同一台电脑、同一个登录用户 |
| 命令能跑，但 MissionOS 就是不认 | 完全退出并重启 MissionOS 桌面端 |
| 安装时提示找不到 pnpm | 让 AI 执行 `npm install -g pnpm` 后重试 |

> **不要**在 MissionOS 里点「新建自定义运行时」再手工填命令。那是给特殊封装脚本用的高级功能，普通使用不需要。
