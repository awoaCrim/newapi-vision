# new-api Vision Interception

让纯文本模型也能处理图片，并避免向目标模型重复转发历史图片的 base64 数据。

[![Base](https://img.shields.io/badge/base-new--api-green)](https://github.com/QuantumNous/new-api)
[![Trigger](https://img.shields.io/badge/trigger-model%20suffix-blue)](#使用方法)
[![License](https://img.shields.io/badge/license-AGPL--3.0-orange)](https://github.com/QuantumNous/new-api/blob/main/LICENSE)

## 项目简介

`new-api Vision Interception` 是一个面向 [new-api](https://github.com/QuantumNous/new-api) 的源码扩展。

启用后，当请求中的模型名称带有指定后缀，例如：

```text
minimax-m3-vision
```

中间件会在请求进入目标模型前：

1. 提取消息中的图片；
2. 调用用户配置的视觉模型生成图片描述；
3. 将图片块替换为文本描述；
4. 移除模型名称中的 `-vision` 后缀；
5. 把纯文本请求转发给原目标模型。

例如：

```text
minimax-m3-vision
        ↓
图片转文字
        ↓
minimax-m3
```

目标文本模型最终收到的是图片描述，而不是原始图片数据。

> [!IMPORTANT]
> 本项目解决的是 **new-api 向目标模型转发时重复携带图片数据** 的问题。
>
> 客户端首次向 new-api 上传图片时，原始请求中仍然包含图片；中间件需要读取和解析图片后，才能将其替换为文本。

## 适用场景

* 只有纯文本模型额度，但需要处理图片输入；
* 希望统一通过文本模型完成后续推理；
* 对话历史中反复携带 base64 图片，导致请求体越来越大；
* 希望将图片识别和文本推理解耦；
* 希望通过缓存减少重复图片的视觉模型调用。

## 功能特性

| 功能         | 说明                                               |
| ---------- | ------------------------------------------------ |
| 用户级配置      | 每个用户可以独立启用功能并配置视觉模型、后缀、提示词和 pHash 阈值             |
| 模型后缀触发     | 模型名称带指定后缀时触发，处理完成后自动移除后缀                         |
| OpenAI 格式  | 支持 `image_url` 图片块，包括 URL 和 data URI             |
| Claude 格式  | 支持 Claude Messages API 的 `image` / `source` 图片格式 |
| URL 图片处理   | 下载 URL 图片并进行类型、大小、尺寸和 SSRF 安全检查                  |
| 批量图片替换     | 请求内提取到的图片会统一替换为文本描述                              |
| 历史图片处理     | 历史图片优先读取缓存，无缓存时使用明确的未解析占位符                       |
| 请求内去重      | 相同图片在一次请求中只需处理一次                                 |
| pHash 相似判断 | 视觉上相近的图片可以复用同一份描述                                |
| 并发合并       | 相同图片的并发分析请求通过 singleflight 合并                    |
| 多用户隔离      | 缓存按用户、视觉模型和提示词隔离                                 |
| 标准计费       | 视觉分析调用沿用 new-api 的标准计费流程                         |
| 严格失败       | 当前图片分析失败时直接返回错误，不把图片静默交给纯文本模型                    |

## 工作原理

```text
客户端请求
model: minimax-m3-vision
messages: 文本 + 图片
          │
          ▼
VisionIntercept 中间件
          │
          ├─ 检查请求路径
          ├─ 检查用户是否启用视觉拦截
          ├─ 检查模型名称是否带触发后缀
          ├─ 提取 OpenAI / Claude 格式图片
          ├─ 下载或解码图片
          ├─ URL 去重
          ├─ 计算 pHash 并进行相似图片分组
          ├─ 查询多层缓存
          └─ 未命中时调用视觉模型
          │
          ▼
将图片块替换为文本描述
          │
          ├─ 移除模型名称中的 -vision 后缀
          └─ 请求体不再向目标模型携带已提取的图片字节
          │
          ▼
目标文本模型
model: minimax-m3
messages: 文本 + 图片描述
```

## 图片处理策略

中间件会区分当前轮次的新图片和历史消息中的旧图片。

### 当前轮次的新图片

最后一条 `user` 消息中的图片被视为新图片。

处理顺序：

1. 查询 pHash 模糊缓存；
2. 查询精确图片缓存；
3. 未命中时调用视觉模型；
4. 将分析结果写入缓存；
5. 使用描述文本替换图片块。

### 历史消息中的旧图片

历史消息中的图片不会无条件再次调用视觉模型。

处理顺序：

1. 查询已有缓存；
2. 命中时复用图片描述；
3. 未命中时替换为未解析占位符：

```text
[This image was not parsed — its visual content is unavailable]
```

这样可以避免长对话中的历史图片被反复分析和重复计费。

## 缓存结构

项目包含多层缓存机制：

| 层级              | 用途                  |
| --------------- | ------------------- |
| L1 请求级缓存        | 避免同一请求内重复分析同一图片     |
| L2 精确缓存         | 按图片、用户、模型和提示词精确复用结果 |
| L3 singleflight | 合并相同图片的并发视觉模型请求     |
| L4 pHash 缓存     | 对视觉上相似的图片复用已有描述     |

缓存键包含用户、视觉模型和提示词信息，避免不同用户或不同配置之间错误复用描述。

## pHash 阈值

pHash 使用图片感知哈希的汉明距离判断两张图片是否相似。

有效范围：

```text
0 ～ 64
```

规则如下：

* `0`：禁用 pHash 相似图片匹配；
* 数值越小：判断越严格；
* 数值越大：判断越宽松，越容易将两张图片识别为相似；
* 默认值：`10`。

示例：

| 阈值 | 效果                   |
| -: | -------------------- |
|  0 | 完全禁用 pHash           |
|  4 | 非常严格，仅匹配高度相似图片       |
| 10 | 推荐默认值                |
| 20 | 比较宽松，可能合并存在明显差异的图片   |
| 64 | 几乎所有图片都可能被视为相似，不建议使用 |

> [!WARNING]
> 阈值设置过高可能导致不同图片错误复用相同描述。

## 安装

### 安装前说明

本项目目前是 **new-api 源码扩展**，不是可以动态加载的独立插件。

安装需要：

* 向 new-api 添加新的后端和前端文件；
* 修改部分现有源码；
* 重新构建前端；
* 重新编译 new-api。

建议先为 new-api 创建 Git 分支：

```bash
cd /path/to/new-api
git checkout -b feature/vision-interception
```

### 环境要求

* 一份可正常构建的 new-api 源码；
* Go `1.25.1`，或与目标 new-api `go.mod` 一致的 Go 版本；
* Node.js 18 或更高版本；
* npm；
* 能够正常完成 new-api 前后端构建。

> [!CAUTION]
> new-api 的目录结构和源码会持续变化。
>
> 本仓库暂未承诺兼容所有 new-api 版本。修改现有文件时请优先手工合并，不要在未比较差异的情况下直接覆盖。

### 1. 克隆仓库

```bash
git clone https://github.com/awoaCrim/newapi-vision.git
cd newapi-vision
```

假设：

```bash
NEW_API=/path/to/new-api
VISION_EXT=/path/to/newapi-vision
```

### 2. 复制新增后端文件

```bash
cp "$VISION_EXT/src/middleware/vision_intercept.go" \
   "$NEW_API/middleware/vision_intercept.go"

mkdir -p "$NEW_API/service/vision"

cp "$VISION_EXT/src/service/vision/vision.go" \
   "$NEW_API/service/vision/vision.go"
```

### 3. 复制新增前端组件

```bash
cp "$VISION_EXT/src/web/profile/components/vision-settings-card.tsx" \
   "$NEW_API/web/default/src/features/profile/components/vision-settings-card.tsx"
```

### 4. 合并现有文件修改

根据 `patches/` 目录中的文件，将对应修改合并到 new-api。

| 补丁文件                             | 目标位置                      | 修改内容                                          |
| -------------------------------- | ------------------------- | --------------------------------------------- |
| `dto_user_settings.go`           | `dto/user_settings.go`    | 添加 `VisionUserSetting` 和 `UserSetting.Vision` |
| `constant_context_key.go`        | `constant/context_key.go` | 添加视觉拦截相关 Context Key                          |
| `controller_user.go.snippet`     | `controller/user.go`      | 接收、校验并保存用户视觉设置                                |
| `router_relay_router.go.snippet` | `router/relay-router.go`  | 挂载 `VisionIntercept` 中间件                      |
| `web_profile_types.ts`           | 前端用户设置类型文件                | 添加 `VisionSettings` 类型                        |
| `web_profile_index.tsx`          | 前端个人资料页面                  | 挂载视觉设置卡片                                      |
| `go.mod.diff`                    | `go.mod`                  | 添加图片 pHash 依赖                                 |

> [!IMPORTANT]
> `patches/go.mod.diff` 当前是依赖修改说明，不是标准 unified diff，不能直接执行 `git apply`。
>
> 请按照文件内容手工添加依赖，然后运行 `go mod tidy`。

### 5. 安装 Go 依赖

需要添加的直接依赖：

```text
github.com/corona10/goimagehash v1.1.0
```

可以在合并完代码后运行：

```bash
cd "$NEW_API"
go mod tidy
```

`go mod tidy` 会自动补充所需的间接依赖。

### 6. 构建前端

new-api 使用 `go:embed` 将前端产物嵌入 Go 二进制，因此必须先生成前端 `dist`。

```bash
cd "$NEW_API/web/default"
npm install
npm run build
```

> [!WARNING]
> 如果跳过前端构建，Go 编译阶段可能因为缺少 `web/default/dist` 而失败。

### 7. 构建后端

```bash
cd "$NEW_API"
go mod tidy
go build -o new-api .
```

### 8. 部署

如果使用二进制部署：

1. 停止旧服务；
2. 备份原二进制；
3. 替换为新二进制；
4. 重启服务。

如果使用 Docker：

```bash
docker build -t new-api-vision .
```

然后使用新镜像重新创建容器。

## 配置方法

安装完成后，进入：

```text
个人资料 → Vision Interception
```

可以配置以下字段。

### Enable Vision

是否启用视觉拦截。

关闭后，即使模型名称带有 `-vision` 后缀，中间件也不会执行图片分析。

### Vision Model

用于生成图片描述的视觉模型名称。

该模型必须已经存在于 new-api 的模型广场或可用模型配置中，例如：

```text
gpt-4o
gemini-2.5-flash
qwen-vl-max
```

实际可填写的名称取决于你的 new-api 模型配置。

### Prompt Template

发送给视觉模型的图片描述提示词。

默认示例：

```text
Please describe this image in detail, including all objects, text, people, colors, layout, and atmosphere.
```

为了让后续文本模型获得更完整的信息，可以根据业务调整提示词。

例如，用于界面截图分析：

```text
Describe this screenshot precisely. Extract all visible text, identify the page structure, buttons, input fields, error messages, selected states, and any abnormal UI behavior.
```

用于文档图片：

```text
Analyze this document image. Preserve all readable text, headings, tables, numbers, labels, and the logical relationship between sections.
```

提示词不能为空，最大长度为 8000 个字符。

### Model Suffix

用于触发视觉拦截的模型后缀。

默认值：

```text
-vision
```

例如目标模型为：

```text
minimax-m3
```

调用时使用：

```text
minimax-m3-vision
```

中间件处理完成后，会将请求中的模型恢复为：

```text
minimax-m3
```

后缀不能为空，最大长度为 64 个字符。

### pHash Threshold

相似图片识别阈值。

推荐从默认值 `10` 开始，根据实际命中情况调整。

## 使用方法

### OpenAI Chat Completions 示例

```bash
curl https://your-new-api.example.com/v1/chat/completions \
  -H "Authorization: Bearer sk-your-token" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "minimax-m3-vision",
    "messages": [
      {
        "role": "user",
        "content": [
          {
            "type": "text",
            "text": "请分析这张图片。"
          },
          {
            "type": "image_url",
            "image_url": {
              "url": "https://example.com/example.png"
            }
          }
        ]
      }
    ]
  }'
```

处理中间件会将请求转换为类似结构：

```json
{
  "model": "minimax-m3",
  "messages": [
    {
      "role": "user",
      "content": [
        {
          "type": "text",
          "text": "请分析这张图片。"
        },
        {
          "type": "text",
          "text": "[Image description: The image shows ...]"
        }
      ]
    }
  ]
}
```

### Base64 图片示例

支持 OpenAI data URI：

```json
{
  "type": "image_url",
  "image_url": {
    "url": "data:image/png;base64,iVBORw0KGgoAAA..."
  }
}
```

### Claude Messages 格式

同时支持 Claude Messages API 的图片结构：

```json
{
  "model": "target-model-vision",
  "messages": [
    {
      "role": "user",
      "content": [
        {
          "type": "text",
          "text": "分析图片内容"
        },
        {
          "type": "image",
          "source": {
            "type": "base64",
            "media_type": "image/png",
            "data": "iVBORw0KGgoAAA..."
          }
        }
      ]
    }
  ]
}
```

## 生效范围

中间件仅处理聊天消息相关接口：

```text
/v1/chat/completions
/chat/completions
/v1/messages
/messages
```

Embedding、音频和其他接口不会进行视觉拦截。

如果请求没有图片，但模型名称带有触发后缀，中间件只会移除后缀，然后继续转发。

## 安装验证

### 1. 检查设置页面

登录 new-api 后进入个人资料页。

如果出现以下设置卡片，说明前端安装成功：

```text
Vision Interception
```

### 2. 检查日志

发送带图请求后，日志中应出现类似内容：

```text
[vision] intercepting model=minimax-m3-vision suffix=-vision
[vision] found 2 images (...)
[vision] 2 groups: 1 cached, 1 api-call
[vision] replaced 2/2 images (0 failed)
```

### 3. 检查目标模型

请求：

```text
minimax-m3-vision
```

应最终路由到：

```text
minimax-m3
```

### 4. 检查请求结果

目标文本模型应能够根据自动生成的图片描述回答问题。

## 图片限制

为减少内存耗尽、超大图片和恶意 URL 带来的风险，项目包含以下限制。

| 类型            |       限制 |
| ------------- | -------: |
| data URI 原始长度 |   20 MiB |
| Base64 解码读取上限 |   15 MiB |
| URL 图片下载上限    |   15 MiB |
| 图片单边最大尺寸      |  8192 像素 |
| 图片最大总像素       | 2000 万像素 |
| pHash 范围      |     0～64 |
| Prompt 最大长度   |  8000 字符 |
| 模型后缀最大长度      |    64 字符 |
| 单组视觉分析超时      |     30 秒 |

超过限制、图片解析失败或 URL 校验失败时，请求会返回视觉拦截错误。

## URL 图片安全

URL 图片仅接受：

```text
http://
https://
```

下载前会执行 URL 安全校验，并在下载和图片解码阶段检查：

* URL 协议；
* 响应状态；
* Content-Length；
* 实际下载大小；
* 图片格式；
* 图片宽高；
* 图片总像素。

即使服务端没有返回正确的 `Content-Length`，实际读取过程仍会受到大小上限约束。

## 错误处理

### 当前图片分析失败

当当前轮次的新图片无法完成分析时，中间件会停止请求并返回错误。

示例：

```json
{
  "error": {
    "message": "Vision interception failed: image analysis failed for 1 image(s): ...",
    "type": "vision_error"
  }
}
```

项目不会将无法处理的原始图片继续转发给纯文本模型。

### 历史图片没有缓存

历史图片没有缓存时不会再次调用视觉模型，而会替换为未解析占位符。

这不是视觉模型调用失败，也不会导致整个请求失败。

## 计费说明

视觉分析和目标模型请求是两个模型调用阶段：

1. 视觉模型负责将图片转换为文字描述；
2. 目标模型负责根据文字描述完成最终回答。

视觉分析调用使用 new-api 的标准计费流程：

```text
预扣额度
  ↓
视觉模型调用
  ↓
成功结算 / 失败退款
```

因此一次带图请求通常可能产生：

* 视觉模型费用；
* 目标文本模型费用。

缓存命中的图片不会再次调用视觉模型，但主请求仍会按照目标模型正常计费。

具体订阅、钱包、模型倍率和渠道计费行为以当前 new-api 配置为准。

## 性能说明

当前实现包含以下并发控制：

* 图片下载和 pHash 计算最多同时处理 2 个任务；
* 图片分析组最多同时处理 4 个任务；
* 单个视觉分析组超时时间为 30 秒；
* 相同图片并发请求通过 singleflight 合并。

多图请求仍可能明显增加响应时间，实际速度取决于：

* 图片数量和大小；
* URL 图片下载速度；
* 视觉模型响应速度；
* 是否命中缓存；
* new-api 通道负载。

## 目录结构

```text
newapi-vision/
├── src/
│   ├── middleware/
│   │   └── vision_intercept.go
│   ├── service/
│   │   └── vision/
│   │       └── vision.go
│   └── web/
│       └── profile/
│           └── components/
│               └── vision-settings-card.tsx
├── patches/
│   ├── dto_user_settings.go
│   ├── constant_context_key.go
│   ├── controller_user.go.snippet
│   ├── router_relay_router.go.snippet
│   ├── web_profile_types.ts
│   ├── web_profile_index.tsx
│   └── go.mod.diff
└── README.md
```

## 常见问题

### 为什么请求模型要写成 `xxx-vision`？

模型后缀是视觉拦截的触发标志。

例如：

```text
deepseek-v4-vision
```

中间件识别后缀后，会先分析图片，再把实际模型名称改回：

```text
deepseek-v4
```

### 目标模型必须支持图片吗？

不需要。

图片会先被视觉模型转换为文字描述，目标模型只需要支持文本输入。

### 视觉模型必须与目标模型相同吗？

不需要。

例如可以使用：

```text
视觉模型：gpt-4o
目标模型：minimax-m3
请求模型：minimax-m3-vision
```

### 使用后客户端请求体会立即变小吗？

不会。

客户端发送给 new-api 的原始请求仍然包含图片。

本项目主要避免的是：在中间件处理完成后，继续向目标文本模型转发图片数据，以及长对话中反复向目标模型携带历史图片。

### 为什么历史图片没有重新识别？

为了避免每次对话都重新分析全部历史图片。

历史图片只读取缓存；没有缓存时使用未解析占位符。

### 为什么不同图片获得了相同描述？

可能是 pHash 阈值设置过高，导致两张图片被判断为相似。

请降低 `pHash Threshold`，或者设置为 `0` 完全禁用。

### 为什么设置了阈值但没有复用缓存？

可能原因包括：

* 缓存已经过期；
* 图片差异超过当前阈值；
* 用户不同；
* 视觉模型不同；
* Prompt Template 不同；
* pHash 被禁用；
* 图片解析失败，未能计算 pHash。

### 为什么 Go 编译提示找不到前端文件？

请先构建前端：

```bash
cd web/default
npm install
npm run build
```

然后再执行：

```bash
go build -o new-api .
```

### 为什么 `git apply patches/go.mod.diff` 失败？

该文件目前是依赖修改说明，不是标准 Git diff。

请手工修改 `go.mod`，然后运行：

```bash
go mod tidy
```

### 是否支持视频？

不支持。

`video_url` 内容块会被主动跳过，不会作为图片发送给视觉模型。

## 已知限制

* 当前以源码补丁形式发布，没有自动安装器；
* 与 new-api 最新源码的兼容性不能永久保证；
* 历史图片在没有缓存时不会重新分析；
* 缓存位于当前进程内，服务重启后不会保留；
* 多实例部署时，各实例缓存互不共享；
* pHash 只能判断视觉相似性，不能保证语义完全一致；
* 图片描述会损失部分视觉信息，效果取决于视觉模型和提示词；
* 图片分析会增加首轮响应时间和额外模型费用；
* 视频内容暂不处理。

## 升级建议

升级 new-api 前建议：

```bash
git status
git add .
git commit -m "feat: add vision interception"
```

升级完成后重点检查：

* `dto.UserSetting` 是否发生变化；
* Relay 路由和中间件顺序是否变化；
* 用户设置接口是否变化；
* 前端个人资料目录是否变化；
* `go:embed` 的前端产物路径是否变化；
* new-api 的内部计费接口是否变化。

不要在存在未提交改动时直接覆盖源码。

## 许可证

本项目基于 new-api 扩展，许可证与上游保持一致，采用 AGPL-3.0。

完整许可证文本请参考：

* [new-api LICENSE](https://github.com/QuantumNous/new-api/blob/main/LICENSE)

使用、修改或分发本项目时，请遵守 AGPL-3.0 的相关要求。

## 致谢

* [QuantumNous/new-api](https://github.com/QuantumNous/new-api)
* [corona10/goimagehash](https://github.com/corona10/goimagehash)
* [Linux DO](https://linux.do/)

## 免责声明

本项目不是 new-api 官方组件。

在生产环境部署前，请自行完成：

* 源码审查；
* 权限和计费验证；
* URL 图片安全测试；
* 高并发和大图片压力测试；
* 数据隐私与合规评估。
