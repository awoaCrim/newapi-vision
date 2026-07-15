package vision

import (
	"encoding/json"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/plugin"

	"github.com/gin-gonic/gin"
)

func init() {
	plugin.Register(&VisionPlugin{})
}

// VisionPlugin implements plugin.Plugin for vision interception.
type VisionPlugin struct{}

func (p *VisionPlugin) Name() string { return "vision-intercept" }

func (p *VisionPlugin) Middlewares() []gin.HandlerFunc {
	return []gin.HandlerFunc{VisionIntercept()}
}

func (p *VisionPlugin) SettingsDescriptor() *plugin.SettingsDescriptor {
	return &plugin.SettingsDescriptor{
		JSONKey:     "vision",
		JSONSchema:  visionSettingsSchema,
		DefaultJSON: visionSettingsDefault,
	}
}

func (p *VisionPlugin) OnSettingsUpdate(rawJSON json.RawMessage) error {
	var s VisionUserSetting
	if err := common.Unmarshal(rawJSON, &s); err != nil {
		return fmt.Errorf("invalid vision settings: %w", err)
	}
	if s.Enabled && (s.VisionModelName == "" || s.VisionSuffix == "") {
		return fmt.Errorf("vision_model_name and vision_suffix are required when vision is enabled")
	}
	return nil
}

func (p *VisionPlugin) StatusEntries() map[string]interface{} {
	return map[string]interface{}{
		"vision_enabled": true,
	}
}
