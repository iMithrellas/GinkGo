package config

import (
	"sort"
	"strings"
)

// UpsertNamespaceConfig inserts or replaces a [namespaces.<name>] section.
func UpsertNamespaceConfig(existing, name string, values map[string]any) (string, bool) {
	section := "namespaces." + name
	header := "[" + section + "]"
	lines := strings.Split(existing, "\n")
	out := make([]string, 0, len(lines)+8)
	replaced := false

	for i := 0; i < len(lines); {
		line := lines[i]
		trim := strings.TrimSpace(line)
		if trim == header {
			out = append(out, line)
			appendNamespaceOptions(&out, values)
			replaced = true
			i++
			for i < len(lines) {
				next := strings.TrimSpace(lines[i])
				if isSectionHeader(next) {
					break
				}
				i++
			}
			continue
		}
		out = append(out, line)
		i++
	}

	if !replaced {
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, "# Added by namespace init")
		out = append(out, header)
		appendNamespaceOptions(&out, values)
	}

	return strings.Join(out, "\n"), true
}

// DeleteNamespaceConfig removes a [namespaces.<name>] section if present.
func DeleteNamespaceConfig(existing, name string) (string, bool) {
	section := "namespaces." + name
	header := "[" + section + "]"
	lines := strings.Split(existing, "\n")
	out := make([]string, 0, len(lines))
	removed := false

	for i := 0; i < len(lines); {
		line := lines[i]
		trim := strings.TrimSpace(line)
		if trim == header {
			removed = true
			i++
			for i < len(lines) {
				next := strings.TrimSpace(lines[i])
				if isSectionHeader(next) {
					break
				}
				i++
			}
			continue
		}
		out = append(out, line)
		i++
	}

	return strings.Join(out, "\n"), removed
}

func appendNamespaceOptions(out *[]string, values map[string]any) {
	ordered := namespaceOptionOrder(values)
	for _, key := range ordered {
		writeTOMLOptionLines(out, key, values[key], "")
	}
}

func namespaceOptionOrder(values map[string]any) []string {
	pref := []string{"e2ee", "key_provider", "key_id", "read_key", "write_key"}
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, k := range pref {
		if _, ok := values[k]; ok {
			out = append(out, k)
			seen[k] = true
		}
	}
	rest := make([]string, 0, len(values))
	for k := range values {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	out = append(out, rest...)
	return out
}

func isSectionHeader(trim string) bool {
	if trim == "" || strings.HasPrefix(trim, "#") || strings.HasPrefix(trim, ";") {
		return false
	}
	return strings.HasPrefix(trim, "[") && strings.HasSuffix(trim, "]")
}
