// Package inspector provides deep service inspection capabilities.
// It probes services through their protocols rather than reading source code,
// which works for compiled binaries, databases, and production containers.
package inspector

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// InspectionResult contains all discovered metadata about a process/service.
type InspectionResult struct {
	PID         int32    `json:"pid"`
	ProcessName string   `json:"process_name"`
	CommandLine []string `json:"command_line,omitempty"`
	WorkingDir  string   `json:"working_dir,omitempty"`
	EnvVarNames []string `json:"env_var_names,omitempty"` // Names only, not values (security)
	ListenPorts []int    `json:"listen_ports,omitempty"`

	// Protocol-specific probing results
	HTTPInfo *HTTPProbeResult `json:"http_info,omitempty"`
	DBInfo   *DBProbeResult   `json:"db_info,omitempty"`

	// K8s metadata (if available)
	K8sMetadata *K8sMetadata `json:"k8s_metadata,omitempty"`

	// Detected specs/docs
	OpenAPISpec string `json:"openapi_spec,omitempty"`

	// Configuration files (names only, not content)
	ConfigFiles []string `json:"config_files,omitempty"`

	// Package manifest info (dependencies)
	Dependencies []Dependency `json:"dependencies,omitempty"`

	// Source code context (for AI analysis)
	CodeContext *CodeContext `json:"code_context,omitempty"`
}

// HTTPProbeResult contains info from HTTP probing.
type HTTPProbeResult struct {
	ServerHeader    string            `json:"server_header,omitempty"`
	PoweredBy       string            `json:"x_powered_by,omitempty"`
	HealthEndpoint  string            `json:"health_endpoint,omitempty"`
	HealthStatus    int               `json:"health_status,omitempty"`
	Endpoints       []string          `json:"endpoints,omitempty"` // Discovered endpoints
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
}

// DBProbeResult contains database-specific info.
type DBProbeResult struct {
	Type       string   `json:"type"` // postgres, mysql, redis, mongodb
	Version    string   `json:"version,omitempty"`
	Databases  []string `json:"databases,omitempty"`
	TableCount int      `json:"table_count,omitempty"`
}

// K8sMetadata from the Downward API and annotations.
type K8sMetadata struct {
	PodName        string            `json:"pod_name,omitempty"`
	Namespace      string            `json:"namespace,omitempty"`
	ServiceAccount string            `json:"service_account,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	Annotations    map[string]string `json:"annotations,omitempty"`
}

// Dependency represents a package dependency.
type Dependency struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Type    string `json:"type"` // npm, pip, go, maven
}

// Inspector performs deep service inspection.
type Inspector struct {
	timeout time.Duration
}

// New creates a new Inspector.
func New() *Inspector {
	return &Inspector{
		timeout: 5 * time.Second,
	}
}

// InspectProcess performs deep inspection of a process.
func (i *Inspector) InspectProcess(pid int32) (*InspectionResult, error) {
	return i.InspectProcessWithCode(pid, false)
}

// InspectProcessWithCode performs deep inspection with optional source code reading.
func (i *Inspector) InspectProcessWithCode(pid int32, readCode bool) (*InspectionResult, error) {
	result := &InspectionResult{
		PID: pid,
	}

	// 1. Read process metadata from /proc
	if err := i.readProcInfo(pid, result); err != nil {
		return nil, fmt.Errorf("reading proc info: %w", err)
	}

	// 2. Find listening ports for this process
	result.ListenPorts = i.findListenPorts(pid)

	// 3. Scan for config files (names only)
	result.ConfigFiles = i.scanConfigFiles(pid)

	// 4. Read K8s metadata if available
	result.K8sMetadata = i.readK8sMetadata()

	// 5. Parse dependency manifests (package.json, requirements.txt, go.mod)
	result.Dependencies = i.parseDependencyManifests(pid)

	// 6. Read source code context (for AI analysis) - always reads safe files
	if result.WorkingDir != "" {
		result.CodeContext = i.ReadCodeContext(pid, result.WorkingDir, readCode)
	}

	return result, nil
}

// InspectEndpoint probes a network endpoint for service info.
func (i *Inspector) InspectEndpoint(host string, port int) (*InspectionResult, error) {
	result := &InspectionResult{
		ListenPorts: []int{port},
	}

	addr := fmt.Sprintf("%s:%d", host, port)

	// Try HTTP probe first
	if httpResult := i.probeHTTP(addr); httpResult != nil {
		result.HTTPInfo = httpResult
		return result, nil
	}

	// Try database protocols
	if dbResult := i.probeDatabase(addr, port); dbResult != nil {
		result.DBInfo = dbResult
		return result, nil
	}

	return result, nil
}

// readProcInfo reads basic process info from /proc.
func (i *Inspector) readProcInfo(pid int32, result *InspectionResult) error {
	procPath := fmt.Sprintf("/proc/%d", pid)

	// Read command line
	cmdlineBytes, err := os.ReadFile(filepath.Join(procPath, "cmdline"))
	if err == nil {
		// cmdline is null-separated
		parts := strings.Split(string(cmdlineBytes), "\x00")
		result.CommandLine = make([]string, 0)
		for _, p := range parts {
			if p != "" {
				result.CommandLine = append(result.CommandLine, p)
			}
		}
		if len(result.CommandLine) > 0 {
			result.ProcessName = filepath.Base(result.CommandLine[0])
		}
	}

	// Read working directory
	cwd, err := os.Readlink(filepath.Join(procPath, "cwd"))
	if err == nil {
		result.WorkingDir = cwd
	}

	// Read environment variable NAMES (not values - security!)
	envBytes, err := os.ReadFile(filepath.Join(procPath, "environ"))
	if err == nil {
		envPairs := strings.Split(string(envBytes), "\x00")
		result.EnvVarNames = make([]string, 0)
		for _, pair := range envPairs {
			if idx := strings.Index(pair, "="); idx > 0 {
				name := pair[:idx]
				// Filter out common/boring env vars
				if !isBoringEnvVar(name) {
					result.EnvVarNames = append(result.EnvVarNames, name)
				}
			}
		}
	}

	return nil
}

// findListenPorts finds TCP ports the process is listening on.
func (i *Inspector) findListenPorts(pid int32) []int {
	ports := make([]int, 0)

	// Read /proc/{pid}/net/tcp and /proc/{pid}/net/tcp6
	for _, proto := range []string{"tcp", "tcp6"} {
		path := fmt.Sprintf("/proc/%d/net/%s", pid, proto)
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		scanner.Scan() // Skip header

		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 4 {
				continue
			}

			// State 0A = LISTEN
			if fields[3] == "0A" {
				// Parse local address (format: IP:PORT in hex)
				localAddr := fields[1]
				if idx := strings.LastIndex(localAddr, ":"); idx != -1 {
					portHex := localAddr[idx+1:]
					if port, err := strconv.ParseInt(portHex, 16, 32); err == nil {
						ports = append(ports, int(port))
					}
				}
			}
		}
	}

	return uniqueInts(ports)
}

// scanConfigFiles scans for config files (names only, not content).
func (i *Inspector) scanConfigFiles(pid int32) []string {
	configFiles := make([]string, 0)
	rootPath := fmt.Sprintf("/proc/%d/root", pid)

	// Common config patterns
	configPatterns := []string{
		"*.yaml", "*.yml", "*.json", "*.conf", "*.cfg",
		"*.properties", "*.ini", "*.toml",
	}

	// Common config directories
	configDirs := []string{
		"/app", "/config", "/etc", "/opt",
	}

	for _, dir := range configDirs {
		fullDir := filepath.Join(rootPath, dir)

		filepath.WalkDir(fullDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return filepath.SkipDir
			}

			// Skip deep directories
			relPath, _ := filepath.Rel(fullDir, path)
			if strings.Count(relPath, string(os.PathSeparator)) > 2 {
				return filepath.SkipDir
			}

			// Skip common non-config dirs
			if d.IsDir() && isIgnoredDir(d.Name()) {
				return filepath.SkipDir
			}

			if !d.IsDir() {
				for _, pattern := range configPatterns {
					if matched, _ := filepath.Match(pattern, d.Name()); matched {
						// Store relative path only
						relFromRoot, _ := filepath.Rel(rootPath, path)
						configFiles = append(configFiles, relFromRoot)
						break
					}
				}
			}

			return nil
		})
	}

	// Limit to 20 files
	if len(configFiles) > 20 {
		configFiles = configFiles[:20]
	}

	return configFiles
}

// readK8sMetadata reads K8s metadata from Downward API paths.
func (i *Inspector) readK8sMetadata() *K8sMetadata {
	meta := &K8sMetadata{
		Labels:      make(map[string]string),
		Annotations: make(map[string]string),
	}

	// Standard Downward API paths
	if data, err := os.ReadFile("/etc/podinfo/name"); err == nil {
		meta.PodName = strings.TrimSpace(string(data))
	}
	if data, err := os.ReadFile("/etc/podinfo/namespace"); err == nil {
		meta.Namespace = strings.TrimSpace(string(data))
	}

	// Labels file (key="value" format)
	if data, err := os.ReadFile("/etc/podinfo/labels"); err == nil {
		meta.Labels = parseKeyValueFile(string(data))
	}

	// Annotations file
	if data, err := os.ReadFile("/etc/podinfo/annotations"); err == nil {
		meta.Annotations = parseKeyValueFile(string(data))
	}

	// Also try environment variables
	if meta.PodName == "" {
		meta.PodName = os.Getenv("POD_NAME")
	}
	if meta.Namespace == "" {
		meta.Namespace = os.Getenv("POD_NAMESPACE")
	}
	meta.ServiceAccount = os.Getenv("SERVICE_ACCOUNT")

	if meta.PodName == "" && meta.Namespace == "" {
		return nil // Not running in K8s
	}

	return meta
}

// parseDependencyManifests parses package manifests for dependencies.
func (i *Inspector) parseDependencyManifests(pid int32) []Dependency {
	deps := make([]Dependency, 0)
	rootPath := fmt.Sprintf("/proc/%d/root", pid)

	// Check for go.mod
	goModPath := filepath.Join(rootPath, "app", "go.mod")
	if _, err := os.Stat(goModPath); err == nil {
		deps = append(deps, i.parseGoMod(goModPath)...)
	}

	// Check for package.json
	pkgJsonPath := filepath.Join(rootPath, "app", "package.json")
	if _, err := os.Stat(pkgJsonPath); err == nil {
		deps = append(deps, i.parsePackageJson(pkgJsonPath)...)
	}

	// Check for requirements.txt
	reqPath := filepath.Join(rootPath, "app", "requirements.txt")
	if _, err := os.Stat(reqPath); err == nil {
		deps = append(deps, i.parseRequirementsTxt(reqPath)...)
	}

	return deps
}

// parseGoMod extracts dependencies from go.mod.
func (i *Inspector) parseGoMod(path string) []Dependency {
	deps := make([]Dependency, 0)

	data, err := os.ReadFile(path)
	if err != nil {
		return deps
	}

	// Simple regex to match require lines
	requireRegex := regexp.MustCompile(`^\s*(\S+)\s+(v[\d.]+.*)$`)
	inRequireBlock := false

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "require (") {
			inRequireBlock = true
			continue
		}
		if line == ")" {
			inRequireBlock = false
			continue
		}

		if inRequireBlock {
			if matches := requireRegex.FindStringSubmatch(line); len(matches) == 3 {
				deps = append(deps, Dependency{
					Name:    matches[1],
					Version: matches[2],
					Type:    "go",
				})
			}
		}
	}

	// Limit to 30 deps
	if len(deps) > 30 {
		deps = deps[:30]
	}

	return deps
}

// parsePackageJson extracts key dependencies from package.json.
func (i *Inspector) parsePackageJson(path string) []Dependency {
	deps := make([]Dependency, 0)
	// Simplified parsing - in production use encoding/json

	data, err := os.ReadFile(path)
	if err != nil {
		return deps
	}

	// Look for key frameworks
	content := string(data)
	frameworks := []string{"express", "fastify", "koa", "next", "react", "vue", "angular", "nestjs"}

	for _, fw := range frameworks {
		if strings.Contains(content, `"`+fw+`"`) {
			deps = append(deps, Dependency{Name: fw, Type: "npm"})
		}
	}

	return deps
}

// parseRequirementsTxt extracts dependencies from requirements.txt.
func (i *Inspector) parseRequirementsTxt(path string) []Dependency {
	deps := make([]Dependency, 0)

	data, err := os.ReadFile(path)
	if err != nil {
		return deps
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse package==version or package>=version
		parts := regexp.MustCompile(`[=<>!]+`).Split(line, 2)
		if len(parts) >= 1 {
			deps = append(deps, Dependency{
				Name:    strings.TrimSpace(parts[0]),
				Version: "",
				Type:    "pip",
			})
		}
	}

	// Limit to 30 deps
	if len(deps) > 30 {
		deps = deps[:30]
	}

	return deps
}

// probeHTTP performs HTTP probing on an endpoint.
func (i *Inspector) probeHTTP(addr string) *HTTPProbeResult {
	client := &http.Client{
		Timeout: i.timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Try common endpoints
	endpoints := []string{"/", "/health", "/healthz", "/api", "/metrics", "/openapi.json", "/swagger.json"}

	result := &HTTPProbeResult{
		ResponseHeaders: make(map[string]string),
		Endpoints:       make([]string, 0),
	}

	for _, endpoint := range endpoints {
		url := fmt.Sprintf("http://%s%s", addr, endpoint)
		resp, err := client.Get(url)
		if err != nil {
			// Try HTTPS
			url = fmt.Sprintf("https://%s%s", addr, endpoint)
			resp, err = client.Get(url)
			if err != nil {
				continue
			}
		}
		resp.Body.Close()

		result.Endpoints = append(result.Endpoints, endpoint)

		// Extract headers from first successful response
		if result.ServerHeader == "" {
			result.ServerHeader = resp.Header.Get("Server")
			result.PoweredBy = resp.Header.Get("X-Powered-By")

			// Store interesting headers
			for _, h := range []string{"Content-Type", "X-Request-Id", "X-Trace-Id"} {
				if v := resp.Header.Get(h); v != "" {
					result.ResponseHeaders[h] = v
				}
			}
		}

		if endpoint == "/health" || endpoint == "/healthz" {
			result.HealthEndpoint = endpoint
			result.HealthStatus = resp.StatusCode
		}
	}

	if len(result.Endpoints) == 0 {
		return nil
	}

	return result
}

// probeDatabase attempts database protocol detection.
func (i *Inspector) probeDatabase(addr string, port int) *DBProbeResult {
	// Port-based heuristics
	switch port {
	case 5432:
		return i.probePostgres(addr)
	case 3306:
		return i.probeMySQL(addr)
	case 6379:
		return i.probeRedis(addr)
	case 27017:
		return i.probeMongoDB(addr)
	}

	return nil
}

// probePostgres detects PostgreSQL.
func (i *Inspector) probePostgres(addr string) *DBProbeResult {
	conn, err := net.DialTimeout("tcp", addr, i.timeout)
	if err != nil {
		return nil
	}
	defer conn.Close()

	// Send PostgreSQL startup message (SSLRequest)
	sslRequest := []byte{0, 0, 0, 8, 4, 210, 22, 47}
	conn.Write(sslRequest)

	buf := make([]byte, 1)
	conn.SetReadDeadline(time.Now().Add(i.timeout))
	n, err := conn.Read(buf)

	if err == nil && n > 0 {
		// 'N' = no SSL, 'S' = SSL supported - both mean PostgreSQL
		if buf[0] == 'N' || buf[0] == 'S' {
			return &DBProbeResult{
				Type: "postgres",
			}
		}
	}

	return nil
}

// probeMySQL detects MySQL.
func (i *Inspector) probeMySQL(addr string) *DBProbeResult {
	conn, err := net.DialTimeout("tcp", addr, i.timeout)
	if err != nil {
		return nil
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(i.timeout))
	buf := make([]byte, 256)
	n, err := conn.Read(buf)

	if err == nil && n > 4 {
		// MySQL sends a greeting packet
		// Check for protocol version (usually 10)
		if buf[4] == 10 {
			// Extract version string
			verEnd := 5
			for verEnd < n && buf[verEnd] != 0 {
				verEnd++
			}
			version := string(buf[5:verEnd])

			return &DBProbeResult{
				Type:    "mysql",
				Version: version,
			}
		}
	}

	return nil
}

// probeRedis detects Redis.
func (i *Inspector) probeRedis(addr string) *DBProbeResult {
	conn, err := net.DialTimeout("tcp", addr, i.timeout)
	if err != nil {
		return nil
	}
	defer conn.Close()

	// Send PING command
	conn.Write([]byte("*1\r\n$4\r\nPING\r\n"))

	conn.SetReadDeadline(time.Now().Add(i.timeout))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)

	if err == nil && n > 0 {
		response := string(buf[:n])
		if strings.Contains(response, "PONG") || strings.Contains(response, "+PONG") {
			return &DBProbeResult{
				Type: "redis",
			}
		}
		// Also check for NOAUTH response (Redis with auth)
		if strings.Contains(response, "NOAUTH") {
			return &DBProbeResult{
				Type: "redis",
			}
		}
	}

	return nil
}

// probeMongoDB detects MongoDB.
func (i *Inspector) probeMongoDB(addr string) *DBProbeResult {
	conn, err := net.DialTimeout("tcp", addr, i.timeout)
	if err != nil {
		return nil
	}
	defer conn.Close()

	// MongoDB uses a specific wire protocol
	// For now, just detect the port and protocol handshake
	return &DBProbeResult{
		Type: "mongodb",
	}
}

// Helper functions

func isBoringEnvVar(name string) bool {
	boring := []string{
		"PATH", "HOME", "USER", "SHELL", "TERM", "LANG", "LC_",
		"HOSTNAME", "PWD", "SHLVL", "_", "OLDPWD",
	}
	for _, b := range boring {
		if name == b || strings.HasPrefix(name, b) {
			return true
		}
	}
	return false
}

func isIgnoredDir(name string) bool {
	ignored := []string{
		"node_modules", "vendor", ".git", "__pycache__",
		"bin", "obj", ".cache", ".npm", "dist", "build",
	}
	for _, ig := range ignored {
		if name == ig {
			return true
		}
	}
	return false
}

func parseKeyValueFile(content string) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		if idx := strings.Index(line, "="); idx > 0 {
			key := strings.TrimSpace(line[:idx])
			value := strings.Trim(strings.TrimSpace(line[idx+1:]), `"`)
			result[key] = value
		}
	}
	return result
}

func uniqueInts(ints []int) []int {
	seen := make(map[int]bool)
	result := make([]int, 0)
	for _, i := range ints {
		if !seen[i] {
			seen[i] = true
			result = append(result, i)
		}
	}
	return result
}
