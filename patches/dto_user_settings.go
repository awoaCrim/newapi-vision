package dto

import (
	"encoding/json"
)

type UserSetting struct {
	NotifyType                       string  `json:"notify_type,omitempty"`                          // QuotaWarningType 额度预警类型
	QuotaWarningThreshold            float64 `json:"quota_warning_threshold,omitempty"`              // QuotaWarningThreshold 额度预警阈值
	WebhookUrl                       string  `json:"webhook_url,omitempty"`                          // WebhookUrl webhook地址
	WebhookSecret                    string  `json:"webhook_secret,omitempty"`                       // WebhookSecret webhook密钥
	NotificationEmail                string  `json:"notification_email,omitempty"`                   // NotificationEmail 通知邮箱地址
	BarkUrl                          string  `json:"bark_url,omitempty"`                             // BarkUrl Bark推送URL
	GotifyUrl                        string  `json:"gotify_url,omitempty"`                           // GotifyUrl Gotify服务器地址
	GotifyToken                      string  `json:"gotify_token,omitempty"`                         // GotifyToken Gotify应用令牌
	GotifyPriority                   int     `json:"gotify_priority"`                                // GotifyPriority Gotify消息优先级
	UpstreamModelUpdateNotifyEnabled bool    `json:"upstream_model_update_notify_enabled,omitempty"` // 是否接收上游模型更新定时检测通知（仅管理员）
	AcceptUnsetRatioModel            bool    `json:"accept_unset_model_ratio_model,omitempty"`       // AcceptUnsetRatioModel 是否接受未设置价格的模型
	RecordIpLog                      bool    `json:"record_ip_log,omitempty"`                        // 是否记录请求和错误日志IP
	SidebarModules                   string  `json:"sidebar_modules,omitempty"`                      // SidebarModules 左侧边栏模块配置
	BillingPreference                string  `json:"billing_preference,omitempty"`                   // BillingPreference 扣费策略（订阅/钱包）
	Language                         string  `json:"language,omitempty"`                             // Language 用户语言偏好 (zh, en)

	// Extensions holds plugin-specific (and other non-core) settings keyed by
	// top-level JSON key. Populated by UnmarshalJSON for any key not in the
	// core UserSetting field set; merged back by MarshalJSON.
	Extensions map[string]json.RawMessage `json:"-"`
}

var (
	NotifyTypeEmail   = "email"   // Email 邮件
	NotifyTypeWebhook = "webhook" // Webhook
	NotifyTypeBark    = "bark"    // Bark 推送
	NotifyTypeGotify  = "gotify"  // Gotify 推送
)

// coreUserSettingKeys are the JSON keys of UserSetting struct fields.
// Any other top-level key is treated as an extension (plugin settings).
var coreUserSettingKeys = map[string]struct{}{
	"notify_type":                          {},
	"quota_warning_threshold":              {},
	"webhook_url":                          {},
	"webhook_secret":                       {},
	"notification_email":                   {},
	"bark_url":                             {},
	"gotify_url":                           {},
	"gotify_token":                         {},
	"gotify_priority":                      {},
	"upstream_model_update_notify_enabled": {},
	"accept_unset_model_ratio_model":       {},
	"record_ip_log":                        {},
	"sidebar_modules":                      {},
	"billing_preference":                   {},
	"language":                             {},
}

// MarshalJSON merges plugin Extensions into the top-level settings JSON.
// Uses encoding/json directly (not common.*) to avoid recursive custom methods.
func (s UserSetting) MarshalJSON() ([]byte, error) {
	type Alias UserSetting
	base, err := json.Marshal(Alias(s))
	if err != nil {
		return nil, err
	}
	if len(s.Extensions) == 0 {
		return base, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(base, &m); err != nil {
		return nil, err
	}
	for key, val := range s.Extensions {
		if len(val) > 0 {
			m[key] = val
		}
	}
	return json.Marshal(m)
}

// UnmarshalJSON captures non-core top-level keys into Extensions so plugin
// settings survive round-trips regardless of which plugins are currently
// registered (avoids data loss when a plugin is temporarily uninstalled).
func (s *UserSetting) UnmarshalJSON(data []byte) error {
	type Alias UserSetting
	var a Alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*s = UserSetting(a)

	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	s.Extensions = make(map[string]json.RawMessage)
	for key, raw := range m {
		if _, isCore := coreUserSettingKeys[key]; isCore {
			continue
		}
		if len(raw) > 0 {
			s.Extensions[key] = raw
		}
	}
	return nil
}
