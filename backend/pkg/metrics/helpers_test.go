package metrics

import "testing"

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		// Basic paths
		{name: "empty path", input: "", expected: "/"},
		{name: "root path", input: "/", expected: "/"},
		{name: "simple static path", input: "/health", expected: "/health"},
		{name: "api version path", input: "/api/v1", expected: "/api/v1"},

		// Static API paths
		{name: "topology endpoint", input: "/api/v1/topology", expected: "/api/v1/topology"},
		{name: "services list", input: "/api/v1/services", expected: "/api/v1/services"},
		{name: "graph stats", input: "/api/v1/graph/stats", expected: "/api/v1/graph/stats"},
		{name: "k8s status", input: "/api/v1/k8s/status", expected: "/api/v1/k8s/status"},
		{name: "websocket", input: "/api/v1/ws", expected: "/api/v1/ws"},

		// Numeric IDs
		{name: "numeric ID", input: "/api/v1/services/123", expected: "/api/v1/services/:id"},
		{name: "long numeric ID", input: "/api/v1/nodes/999999", expected: "/api/v1/nodes/:id"},

		// UUID paths
		{
			name:     "full UUID",
			input:    "/api/v1/services/550e8400-e29b-41d4-a716-446655440000",
			expected: "/api/v1/services/:uuid",
		},
		{
			name:     "UUID in middle",
			input:    "/api/v1/services/550e8400-e29b-41d4-a716-446655440000/stats",
			expected: "/api/v1/services/:uuid/stats",
		},

		// Short hash/ID patterns
		{name: "short hash", input: "/api/v1/services/a1b2c3d4", expected: "/api/v1/services/:hash"},
		{name: "longer hash", input: "/api/v1/services/deadbeef1234", expected: "/api/v1/services/:hash"},

		// Kubernetes-style names
		{
			name:     "k8s deployment pod",
			input:    "/api/v1/services/nginx-deployment-7d4f8b9c5d",
			expected: "/api/v1/services/nginx-deployment-:hash",
		},
		{
			name:     "k8s pod with hash",
			input:    "/api/v1/nodes/worker-node-x2k9p/stats",
			expected: "/api/v1/nodes/worker-node-:hash/stats",
		},

		// Slug with ID suffix
		{name: "service with numeric id", input: "/api/v1/services/svc-123", expected: "/api/v1/services/svc-:id"},
		{name: "node with name", input: "/api/v1/nodes/node-1/stats", expected: "/api/v1/nodes/node-:id/stats"},

		// IP addresses
		{name: "IP address", input: "/api/v1/nodes/192.168.1.100", expected: "/api/v1/nodes/:ip"},
		{name: "IP in path", input: "/api/v1/services/10.0.0.1/inspect", expected: "/api/v1/services/:ip/inspect"},

		// Mixed/complex paths
		{
			name:     "complex k8s path",
			input:    "/api/v1/services/frontend-deploy-abc12/connections",
			expected: "/api/v1/services/frontend-deploy-:hash/connections",
		},
		{
			name:     "dynamic looking ID",
			input:    "/api/v1/services/svc1a2b3c4d",
			expected: "/api/v1/services/:id",
		},

		// Edge cases
		{name: "trailing slash", input: "/api/v1/services/", expected: "/api/v1/services"},
		{name: "double slash", input: "/api//v1//services", expected: "/api/v1/services"},
		{name: "mixed case static", input: "/API/V1/Services", expected: "/API/V1/Services"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizePath(tt.input)
			if got != tt.expected {
				t.Errorf("NormalizePath(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestNormalizePath_CardinalityPrevention(t *testing.T) {
	// These are examples of high-cardinality paths that MUST be normalized
	// to prevent Prometheus memory exhaustion.
	// The key is that unique dynamic values normalize to the same pattern.
	testCases := []struct {
		paths         []string
		description   string
		maxUniquePaths int
	}{
		{
			description: "multiple UUIDs should normalize to same path",
			paths: []string{
				"/api/v1/services/550e8400-e29b-41d4-a716-446655440000",
				"/api/v1/services/660e8400-e29b-41d4-a716-446655440001",
				"/api/v1/services/770e8400-e29b-41d4-a716-446655440002",
			},
			maxUniquePaths: 1,
		},
		{
			description: "multiple numeric IDs should normalize to same path",
			paths: []string{
				"/api/v1/services/123",
				"/api/v1/services/456",
				"/api/v1/services/789",
			},
			maxUniquePaths: 1,
		},
		{
			description: "multiple IP addresses should normalize to same path",
			paths: []string{
				"/api/v1/nodes/10.0.0.1",
				"/api/v1/nodes/192.168.1.100",
				"/api/v1/nodes/172.16.0.50",
			},
			maxUniquePaths: 1,
		},
		{
			description: "k8s pod names with same prefix should normalize similarly",
			paths: []string{
				"/api/v1/services/nginx-abc12",
				"/api/v1/services/nginx-def34",
				"/api/v1/services/nginx-ghi56",
			},
			maxUniquePaths: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			seen := make(map[string]bool)
			for _, path := range tc.paths {
				normalized := NormalizePath(path)
				seen[normalized] = true
			}

			if len(seen) > tc.maxUniquePaths {
				t.Errorf("Paths normalized to %d unique values (expected <= %d): %v",
					len(seen), tc.maxUniquePaths, seen)
			}
		})
	}
}

func TestIsHexString(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"abc123", true},
		{"DEADBEEF", true},
		{"0123456789abcdef", true},
		{"ghijkl", false},
		{"abc-123", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isHexString(tt.input)
			if got != tt.expected {
				t.Errorf("isHexString(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestNormalizeSegment(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"123", ":id"},
		{"550e8400-e29b-41d4-a716-446655440000", ":uuid"},
		{"deadbeef", ":hash"},
		{"192.168.1.1", ":ip"},
		{"nginx-abc12", "nginx-:hash"},
		{"static", "static"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeSegment(tt.input)
			if got != tt.expected {
				t.Errorf("normalizeSegment(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func BenchmarkNormalizePath(b *testing.B) {
	paths := []string{
		"/api/v1/topology",
		"/api/v1/services/550e8400-e29b-41d4-a716-446655440000",
		"/api/v1/nodes/worker-node-x2k9p/stats",
		"/health",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, p := range paths {
			NormalizePath(p)
		}
	}
}
