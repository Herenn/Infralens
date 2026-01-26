package inspector

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
)

// CodeContext contains source code context for AI analysis.
type CodeContext struct {
	ProjectName   string            `json:"project_name,omitempty"`
	Description   string            `json:"description,omitempty"`
	README        string            `json:"readme,omitempty"`         // First 2000 chars of README
	EntryPoint    string            `json:"entry_point,omitempty"`    // Main file content (first 100 lines)
	EntryPointFile string           `json:"entry_point_file,omitempty"`
	Dockerfile    string            `json:"dockerfile,omitempty"`     // Dockerfile content
	Dependencies  map[string]string `json:"dependencies,omitempty"`   // From package.json/go.mod
	Scripts       map[string]string `json:"scripts,omitempty"`        // npm scripts or Makefile targets
	EnvExample    []string          `json:"env_example,omitempty"`    // .env.example variables
	
	// Deep analysis (opt-in)
	SourceFiles   []SourceFile      `json:"source_files,omitempty"`   // Additional source files
}

// SourceFile represents a source code file.
type SourceFile struct {
	Path    string `json:"path"`
	Content string `json:"content"` // First N lines
	Lines   int    `json:"lines"`   // Total lines in file
}

const (
	maxREADMESize     = 3000  // chars
	maxEntryPointLines = 150  // lines
	maxDockerfileSize = 2000  // chars
	maxSourceFileLines = 100  // lines per file
	maxSourceFiles    = 5     // for deep analysis
)

// ReadCodeContext reads source code context from a process's working directory.
func (i *Inspector) ReadCodeContext(pid int32, cwd string, deep bool) *CodeContext {
	if cwd == "" {
		return nil
	}

	// Use /proc/{pid}/root to access container filesystem
	rootPath := fmt.Sprintf("/proc/%d/root", pid)
	basePath := filepath.Join(rootPath, cwd)
	
	// Fallback to direct path if /proc path doesn't work
	if !pathExists(basePath) {
		basePath = cwd
	}
	
	if !pathExists(basePath) {
		log.Debugf("Code context: base path not accessible: %s", basePath)
		return nil
	}

	ctx := &CodeContext{
		Dependencies: make(map[string]string),
		Scripts:      make(map[string]string),
	}

	// 1. Read README.md
	ctx.README = readFileHead(filepath.Join(basePath, "README.md"), maxREADMESize)
	if ctx.README == "" {
		ctx.README = readFileHead(filepath.Join(basePath, "readme.md"), maxREADMESize)
	}

	// 2. Read package.json (Node.js)
	if pkgJSON := readFileHead(filepath.Join(basePath, "package.json"), 5000); pkgJSON != "" {
		ctx.parsePackageJSON(pkgJSON)
	}

	// 3. Read go.mod (Go)
	if goMod := readFileHead(filepath.Join(basePath, "go.mod"), 2000); goMod != "" {
		ctx.parseGoMod(goMod)
	}

	// 4. Read requirements.txt (Python)
	if reqTxt := readFileHead(filepath.Join(basePath, "requirements.txt"), 2000); reqTxt != "" {
		ctx.parseRequirementsTxt(reqTxt)
	}

	// 5. Read Dockerfile
	ctx.Dockerfile = readFileHead(filepath.Join(basePath, "Dockerfile"), maxDockerfileSize)

	// 6. Read .env.example
	ctx.EnvExample = readEnvExample(filepath.Join(basePath, ".env.example"))
	if len(ctx.EnvExample) == 0 {
		ctx.EnvExample = readEnvExample(filepath.Join(basePath, ".env.sample"))
	}

	// 7. Find and read entry point
	ctx.findEntryPoint(basePath)

	// 8. Deep analysis (if requested)
	if deep {
		ctx.readSourceFiles(basePath)
	}

	return ctx
}

func (ctx *CodeContext) parsePackageJSON(content string) {
	// Simple parsing without full JSON unmarshal
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, `"name"`) {
			ctx.ProjectName = extractJSONValue(line)
		}
		if strings.Contains(line, `"description"`) {
			ctx.Description = extractJSONValue(line)
		}
	}
	
	// Extract dependencies section
	inDeps := false
	for _, line := range lines {
		if strings.Contains(line, `"dependencies"`) || strings.Contains(line, `"devDependencies"`) {
			inDeps = true
			continue
		}
		if inDeps {
			if strings.Contains(line, "}") {
				inDeps = false
				continue
			}
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				name := strings.Trim(strings.TrimSpace(parts[0]), `"`)
				version := strings.Trim(strings.TrimSpace(parts[1]), `",`)
				if name != "" && !strings.HasPrefix(name, "@") {
					ctx.Dependencies[name] = version
				}
			}
		}
	}
}

func (ctx *CodeContext) parseGoMod(content string) {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			ctx.ProjectName = strings.TrimPrefix(line, "module ")
		}
		// Skip dependency parsing for go.mod (too complex)
	}
}

func (ctx *CodeContext) parseRequirementsTxt(content string) {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Parse package==version or package>=version
		for _, sep := range []string{"==", ">=", "<=", "~=", "!="} {
			if strings.Contains(line, sep) {
				parts := strings.SplitN(line, sep, 2)
				if len(parts) == 2 {
					ctx.Dependencies[parts[0]] = parts[1]
				}
				break
			}
		}
	}
}

func (ctx *CodeContext) findEntryPoint(basePath string) {
	// Common entry point files in priority order
	entryPoints := []string{
		"main.go",
		"cmd/main.go",
		"src/main.go",
		"app.py",
		"main.py",
		"server.py",
		"index.js",
		"src/index.js",
		"server.js",
		"app.js",
		"index.ts",
		"src/index.ts",
		"server.ts",
		"app.ts",
		"main.rs",
		"src/main.rs",
		"Program.cs",
		"Main.java",
	}

	for _, entry := range entryPoints {
		fullPath := filepath.Join(basePath, entry)
		if content := readFileLines(fullPath, maxEntryPointLines); content != "" {
			ctx.EntryPointFile = entry
			ctx.EntryPoint = content
			return
		}
	}
}

func (ctx *CodeContext) readSourceFiles(basePath string) {
	// Find additional source files for deep analysis
	extensions := []string{".go", ".py", ".js", ".ts", ".java", ".rs", ".cs"}
	skipDirs := map[string]bool{
		"node_modules": true,
		"vendor":       true,
		".git":         true,
		"__pycache__":  true,
		"dist":         true,
		"build":        true,
		"target":       true,
	}

	var files []SourceFile
	
	filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() && skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		
		if len(files) >= maxSourceFiles {
			return filepath.SkipAll
		}

		ext := filepath.Ext(info.Name())
		for _, e := range extensions {
			if ext == e {
				relPath, _ := filepath.Rel(basePath, path)
				content := readFileLines(path, maxSourceFileLines)
				if content != "" {
					files = append(files, SourceFile{
						Path:    relPath,
						Content: content,
						Lines:   countLines(path),
					})
				}
				break
			}
		}
		return nil
	})

	ctx.SourceFiles = files
}

// Helper functions

func extractJSONValue(line string) string {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return ""
	}
	value := strings.TrimSpace(parts[1])
	value = strings.Trim(value, `",`)
	return value
}

func readFileHead(path string, maxChars int) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	buf := make([]byte, maxChars)
	n, _ := file.Read(buf)
	if n == 0 {
		return ""
	}
	return string(buf[:n])
}

func readFileLines(path string, maxLines int) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	lineNum := 1
	for scanner.Scan() && len(lines) < maxLines {
		// Include line numbers for AI to reference
		lines = append(lines, fmt.Sprintf("%4d | %s", lineNum, scanner.Text()))
		lineNum++
	}
	
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func readEnvExample(path string) []string {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	var vars []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Only extract variable name, not value
		if idx := strings.Index(line, "="); idx > 0 {
			vars = append(vars, line[:idx])
		}
	}
	return vars
}

func countLines(path string) int {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()

	count := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		count++
	}
	return count
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
