# new-api Vision Interception

在请求进入目标模型前，将图片转换为文字描述，让纯文本模型也能处理图片。

[![Base](https://img.shields.io/badge/base-new--api-green)](https://github.com/QuantumNous/new-api)
[![License](https://img.shields.io/badge/license-AGPL--3.0-orange)](https://github.com/QuantumNous/new-api/blob/main/LICENSE)

## 简介

当请求模型名带有 `-vision` 后缀时，中间件会：

1. 提取消息中的图片；
2. 调用用户配置的视觉模型生成描述；
3. 将图片块替换为文本；
4. 移除模型名中的 `-vision`；
5. 将请求转发给目标文本模型。

例如：

```text
deepseek-v4-flash-vision
        ↓ 图片转文字
deepseek-v4-flash
```

目标模型本身不需要支持图片输入。

## 特性

* 支持 OpenAI 和 Claude 消息格式
* 支持 base64 和 URL 图片
* 用户级视觉模型、Prompt、后缀和 pHash 配置
* 请求内图片去重
* pHash 相似图片缓存
* singleflight 并发请求合并
* 缓存按用户、视觉模型和 Prompt 隔离
* 视觉调用使用 new-api 标准计费流程
* 图片分析失败时直接返回错误
* 转发给目标模型时不再携带已处理图片

## 工作流程

```text
带图请求
model: deepseek-v4-flash-vision
        │
        ▼
提取图片并查询缓存
        │
        ├─ 命中：复用描述
        └─ 未命中：调用视觉模型
        │
        ▼
图片块替换为文本描述
model: deepseek-v4-flash
        │
        ▼
目标文本模型
```

## 安装

### 前置条件

* 一份可正常构建的 new-api 源码
* 与目标 new-api `go.mod` 匹配的 Go 版本
* 与目标 new-api 前端项目兼容的 Node.js 和包管理器

建议先创建独立分支：

```bash
cd <new-api>
git checkout -b feature/vision-interception
```

### 1. 复制新增文件

```bash
cp src/middleware/vision_intercept.go \
  <new-api>/middleware/vision_intercept.go

mkdir -p <new-api>/service/vision

cp src/service/vision/vision.go \
  <new-api>/service/vision/vision.go

cp src/web/profile/components/vision-settings-card.tsx \
  <new-api>/web/default/src/features/profile/components/
```

### 2. 合并补丁

按照 `patches/` 中的说明修改对应文件：

| 补丁文件                             | 目标文件                                                   |
| -------------------------------- | ------------------------------------------------------ |
| `dto_user_settings.go`           | `<new-api>/dto/user_settings.go`                       |
| `constant_context_key.go`        | `<new-api>/constant/context_key.go`                    |
| `controller_user.go.snippet`     | `<new-api>/controller/user.go`                         |
| `router_relay_router.go.snippet` | `<new-api>/router/relay-router.go`                     |
| `web_profile_types.ts`           | `<new-api>/web/default/src/features/profile/types.ts`  |
| `web_profile_index.tsx`          | `<new-api>/web/default/src/features/profile/index.tsx` |
| `go.mod.diff`                    | `<new-api>/go.mod`                                     |

`go.mod.diff` 是依赖修改说明，不是标准 Git diff，不能直接执行 `git apply`。

需要添加：

```text
github.com/corona10/goimagehash v1.1.0
```

然后执行：

```bash
cd <new-api>
go mod tidy
```

### 3. 构建前端

前端产物会被 Go 通过 `go:embed` 打包，因此需要先完成前端构建：

```bash
cd <new-api>/web/default
npm install
npm run build
```

### 4. 构建后端

```bash
cd <new-api>
go mod tidy
go build -o new-api .
```

构建完成后替换原二进制并重启服务。Docker 部署需要重新构建镜像。

## 配置

进入：

```text
个人资料 → Vision Interception
```

可配置以下内容：

| 配置              | 说明                |
| --------------- | ----------------- |
| Enable Vision   | 是否启用视觉拦截          |
| Vision Model    | 用于生成图片描述的视觉模型     |
| Prompt Template | 图片描述提示词           |
| Model Suffix    | 触发后缀，默认 `-vision` |
| pHash Threshold | 相似图片判断阈值，默认 `10`  |

视觉模型必须是当前 new-api 中可用的模型。

默认 Prompt：

```text
Please describe this image in detail, including all objects, text, people, colors, layout, and atmosphere.
```

## pHash 阈值

pHash 使用汉明距离判断图片是否相似。

有效范围：

```text
0 ～ 64
```

* `0`：禁用 pHash，每张图片独立处理
* 阈值越低：判断越严格
* 阈值越高：判断越宽松
* 默认值：`10`

判断逻辑为：

```text
HammingDistance <= threshold
```

阈值过高可能导致不同图片错误复用相同描述。

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

处理后发送给目标模型的内容类似：

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

## 历史图片

最后一条 `user` 消息中的图片视为当前图片：

* 缓存命中时复用描述
* 未命中时调用视觉模型

历史消息中的图片只查询缓存，不会重新调用视觉模型。

历史图片没有缓存时会被替换为：

```text
[This image was not parsed — its visual content is unavailable]
```

## 支持的接口

```text
/v1/chat/completions
/chat/completions
/v1/messages
/messages
```

其他接口不会触发视觉拦截。

## 验证

安装完成后：

1. 个人资料页应出现 `Vision Interception` 设置卡片；
2. 启用功能并配置视觉模型；
3. 使用 `deepseek-v4-flash-vision` 发送带图请求；
4. 检查日志：

```text
[vision] intercepting model=deepseek-v4-flash-vision suffix=-vision
[vision] found 1 images (...)
[vision] replaced 1/1 images (0 failed)
```

## 限制

| 项目          |      限制 |
| ----------- | ------: |
| data URI 长度 |  20 MiB |
| URL 图片下载大小  |  15 MiB |
| 图片单边尺寸      | 8192 像素 |
| 图片总像素       |  2000 万 |
| pHash 阈值    |    0～64 |
| Prompt 长度   | 8000 字符 |
| 模型后缀长度      |   64 字符 |
| 单组视觉分析超时    |    30 秒 |

URL 图片仅支持 `http` 和 `https`，并会进行 SSRF、大小和图片尺寸检查。

`video_url` 不会被处理。

## 计费

一次带图请求可能包含两部分费用：

1. 视觉模型生成图片描述；
2. 目标文本模型生成最终回答。

缓存命中时不会再次调用视觉模型，但目标模型请求仍会正常计费。

## 已知限制

* 当前需要手工合并源码
* new-api 更新后可能需要重新适配
* 缓存保存在进程内，服务重启后会丢失
* 多实例之间不共享缓存
* 历史图片没有缓存时不会重新分析
* 图片转文字可能损失部分视觉信息

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

本项目采用 AGPL-3.0，与 new-api 保持一致。

## 致谢

* [new-api](https://github.com/QuantumNous/new-api)
* [goimagehash](https://github.com/corona10/goimagehash)
* [Linux DO](https://linux.do/)
