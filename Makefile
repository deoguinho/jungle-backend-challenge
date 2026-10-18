COMPOSE ?= docker compose
KIND_CLUSTER ?= jungle-local

.PHONY: help up infra-up cluster-up down cluster-down status logs migrate-up migrate-down test test-race test-integration smoke-local coverage vet fmt

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
	@echo "test-integration  Run PostgreSQL integration and concurrency tests (TEST_DATABASE_URL required)"
	@echo "smoke-local   Check OIDC, authorization, HTTP/SQS, reconciliation and metrics"
	@echo "coverage      Generate and print Go coverage summary"
	@echo "vet           Run go vet"
	@echo "fmt           Format Go source files"

up: infra-up
	$(COMPOSE) run --build --rm kind-bootstrap

infra-up:
	$(COMPOSE) up -d --build postgres redis keycloak ministack swagger-ui

cluster-up: infra-up
	$(COMPOSE) run --build --rm kind-bootstrap

down:
	$(COMPOSE) down

cluster-down:
	$(COMPOSE) run --rm --no-deps --entrypoint kind kind-bootstrap delete cluster --name $(KIND_CLUSTER)

status:
	$(COMPOSE) ps
	docker exec $(KIND_CLUSTER)-control-plane kubectl --kubeconfig=/etc/kubernetes/admin.conf get pods -A

logs:
	$(COMPOSE) logs -f

migrate-up:
	docker exec $(KIND_CLUSTER)-control-plane kubectl --kubeconfig=/etc/kubernetes/admin.conf -n jungle delete job database-migrations --ignore-not-found --wait=true
	cat deploy/kubernetes/jobs/migrations.yaml | docker exec -i $(KIND_CLUSTER)-control-plane kubectl --kubeconfig=/etc/kubernetes/admin.conf apply -f -
	docker exec $(KIND_CLUSTER)-control-plane kubectl --kubeconfig=/etc/kubernetes/admin.conf -n jungle wait --for=condition=complete job/database-migrations --timeout=180s

migrate-down:
	docker exec $(KIND_CLUSTER)-control-plane kubectl --kubeconfig=/etc/kubernetes/admin.conf -n jungle delete job database-migrations-down --ignore-not-found --wait=true
	cat deploy/kubernetes/jobs/migrations-down.yaml | docker exec -i $(KIND_CLUSTER)-control-plane kubectl --kubeconfig=/etc/kubernetes/admin.conf apply -f -
	docker exec $(KIND_CLUSTER)-control-plane kubectl --kubeconfig=/etc/kubernetes/admin.conf -n jungle wait --for=condition=complete job/database-migrations-down --timeout=180s

test:
	go test ./...

test-race:
	go test -race ./...

test-integration:
	@if [ -z "$(TEST_DATABASE_URL)" ]; then echo "Set TEST_DATABASE_URL to a dedicated PostgreSQL database with migrations 001-009 applied"; exit 1; fi
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test ./internal/adapters/outbound/postgres -run 'Test(Concurrent|Inbox|Outbox|ThreeIndependent|InboxRecoveryAcrossSeparateProcesses)' -count=1 -v

smoke-local:
	$(COMPOSE) run --rm --entrypoint /bin/bash kind-bootstrap /workspace/scripts/smoke-local.sh

coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

vet:
	go vet ./...

fmt:
	gofmt -w ./cmd ./internal
