#!/bin/bash
#
# InfraLens Uninstall Script
# ==========================
# Removes all InfraLens components
#

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() { echo -e "${YELLOW}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[DONE]${NC} $1"; }

if [ "$EUID" -ne 0 ]; then
    echo -e "${RED}Please run as root (sudo)${NC}"
    exit 1
fi

echo ""
echo "This will remove InfraLens completely."
read -p "Are you sure? (y/N): " confirm
if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
    echo "Cancelled."
    exit 0
fi

echo ""

# Stop services
log_info "Stopping services..."
systemctl stop infralens-agent 2>/dev/null || true
systemctl stop infralens-backend 2>/dev/null || true
systemctl disable infralens-agent 2>/dev/null || true
systemctl disable infralens-backend 2>/dev/null || true
log_success "Services stopped"

# Remove systemd files
log_info "Removing systemd services..."
rm -f /etc/systemd/system/infralens-agent.service
rm -f /etc/systemd/system/infralens-backend.service
systemctl daemon-reload
log_success "Systemd services removed"

# Remove binaries
log_info "Removing binaries..."
rm -f /usr/local/bin/infralens-agent
rm -f /usr/local/bin/infralens-backend
log_success "Binaries removed"

# Remove frontend
log_info "Removing frontend..."
rm -rf /var/www/infralens
rm -f /etc/nginx/sites-enabled/infralens
rm -f /etc/nginx/sites-available/infralens
systemctl restart nginx 2>/dev/null || true
log_success "Frontend removed"

# Remove source
log_info "Removing source files..."
rm -rf /opt/infralens
log_success "Source files removed"

echo ""
echo -e "${GREEN}InfraLens has been completely removed.${NC}"
echo ""
