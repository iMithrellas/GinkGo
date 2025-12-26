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
