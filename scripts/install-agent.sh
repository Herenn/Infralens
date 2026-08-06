#!/bin/bash
#
# InfraLens Agent-Only Installation Script
# ========================================
# Installs: Agent only (connects to remote backend)
# For: Additional servers to monitor
#
# Usage: curl -sSL https://raw.githubusercontent.com/Herenn/Infralens/main/scripts/install-agent.sh | sudo bash -s -- --backend=YOUR_BACKEND_IP:8080
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

# Default values
BACKEND_URL=""
NODE_NAME=$(hostname)
API_KEY=""

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --backend=*)
            BACKEND_URL="${1#*=}"
            shift
            ;;
        --node=*)
            NODE_NAME="${1#*=}"
            shift
            ;;
        --api-key=*)
            API_KEY="${1#*=}"
            shift
            ;;
        --help|-h)
            echo "InfraLens Agent Installation"
            echo ""
            echo "Usage:"
            echo "  sudo bash install-agent.sh --backend=IP:PORT [--node=NAME] [--api-key=KEY]"
            echo ""
            echo "Options:"
            echo "  --backend=IP:PORT   Backend server address or URL (required)"
            echo "                      Supports https:// URLs, e.g. https://infralens.example.com"
            echo "  --node=NAME         Node name (default: hostname)"
            echo "  --api-key=KEY       API key if the backend requires authentication"
            echo ""
            echo "Example:"
            echo "  sudo bash install-agent.sh --backend=192.168.1.100:8080 --api-key=secret"
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            exit 1
            ;;
    esac
done

# Check required arguments
if [ -z "$BACKEND_URL" ]; then
    log_error "Backend URL is required!"
    echo ""
    echo "Usage: sudo bash install-agent.sh --backend=IP:PORT"
    echo "Example: sudo bash install-agent.sh --backend=192.168.1.100:8080"
    exit 1
fi

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

log_info "Installing InfraLens Agent on $OS..."
log_info "Backend: $BACKEND_URL"
log_info "Node name: $NODE_NAME"

# ============================================
# Install Dependencies
# ============================================
log_info "Installing dependencies..."

case $OS in
    ubuntu|debian)
        apt-get update
        apt-get install -y curl wget git clang llvm libbpf-dev build-essential
        ;;
    centos|rhel|fedora)
        dnf install -y curl wget git clang llvm libbpf-devel gcc make
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
    
    # Pinned digests for go1.24.0, published at https://go.dev/dl/.
    # The tarball is extracted into /usr/local and everything below is built
    # with it, so an unverified download here compromises the whole install.
    case $GO_ARCH in
        amd64) GO_SHA256="dea9ca38a0b852a74e81c26134671af7c0fbe65d81b0dc1c5bfe22cf7d4c8858" ;;
        arm64) GO_SHA256="c3fa6d16ffa261091a5617145553c71d21435ce547e44cc6dfb7470865527cc7" ;;
    esac

    wget -q "https://go.dev/dl/go1.24.0.linux-${GO_ARCH}.tar.gz" -O /tmp/go.tar.gz
    echo "${GO_SHA256}  /tmp/go.tar.gz" | sha256sum -c - || {
        log_error "Go toolchain checksum mismatch - refusing to install"
        rm -f /tmp/go.tar.gz
        exit 1
    }
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/go.tar.gz
    rm /tmp/go.tar.gz
    
    export PATH=$PATH:/usr/local/go/bin
    echo 'export PATH=$PATH:/usr/local/go/bin' >> /etc/profile.d/go.sh
fi

log_success "Go $(go version | grep -oP '\d+\.\d+\.\d+')"

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
# Create Systemd Service
# ============================================
log_info "Creating systemd service..."

AGENT_ARGS="--backend=${BACKEND_URL} --node=${NODE_NAME} --inspect"

cat > /etc/systemd/system/infralens-agent.service << EOF
[Unit]
Description=InfraLens eBPF Agent
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/infralens-agent ${AGENT_ARGS}
$([ -n "$API_KEY" ] && echo "Environment=INFRALENS_API_KEY=${API_KEY}")
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
chmod 600 /etc/systemd/system/infralens-agent.service

# Reload and start service
systemctl daemon-reload
systemctl enable infralens-agent
systemctl start infralens-agent

log_success "Systemd service created and started"

# ============================================
# Test Connection
# ============================================
log_info "Testing connection to backend..."
sleep 3

if systemctl is-active --quiet infralens-agent; then
    log_success "Agent is running!"
else
    log_warn "Agent may have issues. Check logs: journalctl -u infralens-agent -f"
fi

# ============================================
# Final Status
# ============================================
echo ""
echo "============================================"
echo -e "${GREEN}InfraLens Agent Installation Complete!${NC}"
echo "============================================"
echo ""
echo "Service Status:"
systemctl status infralens-agent --no-pager -l | head -8
echo ""
echo "Configuration:"
echo "  Backend: $BACKEND_URL"
echo "  Node Name: $NODE_NAME"
echo ""
echo "Commands:"
echo "  View logs:     journalctl -u infralens-agent -f"
echo "  Restart:       systemctl restart infralens-agent"
echo "  Stop:          systemctl stop infralens-agent"
echo "  Reconfigure:   Edit /etc/systemd/system/infralens-agent.service"
echo ""
echo "This server should now appear in InfraLens dashboard!"
echo ""
