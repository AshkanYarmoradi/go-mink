package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	rootDir := `c:\Users\ayarm\Sources\go-mink`
	oldStr := []byte("go-mink.dev")
	newStr := []byte("go-mink.dev")
	
	count := 0
	
	err := filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == ".gemini" || name == "build" {
				return filepath.SkipDir
			}
			return nil
		}
		
		ext := filepath.Ext(path)
		name := d.Name()
		
		// Only process go files, go.mod, md files, AGENTS.md, and yaml files
		if ext == ".go" || name == "go.mod" || ext == ".md" || ext == ".yml" || ext == ".yaml" {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			
			if bytes.Contains(content, oldStr) {
				// To preserve actual web links, we temporarily protect https:// URLs
				content = bytes.ReplaceAll(content, []byte("https://github.com/AshkanYarmoradi/go-mink"), []byte("https://github.com/AshkanYarmoradi/go-mink"))
				content = bytes.ReplaceAll(content, []byte("http://github.com/AshkanYarmoradi/go-mink"), []byte("http://github.com/AshkanYarmoradi/go-mink"))
				
				// Perform the main replacement
				newContent := bytes.ReplaceAll(content, oldStr, newStr)
				
				// Restore the web links
				newContent = bytes.ReplaceAll(newContent, []byte("https://github.com/AshkanYarmoradi/go-mink"), []byte("https://github.com/AshkanYarmoradi/go-mink"))
				newContent = bytes.ReplaceAll(newContent, []byte("http://github.com/AshkanYarmoradi/go-mink"), []byte("http://github.com/AshkanYarmoradi/go-mink"))
				
				err = os.WriteFile(path, newContent, 0644)
				if err != nil {
					return err
				}
				fmt.Printf("Updated: %s\n", strings.TrimPrefix(path, rootDir))
				count++
			}
		}
		
		return nil
	})
	
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	
	fmt.Printf("Successfully updated %d files.\n", count)
}
