BINARY_DIR := bin
AGENT_BINARY := $(BINARY_DIR)/kno-agent
CLI_BINARY := $(BINARY_DIR)/kno
IMAGE ?= kubenode-observer/kno-agent:dev
WORKLOAD_IMAGE ?= kubenode-observer/exec-test:dev
CPU_HOG_IMAGE ?= kubenode-observer/cpu-hog:dev
CONTENTION_IMAGE ?= kubenode-observer/cpu-contention:dev
BENCHMARK_IMAGE ?= kubenode-observer/observer-benchmark:dev
COST_AGENT_IMAGE ?= kubenode-observer/kno-agent:cost
METRICS_AGENT_IMAGE ?= kubenode-observer/kno-agent:metrics-cache
MEMORY_AGENT_IMAGE ?= kubenode-observer/kno-agent:memory
KATA_AGENT_IMAGE ?= kubenode-observer/kno-agent:kata-host
VMM_AGENT_IMAGE ?= kubenode-observer/kno-agent:kata-vmm
GUEST_IMAGE ?= kubenode-observer/guest-probe:dev
GUEST_AGENT_IMAGE ?= kubenode-observer/kno-agent:guest
CLOCK_AGENT_IMAGE ?= kubenode-observer/kno-agent:clock
CLOCK_GUEST_IMAGE ?= kubenode-observer/guest-probe:clock
MINIKUBE_PROFILE ?= kno-dev
GO_BUILD_ENV ?= CGO_ENABLED=0
GO_BUILD_FLAGS ?= -trimpath -ldflags="-s -w"

.PHONY: generate build test fmt check image image-local workload-image cpu-hog-image contention-image benchmark-image experiment-build smoke cpu-smoke runqlat-smoke benchmark accuracy experiment cost metrics-compare memory stability integration install-minikube uninstall-minikube demo deploy undeploy

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

# Baseline must already be built/preloaded from the pre-optimization revision.
metrics-compare: benchmark-image experiment-build
	$(MAKE) image-local IMAGE=$(METRICS_AGENT_IMAGE)
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true $(BENCHMARK_IMAGE)
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true $(METRICS_AGENT_IMAGE)
	./bin/kno-experiment --mode metrics --context $(MINIKUBE_PROFILE) --image $(BENCHMARK_IMAGE) --baseline-agent-image $(COST_AGENT_IMAGE) --candidate-agent-image $(METRICS_AGENT_IMAGE) --duration=30s $(EXPERIMENT_FLAGS)

memory: benchmark-image experiment-build
	$(MAKE) image-local IMAGE=$(MEMORY_AGENT_IMAGE)
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true $(BENCHMARK_IMAGE)
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true $(MEMORY_AGENT_IMAGE)
	./bin/kno-experiment --mode memory --context $(MINIKUBE_PROFILE) --image $(BENCHMARK_IMAGE) --memory-agent-image $(MEMORY_AGENT_IMAGE) --duration=1m $(EXPERIMENT_FLAGS)

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

.PHONY: kata-check kata-install kata-test m8-test
kata-check:
	KUBE_CONTEXT=$(MINIKUBE_PROFILE) bash hack/kata-preflight.sh

# Requires the verified official archive; does not pull runtime images.
kata-install:
	KUBE_CONTEXT=$(MINIKUBE_PROFILE) bash hack/install-kata-minikube.sh

kata-test: workload-image cpu-hog-image
	$(MAKE) image-local IMAGE=$(KATA_AGENT_IMAGE)
	docker build -f examples/kata-boundary/Dockerfile.local -t kubenode-observer/kata-boundary:dev .
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true $(KATA_AGENT_IMAGE) kubenode-observer/kata-boundary:dev $(CPU_HOG_IMAGE)
	KUBE_CONTEXT=$(MINIKUBE_PROFILE) AGENT_IMAGE=$(KATA_AGENT_IMAGE) bash hack/kata-test.sh

# Requires an installed Kata runtime; pins only the temporary test processes.
m8-test: workload-image cpu-hog-image
	$(MAKE) image-local IMAGE=$(VMM_AGENT_IMAGE)
	docker build -f examples/kata-boundary/Dockerfile.local -t kubenode-observer/kata-boundary:dev .
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true $(VMM_AGENT_IMAGE) kubenode-observer/kata-boundary:dev $(CPU_HOG_IMAGE)
	KUBE_CONTEXT=$(MINIKUBE_PROFILE) AGENT_IMAGE=$(VMM_AGENT_IMAGE) KATA_M8_TEST=true bash hack/kata-test.sh

.PHONY: guest-image guest-install guest-check guest-kernel-check m9-test
guest-image:
	$(GO_BUILD_ENV) go build $(GO_BUILD_FLAGS) -o $(BINARY_DIR)/kno-guest ./cmd/kno-guest
	docker build -f examples/guest-probe/Dockerfile.local -t $(GUEST_IMAGE) .

guest-install:
	KUBE_CONTEXT=$(MINIKUBE_PROFILE) bash hack/install-kata-guest.sh

guest-check: guest-image
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true $(GUEST_IMAGE)
	KUBE_CONTEXT=$(MINIKUBE_PROFILE) bash hack/guest-check.sh

guest-kernel-check:
	CGO_ENABLED=0 go test -c -o $(BINARY_DIR)/kno-probe.test ./internal/probe
	$(CC) -nostdlib -static -o $(BINARY_DIR)/kno-kernel-child examples/guest-probe/kernel-child.S
	docker build -f examples/guest-probe/Dockerfile.kernel-test -t kubenode-observer/guest-kernel-test:dev .
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true kubenode-observer/guest-kernel-test:dev
	KUBE_CONTEXT=$(MINIKUBE_PROFILE) GUEST_CHECK_MODE=kernel bash hack/guest-check.sh

m9-test: guest-image workload-image contention-image
	$(MAKE) image-local IMAGE=$(GUEST_AGENT_IMAGE)
	docker build -f examples/guest-probe/Dockerfile.workload -t kubenode-observer/guest-workload:dev .
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true $(GUEST_IMAGE) $(GUEST_AGENT_IMAGE) kubenode-observer/guest-workload:dev
	KUBE_CONTEXT=$(MINIKUBE_PROFILE) AGENT_IMAGE=$(GUEST_AGENT_IMAGE) bash hack/guest-test.sh

.PHONY: m10-clock-test
m10-clock-test: workload-image contention-image
	$(MAKE) guest-image GUEST_IMAGE=$(CLOCK_GUEST_IMAGE)
	$(MAKE) image-local IMAGE=$(CLOCK_AGENT_IMAGE)
	docker build -f examples/guest-probe/Dockerfile.workload -t kubenode-observer/guest-workload:dev .
	minikube -p $(MINIKUBE_PROFILE) image load --overwrite=true $(CLOCK_GUEST_IMAGE) $(CLOCK_AGENT_IMAGE) kubenode-observer/guest-workload:dev
	KUBE_CONTEXT=$(MINIKUBE_PROFILE) CLOCK_AGENT_IMAGE=$(CLOCK_AGENT_IMAGE) CLOCK_GUEST_IMAGE=$(CLOCK_GUEST_IMAGE) bash hack/clock-test.sh


