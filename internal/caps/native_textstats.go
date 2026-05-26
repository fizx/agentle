package caps

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

// textStats is a built-in native plugin: small text utilities that are trivial in
// Go and would be awkward to ship as a sandboxed subprocess. It demonstrates the
// native-plugin path (in-process, not editable) alongside the Python "text-tools"
// script plugin.
type textStats struct{}

func (textStats) Tools() []map[string]any {
	strProp := map[string]any{"text": map[string]any{"type": "string"}}
	return []map[string]any{
		{
			"name":        "wordcount",
			"description": "Count words, characters and lines in text.",
			"inputSchema": map[string]any{"type": "object", "properties": strProp, "required": []string{"text"}},
		},
		{
			"name":        "slugify",
			"description": "Turn text into a url-safe, lowercase, hyphenated slug.",
			"inputSchema": map[string]any{"type": "object", "properties": strProp, "required": []string{"text"}},
		},
	}
}

func (textStats) Call(_ context.Context, tool string, args map[string]any) (string, error) {
	text, _ := args["text"].(string)
	switch tool {
	case "wordcount":
		words := len(strings.Fields(text))
		lines := 0
		if text != "" {
			lines = strings.Count(text, "\n") + 1
		}
		return fmt.Sprintf("words=%d chars=%d lines=%d", words, len([]rune(text)), lines), nil
	case "slugify":
		return slugify(text), nil
	default:
		return "", fmt.Errorf("unknown tool %q", tool)
	}
}

// slugify lowercases, replaces runs of non-alphanumerics with single hyphens, and
// trims leading/trailing hyphens.
func slugify(s string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevHyphen = false
		} else if !prevHyphen {
			b.WriteByte('-')
			prevHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func init() {
	RegisterNativePlugin(NativePluginInfo{
		ID:      "pl_native_textstats",
		Name:    "text-stats (native)",
		Version: "1.0.0",
		Plugin:  textStats{},
	})
}
