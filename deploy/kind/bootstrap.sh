#!/usr/bin/env bash
set -Eeuo pipefail

cluster="${KIND_CLUSTER:-jungle-local}"
config="${KIND_CONFIG:-/workspace/deploy/kind/kind.yaml}"
image="${APP_IMAGE:-jungle-backend:local}"

if ! docker info >/dev/null 2>&1; then
  echo "Docker Engine is unavailable through /var/run/docker.sock" >&2
  exit 1
fi

if ! kind get clusters | grep -Fxq "$cluster"; then
  kind create cluster --name "$cluster" --config "$config" --wait 180s
else
  echo "kind cluster '$cluster' already exists; reusing it"
fi

# Connect kind nodes to the Compose bridge. This gives Pods a stable route to
# published dependency ports without embedding an ephemeral host address in manifests.
compose_network="${COMPOSE_NETWORK:-jungle-local}"
for node in $(kind get nodes --name "$cluster"); do
  if ! docker network inspect "$compose_network" >/dev/null 2>&1; then
    echo "Compose network '$compose_network' was not found" >&2
    exit 1
  fi
  if ! docker network inspect "$compose_network" --format '{{json .Containers}}' | grep -Fq "$node"; then
    docker network connect "$compose_network" "$node" 2>/dev/null || true
  fi
done

gateway="$(docker network inspect "$compose_network" --format '{{(index .IPAM.Config 0).Gateway}}')"
if [[ -z "$gateway" ]]; then
  echo "Could not resolve gateway for Docker network '$compose_network'" >&2
  exit 1
fi

kubectl --context "kind-$cluster" apply -f /workspace/deploy/kubernetes/base/namespace.yaml

kubectl --context "kind-$cluster" -n jungle create configmap local-dependencies \
  --from-literal=POSTGRES_HOST="$gateway" \
  --from-literal=POSTGRES_PORT="${POSTGRES_PORT:-5432}" \
  --from-literal=POSTGRES_DB="${POSTGRES_DB:-jungle}" \
  --from-literal=REDIS_ADDR="$gateway:${REDIS_PORT:-6379}" \
  --from-literal=AWS_ENDPOINT_URL="http://$gateway:${MINISTACK_PORT:-4566}" \
  --from-literal=AWS_REGION="${AWS_REGION:-us-east-1}" \
  --from-literal=OIDC_ISSUER="${OIDC_ISSUER:-http://host.docker.internal:8081}/realms/jungle" \
  --from-literal=SQS_QUEUE_URL="http://$gateway:${MINISTACK_PORT:-4566}/000000000000/wager-transactions.fifo" \
  --from-literal=SQS_EVENTS_QUEUE_URL="http://$gateway:${MINISTACK_PORT:-4566}/000000000000/wager-events.fifo" \
  --dry-run=client -o yaml | kubectl --context "kind-$cluster" -n jungle apply -f -

kubectl --context "kind-$cluster" -n jungle create secret generic local-credentials \
  --from-literal=POSTGRES_USER="${POSTGRES_USER:-jungle}" \
  --from-literal=POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-jungle-local-only}" \
  --from-literal=DATABASE_URL="postgres://${POSTGRES_USER:-jungle}:${POSTGRES_PASSWORD:-jungle-local-only}@$gateway:${POSTGRES_PORT:-5432}/${POSTGRES_DB:-jungle}?sslmode=disable" \
  --from-literal=AWS_ACCESS_KEY_ID=test \
  --from-literal=AWS_SECRET_ACCESS_KEY=test \
  --from-literal=OIDC_CLIENT_ID="${OIDC_CLIENT_ID:-jungle-api}" \
  --from-literal=OIDC_CLIENT_SECRET="${OIDC_CLIENT_SECRET:-jungle-api-secret-local-only}" \
  --dry-run=client -o yaml | kubectl --context "kind-$cluster" -n jungle apply -f -

if [[ "${APP_BUILD:-1}" == "1" ]]; then
  if [[ -f /workspace/go.mod ]]; then
    docker build -f /workspace/Dockerfile -t "$image" /workspace
  else
    echo "go.mod is absent; skipping application image build (set APP_BUILD=0 to disable explicitly)"
  fi
fi

if docker image inspect "$image" >/dev/null 2>&1; then
  kind load docker-image "$image" --name "$cluster"
  kubectl --context "kind-$cluster" -n jungle delete job database-migrations --ignore-not-found --wait=true
  kubectl --context "kind-$cluster" -n jungle apply -f /workspace/deploy/kubernetes/jobs/migrations.yaml
  kubectl --context "kind-$cluster" -n jungle set image job/database-migrations migrate="$image"
  kubectl --context "kind-$cluster" -n jungle wait --for=condition=complete job/database-migrations --timeout=180s
  kubectl --context "kind-$cluster" -n jungle apply -f /workspace/deploy/kubernetes/base/workloads.yaml
  for deployment in api worker-sqs worker-reference worker-outbox; do
    kubectl --context "kind-$cluster" -n jungle set image deployment/"$deployment" "$deployment"="$image"
    kubectl --context "kind-$cluster" -n jungle patch deployment "$deployment" --type=merge \
      -p "{\"spec\":{\"template\":{\"spec\":{\"hostAliases\":[{\"ip\":\"$gateway\",\"hostnames\":[\"host.docker.internal\"]}]}}}}"
  done
else
  echo "Application image '$image' is not available; cluster and local dependency configuration are ready, but app workloads remain pending."
fi

echo "kind cluster '$cluster' is ready; API port mapping is http://localhost:8080"
