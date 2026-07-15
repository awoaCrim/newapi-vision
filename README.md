# new-api Vision Plugin

可安装到 [new-api](https://github.com/QuantumNous/new-api) 的 **Vision Interception** 插件：在请求到达目标模型前，把图片转成文字描述，让纯文本模型也能「看图」。

## 功能

- 用户级配置（个人资料页）：启用、视觉模型、后缀、prompt、pHash 阈值
- 模型名加后缀（默认 `-vision`）触发拦截
- OpenAI / Claude 消息格式；URL 去重 + pHash 聚类
- 经系统模型广场渠道调用视觉模型并计费到当前用户
- 分析失败严格返回错误（不静默降级）
- `main.go` 空白 import 安装；删除 import 即可卸载

## 目录

```
plugin/
  plugin.go              # Plugin 接口 + SettingsDescriptor
  registry.go            # 注册表 Register / RelayMiddlewares / StatusMap
  vision/
    register.go          # init() 注册
    settings.go          # VisionUserSetting + schema
    middleware.go        # 拦截中间件
    vision.go            # 分析 / 缓存 / 计费调用
frontend/profile/
  vision-settings-card.tsx
docs/
  integration.md         # 宿主 new-api 需要改哪些地方
```

## 安装（宿主 new-api）

1. 将 `plugin/` 复制到 new-api 源码根目录（或把本仓库作为 subtree / 复制包路径）。
2. 按 [docs/integration.md](docs/integration.md) 修改宿主：`dto.UserSetting.Extensions`、`controller`、`router`、`main` 空白 import、前端 lazy 卡片。
3. 编译：

```go
// main.go
import _ "github.com/QuantumNous/new-api/plugin/vision"
```

4. 卸载：删除该 import 后重新编译；Go 链接器会剔除 `goimagehash` 等 vision 专属依赖。

## 模块路径

当前 Go 包 import 仍使用宿主模块路径：

```text
github.com/QuantumNous/new-api/plugin
github.com/QuantumNous/new-api/plugin/vision
```

复制进 new-api 树后即可直接 `go build`，无需改 module 名。若你 fork 的模块路径不同，请全局替换。

## 使用

1. 个人资料 → Vision Interception → 开启并选择模型广场中的视觉模型  
2. 请求模型名加后缀，例如 `minimax-m3-vision`  
3. 网关去掉后缀，用文字描述替换图片后再转发给目标模型  

## License

与 new-api 一致，遵循其开源协议（AGPL-3.0 等）。本插件代码用于在兼容宿主上扩展功能。

## 致谢

- [new-api](https://github.com/QuantumNous/new-api)
- [Linux DO](https://linux.do/)
