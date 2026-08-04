# new-api Vision Interception（内嵌版）

> **本仓库已从「插件化」重构为「内嵌补丁」形式**：vision 拦截代码不再依赖插件框架，而是作为 new-api 源码的一部分直接合并。

在请求到达目标模型前，把图片转成文字描述，让纯文本模型也能「看图」。

## 为什么是内嵌形式

2026-07 曾将 vision 拦截提取为可安装插件（`plugin/` + `UserSetting.Extensions` 扩展机制）。该方案在还原后不再使用——vision 恢复为与上游 new-api 一致的内嵌实现：

- 中间件硬编码挂载于 `router/relay-router.go`
- 用户设置直接存储在 `UserSetting.Vision`（`dto/user_settings.go`）
- 设置持久化为普通 JSON，无需扩展机制

本仓库保存这份内嵌实现的完整源码与合并补丁，方便在任何 new-api 版本上重建该功能。

## 功能

- 用户级配置（个人资料页）：启用、视觉模型、后缀、prompt、pHash 阈值
- 模型名加后缀（默认 `-vision`）触发拦截
- OpenAI / Claude 消息格式；URL 去重 + pHash 聚类
- 经系统模型广场渠道调用视觉模型并计费到当前用户
- 分析失败严格返回错误（不静默降级）
- 多层缓存：请求级去重（L1）、全局 LRU（L2）、singleflight（L3）、pHash 模糊缓存（L4）

## 目录

```
src/
  middleware/vision_intercept.go   # 拦截中间件（复制到 new-api 的 middleware/）
  service/vision/vision.go         # 分析 / 缓存 / 计费（复制到 new-api 的 service/vision/）
  web/profile/components/          # 前端设置卡片
patches/
  dto_user_settings.go             # 完整新文件（含 VisionUserSetting）
  constant_context_key.go          # 完整新文件（含 ContextKeyVisionIntercepted）
  controller_user.go.snippet       # controller/user.go 修改片段
  router_relay_router.go.snippet   # router/relay-router.go 修改片段
  web_profile_types.ts             # 完整新文件（含 VisionSettings）
  web_profile_index.tsx            # 完整新文件（挂载设置卡片）
  go.mod.diff                      # go.mod 依赖变更
docs/
  integration.md                   # 详细合并步骤
```

## 合并到 new-api

```bash
# 1. 复制新增源码
cp src/middleware/vision_intercept.go <new-api>/middleware/
cp -r src/service/vision <new-api>/service/vision
cp src/web/profile/components/vision-settings-card.tsx <new-api>/web/default/src/features/profile/components/

# 2. 按 patches/ 修改既有文件（详见 docs/integration.md）
#    dto/user_settings.go、constant/context_key.go、
#    controller/user.go、router/relay-router.go、
#    web/types.ts、web/index.tsx、go.mod

# 3. 构建
go mod tidy && go build -o new-api .
```

## 使用

1. 个人资料 → Vision Interception → 开启并选择模型广场中的视觉模型
2. 请求模型名加后缀，例如 `minimax-m3-vision`
3. 网关去掉后缀，用文字描述替换图片后再转发给目标模型

## License

与 new-api 一致（AGPL-3.0）。本代码用于在兼容宿主上扩展功能。

## 致谢

- [new-api](https://github.com/QuantumNous/new-api)
- [Linux DO](https://linux.do/)
