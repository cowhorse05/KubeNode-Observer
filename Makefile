BINARY_DIR := bin
AGENT_BINARY := $(BINARY_DIR)/kno-agent
CLI_BINARY := $(BINARY_DIR)/kno
IMAGE ?= kubenode-observer/kno-agent:dev
WORKLOAD_IMAGE ?= kubenode-observer/exec-test:dev
MINIKUBE_PROFILE ?= kno-dev
GO_BUILD_ENV ?= CGO_ENABLED=0
GO_BUILD_FLAGS ?= -trimpath -ldflags="-s -w"

.PHONY: generate build test fmt check image image-local workload-image smoke deploy undeploy

generate:
	go generate ./internal/bpfgen

build:
	$(GO_BUILD_ENV) go build $(GO_BUILD_FLAGS) -o $(AGENT_BINARY) ./cmd/kno-agent
	$(GO_BUILD_ENV) go build $(GO_BUILD_FLAGS) -o $(CLI_BINARY) ./cmd/kno

test:
	go test ./...

fmt:
	gofmt -w cmd internal examples/exec-test

check:
	bash hack/check-env.sh

image:
	docker build -t $(IMAGE) .

image-local: build
	docker build -f Dockerfile.local -t $(IMAGE) .

workload-image:
	$(GO_BUILD_ENV) go build $(GO_BUILD_FLAGS) -o $(BINARY_DIR)/kno-exec-test ./examples/exec-test
	docker build -f examples/exec-test/Dockerfile.local -t $(WORKLOAD_IMAGE) .

smoke: workload-image
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true $(WORKLOAD_IMAGE)
	bash hack/smoke-test.sh

deploy:
	kubectl apply -f deploy/minikube/kno-agent.yaml

undeploy:
	kubectl delete -f deploy/minikube/kno-agent.yaml --ignore-not-found


