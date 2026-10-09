package main

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
	"sync"
	"text/template"

	"gopkg.in/yaml.v3"
)

//go:embed messages.default.yaml
var defaultMessagesYAML []byte

var (
	messagesMutex sync.RWMutex
	messages      = mustParseDefaultMessages()
)

func parseMessages(raw map[string]string) (map[string]*template.Template, error) {
	res := make(map[string]*template.Template, len(raw))
	for key, text := range raw {
		tmpl, err := template.New(key).Parse(text)
		if err != nil {
			return nil, fmt.Errorf("message %q: %w", key, err)
		}
		res[key] = tmpl
	}
	return res, nil
}

func mustParseDefaultMessages() map[string]*template.Template {
	var raw map[string]string
	if err := yaml.Unmarshal(defaultMessagesYAML, &raw); err != nil {
		panic(fmt.Sprint("default messages: ", err))
	}
	res, err := parseMessages(raw)
	if err != nil {
		panic(fmt.Sprint("default messages: ", err))
	}
	return res
}

// resetMessages restores the embedded defaults (used by tests).
func resetMessages() {
	messagesMutex.Lock()
	messages = mustParseDefaultMessages()
	messagesMutex.Unlock()
}

// LoadMessagesFile overlays the messages in the given YAML (or JSON) file on
// top of the defaults. Only known keys are accepted.
func LoadMessagesFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading messages file: %w", err)
	}
	var raw map[string]string
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing messages file: %w", err)
	}

	messagesMutex.Lock()
	defer messagesMutex.Unlock()
	for key := range raw {
		if _, ok := messages[key]; !ok {
			return fmt.Errorf("messages file: unknown key %q", key)
		}
	}
	overrides, err := parseMessages(raw)
	if err != nil {
		return fmt.Errorf("messages file: %w", err)
	}
	for key, tmpl := range overrides {
		messages[key] = tmpl
	}
	return nil
}

// T renders the message template for key with the given data.
func T(key string, data map[string]any) string {
	messagesMutex.RLock()
	tmpl, ok := messages[key]
	messagesMutex.RUnlock()
	if !ok {
		fmt.Println("  missing message key:", key)
		return "!(" + key + ")"
	}
	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		fmt.Println("  error rendering message", key+":", err)
		return "!(" + key + ")"
	}
	return sb.String()
}
