COMPOSE ?= docker compose
KIND_CLUSTER ?= jungle-local

.PHONY: help up infra-up cluster-up down cluster-down status logs migrate-up migrate-down test test-race coverage vet fmt

help:
	@echo "up            Build/start local dependencies and provision the kind workloads"
	@echo "infra-up      Start PostgreSQL, Redis, Keycloak and MiniStack with Compose"
	@echo "cluster-up    Run the one-shot kind bootstrap service"
	@echo "down          Stop Compose services (does not delete the kind cluster)"
	@echo "cluster-down  Delete the local kind cluster and its workloads"
	@echo "status        Show Compose and Kubernetes status"
	@echo "logs          Follow Compose service logs"
	@echo "migrate-up    Apply the Kubernetes database migration Job"
	@echo "migrate-down  Apply the documented migration rollback Job"
	@echo "test          Run Go unit/integration tests"
	@echo "test-race     Run Go tests with the race detector"
	@echo "coverage      Generate and print Go coverage summary"
	@echo "vet           Run go vet"
	@echo "fmt           Format Go source files"

up: infra-up
	$(COMPOSE) run --rm kind-bootstrap

infra-up:
	$(COMPOSE) up -d --build postgres redis keycloak ministack

cluster-up: infra-up
	$(COMPOSE) run --rm kind-bootstrap

down:
	$(COMPOSE) down

cluster-down:
	kind delete cluster --name $(KIND_CLUSTER)

status:
	$(COMPOSE) ps
	kubectl get pods -A

logs:
	$(COMPOSE) logs -f

migrate-up:
	kubectl -n jungle delete job database-migrations --ignore-not-found --wait=true
	kubectl apply -f deploy/kubernetes/jobs/migrations.yaml
	kubectl wait --for=condition=complete job/database-migrations --timeout=180s

migrate-down:
	kubectl -n jungle delete job database-migrations-down --ignore-not-found --wait=true
	kubectl apply -f deploy/kubernetes/jobs/migrations-down.yaml
	kubectl wait --for=condition=complete job/database-migrations-down --timeout=180s

test:
	go test ./...

test-race:
	go test -race ./...

coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

vet:
	go vet ./...

fmt:
	gofmt -w ./cmd ./internal
