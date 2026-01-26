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
	ProjectName    string            `json:"project_name,omitempty"`
	Description    string            `json:"description,omitempty"`
	ProjectRoot    string            `json:"project_root,omitempty"`   // Detected project root path
	Framework      string            `json:"framework,omitempty"`      // Detected framework (Laravel, Express, etc.)
	README         string            `json:"readme,omitempty"`         // First 2000 chars of README
	EntryPoint     string            `json:"entry_point,omitempty"`    // Main file content (first 100 lines)
	EntryPointFile string            `json:"entry_point_file,omitempty"`
	Dockerfile     string            `json:"dockerfile,omitempty"`     // Dockerfile content
	Dependencies   map[string]string `json:"dependencies,omitempty"`   // From package.json/go.mod/composer.json
	Scripts        map[string]string `json:"scripts,omitempty"`        // npm scripts or Makefile targets
	EnvExample     []string          `json:"env_example,omitempty"`    // .env.example variables
	
	// Deep analysis (opt-in)
	SourceFiles    []SourceFile      `json:"source_files,omitempty"`   // Additional source files
}

// SourceFile represents a source code file.
type SourceFile struct {
	Path    string `json:"path"`
	Content string `json:"content"` // First N lines
	Lines   int    `json:"lines"`   // Total lines in file
}

const (
	maxREADMESize      = 3000  // chars
	maxEntryPointLines = 150   // lines
	maxDockerfileSize  = 2000  // chars
	maxSourceFileLines = 100   // lines per file
	maxSourceFiles     = 5     // for deep analysis
)

// Identity files that indicate project root (ordered by priority)
var identityFiles = []string{
	"composer.json",      // PHP (Composer)
	"package.json",       // Node.js
	"go.mod",             // Go
	"requirements.txt",   // Python
	"pyproject.toml",     // Python (modern)
	"Cargo.toml",         // Rust
	"pom.xml",            // Java (Maven)
	"build.gradle",       // Java (Gradle)
	"Gemfile",            // Ruby
	"mix.exs",            // Elixir
}

// Nested folder suffixes that often indicate we're NOT at the project root
var nestedFolderSuffixes = []string{
	"/public",
	"/web",
	"/dist",
	"/build",
	"/bin",
	"/www",
	"/html",
	"/static",
	"/assets",
	"/out",
}

// DetectProjectRoot finds the actual project root by looking for identity files.
// Interpreted languages like PHP and Node.js often run from nested directories.
func DetectProjectRoot(cwd string) string {
	// Check if CWD ends with a nested folder suffix
	for _, suffix := range nestedFolderSuffixes {
		if strings.HasSuffix(cwd, suffix) {
			parentDir := filepath.Dir(cwd)
			// Check parent for identity files
			for _, idFile := range identityFiles {
				if pathExists(filepath.Join(parentDir, idFile)) {
					log.Debugf("Project root detected at parent: %s (found %s)", parentDir, idFile)
					return parentDir
				}
			}
			// Also try grandparent (e.g., /var/www/app/public -> /var/www/app)
			grandparentDir := filepath.Dir(parentDir)
			for _, idFile := range identityFiles {
				if pathExists(filepath.Join(grandparentDir, idFile)) {
					log.Debugf("Project root detected at grandparent: %s (found %s)", grandparentDir, idFile)
					return grandparentDir
				}
			}
		}
	}
	
	// Walk up from CWD looking for identity files (up to 3 levels)
	currentPath := cwd
	for i := 0; i < 3; i++ {
		for _, idFile := range identityFiles {
			if pathExists(filepath.Join(currentPath, idFile)) {
				if currentPath != cwd {
					log.Debugf("Project root detected: %s (found %s)", currentPath, idFile)
				}
				return currentPath
			}
		}
		parent := filepath.Dir(currentPath)
		if parent == currentPath {
			break // Reached filesystem root
		}
		currentPath = parent
	}
	
	// Default to original CWD
	return cwd
}

// DetectFramework identifies the framework from identity files.
func DetectFramework(basePath string) string {
	// PHP Frameworks
	if pathExists(filepath.Join(basePath, "artisan")) {
		return "Laravel"
	}
	if pathExists(filepath.Join(basePath, "bin", "console")) && pathExists(filepath.Join(basePath, "symfony.lock")) {
		return "Symfony"
	}
	if pathExists(filepath.Join(basePath, "wp-config.php")) {
		return "WordPress"
	}
	if pathExists(filepath.Join(basePath, "index.php")) && pathExists(filepath.Join(basePath, "system", "core")) {
		return "CodeIgniter"
	}
	
	// Node.js Frameworks (check package.json dependencies)
	if pkgContent := readFileHead(filepath.Join(basePath, "package.json"), 5000); pkgContent != "" {
		if strings.Contains(pkgContent, "\"express\"") {
			return "Express.js"
		}
		if strings.Contains(pkgContent, "\"next\"") {
			return "Next.js"
		}
		if strings.Contains(pkgContent, "\"nuxt\"") {
			return "Nuxt.js"
		}
		if strings.Contains(pkgContent, "\"@nestjs/core\"") {
			return "NestJS"
		}
		if strings.Contains(pkgContent, "\"fastify\"") {
			return "Fastify"
		}
		if strings.Contains(pkgContent, "\"koa\"") {
			return "Koa"
		}
		if strings.Contains(pkgContent, "\"react\"") {
			return "React"
		}
		if strings.Contains(pkgContent, "\"vue\"") {
			return "Vue.js"
		}
		if strings.Contains(pkgContent, "\"angular\"") || strings.Contains(pkgContent, "\"@angular/core\"") {
			return "Angular"
		}
	}
	
	// Python Frameworks
	if reqContent := readFileHead(filepath.Join(basePath, "requirements.txt"), 2000); reqContent != "" {
		if strings.Contains(reqContent, "django") || strings.Contains(reqContent, "Django") {
			return "Django"
		}
		if strings.Contains(reqContent, "flask") || strings.Contains(reqContent, "Flask") {
			return "Flask"
		}
		if strings.Contains(reqContent, "fastapi") || strings.Contains(reqContent, "FastAPI") {
			return "FastAPI"
		}
	}
	
	// Go Frameworks (check go.mod)
	if goModContent := readFileHead(filepath.Join(basePath, "go.mod"), 2000); goModContent != "" {
		if strings.Contains(goModContent, "github.com/gin-gonic/gin") {
			return "Gin"
		}
		if strings.Contains(goModContent, "github.com/gofiber/fiber") {
			return "Fiber"
		}
		if strings.Contains(goModContent, "github.com/labstack/echo") {
			return "Echo"
		}
		if strings.Contains(goModContent, "github.com/gorilla/mux") {
			return "Gorilla Mux"
		}
	}
	
	return ""
}

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

	// Detect actual project root (handles nested folders like /public, /dist, etc.)
	projectRoot := DetectProjectRoot(basePath)
	if projectRoot != basePath {
		log.Infof("Code context: detected project root at %s (CWD was %s)", projectRoot, basePath)
		basePath = projectRoot
	}

	ctx := &CodeContext{
		ProjectRoot:  projectRoot,
		Dependencies: make(map[string]string),
		Scripts:      make(map[string]string),
	}

	// Detect framework
	ctx.Framework = DetectFramework(basePath)
	if ctx.Framework != "" {
		log.Debugf("Code context: detected framework: %s", ctx.Framework)
	}

	// 1. Read README.md
	ctx.README = readFileHead(filepath.Join(basePath, "README.md"), maxREADMESize)
	if ctx.README == "" {
		ctx.README = readFileHead(filepath.Join(basePath, "readme.md"), maxREADMESize)
	}

	// 2. Read composer.json (PHP) - HIGHEST PRIORITY for PHP projects
	if composerJSON := readFileHead(filepath.Join(basePath, "composer.json"), 5000); composerJSON != "" {
		ctx.parseComposerJSON(composerJSON)
	}

	// 3. Read package.json (Node.js)
	if pkgJSON := readFileHead(filepath.Join(basePath, "package.json"), 5000); pkgJSON != "" {
		ctx.parsePackageJSON(pkgJSON)
	}

	// 4. Read go.mod (Go)
	if goMod := readFileHead(filepath.Join(basePath, "go.mod"), 2000); goMod != "" {
		ctx.parseGoMod(goMod)
	}

	// 5. Read requirements.txt (Python)
	if reqTxt := readFileHead(filepath.Join(basePath, "requirements.txt"), 2000); reqTxt != "" {
		ctx.parseRequirementsTxt(reqTxt)
	}

	// 6. Read Dockerfile
	ctx.Dockerfile = readFileHead(filepath.Join(basePath, "Dockerfile"), maxDockerfileSize)

	// 7. Read .env.example
	ctx.EnvExample = readEnvExample(filepath.Join(basePath, ".env.example"))
	if len(ctx.EnvExample) == 0 {
		ctx.EnvExample = readEnvExample(filepath.Join(basePath, ".env.sample"))
	}

	// 8. Find and read entry point (framework-aware)
	ctx.findEntryPoint(basePath)

	// 9. Read framework-specific files
	ctx.readFrameworkFiles(basePath)

	// 10. Deep analysis (if requested)
	if deep {
		ctx.readSourceFiles(basePath)
	}

	return ctx
}

func (ctx *CodeContext) parseComposerJSON(content string) {
	// Parse composer.json for PHP projects
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, `"name"`) && ctx.ProjectName == "" {
			ctx.ProjectName = extractJSONValue(line)
		}
		if strings.Contains(line, `"description"`) && ctx.Description == "" {
			ctx.Description = extractJSONValue(line)
		}
	}
	
	// Extract require section (dependencies)
	inRequire := false
	for _, line := range lines {
		if strings.Contains(line, `"require"`) && !strings.Contains(line, `"require-dev"`) {
			inRequire = true
			continue
		}
		if inRequire {
			if strings.Contains(line, "}") {
				inRequire = false
				continue
			}
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				name := strings.Trim(strings.TrimSpace(parts[0]), `"`)
				version := strings.Trim(strings.TrimSpace(parts[1]), `",`)
				// Skip PHP version and ext-* requirements
				if name != "" && !strings.HasPrefix(name, "php") && !strings.HasPrefix(name, "ext-") {
					ctx.Dependencies[name] = version
				}
			}
		}
	}
	
	// Also check for scripts
	inScripts := false
	for _, line := range lines {
		if strings.Contains(line, `"scripts"`) {
			inScripts = true
			continue
		}
		if inScripts {
			if strings.Contains(line, "}") {
				inScripts = false
				continue
			}
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				name := strings.Trim(strings.TrimSpace(parts[0]), `"`)
				// Just store script names, not the full commands
				if name != "" {
					ctx.Scripts[name] = "(defined)"
				}
			}
		}
	}
}

func (ctx *CodeContext) parsePackageJSON(content string) {
	// Simple parsing without full JSON unmarshal
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, `"name"`) && ctx.ProjectName == "" {
			ctx.ProjectName = extractJSONValue(line)
		}
		if strings.Contains(line, `"description"`) && ctx.Description == "" {
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
	// Framework-specific entry points first
	frameworkEntryPoints := map[string][]string{
		"Laravel": {
			"routes/web.php",
			"routes/api.php",
			"app/Http/Kernel.php",
		},
		"Symfony": {
			"src/Kernel.php",
			"config/routes.yaml",
		},
		"Express.js": {
			"app.js",
			"server.js",
			"index.js",
			"src/app.js",
			"src/index.js",
		},
		"Next.js": {
			"pages/_app.js",
			"pages/_app.tsx",
			"src/pages/_app.tsx",
			"app/layout.tsx",
		},
		"Django": {
			"manage.py",
			"urls.py",
			"settings.py",
		},
		"Flask": {
			"app.py",
			"main.py",
			"wsgi.py",
		},
	}
	
	// Try framework-specific entry points first
	if ctx.Framework != "" {
		if entries, ok := frameworkEntryPoints[ctx.Framework]; ok {
			for _, entry := range entries {
				fullPath := filepath.Join(basePath, entry)
				if content := readFileLines(fullPath, maxEntryPointLines); content != "" {
					ctx.EntryPointFile = entry
					ctx.EntryPoint = content
					return
				}
			}
		}
	}
	
	// Common entry point files in priority order
	entryPoints := []string{
		// PHP
		"public/index.php",
		"index.php",
		// Go
		"main.go",
		"cmd/main.go",
		"src/main.go",
		// Python
		"app.py",
		"main.py",
		"server.py",
		// Node.js
		"index.js",
		"src/index.js",
		"server.js",
		"app.js",
		"index.ts",
		"src/index.ts",
		"server.ts",
		"app.ts",
		// Rust
		"main.rs",
		"src/main.rs",
		// Others
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

// readFrameworkFiles reads important framework-specific files for AI context.
func (ctx *CodeContext) readFrameworkFiles(basePath string) {
	// Skip if no source files slice yet
	if ctx.SourceFiles == nil {
		ctx.SourceFiles = []SourceFile{}
	}
	
	// Framework-specific important files
	frameworkFiles := map[string][]string{
		"Laravel": {
			"routes/web.php",
			"routes/api.php",
			"app/Http/Controllers/Controller.php",
			"config/app.php",
			"database/migrations",
		},
		"Symfony": {
			"config/routes.yaml",
			"config/services.yaml",
			"src/Controller",
		},
		"Express.js": {
			"routes/index.js",
			"middleware/auth.js",
		},
		"Django": {
			"urls.py",
			"models.py",
			"views.py",
		},
		"Flask": {
			"routes.py",
			"models.py",
		},
		"Next.js": {
			"next.config.js",
			"pages/api",
		},
	}
	
	if ctx.Framework == "" {
		return
	}
	
	files, ok := frameworkFiles[ctx.Framework]
	if !ok {
		return
	}
	
	for _, file := range files {
		fullPath := filepath.Join(basePath, file)
		
		// Check if it's a directory
		info, err := os.Stat(fullPath)
		if err != nil {
			continue
		}
		
		if info.IsDir() {
			// Read first file in the directory
			entries, err := os.ReadDir(fullPath)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if !entry.IsDir() && len(ctx.SourceFiles) < maxSourceFiles {
					filePath := filepath.Join(fullPath, entry.Name())
					content := readFileLines(filePath, maxSourceFileLines)
					if content != "" {
						ctx.SourceFiles = append(ctx.SourceFiles, SourceFile{
							Path:    filepath.Join(file, entry.Name()),
							Content: content,
							Lines:   countLines(filePath),
						})
					}
					break // Just read the first file
				}
			}
		} else {
			// Read the file
			if len(ctx.SourceFiles) < maxSourceFiles {
				content := readFileLines(fullPath, maxSourceFileLines)
				if content != "" {
					ctx.SourceFiles = append(ctx.SourceFiles, SourceFile{
						Path:    file,
						Content: content,
						Lines:   countLines(fullPath),
					})
				}
			}
		}
	}
}

func (ctx *CodeContext) readSourceFiles(basePath string) {
	// Skip if path is a system directory
	if strings.HasPrefix(basePath, "/etc/") || 
	   strings.HasPrefix(basePath, "/usr/") || 
	   strings.HasPrefix(basePath, "/lib/") ||
	   strings.HasPrefix(basePath, "/bin/") ||
	   strings.HasPrefix(basePath, "/sbin/") {
		log.Debugf("Skipping source file scan for system path: %s", basePath)
		return
	}
	
	// Find additional source files for deep analysis
	extensions := []string{".go", ".py", ".js", ".ts", ".java", ".rs", ".cs", ".php", ".rb"}
	skipDirs := map[string]bool{
		"node_modules": true,
		"vendor":       true,
		".git":         true,
		"__pycache__":  true,
		"dist":         true,
		"build":        true,
		"target":       true,
		"cache":        true,
		"storage":      true,
		"logs":         true,
		"tmp":          true,
		"temp":         true,
	}

	// Prioritized files to look for
	priorityFiles := []string{
		"routes/web.php",
		"routes/api.php",
		"app/Http/Controllers/Controller.php",
		"src/Controller",
		"controllers",
		"api",
		"services",
	}
	
	var files []SourceFile
	
	// First, try to read priority files
	for _, priorityFile := range priorityFiles {
		if len(files) >= maxSourceFiles {
			break
		}
		fullPath := filepath.Join(basePath, priorityFile)
		info, err := os.Stat(fullPath)
		if err != nil {
			continue
		}
		
		if info.IsDir() {
			// Read first few files from priority directory
			entries, _ := os.ReadDir(fullPath)
			for _, entry := range entries {
				if len(files) >= maxSourceFiles {
					break
				}
				if !entry.IsDir() {
					filePath := filepath.Join(fullPath, entry.Name())
					ext := filepath.Ext(entry.Name())
					for _, e := range extensions {
						if ext == e {
							content := readFileLines(filePath, maxSourceFileLines)
							if content != "" {
								files = append(files, SourceFile{
									Path:    filepath.Join(priorityFile, entry.Name()),
									Content: content,
									Lines:   countLines(filePath),
								})
							}
							break
						}
					}
				}
			}
		} else {
			content := readFileLines(fullPath, maxSourceFileLines)
			if content != "" {
				files = append(files, SourceFile{
					Path:    priorityFile,
					Content: content,
					Lines:   countLines(fullPath),
				})
			}
		}
	}
	
	// Then walk the directory for remaining slots
	if len(files) < maxSourceFiles {
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
					
					// Skip if already added
					alreadyAdded := false
					for _, f := range files {
						if f.Path == relPath {
							alreadyAdded = true
							break
						}
					}
					if alreadyAdded {
						break
					}
					
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
	}

	// Append to existing source files (framework files may already be there)
	if ctx.SourceFiles == nil {
		ctx.SourceFiles = files
	} else {
		// Add non-duplicate files
		existingPaths := make(map[string]bool)
		for _, f := range ctx.SourceFiles {
			existingPaths[f.Path] = true
		}
		for _, f := range files {
			if !existingPaths[f.Path] && len(ctx.SourceFiles) < maxSourceFiles*2 {
				ctx.SourceFiles = append(ctx.SourceFiles, f)
			}
		}
	}
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
