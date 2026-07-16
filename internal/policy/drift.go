package policy

import (
	"path"
	"sort"
	"strings"

	"github.com/goalforge/goalforge/internal/gitops"
)

func OutOfScopeChanges(scope string, changes []gitops.FileChange) []string {
	patterns := strings.Split(scope, ",")
	allowed := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		if pattern = strings.TrimSpace(strings.TrimPrefix(pattern, "./")); pattern != "" {
			allowed = append(allowed, pattern)
		}
	}
	var violations []string
	for _, change := range changes {
		name := strings.TrimPrefix(strings.ReplaceAll(change.Path, "\\", "/"), "./")
		matched := false
		for _, pattern := range allowed {
			if strings.HasSuffix(pattern, "/**") {
				prefix := strings.TrimSuffix(pattern, "/**")
				matched = name == prefix || strings.HasPrefix(name, prefix+"/")
			} else if strings.ContainsAny(pattern, "*?[") {
				matched, _ = path.Match(pattern, name)
			} else {
				matched = name == pattern || strings.HasPrefix(name, strings.TrimSuffix(pattern, "/")+"/")
			}
			if matched {
				break
			}
		}
		if !matched {
			violations = append(violations, name)
		}
	}
	sort.Strings(violations)
	return violations
}
