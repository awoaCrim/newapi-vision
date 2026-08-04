# new-api Vision Interception

在请求到达目标模型前，把图片转成文字描述，让纯文本模型也能「看图」。

[![Base](https://img.shields.io/badge/base-new--api-green)](https://github.com/QuantumNous/new-api)
[![Suffix](https://img.shields.io/badge/model%20suffix--vision-blue)]()
[![License](https://img.shields.io/badge/license-AGPL--3.0-orange)](https://github.com/QuantumNous/new-api/blob/main/LICENSE)

## 目录

- [简介](#简介)
- [特性](#特性)
- [工作原理](#工作原理)
- [合并到 new-api](#合并到-new-api)
- [使用](#使用)
- [目录结构](#目录结构)
- [限制与注意](#限制与注意)
- [许可证](#许可证)
- [致谢](#致谢)

## 简介

这是 new-api 的扩展：当请求的模型名带 `-vision` 后缀时，网关先拦截请求，把消息中的图片（base64 或 URL）交给配置的视觉模型转成文字描述，再以纯文本请求转发给目标模型。用户请求的目标模型保持不变，网关只替换消息内容。

典型场景：只有纯文本模型的通道/额度，却要处理带图请求；或希望多模态输入统一收敛到文本通道。

## 特性

| 特性 | 说明 |
|------|------|
| 用户级配置 | 个人资料页开关：启用、视觉模型、后缀、prompt、pHash 阈值 |
| 模型名触发 | 请求模型名带后缀（默认 `-vision`）即拦截，去后缀后转发 |
| 消息格式 | OpenAI / Claude 两种消息格式均支持 |
| URL 图片 | 自动下载并参与 pHash 查重，含 SSRF 校验与大小 / 维度限制 |
| 标准计费 | 独立子计费生命周期：预扣 / 成功结算 / 失败自动退款，走 new-api 标准计费主路径，按视觉模型计费 |
| 严格失败 | 分析失败返回错误，不静默降级；旧图无缓存数据时返回占位符文本 |
| 防 base64 堆积 | 批量替换：请求内所有图片（base64 / URL）统一替换为文字描述，转发请求体不再携带图片字节，仅向目标模型发送纯文本 |
| 多层缓存 | L1 请求级去重、L2 全局 LRU、L3 singleflight、L4 pHash 模糊缓存，缓存键按用户 + 模型隔离；占位符替换不影响缓存键结构，旧图占位符路径只读不写，缓存命中不受污染 |

## 工作原理

```
请求（model: xxx-vision，含图片）
  │
  ▼
vision_intercept.go 中间件
  ├─ 模型名去后缀，标记 ContextKeyVisionIntercepted
  ├─ 提取全部图片：base64 / URL 下载（SSRF 校验 + 大小限制）
  ├─ URL 去重 → pHash 聚类分组（新图走完整分析，旧图仅查缓存）
  ├─ 计算 pHash → 查 L1~L4 缓存 → 未命中进入模型广场
  └─ 走用户通道调用视觉模型，得到文字描述
  │
  ▼
批量替换：所有图片块（含 base64）整体替换为文字描述
  │  （转发体不再携带任何图片字节，仅纯文本）
  ▼
标准计费：预扣 → 成功结算（PostTextConsumeQuota）/ 失败自动退款（Refund）
```

## 合并到 new-api

前置：一份可编译的 new-api 源码（Go 版本满足 go.mod 要求）。

```bash
# 1. 复制新增源码
cp src/middleware/vision_intercept.go <new-api>/middleware/
cp -r src/service/vision <new-api>/service/vision
cp src/web/profile/components/vision-settings-card.tsx \
   <new-api>/web/default/src/features/profile/components/

# 2. 按 patches/ 修改既有文件
#    dto/user_settings.go、constant/context_key.go、controller/user.go、
#    router/relay-router.go、web/types.ts、web/index.tsx、go.mod

# 3. 构建
go mod tidy && go build -o new-api .
```

patches/ 约定：完整新文件可直接复制；`.snippet` / `.diff` 是既有文件的修改片段，需按注释手工应用。

## 使用

1. 个人资料页 → Vision Interception → 启用，选择模型广场中的视觉模型，按需调整 prompt 与 pHash 阈值
2. 请求时模型名加后缀，例如 `minimax-m3-vision`
3. 网关去掉后缀，用文字描述替换图片后再转发

pHash 阈值（0 ~ 100）：越低越宽松，越容易被判定为相似图片；相同 pHash 的图片命中缓存后不再重复调用视觉模型。

## 目录结构

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
```

## 限制与注意

- **URL 图片**：仅接受 http/https，经 SSRF 校验；超限或解析失败会返回错误，可换 base64 或直接关闭
- **请求体替换**：所有图片块（含 base64）整体替换为文字描述，转发给目标模型时不携带图片字节；`video_url` 块有意跳过（视频无法描述，且为 URL 形式，不含 base64）
- **占位符**：旧图（缓存建立前）返回占位符文本，仍会正常计费主请求
- **分析失败**：严格返回错误，不会把原始图片透传给纯文本模型
- **缓存隔离**：缓存键含用户与目标模型，不同用户 / 模型之间互不共享；占位符替换不影响缓存键结构，旧图占位符路径只读不写缓存
- **计费**：视觉分析按视觉模型价格走标准计费主路径，主请求仍按目标模型结算；订阅用户遵循 new-api 标准接口默认行为

## 许可证

与 new-api 一致（AGPL-3.0），完整文本见 [upstream LICENSE](https://github.com/QuantumNous/new-api/blob/main/LICENSE)。

## 致谢

- [new-api](https://github.com/QuantumNous/new-api)
- [Linux DO](https://linux.do/)
