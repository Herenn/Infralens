#!/bin/bash
#
# InfraLens Full Installation Script
# ==================================
# Installs: Agent + Backend + Frontend
# For: Main monitoring server
#
# Usage: curl -sSL https://raw.githubusercontent.com/Herenn/Infralens/main/scripts/install-full.sh | sudo bash
#

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

# Check if running as root
if [ "$EUID" -ne 0 ]; then
    log_error "Please run as root (sudo)"
    exit 1
fi

# Detect OS
if [ -f /etc/os-release ]; then
    . /etc/os-release
    OS=$ID
else
    log_error "Cannot detect OS"
    exit 1
fi

log_info "Installing InfraLens (Full Stack) on $OS..."

# ============================================
# Install Dependencies
# ============================================
log_info "Installing dependencies..."

case $OS in
    ubuntu|debian)
        apt-get update
        apt-get install -y curl wget git clang llvm libbpf-dev build-essential nginx
        ;;
    centos|rhel|fedora)
        dnf install -y curl wget git clang llvm libbpf-devel gcc make nginx
        ;;
    *)
        log_error "Unsupported OS: $OS"
        exit 1
        ;;
esac

# ============================================
# Install Go 1.24
# ============================================
if ! command -v go &> /dev/null || [[ $(go version | grep -oP '\d+\.\d+' | head -1) < "1.24" ]]; then
    log_info "Installing Go 1.24..."
    
    ARCH=$(uname -m)
    case $ARCH in
        x86_64) GO_ARCH="amd64" ;;
        aarch64|arm64) GO_ARCH="arm64" ;;
        *) log_error "Unsupported architecture: $ARCH"; exit 1 ;;
    esac
    
    wget -q "https://go.dev/dl/go1.24.0.linux-${GO_ARCH}.tar.gz" -O /tmp/go.tar.gz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/go.tar.gz
    rm /tmp/go.tar.gz
    
    # Add to PATH for current session and permanently
    export PATH=$PATH:/usr/local/go/bin
    echo 'export PATH=$PATH:/usr/local/go/bin' >> /etc/profile.d/go.sh
fi

log_success "Go $(go version | grep -oP '\d+\.\d+\.\d+')"

# ============================================
# Install Node.js 20
# ============================================
if ! command -v node &> /dev/null; then
    log_info "Installing Node.js 20..."
    curl -fsSL https://deb.nodesource.com/setup_20.x | bash -
    apt-get install -y nodejs
fi

log_success "Node.js $(node --version)"

# ============================================
# Clone/Update Repository
# ============================================
INSTALL_DIR="/opt/infralens"

if [ -d "$INSTALL_DIR" ]; then
    log_info "Updating existing installation..."
    cd $INSTALL_DIR
    git pull origin main
else
    log_info "Cloning InfraLens..."
    git clone https://github.com/Herenn/Infralens.git $INSTALL_DIR
    cd $INSTALL_DIR
fi

# ============================================
# Build Agent
# ============================================
log_info "Building eBPF Agent..."

cd $INSTALL_DIR/agent

# Install bpf2go
go install github.com/cilium/ebpf/cmd/bpf2go@v0.12.3
export PATH=$PATH:$(go env GOPATH)/bin

# Generate eBPF bindings
cd collector
go generate ./...

# Build agent
cd ..
go build -o /usr/local/bin/infralens-agent .

log_success "Agent built: /usr/local/bin/infralens-agent"

# ============================================
# Build Backend
# ============================================
log_info "Building Backend..."

cd $INSTALL_DIR/backend
go build -o /usr/local/bin/infralens-backend .

log_success "Backend built: /usr/local/bin/infralens-backend"

# ============================================
# Build Frontend
# ============================================
log_info "Building Frontend..."

cd $INSTALL_DIR/frontend
npm ci
npm run build

# Copy to nginx directory
rm -rf /var/www/infralens
mkdir -p /var/www/infralens
cp -r dist/* /var/www/infralens/

log_success "Frontend built: /var/www/infralens"

# ============================================
# Configure Nginx
# ============================================
log_info "Configuring Nginx..."

cat > /etc/nginx/sites-available/infralens << 'EOF'
server {
    listen 3001;
    server_name _;

    root /var/www/infralens;
    index index.html;

    # Frontend
    location / {
        try_files $uri $uri/ /index.html;
    }

    # API Proxy
    location /api/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_read_timeout 86400;
    }
}
EOF

# Enable site
ln -sf /etc/nginx/sites-available/infralens /etc/nginx/sites-enabled/
rm -f /etc/nginx/sites-enabled/default 2>/dev/null || true

nginx -t && systemctl restart nginx
systemctl enable nginx

log_success "Nginx configured on port 3001"

# ============================================
# Create Systemd Services
# ============================================
log_info "Creating systemd services..."

# Backend service
cat > /etc/systemd/system/infralens-backend.service << EOF
[Unit]
Description=InfraLens Backend API Server
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/infralens-backend --debug
Restart=always
RestartSec=5
Environment=OPENAI_API_KEY=
Environment=ANTHROPIC_API_KEY=
Environment=GEMINI_API_KEY=

[Install]
WantedBy=multi-user.target
EOF

# Agent service
cat > /etc/systemd/system/infralens-agent.service << EOF
[Unit]
Description=InfraLens eBPF Agent
After=network.target infralens-backend.service

[Service]
Type=simple
ExecStart=/usr/local/bin/infralens-agent --backend=localhost:8080 --node=$(hostname) --inspect
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

# Reload and start services
systemctl daemon-reload
systemctl enable infralens-backend infralens-agent
systemctl start infralens-backend
sleep 2
systemctl start infralens-agent

log_success "Systemd services created and started"

# ============================================
# Final Status
# ============================================
echo ""
echo "============================================"
echo -e "${GREEN}InfraLens Installation Complete!${NC}"
echo "============================================"
echo ""
echo "Services:"
systemctl status infralens-backend --no-pager -l | head -5
echo ""
systemctl status infralens-agent --no-pager -l | head -5
echo ""
echo "Access InfraLens at:"
echo -e "  ${BLUE}http://$(hostname -I | awk '{print $1}'):3001${NC}"
echo ""
echo "Configure AI (optional):"
echo "  Edit /etc/systemd/system/infralens-backend.service"
echo "  Add your API keys, then: systemctl daemon-reload && systemctl restart infralens-backend"
echo ""
echo "Logs:"
echo "  journalctl -u infralens-backend -f"
echo "  journalctl -u infralens-agent -f"
echo ""
