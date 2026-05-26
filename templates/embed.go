// Package templates embeds the README templates bundled with the binary.
// Each theme is a markdown file with Go template placeholders (e.g.
// {{.CommunityName}}); themes are selected by `agent-community init --theme`.
package templates

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed community
var content embed.FS

// ReadmeTemplate returns the raw template bytes for the named theme. If no
// such theme exists, the "default" theme is returned with an error so the CLI
// can warn the user.
func ReadmeTemplate(theme string) ([]byte, error) {
	path := fmt.Sprintf("community/README.%s.md.tmpl", theme)
	data, err := content.ReadFile(path)
	if err == nil {
		return data, nil
	}
	def, defErr := content.ReadFile("community/README.default.md.tmpl")
	if defErr != nil {
		return nil, fmt.Errorf("theme %q not found and default template missing: %w", theme, defErr)
	}
	return def, fmt.Errorf("theme %q not found; using default", theme)
}

// ReadmeThemes returns the filenames (e.g. README.cheeses.md.tmpl) of all
// available themes.
func ReadmeThemes() []string {
	var out []string
	_ = fs.WalkDir(content, "community", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		base := strings.TrimPrefix(path, "community/README.")
		if !strings.HasSuffix(base, ".md.tmpl") {
			return nil
		}
		out = append(out, strings.TrimSuffix(base, ".md.tmpl"))
		return nil
	})
	sort.Strings(out)
	return out
}
