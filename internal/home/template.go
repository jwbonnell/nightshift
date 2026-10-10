package home

import (
	_ "embed"
	"os"
)

//go:embed spec-template.md
var embeddedTemplate string

// ReadTemplate returns home's own spec-template.md if present, falling
// back to the embedded default otherwise.
func ReadTemplate(home string) (string, error) {
	data, err := os.ReadFile(TemplatePath(home))
	if err == nil {
		return string(data), nil
	}
	if os.IsNotExist(err) {
		return embeddedTemplate, nil
	}
	return "", err
}
