// Package branding provides canonical product names without breaking legacy deployments.
package branding

import (
	"fmt"
	"os"
	"strings"
)

// Lookup prefers a nonblank LinkSphere value and retains the original environment
// variable as a fallback. The returned name lets callers identify invalid input
// without including a potentially sensitive value in an error.
func Lookup(lookup func(string) (string, bool), suffix string) (string, string, bool) {
	for _, prefix := range []string{"LINKSPHERE_", "GOSSHD_"} {
		name := prefix + suffix
		if value, ok := lookup(name); ok && strings.TrimSpace(value) != "" {
			return value, name, true
		}
	}
	return "", "", false
}

func Getenv(suffix string) string {
	value, _, _ := Lookup(os.LookupEnv, suffix)
	return value
}

// ConsistentValue refuses conflicting aliases for identity-bearing values such
// as encryption keys. It never includes either value in the error.
func ConsistentValue(lookup func(string) (string, bool), suffix string) (string, error) {
	canonical, _ := lookup("LINKSPHERE_" + suffix)
	legacy, _ := lookup("GOSSHD_" + suffix)
	if strings.TrimSpace(canonical) != "" && strings.TrimSpace(legacy) != "" && strings.TrimSpace(canonical) != strings.TrimSpace(legacy) {
		return "", fmt.Errorf("LINKSPHERE_%s and GOSSHD_%s disagree", suffix, suffix)
	}
	value, _, _ := Lookup(lookup, suffix)
	return value, nil
}
