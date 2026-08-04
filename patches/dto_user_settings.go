package dto

type UserSetting struct {
	NotifyType                       string              `json:"notify_type,omitempty"`                          // QuotaWarningType 额度预警类型
	QuotaWarningThreshold            float64             `json:"quota_warning_threshold,omitempty"`              // QuotaWarningThreshold 额度预警阈值
	WebhookUrl                       string              `json:"webhook_url,omitempty"`                          // WebhookUrl webhook地址
	WebhookSecret                    string              `json:"webhook_secret,omitempty"`                       // WebhookSecret webhook密钥
	NotificationEmail                string              `json:"notification_email,omitempty"`                   // NotificationEmail 通知邮箱地址
	BarkUrl                          string              `json:"bark_url,omitempty"`                             // BarkUrl Bark推送URL
	GotifyUrl                        string              `json:"gotify_url,omitempty"`                           // GotifyUrl Gotify服务器地址
	GotifyToken                      string              `json:"gotify_token,omitempty"`                         // GotifyToken Gotify应用令牌
	GotifyPriority                   int                 `json:"gotify_priority"`                                // GotifyPriority Gotify消息优先级
	UpstreamModelUpdateNotifyEnabled bool                `json:"upstream_model_update_notify_enabled,omitempty"` // 是否接收上游模型更新定时检测通知（仅管理员）
	AcceptUnsetRatioModel            bool                `json:"accept_unset_model_ratio_model,omitempty"`       // AcceptUnsetRatioModel 是否接受未设置价格的模型
	RecordIpLog                      bool                `json:"record_ip_log,omitempty"`                        // 是否记录请求和错误日志IP
	SidebarModules                   string              `json:"sidebar_modules,omitempty"`                      // SidebarModules 左侧边栏模块配置
	BillingPreference                string              `json:"billing_preference,omitempty"`                   // BillingPreference 扣费策略（订阅/钱包）
	Language                         string              `json:"language,omitempty"`                             // Language 用户语言偏好 (zh, en)
	Vision                           *VisionUserSetting  `json:"vision,omitempty"`                               // Vision 用户级视觉拦截设置
}

// VisionUserSetting 用户级视觉拦截配置
// Vision 模型直接选用系统模型广场中已有的模型，消耗计入当前账户
type VisionUserSetting struct {
	Enabled         bool   `json:"enabled"`
	VisionModelName string `json:"vision_model_name"` // 视觉模型名称，需与系统中已配置的模型一致
	PromptTemplate  string `json:"prompt_template"`
	VisionSuffix    string `json:"vision_suffix"` // 触发视觉拦截的模型后缀
	PhashThreshold  int    `json:"phash_threshold"` // 汉明距离阈值：相似图片聚类，0=禁用（默认10）
}

var (
	NotifyTypeEmail   = "email"   // Email 邮件
	NotifyTypeWebhook = "webhook" // Webhook
	NotifyTypeBark    = "bark"    // Bark 推送
	NotifyTypeGotify  = "gotify"  // Gotify 推送
)
