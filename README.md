# Gemini 原生签名整流

此插件支持 Sub2API `v0.2.23` 的原生 Gemini 请求。
上游返回思考签名相关 HTTP 400 时，插件清理签名后最多重试一次。
它覆盖 API Key/中转、OAuth AI Studio、OAuth Code Assist 和 Vertex ServiceAccount。

## 启用

在管理员「系统设置 → 网关服务 → 请求整流器」中，启用总开关和「Gemini 原生签名整流」，然后保存。
新选项默认关闭。已有 Thinking 和 API Key 签名开关不控制此功能。
已配置的错误策略优先。读取设置失败时不重试。

`countTokens` 使用相同的一次重试。成功时返回上游实际 `totalTokens`，再次收到 400 时返回错误。
插件不更改现有估算分支。签名清理可能丢失此前的思考连续性。

## 构建

```sh
UPSTREAM_TAG=v0.2.23 PYTHONDONTWRITEBYTECODE=1 python3 -B scripts/build.py
```

脚本下载公开上游源码，应用 `patches/gemini-signature-rectifier.patch`，运行测试，并生成服务器和安装器：

```text
dist/build/<os>-<arch>/sub2api-plugins-v0.2.23-<os>-<arch>
dist/build/<os>-<arch>/sub2api-plugins-installer-v0.2.23-<os>-<arch>
```

构建支持 Linux amd64/arm64、Darwin amd64/arm64 和 Windows amd64。Windows 文件名带 `.exe`。
在 [Sub2API-Plugins](https://github.com/Nanboom233/Sub2API-Plugins) 中注册本插件后，父仓库构建会组合所有已启用插件。
测试平铺在 `tests/`，不进入生产补丁。

## 回退与许可证

关闭「Gemini 原生签名整流」可停止新增重试。二进制回退使用安装器保留的程序备份。
插件采用 `LGPL-3.0-or-later`，见 [LICENSE](LICENSE) 和 [licenses/](licenses/)。
上游项目为 [Sub2API](https://github.com/Wei-Shaw/sub2api)。
