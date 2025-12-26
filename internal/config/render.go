package config

import (
	"fmt"
	"sort"
	"strings"
)

// RenderDefaultTOML renders a TOML config with defaults from GetConfigOptions.
func RenderDefaultTOML() string {
	var b strings.Builder
	b.WriteString("# GinkGo configuration (TOML)\n")

	opts := GetConfigOptions()
	topLevel := make([]ConfigOption, 0, len(opts))
	sections := make(map[string][]ConfigOption)
	sectionOrder := make([]string, 0)

	for _, o := range opts {
		if !strings.Contains(o.Key, ".") {
			topLevel = append(topLevel, o)
			continue
		}
		parts := strings.SplitN(o.Key, ".", 2)
		section := parts[0]
		if _, ok := sections[section]; !ok {
			sectionOrder = append(sectionOrder, section)
		}
		sections[section] = append(sections[section], ConfigOption{
			Key:     parts[1],
			Default: o.Default,
			Comment: o.Comment,
		})
	}

	for _, o := range topLevel {
		writeTOMLOption(&b, o.Key, o.Default, o.Comment)
	}

	for _, section := range sectionOrder {
		opts := sections[section]
		if len(opts) == 0 {
			continue
		}
		b.WriteString("[" + section + "]\n")
		for _, o := range opts {
			writeTOMLOption(&b, o.Key, o.Default, o.Comment)
		}
		b.WriteString("\n")
	}

	return b.String()
}

// UpdateTOML merges defaults into an existing TOML string and comments out unknown keys.
func UpdateTOML(existing string) (string, bool) {
	lines := strings.Split(existing, "\n")
	opts := GetConfigOptions()

	known := make(map[string]ConfigOption, len(opts))
	mapPrefixes := make(map[string]bool)
	for _, o := range opts {
		known[o.Key] = o
		if _, ok := o.Default.(map[string]any); ok {
			mapPrefixes[o.Key] = true
		}
	}

	existingKeys := make(map[string]bool)
	prefixSeen := make(map[string]bool)
	currentSection := ""
	out := make([]string, 0, len(lines))
	changed := false

	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") || strings.HasPrefix(trim, ";") {
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(trim, "[") && strings.HasSuffix(trim, "]") {
			currentSection = strings.TrimSpace(trim[1 : len(trim)-1])
			out = append(out, line)
			continue
		}
		key, ok := parseTOMLKey(line)
		if !ok {
			out = append(out, line)
			continue
		}
		fullKey := key
		if currentSection != "" {
			fullKey = currentSection + "." + key
		}
		existingKeys[fullKey] = true
		for prefix := range mapPrefixes {
			if fullKey == prefix || strings.HasPrefix(fullKey, prefix+".") {
				prefixSeen[prefix] = true
			}
		}
		if !isKnownKey(fullKey, known, mapPrefixes) {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			out = append(out, indent+"# OUTDATED: option removed from config schema")
			out = append(out, indent+"# "+strings.TrimLeft(line, " \t"))
			changed = true
			continue
		}
		out = append(out, line)
	}

	missingTop := make([]ConfigOption, 0)
	missingSections := make(map[string][]ConfigOption)
	sectionOrder := make([]string, 0)
	for _, o := range opts {
		if _, ok := o.Default.(map[string]any); ok {
			if prefixSeen[o.Key] {
				continue
			}
		} else if existingKeys[o.Key] {
			continue
		}
		if strings.Contains(o.Key, ".") {
			parts := strings.SplitN(o.Key, ".", 2)
			section := parts[0]
			if _, ok := missingSections[section]; !ok {
				sectionOrder = append(sectionOrder, section)
			}
			missingSections[section] = append(missingSections[section], ConfigOption{
				Key:     parts[1],
				Default: o.Default,
				Comment: o.Comment,
			})
		} else {
			missingTop = append(missingTop, o)
		}
	}

	if len(missingTop) > 0 || len(missingSections) > 0 {
		out = append(out, "", "# Added by config update")
		for _, o := range missingTop {
			writeTOMLOptionLines(&out, o.Key, o.Default, o.Comment)
		}
		for _, section := range sectionOrder {
			opts := missingSections[section]
			if len(opts) == 0 {
				continue
			}
			out = append(out, "["+section+"]")
			for _, o := range opts {
				writeTOMLOptionLines(&out, o.Key, o.Default, o.Comment)
			}
		}
		changed = true
	}

	return strings.Join(out, "\n"), changed
}

func parseTOMLKey(line string) (string, bool) {
	idx := strings.Index(line, "=")
	if idx == -1 {
		return "", false
	}
	key := strings.TrimSpace(line[:idx])
	if key == "" || strings.HasPrefix(key, "[") {
		return "", false
	}
	if strings.HasPrefix(key, "\"") || strings.HasPrefix(key, "'") {
		return "", false
	}
	return key, true
}

func isKnownKey(key string, known map[string]ConfigOption, prefixes map[string]bool) bool {
	if _, ok := known[key]; ok {
		return true
	}
	for prefix := range prefixes {
		if key == prefix || strings.HasPrefix(key, prefix+".") {
			return true
		}
	}
	return false
}

func writeTOMLOption(b *strings.Builder, key string, value any, comment string) {
	if comment != "" {
		b.WriteString("# " + comment + "\n")
	}
	switch v := value.(type) {
	case string:
		b.WriteString(fmt.Sprintf("%s = \"%s\"\n\n", key, v))
	case bool, int, int64:
		b.WriteString(fmt.Sprintf("%s = %v\n\n", key, v))
	case []string:
		b.WriteString(fmt.Sprintf("%s = [", key))
		for i, s := range v {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(fmt.Sprintf("\"%s\"", s))
		}
		b.WriteString("]\n\n")
	case map[string]any:
		if len(v) == 0 {
			b.WriteString(fmt.Sprintf("%s = {}\n\n", key))
			return
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString(fmt.Sprintf("%s = {", key))
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(fmt.Sprintf("%s = \"%v\"", k, v[k]))
		}
		b.WriteString("}\n\n")
	}
}

func writeTOMLOptionLines(lines *[]string, key string, value any, comment string) {
	if comment != "" {
		*lines = append(*lines, "# "+comment)
	}
	switch v := value.(type) {
	case string:
		*lines = append(*lines, fmt.Sprintf("%s = \"%s\"", key, v), "")
	case bool, int, int64:
		*lines = append(*lines, fmt.Sprintf("%s = %v", key, v), "")
	case []string:
		var b strings.Builder
		b.WriteString(fmt.Sprintf("%s = [", key))
		for i, s := range v {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(fmt.Sprintf("\"%s\"", s))
		}
		b.WriteString("]")
		*lines = append(*lines, b.String(), "")
	case map[string]any:
		if len(v) == 0 {
			*lines = append(*lines, fmt.Sprintf("%s = {}", key), "")
			return
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString(fmt.Sprintf("%s = {", key))
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(fmt.Sprintf("%s = \"%v\"", k, v[k]))
		}
		b.WriteString("}")
		*lines = append(*lines, b.String(), "")
	}
}
