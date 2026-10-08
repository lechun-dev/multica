# 官方 DSH 接入 MissionOS

## 1. 安装与授权

安装 DeepSeek 官方发布的 **DeepSeek Harness** 桌面端，不需要自行打包 App。
打开官方 App，在模型设置中完成授权，并先确认能正常聊天。

API 密钥只填写到官方 App 的设置中，不要发给 AI、同事或粘贴到排障日志。
CLI 自动接入不能修复失效密钥；若提示认证失败，请在官方设置中更新授权。

## 2. 自动接入

更新 MissionOS 到支持官方 ACP 的版本，启动本地服务。
MissionOS 会优先使用已有的兼容 dsh 命令；没有可用命令时使用官方 App 自带的
启动入口，不安装插件、不替换现有命令、不修改系统 PATH，也不要求管理员权限。

macOS 默认支持 `/Applications/DeepSeek Harness.app` 和
`~/Applications/DeepSeek Harness.app`；Windows 支持 LOCALAPPDATA/Programs 和
ProgramFiles 下的 DeepSeek Harness。自定义路径可以通过 `MULTICA_DSH_PATH`
指定启动入口；显式指定的路径不会被自动替换。

## 3. 检查与使用

在 **运行环境 → 当前机器** 的标题栏点击 **DSH CLI**，确认显示
“DSH CLI 可用，已通过官方 ACP 验证”。检测有明确超时，不再等待旧 multica 配置。
从运行环境的模型列表选择模型与支持的思考强度，再发起一个简单任务。

检测成功只代表 ACP 可连接，不代表 API 密钥或模型额度有效。
若显示检测超时，检查官方 App 安装后重试；若显示协议不兼容，更新官方 App。
模型认证失败请回官方 App 更新授权，不要安装所谓“修复插件”。

## 迁移说明

不再需要 `dsh-profile-multica` 或自定义 `multica` 配置。
`MULTICA_DSH_PROFILE_BUNDLE`、`MULTICA_DSH_PLUGIN_PATH` 已停用。
MissionOS 不会删除你已有的 DSH 配置。旧自定义协议会话不会自动迁移；续接被拒绝时
正常重试流程会开启新会话，不会声称保留旧历史。
