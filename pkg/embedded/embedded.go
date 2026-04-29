package embedded

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
)

//go:embed all:*.tmpl all:Dockerfile.*
var FS embed.FS

func RenderTemplate(name string, data any) ([]byte, error) {
	tmplData, err := FS.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("read embedded %s: %w", name, err)
	}

	tmpl, err := template.New(name).Parse(string(tmplData))
	if err != nil {
		return nil, fmt.Errorf("parse template %s: %w", name, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute template %s: %w", name, err)
	}
	return buf.Bytes(), nil
}

func WriteFile(dir, filename string, content []byte, perm os.FileMode) error {
	path := filepath.Join(dir, filename)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, content, perm)
}

func CopyStatic(name, dir, filename string) error {
	data, err := FS.ReadFile(name)
	if err != nil {
		return fmt.Errorf("read embedded %s: %w", name, err)
	}
	return WriteFile(dir, filename, data, 0644)
}
