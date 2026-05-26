package caps

import (
	"context"
	"sort"
	"sync"
)

// NativePlugin is a capability plugin implemented in Go and run in-process,
// instead of as a sandboxed subprocess. It exposes the same MCP surface as a
// script plugin (a tool catalog + per-tool calls) but needs no runtime and is
// not user-editable: its behavior lives in code, registered via
// RegisterNativePlugin and resolved by id.
type NativePlugin interface {
	// Tools returns the MCP tool catalog (name/description/inputSchema objects),
	// the same shape a script plugin prints for argv "list".
	Tools() []map[string]any
	// Call invokes one tool by name with decoded arguments and returns its text
	// result (mirroring a script plugin's stdout for argv "call").
	Call(ctx context.Context, tool string, args map[string]any) (string, error)
}

// NativePluginInfo is a registry entry: a native plugin plus the stable id and
// display metadata the control plane lists it under.
type NativePluginInfo struct {
	ID      string
	Name    string
	Version string // code version; recorded as the plugin's seeded version note
	Plugin  NativePlugin
}

var (
	nativeMu       sync.RWMutex
	nativeRegistry = map[string]NativePluginInfo{}
)

// RegisterNativePlugin adds (or replaces) a native plugin in the global registry.
// Built-ins register from init(); the platform resolves them by id at run time
// and the server seeds a matching store row so they appear in the plugins list.
func RegisterNativePlugin(info NativePluginInfo) {
	nativeMu.Lock()
	defer nativeMu.Unlock()
	nativeRegistry[info.ID] = info
}

// NativePluginByID returns the registered native plugin for an id.
func NativePluginByID(id string) (NativePlugin, bool) {
	nativeMu.RLock()
	defer nativeMu.RUnlock()
	e, ok := nativeRegistry[id]
	return e.Plugin, ok
}

// NativePlugins returns all registered native plugins, ordered by name — used to
// seed/reconcile the store rows that surface them in the dashboard.
func NativePlugins() []NativePluginInfo {
	nativeMu.RLock()
	defer nativeMu.RUnlock()
	out := make([]NativePluginInfo, 0, len(nativeRegistry))
	for _, e := range nativeRegistry {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
