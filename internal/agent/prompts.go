package agent

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"
)

//go:embed prompts/*.md
var promptsFS embed.FS

var parsedTemplates *template.Template

func init() {
	var err error
	// text/template.ParseFS parses all matching files in the embedded FS.
	parsedTemplates, err = template.ParseFS(promptsFS, "prompts/*.md")
	if err != nil {
		panic(fmt.Sprintf("Failed to parse prompt templates: %v", err))
	}
}

// GetPrompt renders the prompt template associated with the specific role.
func GetPrompt(role string, data interface{}) (string, error) {
	var tplName string
	switch role {
	case "investigator":
		tplName = "investigator.md"
	case "reporter":
		tplName = "reporter.md"
	default:
		// phase_UserAccess -> phase_UserAccess.md
		tplName = role + ".md"
	}

	var buf bytes.Buffer
	if err := parsedTemplates.ExecuteTemplate(&buf, tplName, data); err != nil {
		return "", fmt.Errorf("failed to render template %s: %w", tplName, err)
	}
	return buf.String(), nil
}
