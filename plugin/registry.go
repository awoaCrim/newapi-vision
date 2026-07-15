package plugin

import (
	"sort"
	"sync"

	"github.com/gin-gonic/gin"
)

var (
	plugins = make(map[string]Plugin)
	mu      sync.RWMutex
)

// Register registers a plugin with the global registry.
// Typically called from a plugin package's init() function.
func Register(p Plugin) {
	mu.Lock()
	defer mu.Unlock()
	plugins[p.Name()] = p
}

// Get returns the plugin with the given name, or nil if not registered.
func Get(name string) Plugin {
	mu.RLock()
	defer mu.RUnlock()
	return plugins[name]
}

// All returns all registered plugins sorted by name.
func All() []Plugin {
	mu.RLock()
	defer mu.RUnlock()
	result := make([]Plugin, 0, len(plugins))
	for _, p := range plugins {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name() < result[j].Name()
	})
	return result
}

// IsRegistered reports whether a plugin with the given name is registered.
func IsRegistered(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := plugins[name]
	return ok
}

// RelayMiddlewares returns the combined middleware chain from all
// registered plugins, in sorted-by-name order.
func RelayMiddlewares() []gin.HandlerFunc {
	var handlers []gin.HandlerFunc
	for _, p := range All() {
		handlers = append(handlers, p.Middlewares()...)
	}
	return handlers
}

// StatusMap returns a merged map of all plugin status entries.
func StatusMap() map[string]interface{} {
	result := make(map[string]interface{})
	for _, p := range All() {
		for k, v := range p.StatusEntries() {
			result[k] = v
		}
	}
	return result
}

// KnownSettingsKeys returns the set of JSON keys claimed by registered plugins.
func KnownSettingsKeys() map[string]bool {
	result := make(map[string]bool)
	for _, p := range All() {
		desc := p.SettingsDescriptor()
		if desc != nil && desc.JSONKey != "" {
			result[desc.JSONKey] = true
		}
	}
	return result
}
