# new-api Vision Interception

在请求到达目标模型前，将图片转换为文字描述，让纯文本模型也能处理图片；批量替换后，转发请求体不再携带图片字节，避免历史图片 base64 在上游请求中反复堆积。

[![Base](https://img.shields.io/badge/base-new--api-green)](https://github.com/QuantumNous/new-api)
[![Suffix](https://img.shields.io/badge/model%20suffix--vision-blue)](#使用)
[![License](https://img.shields.io/badge/license-AGPL--3.0-orange)](https://github.com/QuantumNous/new-api/blob/main/LICENSE)

## 目录

* [简介](#简介)
* [特性](#特性)
* [工作原理](#工作原理)
* [安装](#安装)
* [使用](#使用)
* [安装验证](#安装验证)
* [目录结构](#目录结构)
* [限制与注意](#限制与注意)
* [计费说明](#计费说明)
* [许可证](#许可证)
* [致谢](#致谢)

## 简介

这是一个面向 [new-api](https://github.com/QuantumNous/new-api) 的视觉拦截扩展。

当请求中的模型名称带有指定后缀时，网关会先拦截请求，将消息中的图片交给用户配置的视觉模型生成文字描述，再将图片块整体替换为文本，并把请求转发给原目标模型。

例如：

```text
deepseek-v4-flash-vision
            ↓
调用视觉模型分析图片
            ↓
deepseek-v4-flash
```

目标模型最终收到的是图片描述，不再需要自身支持视觉输入。

支持的图片来源包括：

* OpenAI 消息格式中的 base64 图片；
* OpenAI 消息格式中的 URL 图片；
* Claude Messages 格式中的 base64 图片；
* Claude Messages 格式中的 URL 图片。

所有被提取的图片块都会批量替换为文字描述。转发给目标模型时，请求体不再携带这些图片的原始字节。

> 本项目减少的是 new-api 向目标模型转发时重复携带图片数据的问题。客户端第一次向 new-api 发送请求时，原始请求中仍然需要包含图片。

典型使用场景：

* 只有纯文本模型通道或额度，但需要处理图片输入；
* 希望把视觉识别和文本推理解耦；
* 对话历史中反复携带 base64 图片，导致请求体持续增大；
* 希望通过缓存减少相同或相似图片的重复视觉调用；
* 希望将多模态输入统一收敛为文本后再交给目标模型处理。

## 特性

| 特性          | 说明                                                    |
| ----------- | ----------------------------------------------------- |
| 用户级配置       | 每个用户可以独立配置启用状态、视觉模型、触发后缀、Prompt 和 pHash 阈值            |
| 模型名触发       | 请求模型名带指定后缀时触发，处理完成后移除后缀再转发                            |
| OpenAI 格式   | 支持 `image_url` 图片块，包括 URL 和 data URI                  |
| Claude 格式   | 支持 Claude Messages API 的 `image` / `source` 图片格式及嵌套内容 |
| URL 图片      | 自动下载并参与 pHash 计算，包含 SSRF 校验、下载大小限制和图片尺寸限制             |
| 批量替换        | 请求内提取到的所有图片块统一替换为文字描述                                 |
| 防 base64 堆积 | 转发给目标模型的请求体不再携带已提取图片的原始字节                             |
| 新旧图片区分      | 最后一条 `user` 消息中的图片走完整分析流程，历史图片只查询缓存                   |
| 严格失败        | 当前图片分析失败时返回错误，不静默降级，也不把原始图片继续交给纯文本模型                  |
| 历史图片占位符     | 历史图片没有缓存时使用明确的未解析占位符，不重新调用视觉模型                        |
| 请求内去重       | 同一请求中的相同 URL 图片只处理一次                                  |
| 多层缓存        | 包含请求级缓存、全局 LRU、singleflight 和 pHash 模糊缓存              |
| 缓存隔离        | 缓存按照用户、视觉模型和 Prompt 隔离，避免跨用户或跨配置复用                    |
| 标准计费        | 视觉调用使用 new-api 标准预扣、结算和退款流程                           |

## 工作原理

```text
请求
model: deepseek-v4-flash-vision
messages: 文本 + 图片
  │
  ▼
vision_intercept.go 中间件
  │
  ├─ 检查请求路径
  ├─ 读取用户视觉拦截设置
  ├─ 检查模型名称是否带触发后缀
  ├─ 移除模型后缀，得到真实目标模型
  ├─ 提取 OpenAI / Claude 格式中的全部图片
  ├─ URL 图片执行 SSRF 校验、下载和大小检查
  ├─ 相同 URL 去重
  ├─ 计算图片 pHash
  ├─ 按 pHash 汉明距离进行相似图片分组
  ├─ 新图查询缓存，未命中时调用视觉模型
  └─ 旧图只查询缓存，无缓存时使用占位符
  │
  ▼
批量替换所有图片块
  │
  ├─ 图片块变为文字描述
  └─ 转发请求体不再携带图片字节
  │
  ▼
目标模型
model: deepseek-v4-flash
messages: 文本 + 图片描述
```

### 新图与旧图

中间件会找到请求中最后一条 `user` 消息。

该消息中的图片被视为当前轮次的新图，其他消息中的图片被视为历史旧图。

#### 新图

新图会执行完整处理流程：

1. 计算 pHash；
2. 查询 pHash 模糊缓存；
3. 查询精确缓存；
4. 缓存未命中时调用视觉模型；
5. 将视觉描述写入缓存；
6. 使用描述替换图片块。

#### 旧图

旧图只读取已有缓存：

1. 查询 pHash 模糊缓存；
2. 查询精确缓存；
3. 命中时复用已有描述；
4. 未命中时使用占位符，不调用视觉模型。

占位符内容为：

```text
[This image was not parsed — its visual content is unavailable]
```

占位符路径只读不写，不会将占位符作为正常图片描述写入缓存。

### 缓存层级

| 层级              | 作用                   |
| --------------- | -------------------- |
| L1 请求级缓存        | 避免一次请求内重复分析相同图片      |
| L2 全局 LRU       | 精确复用相同图片的已有描述        |
| L3 singleflight | 合并相同图片的并发视觉调用，减少缓存击穿 |
| L4 pHash 模糊缓存   | 对视觉上相似的图片复用已有描述      |

缓存范围包含用户、视觉模型和 Prompt 信息，避免不同用户、不同视觉模型或不同提示词之间错误复用描述。

## 安装

### 前置条件

* 一份可正常构建的 new-api 源码；
* 与目标 new-api `go.mod` 匹配的 Go 版本；
* 与目标 new-api 前端项目及构建工具兼容的 Node.js 版本；
* 对应的前端包管理器；
* 能够正常完成 new-api 前端和后端构建。

new-api 前端产物会通过 Go 的 `go:embed web/default/dist` 打包进二进制，因此不能跳过前端构建。

> 本项目目前是源码扩展，不是可以动态加载的独立插件。
> new-api 上游目录和代码可能发生变化，建议在独立 Git 分支中完成合并。

建议先创建分支：

```bash
cd <new-api>
git checkout -b feature/vision-interception
```

### 1. 复制新增后端文件

```bash
cp src/middleware/vision_intercept.go \
  <new-api>/middleware/vision_intercept.go

mkdir -p <new-api>/service/vision

cp src/service/vision/vision.go \
  <new-api>/service/vision/vision.go
```

### 2. 复制新增前端组件

```bash
cp src/web/profile/components/vision-settings-card.tsx \
  <new-api>/web/default/src/features/profile/components/vision-settings-card.tsx
```

### 3. 修改现有文件

按照 `patches/` 目录中的说明，将对应内容合并到 new-api。

| 补丁文件                             | 目标文件                                                   | 作用                                            |
| -------------------------------- | ------------------------------------------------------ | --------------------------------------------- |
| `dto_user_settings.go`           | `<new-api>/dto/user_settings.go`                       | 添加 `VisionUserSetting` 和 `UserSetting.Vision` |
| `constant_context_key.go`        | `<new-api>/constant/context_key.go`                    | 添加视觉拦截相关 Context Key                          |
| `controller_user.go.snippet`     | `<new-api>/controller/user.go`                         | 接收、校验并保存用户视觉设置                                |
| `router_relay_router.go.snippet` | `<new-api>/router/relay-router.go`                     | 在 Relay 路由中挂载视觉拦截中间件                          |
| `web_profile_types.ts`           | `<new-api>/web/default/src/features/profile/types.ts`  | 添加视觉设置前端类型                                    |
| `web_profile_index.tsx`          | `<new-api>/web/default/src/features/profile/index.tsx` | 在个人资料页挂载视觉设置卡片                                |
| `go.mod.diff`                    | `<new-api>/go.mod`                                     | 添加图片 pHash 依赖                                 |

其中：

* 完整文件内容需要与目标文件合并；
* `.snippet` 文件只包含需要插入或修改的代码片段；
* `go.mod.diff` 是依赖修改说明，不是标准 unified diff，不能直接执行 `git apply`。

需要添加的直接依赖：

```text
github.com/corona10/goimagehash v1.1.0
```

间接依赖会在执行 `go mod tidy` 后自动补全。

### 4. 整理 Go 依赖

```bash
cd <new-api>
go mod tidy
```

### 5. 构建前端

```bash
cd <new-api>/web/default
npm install
npm run build
```

如果目标 new-api 使用的不是 npm，请按照其现有锁文件和构建方式执行对应命令。

前端构建完成后应生成：

```text
<new-api>/web/default/dist
```

### 6. 构建后端

```bash
cd <new-api>
go mod tidy
go build -o new-api .
```

### 7. 部署

二进制部署：

1. 停止当前服务；
2. 备份原二进制；
3. 替换为新构建的 `new-api`；
4. 重启服务。

Docker 部署：

1. 将修改后的源码放入镜像构建上下文；
2. 重新构建镜像；
3. 使用新镜像重新创建容器。

## 使用

### 1. 配置视觉拦截

进入：

```text
个人资料 → Vision Interception
```

配置项包括：

| 配置              | 说明                       |
| --------------- | ------------------------ |
| Enable Vision   | 是否启用视觉拦截                 |
| Vision Model    | 用于生成图片描述的视觉模型            |
| Prompt Template | 发送给视觉模型的图片描述提示词          |
| Model Suffix    | 触发视觉拦截的模型后缀，默认 `-vision` |
| pHash Threshold | 相似图片判断阈值，默认 `10`         |

### Vision Model

视觉模型名称必须与 new-api 中已经可用的模型名称一致。

中间件会使用当前用户的通道和账户配置调用该模型。

例如：

```text
gpt-4o
gemini-2.5-flash
qwen-vl-max
```

实际可用名称以当前 new-api 模型配置为准。

### Prompt Template

默认 Prompt：

```text
Please describe this image in detail, including all objects, text, people, colors, layout, and atmosphere.
```

Prompt 不能为空，最大长度为 8000 个字符。

Prompt 也会参与缓存隔离。修改 Prompt 后，旧 Prompt 产生的缓存描述不会被错误复用于新 Prompt。

### Model Suffix

默认后缀：

```text
-vision
```

例如目标模型是：

```text
deepseek-v4-flash
```

调用时使用：

```text
deepseek-v4-flash-vision
```

中间件处理图片后会将模型名恢复为：

```text
deepseek-v4-flash
```

后缀不能为空，最大长度为 64 个字符。

### pHash Threshold

pHash 使用图片感知哈希的汉明距离判断图片是否相似。

有效范围：

```text
0 ～ 64
```

规则：

* `0`：禁用 pHash，相同或相似图片不进行 pHash 聚类，每张唯一图片独立处理；
* 阈值越低：匹配越严格；
* 阈值越高：匹配越宽松；
* 默认值：`10`。

判断条件为：

```text
HammingDistance <= threshold
```

阈值越高，越容易将两张图片判定为相似。

阈值设置过高可能导致不同图片错误复用相同描述。

### 2. 发送请求

OpenAI Chat Completions 示例：

```bash
curl https://your-new-api.example.com/v1/chat/completions \
  -H "Authorization: Bearer sk-your-token" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-v4-flash-vision",
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

中间件处理后，目标模型收到的请求结构类似：

```json
{
  "model": "deepseek-v4-flash",
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

### Base64 图片

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

支持 Claude Messages API 图片结构：

```json
{
  "model": "deepseek-v4-flash-vision",
  "messages": [
    {
      "role": "user",
      "content": [
        {
          "type": "text",
          "text": "请分析这张图片。"
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

## 安装验证

### 1. 检查个人资料页

安装并重启后，个人资料页应出现：

```text
Vision Interception
```

设置卡片。

### 2. 启用并保存配置

启用视觉拦截，并配置：

* 可用的视觉模型；
* Prompt；
* `-vision` 后缀；
* pHash 阈值。

### 3. 发送带图请求

使用：

```text
deepseek-v4-flash-vision
```

作为请求模型名。

### 4. 检查日志

正常情况下应出现类似日志：

```text
[vision] intercepting model=deepseek-v4-flash-vision suffix=-vision
[vision] found 2 images (...)
[vision] 2 groups: 1 cached, 1 api-call
[vision] replaced 2/2 images (0 failed)
```

日志含义：

* `intercepting`：模型后缀已被识别；
* `found N images`：成功提取图片；
* `cached`：命中缓存或使用历史图片占位符；
* `api-call`：实际调用视觉模型的图片组数量；
* `replaced N/M images`：图片块替换完成。

## 支持的接口

中间件只处理以下聊天接口：

```text
/v1/chat/completions
/chat/completions
/v1/messages
/messages
```

Embedding、音频和其他接口不会触发视觉拦截。

如果请求模型名带有视觉后缀，但消息中没有图片，中间件只会移除模型后缀，然后继续转发。

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

## 限制与注意

### URL 图片

URL 图片仅接受：

```text
http://
https://
```

下载和解析过程中会检查：

* URL 协议；
* SSRF 风险；
* HTTP 响应状态；
* `Content-Length`；
* 实际下载大小；
* 图片格式；
* 图片宽高；
* 图片总像素。

URL 图片下载失败、校验失败或超过限制时，请求会返回错误。

### 图片限制

| 项目            |       限制 |
| ------------- | -------: |
| data URI 原始长度 |   20 MiB |
| Base64 解码读取上限 |   15 MiB |
| URL 图片下载上限    |   15 MiB |
| 图片单边最大尺寸      |  8192 像素 |
| 图片最大总像素       | 2000 万像素 |
| pHash 阈值      |     0～64 |
| Prompt 最大长度   |  8000 字符 |
| 模型后缀最大长度      |    64 字符 |
| 单组视觉分析超时      |     30 秒 |

### 图片替换

所有成功提取的图片块，包括 base64 和 URL 图片，都会被整体替换为文字描述。

转发给目标模型时，请求体不再携带这些图片的原始字节。

### 视频

`video_url` 内容块会被主动跳过。

视频无法通过当前图片分析流程处理，并且不会被错误地作为图片发送给视觉模型。

### 历史图片

历史图片只查询缓存。

缓存没有对应描述时，不会重新调用视觉模型，而是使用：

```text
[This image was not parsed — its visual content is unavailable]
```

历史图片占位符仍会保留其在消息中的上下文位置，但目标模型无法获取该图片的真实视觉内容。

### 分析失败

当前轮次的新图片分析失败时，中间件会返回错误。

不会：

* 静默忽略失败；
* 使用空描述继续请求；
* 将原始图片透传给纯文本目标模型。

错误响应类似：

```json
{
  "error": {
    "message": "Vision interception failed: image analysis failed for 1 image(s): ...",
    "type": "vision_error"
  }
}
```

### 缓存

缓存位于当前 new-api 进程内。

因此：

* 服务重启后缓存会丢失；
* 多实例部署时，各实例默认不共享缓存；
* 缓存命中情况可能因请求落到不同实例而变化；
* 当前实现不是持久化分布式缓存。

### pHash

pHash 判断的是图片视觉相似度，不保证语义完全一致。

阈值过高可能导致不同图片被分到同一组，并复用相同描述。

### 性能

图片请求会增加：

* 图片下载时间；
* 图片解码和 pHash 计算开销；
* 视觉模型调用时间；
* 额外视觉模型费用。

当前实现包含以下并发限制：

* 图片下载和 pHash 计算并发限制为 2；
* 图片分析组并发限制为 4；
* 单个视觉分析组超时时间为 30 秒；
* 相同图片并发调用通过 singleflight 合并。

## 计费说明

图片处理和最终回答属于两个模型调用阶段：

1. 视觉模型将图片转换为文字描述；
2. 目标模型根据文字描述完成最终回答。

视觉调用使用 new-api 标准计费流程：

```text
预扣
  ↓
视觉模型调用
  ↓
成功结算
或
失败退款
```

视觉模型调用按照所选视觉模型的价格计费。

目标请求仍按照目标模型正常计费。

因此，一次未命中缓存的带图请求通常会产生：

* 视觉模型费用；
* 目标模型费用。

缓存命中时不会重复调用视觉模型，但目标模型请求仍会正常计费。

订阅、钱包、模型倍率和渠道计费行为遵循当前 new-api 的标准逻辑和用户配置。

## 已知限制

* 当前需要手工合并源码；
* 不是 new-api 官方插件；
* new-api 更新后可能需要重新适配；
* 缓存保存在进程内；
* 多实例之间默认不共享缓存；
* 历史图片没有缓存时不会重新分析；
* pHash 只能判断视觉相似度；
* 图片转换为文字后可能损失部分视觉信息；
* 效果依赖视觉模型和 Prompt；
* 视频内容暂不支持。

## 许可证

本项目与 new-api 保持一致，采用 AGPL-3.0 许可证。

完整许可证文本请参考：

* [new-api LICENSE](https://github.com/QuantumNous/new-api/blob/main/LICENSE)

## 致谢

* [QuantumNous/new-api](https://github.com/QuantumNous/new-api)
* [corona10/goimagehash](https://github.com/corona10/goimagehash)
* [Linux DO](https://linux.do/)
