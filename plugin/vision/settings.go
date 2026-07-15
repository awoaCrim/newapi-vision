package vision

// VisionUserSetting is the per-user vision interception configuration.
// The vision model is selected from the system model marketplace; usage is
// billed to the requesting user's account.
type VisionUserSetting struct {
	Enabled         bool   `json:"enabled"`
	VisionModelName string `json:"vision_model_name"` // must match a configured model
	PromptTemplate  string `json:"prompt_template"`
	VisionSuffix    string `json:"vision_suffix"`    // model name suffix that triggers interception
	PhashThreshold  int    `json:"phash_threshold"`  // Hamming distance threshold; 0 = disabled (default 10)
}

var visionSettingsSchema = []byte(`{
  "type": "object",
  "properties": {
    "enabled": {"type": "boolean", "default": false},
    "vision_model_name": {"type": "string", "default": "gpt-4o"},
    "prompt_template": {"type": "string"},
    "vision_suffix": {"type": "string", "default": "-vision"},
    "phash_threshold": {"type": "integer", "default": 10, "minimum": 0, "maximum": 64}
  }
}`)

var visionSettingsDefault = []byte(`{
  "enabled": false,
  "vision_model_name": "gpt-4o",
  "prompt_template": "Please describe this image in detail, including all objects, text, people, colors, layout, and atmosphere.",
  "vision_suffix": "-vision",
  "phash_threshold": 10
}`)
