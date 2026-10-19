BINARY_DIR := bin
AGENT_BINARY := $(BINARY_DIR)/kno-agent
CLI_BINARY := $(BINARY_DIR)/kno
IMAGE ?= kubenode-observer/kno-agent:dev
WORKLOAD_IMAGE ?= kubenode-observer/exec-test:dev
CPU_HOG_IMAGE ?= kubenode-observer/cpu-hog:dev
CONTENTION_IMAGE ?= kubenode-observer/cpu-contention:dev
MINIKUBE_PROFILE ?= kno-dev
GO_BUILD_ENV ?= CGO_ENABLED=0
GO_BUILD_FLAGS ?= -trimpath -ldflags="-s -w"

.PHONY: generate build test fmt check image image-local workload-image cpu-hog-image contention-image smoke cpu-smoke runqlat-smoke deploy undeploy

generate:
	go generate ./internal/bpfgen

build:
	$(GO_BUILD_ENV) go build $(GO_BUILD_FLAGS) -o $(AGENT_BINARY) ./cmd/kno-agent
	$(GO_BUILD_ENV) go build $(GO_BUILD_FLAGS) -o $(CLI_BINARY) ./cmd/kno

test:
	go test ./...

fmt:
	gofmt -w cmd internal examples/exec-test examples/cpu-hog examples/cpu-contention

check:
	bash hack/check-env.sh

image:
	docker build -t $(IMAGE) .

image-local: build
	docker build -f Dockerfile.local -t $(IMAGE) .

workload-image:
	$(GO_BUILD_ENV) go build $(GO_BUILD_FLAGS) -o $(BINARY_DIR)/kno-exec-test ./examples/exec-test
	docker build -f examples/exec-test/Dockerfile.local -t $(WORKLOAD_IMAGE) .

cpu-hog-image:
	$(GO_BUILD_ENV) go build $(GO_BUILD_FLAGS) -o $(BINARY_DIR)/kno-cpu-hog ./examples/cpu-hog
	docker build -f examples/cpu-hog/Dockerfile.local -t $(CPU_HOG_IMAGE) .

contention-image:
	$(GO_BUILD_ENV) go build $(GO_BUILD_FLAGS) -o $(BINARY_DIR)/kno-cpu-contention ./examples/cpu-contention
	docker build -f examples/cpu-contention/Dockerfile.local -t $(CONTENTION_IMAGE) .

smoke: workload-image
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true $(WORKLOAD_IMAGE)
	bash hack/smoke-test.sh

cpu-smoke: build cpu-hog-image
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true $(CPU_HOG_IMAGE)
	bash hack/cpu-hotspot-test.sh

runqlat-smoke: build contention-image
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true $(CONTENTION_IMAGE)
	bash hack/runqlat-test.sh

deploy:
	kubectl apply -f deploy/minikube/kno-agent.yaml

undeploy:
	kubectl delete -f deploy/minikube/kno-agent.yaml --ignore-not-found


