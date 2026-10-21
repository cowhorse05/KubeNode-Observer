BINARY_DIR := bin
AGENT_BINARY := $(BINARY_DIR)/kno-agent
CLI_BINARY := $(BINARY_DIR)/kno
IMAGE ?= kubenode-observer/kno-agent:dev
WORKLOAD_IMAGE ?= kubenode-observer/exec-test:dev
CPU_HOG_IMAGE ?= kubenode-observer/cpu-hog:dev
CONTENTION_IMAGE ?= kubenode-observer/cpu-contention:dev
BENCHMARK_IMAGE ?= kubenode-observer/observer-benchmark:dev
COST_AGENT_IMAGE ?= kubenode-observer/kno-agent:cost
MINIKUBE_PROFILE ?= kno-dev
GO_BUILD_ENV ?= CGO_ENABLED=0
GO_BUILD_FLAGS ?= -trimpath -ldflags="-s -w"

.PHONY: generate build test fmt check image image-local workload-image cpu-hog-image contention-image benchmark-image experiment-build smoke cpu-smoke runqlat-smoke benchmark accuracy experiment cost stability integration install-minikube uninstall-minikube demo deploy undeploy

generate:
	go generate ./internal/bpfgen

build:
	$(GO_BUILD_ENV) go build $(GO_BUILD_FLAGS) -o $(AGENT_BINARY) ./cmd/kno-agent
	$(GO_BUILD_ENV) go build $(GO_BUILD_FLAGS) -o $(CLI_BINARY) ./cmd/kno

test:
	go test ./...

fmt:
	gofmt -w cmd internal examples/exec-test examples/cpu-hog examples/cpu-contention examples/observer-benchmark

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

benchmark-image:
	$(GO_BUILD_ENV) go build $(GO_BUILD_FLAGS) -o $(BINARY_DIR)/kno-observer-benchmark ./examples/observer-benchmark
	docker build -f examples/observer-benchmark/Dockerfile.local -t $(BENCHMARK_IMAGE) .

smoke: workload-image
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true $(WORKLOAD_IMAGE)
	bash hack/smoke-test.sh

cpu-smoke: build cpu-hog-image
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true $(CPU_HOG_IMAGE)
	bash hack/cpu-hotspot-test.sh

runqlat-smoke: build contention-image
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true $(CONTENTION_IMAGE)
	bash hack/runqlat-test.sh

experiment-build:
	$(GO_BUILD_ENV) go build $(GO_BUILD_FLAGS) -o $(BINARY_DIR)/kno-experiment ./cmd/kno-experiment

benchmark: benchmark-image experiment-build
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true $(BENCHMARK_IMAGE)
	./bin/kno-experiment --mode paired --context $(MINIKUBE_PROFILE) --image $(BENCHMARK_IMAGE) $(EXPERIMENT_FLAGS)

accuracy: benchmark-image experiment-build
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true $(BENCHMARK_IMAGE)
	./bin/kno-experiment --mode accuracy --context $(MINIKUBE_PROFILE) --image $(BENCHMARK_IMAGE) $(EXPERIMENT_FLAGS)

experiment: benchmark-image experiment-build
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true $(BENCHMARK_IMAGE)
	./bin/kno-experiment --mode all --context $(MINIKUBE_PROFILE) --image $(BENCHMARK_IMAGE) $(EXPERIMENT_FLAGS)

cost: benchmark-image experiment-build
	$(MAKE) image-local IMAGE=$(COST_AGENT_IMAGE)
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true $(BENCHMARK_IMAGE)
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true $(COST_AGENT_IMAGE)
	./bin/kno-experiment --mode cost --context $(MINIKUBE_PROFILE) --image $(BENCHMARK_IMAGE) --cost-agent-image $(COST_AGENT_IMAGE) $(EXPERIMENT_FLAGS)

stability: workload-image
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true $(WORKLOAD_IMAGE)
	bash hack/stability-test.sh

integration:
	bash hack/integration-test.sh

install-minikube:
	bash hack/install-minikube.sh

uninstall-minikube:
	bash hack/uninstall-minikube.sh

demo:
	bash hack/demo.sh

deploy:
	kubectl --context $(MINIKUBE_PROFILE) apply -f deploy/minikube/kno-agent.yaml

undeploy:
	kubectl --context $(MINIKUBE_PROFILE) delete -f deploy/minikube/kno-agent.yaml --ignore-not-found


