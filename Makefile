# InfraLens Makefile

.PHONY: all build clean test lint generate docker-build docker-push deploy

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
GOGENERATE=$(GOCMD) generate

# Binary names
AGENT_BINARY=infralens-agent
BACKEND_BINARY=infralens-backend

# Docker
DOCKER_REGISTRY?=infralens
DOCKER_TAG?=latest

# Build directories
BUILD_DIR=build
AGENT_DIR=agent
BACKEND_DIR=backend
FRONTEND_DIR=frontend

all: generate build

## Generate eBPF code
generate:
	@echo "Generating eBPF code..."
	cd $(AGENT_DIR) && $(GOGENERATE) ./...

## Build all binaries
build: build-agent build-backend

build-agent:
	@echo "Building agent..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 $(GOBUILD) -o $(BUILD_DIR)/$(AGENT_BINARY) ./$(AGENT_DIR)

build-backend:
	@echo "Building backend..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 $(GOBUILD) -o $(BUILD_DIR)/$(BACKEND_BINARY) ./$(BACKEND_DIR)

build-frontend:
	@echo "Building frontend..."
	cd $(FRONTEND_DIR) && npm ci && npm run build

## Run tests
test:
	$(GOTEST) -v -race ./...

## Run linter
lint:
	golangci-lint run ./...

## Clean build artifacts
clean:
	rm -rf $(BUILD_DIR)
	rm -f $(AGENT_DIR)/ebpf/bpf_*.go
	rm -f $(AGENT_DIR)/ebpf/bpf_*.o
	cd $(FRONTEND_DIR) && rm -rf dist node_modules

## Download dependencies
deps:
	$(GOMOD) download
	$(GOMOD) tidy
	cd $(FRONTEND_DIR) && npm install

## Docker build
docker-build: docker-build-agent docker-build-backend docker-build-frontend

docker-build-agent:
	docker build -t $(DOCKER_REGISTRY)/agent:$(DOCKER_TAG) -f docker/Dockerfile.agent .

docker-build-backend:
	docker build -t $(DOCKER_REGISTRY)/backend:$(DOCKER_TAG) -f docker/Dockerfile.backend .

docker-build-frontend:
	docker build -t $(DOCKER_REGISTRY)/frontend:$(DOCKER_TAG) -f docker/Dockerfile.frontend .

## Docker push
docker-push:
	docker push $(DOCKER_REGISTRY)/agent:$(DOCKER_TAG)
	docker push $(DOCKER_REGISTRY)/backend:$(DOCKER_TAG)
	docker push $(DOCKER_REGISTRY)/frontend:$(DOCKER_TAG)

## Deploy to Kubernetes
deploy:
	kubectl apply -k deploy/k8s/

undeploy:
	kubectl delete -k deploy/k8s/

## Development helpers
dev-backend:
	$(GOCMD) run ./$(BACKEND_DIR) --debug

dev-frontend:
	cd $(FRONTEND_DIR) && npm run dev

## Help
help:
	@echo "InfraLens Build System"
	@echo ""
	@echo "Usage:"
	@echo "  make [target]"
	@echo ""
	@echo "Targets:"
	@echo "  all            Build everything (default)"
	@echo "  generate       Generate eBPF code from C sources"
	@echo "  build          Build all Go binaries"
	@echo "  build-agent    Build the agent binary"
	@echo "  build-backend  Build the backend binary"
	@echo "  build-frontend Build the frontend"
	@echo "  test           Run tests"
	@echo "  lint           Run linter"
	@echo "  clean          Clean build artifacts"
	@echo "  deps           Download dependencies"
	@echo "  docker-build   Build Docker images"
	@echo "  docker-push    Push Docker images"
	@echo "  deploy         Deploy to Kubernetes"
	@echo "  undeploy       Remove from Kubernetes"
	@echo "  dev-backend    Run backend in dev mode"
	@echo "  dev-frontend   Run frontend in dev mode"
	@echo "  help           Show this help"
