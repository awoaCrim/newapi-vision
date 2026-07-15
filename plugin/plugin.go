package plugin

import (
	"encoding/json"

	"github.com/gin-gonic/gin"
)

// Plugin is the interface that all installable features must implement.
// Plugins register themselves via init() and are discovered through the
// global registry. Removing a plugin is as simple as deleting its import
// line from main.go; the Go linker will not pull in the package or its
// transitive dependencies.
type Plugin interface {
	// Name returns a unique kebab-case identifier (e.g. "vision-intercept").
	Name() string

	// Middlewares returns an ordered slice of Gin middleware handlers
	// that should be applied to the relay HTTP route group.
	// Return nil/empty if the plugin does not inject middleware.
	Middlewares() []gin.HandlerFunc

	// SettingsDescriptor returns the plugin's user-settings schema, or nil
	// if the plugin has no user-facing settings.
	SettingsDescriptor() *SettingsDescriptor

	// OnSettingsUpdate is called when a user updates their settings.
	// The raw JSON bytes for the plugin's key are passed; the plugin
	// is responsible for deserialization and validation.
	// Return an error to reject the update.
	OnSettingsUpdate(rawJSON json.RawMessage) error

	// StatusEntries returns key-value pairs to inject into the /api/status
	// response. This is how the frontend learns which plugins are active.
	// Example: {"vision_enabled": true}
	StatusEntries() map[string]interface{}
}

// SettingsDescriptor describes a plugin's user-settings extension.
type SettingsDescriptor struct {
	// JSONKey is the key under the user's settings JSON where this
	// plugin's settings object lives (e.g. "vision").
	JSONKey string

	// JSONSchema is a JSON Schema draft-07 object describing the
	// plugin's settings structure. Used for validation and for
	// generating frontend forms dynamically in the future.
	JSONSchema []byte

	// DefaultJSON is the default value (as JSON bytes) for the
	// plugin's settings when a user has not configured them.
	DefaultJSON []byte
}
