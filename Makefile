CONTROLLER_GEN ?= $(shell which controller-gen 2>/dev/null || echo "go run sigs.k8s.io/controller-tools/cmd/controller-gen")
BINARY = bin/agent-access-controller
IMG ?= quay.io/azaalouk/agent-access-control:latest

.PHONY: all build generate manifests run test install clean docker-build docker-push deploy undeploy

all: generate manifests build

build:
	go build -o $(BINARY) ./cmd/main.go

generate:
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./api/..."

manifests:
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:artifacts:config=config/crd/bases

run: generate manifests
	go run ./cmd/main.go

test:
	go test ./... -v -coverprofile cover.out

install: manifests
	kubectl apply -f config/crd/bases/

uninstall:
	kubectl delete -f config/crd/bases/ --ignore-not-found

docker-build:
	docker build -t $(IMG) .

docker-push:
	docker push $(IMG)

deploy: install
	kubectl apply -f config/rbac/
	kubectl apply -f config/manager/manager.yaml

undeploy:
	kubectl delete -f config/manager/manager.yaml --ignore-not-found
	kubectl delete -f config/rbac/ --ignore-not-found

clean:
	rm -rf bin/ cover.out
