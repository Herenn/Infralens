// Package fingerprint provides service identification based on process names and ports.
package fingerprint

import "strings"

// ServiceType represents the abstract category of a service.
type ServiceType string

const (
	TypeDatabase     ServiceType = "database"
	TypeCache        ServiceType = "cache"
	TypeMessageQueue ServiceType = "message_queue"
	TypeWebServer    ServiceType = "web_server"
	TypeProxy        ServiceType = "proxy"
	TypeApp          ServiceType = "application"
	TypeMonitoring   ServiceType = "monitoring"
	TypeUnknown      ServiceType = "unknown"
)

// ServiceInfo contains the identified service information.
type ServiceInfo struct {
	Type ServiceType `json:"type"`
	Tech string      `json:"tech"` // Specific technology name (e.g., "PostgreSQL", "Redis")
	Icon string      `json:"icon"` // Icon identifier for frontend
}

// portMap maps well-known ports to service info
var portMap = map[uint16]ServiceInfo{
	// Databases
	5432:  {Type: TypeDatabase, Tech: "PostgreSQL", Icon: "postgresql"},
	3306:  {Type: TypeDatabase, Tech: "MySQL", Icon: "mysql"},
	3307:  {Type: TypeDatabase, Tech: "MySQL", Icon: "mysql"},
	27017: {Type: TypeDatabase, Tech: "MongoDB", Icon: "mongodb"},
	27018: {Type: TypeDatabase, Tech: "MongoDB", Icon: "mongodb"},
	1433:  {Type: TypeDatabase, Tech: "SQL Server", Icon: "sqlserver"},
	1521:  {Type: TypeDatabase, Tech: "Oracle", Icon: "oracle"},
	9042:  {Type: TypeDatabase, Tech: "Cassandra", Icon: "cassandra"},
	7000:  {Type: TypeDatabase, Tech: "Cassandra", Icon: "cassandra"},
	5984:  {Type: TypeDatabase, Tech: "CouchDB", Icon: "couchdb"},
	8529:  {Type: TypeDatabase, Tech: "ArangoDB", Icon: "arangodb"},
	26257: {Type: TypeDatabase, Tech: "CockroachDB", Icon: "cockroachdb"},

	// Caches
	6379: {Type: TypeCache, Tech: "Redis", Icon: "redis"},
	6380: {Type: TypeCache, Tech: "Redis", Icon: "redis"},
	11211: {Type: TypeCache, Tech: "Memcached", Icon: "memcached"},

	// Message Queues
	9092:  {Type: TypeMessageQueue, Tech: "Kafka", Icon: "kafka"},
	5672:  {Type: TypeMessageQueue, Tech: "RabbitMQ", Icon: "rabbitmq"},
	15672: {Type: TypeMessageQueue, Tech: "RabbitMQ", Icon: "rabbitmq"},
	4222:  {Type: TypeMessageQueue, Tech: "NATS", Icon: "nats"},
	6222:  {Type: TypeMessageQueue, Tech: "NATS", Icon: "nats"},
	61616: {Type: TypeMessageQueue, Tech: "ActiveMQ", Icon: "activemq"},

	// Web Servers / HTTP
	80:   {Type: TypeWebServer, Tech: "HTTP", Icon: "http"},
	443:  {Type: TypeWebServer, Tech: "HTTPS", Icon: "https"},
	8080: {Type: TypeWebServer, Tech: "HTTP", Icon: "http"},
	8443: {Type: TypeWebServer, Tech: "HTTPS", Icon: "https"},
	3000: {Type: TypeApp, Tech: "Web App", Icon: "webapp"},
	5000: {Type: TypeApp, Tech: "Web App", Icon: "webapp"},
	8000: {Type: TypeApp, Tech: "Web App", Icon: "webapp"},

	// Proxies / Load Balancers
	8001: {Type: TypeProxy, Tech: "Kong", Icon: "kong"},
	8444: {Type: TypeProxy, Tech: "Kong", Icon: "kong"},

	// Monitoring
	9090: {Type: TypeMonitoring, Tech: "Prometheus", Icon: "prometheus"},
	3100: {Type: TypeMonitoring, Tech: "Loki", Icon: "loki"},
	9200: {Type: TypeMonitoring, Tech: "Elasticsearch", Icon: "elasticsearch"},
	9300: {Type: TypeMonitoring, Tech: "Elasticsearch", Icon: "elasticsearch"},
	5601: {Type: TypeMonitoring, Tech: "Kibana", Icon: "kibana"},
	16686: {Type: TypeMonitoring, Tech: "Jaeger", Icon: "jaeger"},
	9411: {Type: TypeMonitoring, Tech: "Zipkin", Icon: "zipkin"},
	4317: {Type: TypeMonitoring, Tech: "OpenTelemetry", Icon: "otel"},

	// Other common services
	22:   {Type: TypeApp, Tech: "SSH", Icon: "ssh"},
	53:   {Type: TypeApp, Tech: "DNS", Icon: "dns"},
	2379: {Type: TypeDatabase, Tech: "etcd", Icon: "etcd"},
	2380: {Type: TypeDatabase, Tech: "etcd", Icon: "etcd"},
	8500: {Type: TypeApp, Tech: "Consul", Icon: "consul"},
	8300: {Type: TypeApp, Tech: "Consul", Icon: "consul"},
}

// commMap maps process names (comm) to service info
var commMap = map[string]ServiceInfo{
	// Databases
	"postgres":       {Type: TypeDatabase, Tech: "PostgreSQL", Icon: "postgresql"},
	"mysqld":         {Type: TypeDatabase, Tech: "MySQL", Icon: "mysql"},
	"mariadbd":       {Type: TypeDatabase, Tech: "MariaDB", Icon: "mariadb"},
	"mongod":         {Type: TypeDatabase, Tech: "MongoDB", Icon: "mongodb"},
	"mongos":         {Type: TypeDatabase, Tech: "MongoDB", Icon: "mongodb"},
	"sqlservr":       {Type: TypeDatabase, Tech: "SQL Server", Icon: "sqlserver"},
	"cassandra":      {Type: TypeDatabase, Tech: "Cassandra", Icon: "cassandra"},
	"cockroach":      {Type: TypeDatabase, Tech: "CockroachDB", Icon: "cockroachdb"},

	// Caches
	"redis-server":   {Type: TypeCache, Tech: "Redis", Icon: "redis"},
	"redis":          {Type: TypeCache, Tech: "Redis", Icon: "redis"},
	"memcached":      {Type: TypeCache, Tech: "Memcached", Icon: "memcached"},
	"dragonfly":      {Type: TypeCache, Tech: "Dragonfly", Icon: "dragonfly"},

	// Message Queues
	"kafka":          {Type: TypeMessageQueue, Tech: "Kafka", Icon: "kafka"},
	"rabbitmq":       {Type: TypeMessageQueue, Tech: "RabbitMQ", Icon: "rabbitmq"},
	"beam.smp":       {Type: TypeMessageQueue, Tech: "RabbitMQ", Icon: "rabbitmq"}, // Erlang runtime
	"nats-server":    {Type: TypeMessageQueue, Tech: "NATS", Icon: "nats"},

	// Web Servers
	"nginx":          {Type: TypeWebServer, Tech: "Nginx", Icon: "nginx"},
	"apache2":        {Type: TypeWebServer, Tech: "Apache", Icon: "apache"},
	"httpd":          {Type: TypeWebServer, Tech: "Apache", Icon: "apache"},
	"caddy":          {Type: TypeWebServer, Tech: "Caddy", Icon: "caddy"},
	"traefik":        {Type: TypeProxy, Tech: "Traefik", Icon: "traefik"},
	"envoy":          {Type: TypeProxy, Tech: "Envoy", Icon: "envoy"},
	"haproxy":        {Type: TypeProxy, Tech: "HAProxy", Icon: "haproxy"},

	// Application Runtimes
	"node":           {Type: TypeApp, Tech: "Node.js", Icon: "nodejs"},
	"npm":            {Type: TypeApp, Tech: "Node.js", Icon: "nodejs"},
	"deno":           {Type: TypeApp, Tech: "Deno", Icon: "deno"},
	"bun":            {Type: TypeApp, Tech: "Bun", Icon: "bun"},
	"python":         {Type: TypeApp, Tech: "Python", Icon: "python"},
	"python3":        {Type: TypeApp, Tech: "Python", Icon: "python"},
	"gunicorn":       {Type: TypeApp, Tech: "Python/Gunicorn", Icon: "python"},
	"uvicorn":        {Type: TypeApp, Tech: "Python/Uvicorn", Icon: "python"},
	"java":           {Type: TypeApp, Tech: "Java", Icon: "java"},
	"ruby":           {Type: TypeApp, Tech: "Ruby", Icon: "ruby"},
	"rails":          {Type: TypeApp, Tech: "Ruby on Rails", Icon: "rails"},
	"php":            {Type: TypeApp, Tech: "PHP", Icon: "php"},
	"php-fpm":        {Type: TypeApp, Tech: "PHP-FPM", Icon: "php"},
	"dotnet":         {Type: TypeApp, Tech: ".NET", Icon: "dotnet"},
	"go":             {Type: TypeApp, Tech: "Go", Icon: "go"},

	// Monitoring
	"prometheus":     {Type: TypeMonitoring, Tech: "Prometheus", Icon: "prometheus"},
	"grafana":        {Type: TypeMonitoring, Tech: "Grafana", Icon: "grafana"},
	"grafana-server": {Type: TypeMonitoring, Tech: "Grafana", Icon: "grafana"},
	"alertmanager":   {Type: TypeMonitoring, Tech: "Alertmanager", Icon: "alertmanager"},
	"loki":           {Type: TypeMonitoring, Tech: "Loki", Icon: "loki"},

	// Container/Orchestration
	"dockerd":        {Type: TypeApp, Tech: "Docker", Icon: "docker"},
	"containerd":     {Type: TypeApp, Tech: "containerd", Icon: "containerd"},
	"kubelet":        {Type: TypeApp, Tech: "Kubernetes", Icon: "kubernetes"},
	"kube-proxy":     {Type: TypeApp, Tech: "Kubernetes", Icon: "kubernetes"},
	"etcd":           {Type: TypeDatabase, Tech: "etcd", Icon: "etcd"},

	// Common tools
	"curl":           {Type: TypeApp, Tech: "cURL", Icon: "curl"},
	"wget":           {Type: TypeApp, Tech: "wget", Icon: "wget"},
	
	// InfraLens specific
	"infralens-agent": {Type: TypeMonitoring, Tech: "InfraLens Agent", Icon: "infralens"},
	"infralens":       {Type: TypeMonitoring, Tech: "InfraLens", Icon: "infralens"},
	"backend":         {Type: TypeApp, Tech: "Go Backend", Icon: "go"},
	
	// Frontend dev servers
	"vite":            {Type: TypeApp, Tech: "Vite", Icon: "vite"},
	"webpack":         {Type: TypeApp, Tech: "Webpack", Icon: "webpack"},
	"next":            {Type: TypeApp, Tech: "Next.js", Icon: "nextjs"},
	"nuxt":            {Type: TypeApp, Tech: "Nuxt.js", Icon: "nuxtjs"},
	
	// System services (to show proper names)
	"sshd":            {Type: TypeApp, Tech: "SSH Server", Icon: "ssh"},
	"systemd":         {Type: TypeApp, Tech: "systemd", Icon: "linux"},
}

// Identify determines the service type and technology based on process name and port.
// It prioritizes process name matching over port matching for more accurate identification.
func Identify(comm string, port uint16) ServiceInfo {
	// Normalize process name (lowercase, trim)
	commLower := strings.ToLower(strings.TrimSpace(comm))

	// First, try exact match on process name
	if info, ok := commMap[commLower]; ok {
		return info
	}

	// Try prefix matching for common patterns
	for prefix, info := range commMap {
		if strings.HasPrefix(commLower, prefix) {
			return info
		}
	}

	// Try substring matching for complex process names
	for key, info := range commMap {
		if strings.Contains(commLower, key) {
			return info
		}
	}

	// Fall back to port-based identification
	if info, ok := portMap[port]; ok {
		return info
	}

	// Unknown service
	return ServiceInfo{
		Type: TypeUnknown,
		Tech: comm, // Use original comm as tech name
		Icon: "unknown",
	}
}

// IdentifyByPort identifies a service based only on port number.
// Useful for destination services where we don't have the process name.
func IdentifyByPort(port uint16) ServiceInfo {
	if info, ok := portMap[port]; ok {
		return info
	}
	return ServiceInfo{
		Type: TypeUnknown,
		Tech: "Unknown",
		Icon: "unknown",
	}
}

// GetTypeColor returns a suggested color for each service type (for frontend use).
func GetTypeColor(t ServiceType) string {
	switch t {
	case TypeDatabase:
		return "#3b82f6" // blue
	case TypeCache:
		return "#ef4444" // red
	case TypeMessageQueue:
		return "#f59e0b" // amber
	case TypeWebServer:
		return "#22c55e" // green
	case TypeProxy:
		return "#8b5cf6" // purple
	case TypeApp:
		return "#06b6d4" // cyan
	case TypeMonitoring:
		return "#ec4899" // pink
	default:
		return "#6b7280" // gray
	}
}
