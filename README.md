# new-api Vision Interception

让纯文本模型也能处理图片，并避免向目标模型重复转发图片 base64。

[![Base](https://img.shields.io/badge/base-new--api-green)](https://github.com/QuantumNous/new-api)
[![License](https://img.shields.io/badge/license-AGPL--3.0-orange)](https://github.com/QuantumNous/new-api/blob/main/LICENSE)

## 简介

这是一个面向 [new-api](https://github.com/QuantumNous/new-api) 的视觉拦截扩展。

当请求模型名带有 `-vision` 后缀时，中间件会：

1. 提取请求中的图片；
2. 调用配置的视觉模型生成图片描述；
3. 将图片替换为文字；
4. 移除模型名中的 `-vision`；
5. 将纯文本请求转发给目标模型。

例如：

```text
deepseek-v4-flash-vision
        ↓ 图片转文字
deepseek-v4-flash
```

目标模型不需要支持图片输入。

## 特性

* 支持 OpenAI 和 Claude 消息格式
* 支持 base64 和 URL 图片
* 用户级视觉模型、Prompt、后缀配置
* 请求内图片去重
* pHash 相似图片缓存
* singleflight 并发请求合并
* 缓存按用户、模型和 Prompt 隔离
* 图片分析使用 new-api 标准计费流程
* 图片分析失败时直接返回错误
* 转发给目标模型时不再携带已处理图片

## 工作流程

```text
带图请求
model: deepseek-v4-flash-vision
        │
        ▼
VisionIntercept 中间件
        │
        ├─ 提取图片
        ├─ 计算 pHash
        ├─ 查询缓存
        └─ 调用视觉模型生成描述
        │
        ▼
图片块替换为文本描述
模型名改为 deepseek-v4-flash
        │
        ▼
目标文本模型
```

## 图片处理规则

最后一条 `user` 消息中的图片视为当前图片：

* 缓存命中时直接复用描述；
* 未命中时调用视觉模型；
* 调用成功后写入缓存。

历史消息中的图片：

* 只查询缓存；
* 缓存未命中时不会重新分析；
* 会替换为以下占位符：

```text
[This image was not parsed — its visual content is unavailable]
```

这样可以避免长对话反复分析历史图片。

## pHash 阈值

pHash 用于判断图片是否相似。

范围：

```text
0 ～ 64
```

* `0`：禁用
* 数值越小：判断越严格
* 数值越大：判断越宽松
* 默认值：`10`

阈值过高可能导致不同图片错误复用同一描述。

## 安装

### 环境要求

* new-api 源码
* 与 new-api `go.mod` 一致的 Go 版本
* Node.js 18+
* npm

建议先创建独立分支：

```bash
cd /path/to/new-api
git checkout -b feature/vision-interception
```

### 1. 复制后端文件

```bash
cp src/middleware/vision_intercept.go \
  <new-api>/middleware/vision_intercept.go

mkdir -p <new-api>/service/vision

cp src/service/vision/vision.go \
  <new-api>/service/vision/vision.go
```

### 2. 复制前端组件

```bash
cp src/web/profile/components/vision-settings-card.tsx \
  <new-api>/web/default/src/features/profile/components/
```

### 3. 合并补丁

按照 `patches/` 目录中的说明修改对应文件：

| 文件                               | 作用             |
| -------------------------------- | -------------- |
| `dto_user_settings.go`           | 添加视觉配置结构       |
| `constant_context_key.go`        | 添加 Context Key |
| `controller_user.go.snippet`     | 保存并校验用户配置      |
| `router_relay_router.go.snippet` | 挂载视觉中间件        |
| `web_profile_types.ts`           | 添加前端类型         |
| `web_profile_index.tsx`          | 挂载设置卡片         |
| `go.mod.diff`                    | 添加 Go 依赖       |

`go.mod.diff` 只是修改说明，不能直接执行 `git apply`。

需要添加：

```text
github.com/corona10/goimagehash v1.1.0
```

然后运行：

```bash
cd <new-api>
go mod tidy
```

### 4. 构建前端

new-api 会通过 `go:embed` 打包前端产物，因此必须先构建前端：

```bash
cd <new-api>/web/default
npm install
npm run build
```

### 5. 构建后端

```bash
cd <new-api>
go mod tidy
go build -o new-api .
```

完成后替换原二进制并重启服务。Docker 用户需要重新构建镜像。

## 配置

进入：

```text
个人资料 → Vision Interception
```

可以配置：

| 配置              | 说明                |
| --------------- | ----------------- |
| Enable Vision   | 是否启用视觉拦截          |
| Vision Model    | 用于生成图片描述的视觉模型     |
| Prompt Template | 图片描述提示词           |
| Model Suffix    | 触发后缀，默认 `-vision` |
| pHash Threshold | 相似图片阈值，默认 `10`    |

视觉模型必须是 new-api 中已经可用的模型。

默认 Prompt 示例：

```text
Please describe this image in detail, including all objects, text, people, colors, layout, and atmosphere.
```

## 使用示例

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

处理后，目标模型收到的请求类似：

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

## 支持的接口

```text
/v1/chat/completions
/chat/completions
/v1/messages
/messages
```

其他接口不会触发视觉拦截。

## 安装验证

安装完成后检查：

1. 个人资料页出现 `Vision Interception` 设置卡片；
2. 请求模型使用 `deepseek-v4-flash-vision`；
3. 日志中出现类似内容：

```text
[vision] intercepting model=deepseek-v4-flash-vision suffix=-vision
[vision] found 1 images (...)
[vision] replaced 1/1 images (0 failed)
```

## 限制

| 项目          |      限制 |
| ----------- | ------: |
| data URI 长度 |  20 MiB |
| 图片下载大小      |  15 MiB |
| 图片单边尺寸      | 8192 像素 |
| 图片总像素       |  2000 万 |
| Prompt 长度   | 8000 字符 |
| 模型后缀长度      |   64 字符 |
| 单组图片分析超时    |    30 秒 |

URL 图片只支持 `http` 和 `https`，并会执行 SSRF、大小和图片尺寸校验。

`video_url` 不会被处理。

## 计费

一次带图请求可能包含两部分费用：

1. 视觉模型生成图片描述；
2. 目标文本模型生成最终回答。

视觉调用使用 new-api 标准计费流程。缓存命中时不会重复调用视觉模型，但目标模型请求仍会正常计费。

## 已知问题

* 当前需要手工合并源码；
* new-api 更新后可能需要重新适配；
* 缓存保存在当前进程内，重启后会丢失；
* 多实例之间不共享缓存；
* 历史图片没有缓存时不会重新分析；
* 图片转文字可能损失部分视觉信息。

## 目录结构

```text
src/
├── middleware/
│   └── vision_intercept.go
├── service/
│   └── vision/
│       └── vision.go
└── web/
    └── profile/
        └── components/
            └── vision-settings-card.tsx

patches/
├── dto_user_settings.go
├── constant_context_key.go
├── controller_user.go.snippet
├── router_relay_router.go.snippet
├── web_profile_types.ts
├── web_profile_index.tsx
└── go.mod.diff
```

## 许可证

本项目采用 AGPL-3.0，许可证与 new-api 保持一致。

## 致谢

* [new-api](https://github.com/QuantumNous/new-api)
* [goimagehash](https://github.com/corona10/goimagehash)
* [Linux DO](https://linux.do/)
