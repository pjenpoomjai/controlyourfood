package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/ledongthuc/pdf"
)

// LoadAllDocuments loads all documents from the knowledge/ directory.
func LoadAllDocuments(dir string) string {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		log.Printf("knowledge directory not found: %s", dir)
		return ""
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("failed to read directory %s: %v", dir, err)
		return ""
	}

	var docs []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		path := filepath.Join(dir, name)
		ext := strings.ToLower(filepath.Ext(name))

		var content string
		switch ext {
		case ".pdf":
			content = loadPDF(path)
		case ".txt", ".md":
			content = loadText(path)
		default:
			continue
		}

		if strings.TrimSpace(content) != "" {
			docs = append(docs, fmt.Sprintf("=== Document: %s ===\n%s", name, content))
			log.Printf("loaded document: %s (%d chars)", name, len(content))
		} else {
			log.Printf("empty document skipped: %s", name)
		}
	}

	if len(docs) == 0 {
		log.Println("no documents found in knowledge/ directory")
		return ""
	}

	combined := strings.Join(docs, "\n\n")
	log.Printf("loaded %d document(s), total %d chars", len(docs), len(combined))
	return combined
}

func loadPDF(path string) string {
	f, r, err := pdf.Open(path)
	if err != nil {
		log.Printf("failed to open PDF %s: %v", path, err)
		return ""
	}
	defer f.Close()

	var sb strings.Builder
	for i := 1; i <= r.NumPage(); i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		text, err := p.GetPlainText(nil)
		if err != nil {
			continue
		}
		sb.WriteString(fmt.Sprintf("[Page %d]\n%s\n\n", i, text))
	}
	return sb.String()
}

func loadText(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("failed to read file %s: %v", path, err)
		return ""
	}
	return string(data)
}
