package llm

import (
	"context"
	"fmt"
	"strings"
)

// ServiceContext contains all the information about a service for AI analysis.
type ServiceContext struct {
	// Basic info
	ServiceID   string `json:"service_id"`
	ServiceName string `json:"service_name"`
	ServiceType string `json:"service_type"` // database, cache, web_server, etc.
	Technology  string `json:"technology"`   // PostgreSQL, Redis, Nginx, etc.
	NodeName    string `json:"node_name"`
	IPAddress   string `json:"ip_address"`

	// Network topology
	IncomingConnections []ConnectionInfo `json:"incoming_connections"`
	OutgoingConnections []ConnectionInfo `json:"outgoing_connections"`

	// Deep inspection data
	ProcessName  string   `json:"process_name,omitempty"`
	CommandLine  string   `json:"command_line,omitempty"`
	ListenPorts  []int    `json:"listen_ports,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
	ConfigFiles  []string `json:"config_files,omitempty"`
	EnvVarNames  []string `json:"env_var_names,omitempty"`

	// HTTP probe results
	HTTPEndpoints []string `json:"http_endpoints,omitempty"`
	ServerHeader  string   `json:"server_header,omitempty"`
	HealthStatus  int      `json:"health_status,omitempty"`

	// Database info
	DBType      string   `json:"db_type,omitempty"`
	DBVersion   string   `json:"db_version,omitempty"`
	DBDatabases []string `json:"db_databases,omitempty"`

	// Source code context (NEW)
	ProjectName    string `json:"project_name,omitempty"`
	ProjectDesc    string `json:"project_desc,omitempty"`
	README         string `json:"readme,omitempty"`
	EntryPointCode string `json:"entry_point_code,omitempty"`
	EntryPointFile string `json:"entry_point_file,omitempty"`
	Dockerfile     string `json:"dockerfile,omitempty"`
}

// ConnectionInfo describes a network connection.
type ConnectionInfo struct {
	RemoteIP    string  `json:"remote_ip"`
	RemoteName  string  `json:"remote_name,omitempty"`
	Port        int     `json:"port"`
	BytesPerSec float64 `json:"bytes_per_sec,omitempty"`
}

// DocumentationRequest represents a request for AI documentation.
type DocumentationRequest struct {
	Context  ServiceContext `json:"context"`
	Question string         `json:"question,omitempty"` // Optional specific question
	Format   string         `json:"format,omitempty"`   // "markdown", "json", "plain"
}

// DocumentationResponse contains the generated documentation.
type DocumentationResponse struct {
	Content    string `json:"content"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	TokensUsed int    `json:"tokens_used,omitempty"`
}

// DocsGenerator generates AI documentation for services.
type DocsGenerator struct {
	manager *Manager
}

// NewDocsGenerator creates a new documentation generator.
func NewDocsGenerator(manager *Manager) *DocsGenerator {
	return &DocsGenerator{manager: manager}
}

// GenerateDocumentation generates documentation for a service.
func (g *DocsGenerator) GenerateDocumentation(ctx context.Context, req DocumentationRequest) (*DocumentationResponse, error) {
	prompt := g.buildPrompt(req)

	messages := []Message{
		{
			Role:    "system",
			Content: systemPrompt,
		},
		{
			Role:    "user",
			Content: prompt,
		},
	}

	resp, err := g.manager.Complete(ctx, CompletionRequest{
		Messages:    messages,
		MaxTokens:   2500, // Allow detailed documentation
		Temperature: 0.5,  // More focused/deterministic
	})
	if err != nil {
		return nil, fmt.Errorf("generating documentation: %w", err)
	}

	return &DocumentationResponse{
		Content:    resp.Content,
		Provider:   resp.Provider,
		Model:      resp.Model,
		TokensUsed: resp.TokensUsed,
	}, nil
}

// GenerateWithProvider generates documentation using a specific provider.
func (g *DocsGenerator) GenerateWithProvider(ctx context.Context, provider Provider, req DocumentationRequest) (*DocumentationResponse, error) {
	prompt := g.buildPrompt(req)

	messages := []Message{
		{
			Role:    "system",
			Content: systemPrompt,
		},
		{
			Role:    "user",
			Content: prompt,
		},
	}

	resp, err := g.manager.CompleteWith(ctx, provider, CompletionRequest{
		Messages:    messages,
		MaxTokens:   2048,
		Temperature: 0.7,
	})
	if err != nil {
		return nil, fmt.Errorf("generating documentation: %w", err)
	}

	return &DocumentationResponse{
		Content:    resp.Content,
		Provider:   resp.Provider,
		Model:      resp.Model,
		TokensUsed: resp.TokensUsed,
	}, nil
}

// AskQuestion asks a specific question about a service.
func (g *DocsGenerator) AskQuestion(ctx context.Context, req DocumentationRequest) (*DocumentationResponse, error) {
	if req.Question == "" {
		return nil, fmt.Errorf("question is required")
	}

	prompt := g.buildQuestionPrompt(req)

	messages := []Message{
		{
			Role:    "system",
			Content: questionSystemPrompt,
		},
		{
			Role:    "user",
			Content: prompt,
		},
	}

	resp, err := g.manager.Complete(ctx, CompletionRequest{
		Messages:    messages,
		MaxTokens:   512, // Keep answers short to save costs
		Temperature: 0.5,
	})
	if err != nil {
		return nil, fmt.Errorf("answering question: %w", err)
	}

	return &DocumentationResponse{
		Content:    resp.Content,
		Provider:   resp.Provider,
		Model:      resp.Model,
		TokensUsed: resp.TokensUsed,
	}, nil
}

// Max limits to prevent token overflow
const (
	maxConnections  = 10
	maxConfigFiles  = 10
	maxEnvVars      = 15
	maxDependencies = 15
)

func (g *DocsGenerator) buildPrompt(req DocumentationRequest) string {
	var sb strings.Builder

	sb.WriteString("Analyze this service and provide comprehensive documentation:\n\n")

	// Basic info
	sb.WriteString("## Service Information\n")
	sb.WriteString(fmt.Sprintf("- **Name**: %s\n", req.Context.ServiceName))
	sb.WriteString(fmt.Sprintf("- **ID**: %s\n", req.Context.ServiceID))
	sb.WriteString(fmt.Sprintf("- **Type**: %s\n", req.Context.ServiceType))
	if req.Context.Technology != "" {
		sb.WriteString(fmt.Sprintf("- **Technology**: %s\n", req.Context.Technology))
	}
	sb.WriteString(fmt.Sprintf("- **Node**: %s\n", req.Context.NodeName))
	sb.WriteString(fmt.Sprintf("- **IP**: %s\n", req.Context.IPAddress))
	sb.WriteString("\n")

	// Process info
	if req.Context.ProcessName != "" {
		sb.WriteString("## Process Details\n")
		sb.WriteString(fmt.Sprintf("- **Process**: %s\n", req.Context.ProcessName))
		if req.Context.CommandLine != "" {
			sb.WriteString(fmt.Sprintf("- **Command**: `%s`\n", req.Context.CommandLine))
		}
		if len(req.Context.ListenPorts) > 0 {
			sb.WriteString(fmt.Sprintf("- **Listening Ports**: %v\n", req.Context.ListenPorts))
		}
		sb.WriteString("\n")
	}

	// Network connections (limited to prevent token overflow)
	if len(req.Context.IncomingConnections) > 0 || len(req.Context.OutgoingConnections) > 0 {
		sb.WriteString("## Network Topology\n")

		if len(req.Context.IncomingConnections) > 0 {
			sb.WriteString("### Incoming Connections\n")
			incomingToShow := req.Context.IncomingConnections
			if len(incomingToShow) > maxConnections {
				incomingToShow = incomingToShow[:maxConnections]
			}
			for _, conn := range incomingToShow {
				name := conn.RemoteName
				if name == "" {
					name = conn.RemoteIP
				}
				sb.WriteString(fmt.Sprintf("- %s → this service (port %d)", name, conn.Port))
				if conn.BytesPerSec > 0 {
					sb.WriteString(fmt.Sprintf(" [%.1f B/s]", conn.BytesPerSec))
				}
				sb.WriteString("\n")
			}
			if len(req.Context.IncomingConnections) > maxConnections {
				sb.WriteString(fmt.Sprintf("- ... and %d more incoming connections\n", len(req.Context.IncomingConnections)-maxConnections))
			}
		}

		if len(req.Context.OutgoingConnections) > 0 {
			sb.WriteString("### Outgoing Connections\n")
			outgoingToShow := req.Context.OutgoingConnections
			if len(outgoingToShow) > maxConnections {
				outgoingToShow = outgoingToShow[:maxConnections]
			}
			for _, conn := range outgoingToShow {
				name := conn.RemoteName
				if name == "" {
					name = conn.RemoteIP
				}
				sb.WriteString(fmt.Sprintf("- this service → %s (port %d)", name, conn.Port))
				if conn.BytesPerSec > 0 {
					sb.WriteString(fmt.Sprintf(" [%.1f B/s]", conn.BytesPerSec))
				}
				sb.WriteString("\n")
			}
			if len(req.Context.OutgoingConnections) > maxConnections {
				sb.WriteString(fmt.Sprintf("- ... and %d more outgoing connections\n", len(req.Context.OutgoingConnections)-maxConnections))
			}
		}
		sb.WriteString("\n")
	}

	// HTTP endpoints
	if len(req.Context.HTTPEndpoints) > 0 || req.Context.ServerHeader != "" {
		sb.WriteString("## HTTP Information\n")
		if req.Context.ServerHeader != "" {
			sb.WriteString(fmt.Sprintf("- **Server**: %s\n", req.Context.ServerHeader))
		}
		if req.Context.HealthStatus > 0 {
			sb.WriteString(fmt.Sprintf("- **Health Status**: %d\n", req.Context.HealthStatus))
		}
		if len(req.Context.HTTPEndpoints) > 0 {
			sb.WriteString(fmt.Sprintf("- **Endpoints**: %s\n", strings.Join(req.Context.HTTPEndpoints, ", ")))
		}
		sb.WriteString("\n")
	}

	// Database info
	if req.Context.DBType != "" {
		sb.WriteString("## Database Information\n")
		sb.WriteString(fmt.Sprintf("- **Type**: %s\n", req.Context.DBType))
		if req.Context.DBVersion != "" {
			sb.WriteString(fmt.Sprintf("- **Version**: %s\n", req.Context.DBVersion))
		}
		if len(req.Context.DBDatabases) > 0 {
			sb.WriteString(fmt.Sprintf("- **Databases**: %s\n", strings.Join(req.Context.DBDatabases, ", ")))
		}
		sb.WriteString("\n")
	}

	// Dependencies
	if len(req.Context.Dependencies) > 0 {
		sb.WriteString("## Dependencies\n")
		for _, dep := range req.Context.Dependencies {
			sb.WriteString(fmt.Sprintf("- %s\n", dep))
		}
		sb.WriteString("\n")
	}

	// Config files
	if len(req.Context.ConfigFiles) > 0 {
		sb.WriteString("## Configuration Files\n")
		for _, file := range req.Context.ConfigFiles[:min(10, len(req.Context.ConfigFiles))] {
			sb.WriteString(fmt.Sprintf("- %s\n", file))
		}
		if len(req.Context.ConfigFiles) > 10 {
			sb.WriteString(fmt.Sprintf("- ... and %d more\n", len(req.Context.ConfigFiles)-10))
		}
		sb.WriteString("\n")
	}

	// Environment variables
	if len(req.Context.EnvVarNames) > 0 {
		sb.WriteString("## Environment Variables (names only)\n")
		for _, env := range req.Context.EnvVarNames[:min(15, len(req.Context.EnvVarNames))] {
			sb.WriteString(fmt.Sprintf("- %s\n", env))
		}
		if len(req.Context.EnvVarNames) > 15 {
			sb.WriteString(fmt.Sprintf("- ... and %d more\n", len(req.Context.EnvVarNames)-15))
		}
		sb.WriteString("\n")
	}

	// ============================================================
	// SOURCE CODE CONTEXT (Most valuable for understanding the app)
	// ============================================================

	hasCodeContext := req.Context.ProjectName != "" || req.Context.README != "" ||
		req.Context.EntryPointCode != "" || req.Context.Dockerfile != ""

	if hasCodeContext {
		sb.WriteString("## Source Code Analysis\n\n")

		// Project info
		if req.Context.ProjectName != "" {
			sb.WriteString(fmt.Sprintf("**Project Name**: %s\n", req.Context.ProjectName))
		}
		if req.Context.ProjectDesc != "" {
			sb.WriteString(fmt.Sprintf("**Description**: %s\n", req.Context.ProjectDesc))
		}
		sb.WriteString("\n")

		// README (most valuable!)
		if req.Context.README != "" {
			sb.WriteString("### README.md\n")
			sb.WriteString("```\n")
			// Truncate README to avoid token explosion
			readme := req.Context.README
			if len(readme) > 2000 {
				readme = readme[:2000] + "\n... (truncated)"
			}
			sb.WriteString(redactSecrets(readme))
			sb.WriteString("\n```\n\n")
		}

		// Entry point code. This is the file most likely to hold a hardcoded
		// credential, so it goes through the same redaction as the rest.
		if req.Context.EntryPointCode != "" {
			sb.WriteString(fmt.Sprintf("### Main Entry Point (%s)\n", req.Context.EntryPointFile))
			sb.WriteString("```\n")
			sb.WriteString(redactSecrets(req.Context.EntryPointCode))
			sb.WriteString("\n```\n\n")
		}

		// Dockerfile. ENV/ARG lines are a common place for a baked-in secret.
		if req.Context.Dockerfile != "" {
			sb.WriteString("### Dockerfile\n")
			sb.WriteString("```dockerfile\n")
			sb.WriteString(redactSecrets(req.Context.Dockerfile))
			sb.WriteString("\n```\n\n")
		}
	}

	return sb.String()
}

func (g *DocsGenerator) buildQuestionPrompt(req DocumentationRequest) string {
	var sb strings.Builder

	sb.WriteString("Based on this service context, answer the following question:\n\n")
	sb.WriteString(g.buildPrompt(req))
	sb.WriteString("\n---\n\n")
	sb.WriteString(fmt.Sprintf("**Question**: %s\n", req.Question))

	return sb.String()
}

const systemPrompt = `You are an expert DevOps and software architect. Your task is to analyze service information and source code from a live infrastructure monitoring system.

**IMPORTANT**: If source code (README, entry point, Dockerfile) is provided, USE IT as the primary source of truth. The code tells you exactly what this service is - analyze it thoroughly.

Generate DETAILED documentation with these sections:

## What This Service Does
Explain the exact purpose and functionality. Be specific:
- What problem does it solve?
- How does it work? (based on the code you see)
- What are its main features/capabilities?

## Technical Stack
Comprehensive list:
- Programming language and version
- Frameworks and libraries (from imports, package manifests)
- Build tools and runtime (from Dockerfile)
- External services it depends on

## Architecture & Data Flow
- How does this service fit into the system?
- What data does it process?
- Where does data come from and go to?
- What protocols/APIs does it use?

## Code Analysis
Based on the actual source code (line numbers are shown as "LINE | code"):
- Key files and their purposes
- Important functions/classes with LINE NUMBERS (e.g. "func main() at Lines 45-120")
- Data structures used with their location
- Configuration patterns

IMPORTANT: When referencing code, include the line numbers so users can find it easily.

## Network Behavior
Based on the connection data:
- What services does it communicate with?
- Incoming vs outgoing traffic patterns
- Port usage and protocols

## Security Considerations
- Authentication/authorization patterns
- Exposed ports and endpoints
- Sensitive data handling
- Potential vulnerabilities

## Performance & Reliability
- Resource usage patterns
- Scaling considerations  
- Error handling approaches
- Monitoring points

## Recommendations
Prioritized, actionable improvements:
1. Security improvements
2. Performance optimizations
3. Reliability enhancements
4. Documentation gaps to fill

Format in Markdown with emoji icons for each section header:
- 🎯 What This Service Does
- 🛠️ Technical Stack
- 🏗️ Architecture & Data Flow
- 💻 Code Analysis
- 🌐 Network Behavior
- 🛡️ Security Considerations
- ⚡ Performance & Reliability
- 📋 Recommendations

**CRITICAL**: The source code includes line numbers (format: "LINE | code"). 
When referencing functions, classes, or important code, ALWAYS include the line numbers.
Example: "The main() function (Lines 45-120) initializes the server..."

Be thorough and technical - this documentation is for engineers.`

const questionSystemPrompt = `You are an expert DevOps and infrastructure analyst helping users understand their infrastructure. 

Answer questions accurately based on the service context provided. If you cannot determine something from the available data, say so clearly. Be concise and direct in your answers.`

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
