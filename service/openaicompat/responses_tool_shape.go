package openaicompat

import (
	"fmt"
	"strings"
)

// ValidateResponsesToolShape checks all declarations before attempting a bridge.
// An unsupported early tool must not hide a malformed later declaration.
func ValidateResponsesToolShape(tools []any) error {
	return validateResponsesToolScope(tools, "tools", 0)
}

func validateResponsesToolScope(tools []any, path string, depth int) error {
	if depth > 8 {
		return fmt.Errorf("%s exceeds the tool namespace depth limit", path)
	}
	names := make(map[string]bool, len(tools))
	for i, raw := range tools {
		at := fmt.Sprintf("%s[%d]", path, i)
		tool, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", at)
		}
		kind, ok := tool["type"].(string)
		if !ok || strings.TrimSpace(kind) == "" {
			return fmt.Errorf("%s.type must be a non-empty string", at)
		}
		if kind == "function" || kind == "custom" || kind == "namespace" {
			name, ok := tool["name"].(string)
			if !ok || strings.TrimSpace(name) == "" {
				return fmt.Errorf("%s.name must be a non-empty string", at)
			}
			if names[name] {
				return fmt.Errorf("%s has duplicate tool names", path)
			}
			names[name] = true
		}
		if raw, exists := tool["defer_loading"]; exists {
			if _, ok := raw.(bool); !ok {
				return fmt.Errorf("%s.defer_loading must be a boolean", at)
			}
		}
		if kind == "namespace" {
			children, ok := tool["tools"].([]any)
			if !ok || len(children) == 0 {
				return fmt.Errorf("%s.tools must be a non-empty array", at)
			}
			if err := validateResponsesToolScope(children, at+".tools", depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}
