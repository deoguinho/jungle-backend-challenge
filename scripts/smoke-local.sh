#!/usr/bin/env bash
set -Eeuo pipefail

api_url="${API_URL:-http://jungle-local-control-plane:30080}"
issuer="${OIDC_ISSUER:-http://host.docker.internal:8081}/realms/jungle"
client_id="${OIDC_CLIENT_ID:-jungle-api}"
client_secret="${OIDC_CLIENT_SECRET:-jungle-api-secret-local-only}"
provider_client_id="${OIDC_PROVIDER_CLIENT_ID:-jungle-provider-local}"
provider_client_secret="${OIDC_PROVIDER_CLIENT_SECRET:-jungle-provider-secret-local-only}"

curl -fsS "$api_url/health/live"
curl -fsS "$api_url/health/ready"

unauthorized_status="$(curl -sS -o /dev/null -w '%{http_code}' -X POST "$api_url/wallets" \
  -H 'Content-Type: application/json' \
  -d '{"playerId":"00000000-0000-4000-8000-000000000001","initialBalance":{"amount":"1.00","currency":"BRL"}}')"
if [[ "$unauthorized_status" != "401" ]]; then
  echo "unauthenticated wallet request returned HTTP $unauthorized_status, expected 401" >&2
  exit 1
fi
printf 'unauthenticated wallet request: HTTP %s (no side effect)\n' "$unauthorized_status"

get_token() {
  local client="$1" secret="$2" response token
  response="$(curl -fsS -u "$client:$secret" -d 'grant_type=client_credentials' \
    "$issuer/protocol/openid-connect/token")"
  token="$(printf '%s' "$response" | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"
  if [[ -z "$token" ]]; then
    echo "Keycloak returned no access token for client $client" >&2
    exit 1
  fi
  printf '%s' "$token"
}

player_id="$(cat /proc/sys/kernel/random/uuid)"
internal_token="$(get_token "$client_id" "$client_secret")"
wallet_response="$(curl -fsS -w '\n%{http_code}' -X POST "$api_url/wallets" \
  -H "Authorization: Bearer $internal_token" \
  -H 'Content-Type: application/json' \
  -d "{\"playerId\":\"$player_id\",\"initialBalance\":{\"amount\":\"100.00\",\"currency\":\"BRL\"}}")"
status="${wallet_response##*$'\n'}"
wallet_body="${wallet_response%$'\n'*}"
if [[ "$status" != "201" ]]; then
  echo "wallet creation returned HTTP $status: $wallet_body" >&2
  exit 1
fi
wallet_id="$(printf '%s' "$wallet_body" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')"
printf 'wallet creation: HTTP %s %s\n' "$status" "$wallet_body"

provider_token="$(get_token "$provider_client_id" "$provider_client_secret")"
provider_wallet_status="$(curl -sS -o /dev/null -w '%{http_code}' -X POST "$api_url/wallets" \
  -H "Authorization: Bearer $provider_token" -H 'Content-Type: application/json' \
  -d '{"playerId":"00000000-0000-4000-8000-000000000002","initialBalance":{"amount":"1.00","currency":"BRL"}}')"
if [[ "$provider_wallet_status" != "401" ]]; then
  echo "provider token wallet request returned HTTP $provider_wallet_status, expected 401" >&2
  exit 1
fi

external_id="$(cat /proc/sys/kernel/random/uuid)"
idempotency_key="$(cat /proc/sys/kernel/random/uuid)"
round_id="$(cat /proc/sys/kernel/random/uuid)"
request="{\"externalTransactionId\":\"$external_id\",\"playerId\":\"$player_id\",\"walletId\":\"$wallet_id\",\"roundId\":\"$round_id\",\"gameId\":\"smoke\",\"kind\":\"BET\",\"money\":{\"amount\":\"10.00\",\"currency\":\"BRL\"}}"
bet_response="$(curl -fsS -w '\n%{http_code}' -X POST "$api_url/wagering/transactions" \
  -H "Authorization: Bearer $provider_token" \
  -H "Idempotency-Key: $idempotency_key" \
  -H 'Content-Type: application/json' -d "$request")"
status="${bet_response##*$'\n'}"
bet_body="${bet_response%$'\n'*}"
if [[ "$status" != "200" ]] || [[ "$bet_body" != *'"status":"PROCESSED"'* ]]; then
  echo "wager returned HTTP $status: $bet_body" >&2
  exit 1
fi
printf 'wager: HTTP %s %s\n' "$status" "$bet_body"

replay="$(curl -fsS -w '\n%{http_code}' -X POST "$api_url/wagering/transactions" \
  -H "Authorization: Bearer $provider_token" \
  -H "Idempotency-Key: $idempotency_key" \
  -H 'Content-Type: application/json' -d "$request")"
status="${replay##*$'\n'}"
replay_body="${replay%$'\n'*}"
if [[ "$status" != "200" ]] || [[ "$replay_body" != *'"idempotentReplay":true'* ]]; then
  echo "idempotent replay returned HTTP $status: $replay_body" >&2
  exit 1
fi
printf 'idempotent replay: HTTP %s %s\n' "$status" "$replay_body"

foreign_provider_status="$(curl -sS -o /dev/null -w '%{http_code}' "$api_url/providers/another-provider/wagering/transactions/$external_id" \
  -H "Authorization: Bearer $provider_token")"
if [[ "$foreign_provider_status" != "401" ]]; then
  echo "provider isolation request returned HTTP $foreign_provider_status, expected 401" >&2
  exit 1
fi

message_id="$(cat /proc/sys/kernel/random/uuid)"
external_id="$(cat /proc/sys/kernel/random/uuid)"
idempotency_key="$(cat /proc/sys/kernel/random/uuid)"
round_id="$(cat /proc/sys/kernel/random/uuid)"
message="{\"messageId\":\"$message_id\",\"type\":\"WagerTransactionRequested\",\"data\":{\"providerId\":\"local\",\"externalTransactionId\":\"$external_id\",\"idempotencyKey\":\"$idempotency_key\",\"playerId\":\"$player_id\",\"walletId\":\"$wallet_id\",\"roundId\":\"$round_id\",\"gameId\":\"smoke\",\"kind\":\"WIN\",\"money\":{\"amount\":\"3.00\",\"currency\":\"BRL\"}}}"
docker run --rm --network "${COMPOSE_NETWORK:-jungle-local}" \
  -e AWS_ACCESS_KEY_ID=test -e AWS_SECRET_ACCESS_KEY=test -e AWS_DEFAULT_REGION=us-east-1 \
  amazon/aws-cli:2.17.50 sqs send-message \
  --endpoint-url http://ministack:4566 \
  --queue-url http://ministack:4566/000000000000/wager-transactions.fifo \
  --message-body "$message" --message-group-id "$wallet_id" \
  --message-deduplication-id "$external_id" >/dev/null

transaction=""
for _ in $(seq 1 35); do
  transaction_response="$(curl -sS -w '\n%{http_code}' "$api_url/providers/local/wagering/transactions/$external_id" \
    -H "Authorization: Bearer $provider_token")"
  status="${transaction_response##*$'\n'}"
  transaction="${transaction_response%$'\n'*}"
  if [[ "$status" == "200" ]] && [[ "$transaction" == *'"status":"PROCESSED"'* ]]; then
    break
  fi
  sleep 1
done
if [[ "$status" != "200" ]] || [[ "$transaction" != *'"status":"PROCESSED"'* ]]; then
  echo "SQS wager was not processed: HTTP $status $transaction" >&2
  exit 1
fi
wallet_after="$(curl -fsS "$api_url/wallets/$wallet_id" -H "Authorization: Bearer $internal_token")"
if [[ "$wallet_after" != *'"amount":"93.00"'* ]]; then
  echo "SQS wager did not update the wallet balance to 93.00: $wallet_after" >&2
  exit 1
fi
inbox_done="$(docker exec jungle-local-postgres-1 psql -U jungle -d jungle -tAc \
  "SELECT completed_at IS NOT NULL FROM app.inbox_messages WHERE consumer_name='wager-transaction-consumer' AND message_id='$message_id'")"
if [[ "$inbox_done" != "t" ]]; then
  echo "SQS inbox record was not completed: message=$message_id" >&2
  exit 1
fi
event_count="$(docker run --rm --network "${COMPOSE_NETWORK:-jungle-local}" \
  -e AWS_ACCESS_KEY_ID=test -e AWS_SECRET_ACCESS_KEY=test -e AWS_DEFAULT_REGION=us-east-1 \
  amazon/aws-cli:2.17.50 sqs get-queue-attributes \
  --endpoint-url http://ministack:4566 \
  --queue-url http://ministack:4566/000000000000/wager-events.fifo \
  --attribute-names ApproximateNumberOfMessages \
  --query 'Attributes.ApproximateNumberOfMessages' --output text)"
if [[ ! "$event_count" =~ ^[0-9]+$ ]] || (( event_count < 1 )); then
  echo "outbox publisher has not placed events on wager-events.fifo" >&2
  exit 1
fi
reconciliation="$(curl -fsS -X POST "$api_url/wallets/$wallet_id/reconciliation" \
  -H "Authorization: Bearer $internal_token")"
if [[ "$reconciliation" != *'"consistent":true'* ]] || [[ "$reconciliation" != *'"amount":"93.00"'* ]]; then
  echo "wallet reconciliation failed: $reconciliation" >&2
  exit 1
fi
printf 'SQS consumer: processed %s, committed inbox; balance 93.00 reconciles; event queue has %s messages\n' "$external_id" "$event_count"

# Exercise the configured poison-message redrive through the running consumer.
dlq_before="$(docker run --rm --network "${COMPOSE_NETWORK:-jungle-local}" \
  -e AWS_ACCESS_KEY_ID=test -e AWS_SECRET_ACCESS_KEY=test -e AWS_DEFAULT_REGION=us-east-1 \
  amazon/aws-cli:2.17.50 sqs get-queue-attributes \
  --endpoint-url http://ministack:4566 \
  --queue-url http://ministack:4566/000000000000/wager-transactions-dlq.fifo \
  --attribute-names ApproximateNumberOfMessages \
  --query 'Attributes.ApproximateNumberOfMessages' --output text)"
if [[ ! "$dlq_before" =~ ^[0-9]+$ ]]; then dlq_before=0; fi
poison_id="$(cat /proc/sys/kernel/random/uuid)"
docker run --rm --network "${COMPOSE_NETWORK:-jungle-local}" \
  -e AWS_ACCESS_KEY_ID=test -e AWS_SECRET_ACCESS_KEY=test -e AWS_DEFAULT_REGION=us-east-1 \
  amazon/aws-cli:2.17.50 sqs send-message \
  --endpoint-url http://ministack:4566 \
  --queue-url http://ministack:4566/000000000000/wager-transactions.fifo \
  --message-body 'not-json' --message-group-id "$poison_id" --message-deduplication-id "$poison_id" >/dev/null
dlq_count="$dlq_before"
for _ in $(seq 1 30); do
  dlq_count="$(docker run --rm --network "${COMPOSE_NETWORK:-jungle-local}" \
    -e AWS_ACCESS_KEY_ID=test -e AWS_SECRET_ACCESS_KEY=test -e AWS_DEFAULT_REGION=us-east-1 \
    amazon/aws-cli:2.17.50 sqs get-queue-attributes \
    --endpoint-url http://ministack:4566 \
    --queue-url http://ministack:4566/000000000000/wager-transactions-dlq.fifo \
    --attribute-names ApproximateNumberOfMessages \
    --query 'Attributes.ApproximateNumberOfMessages' --output text)"
  if [[ "$dlq_count" =~ ^[0-9]+$ ]] && (( dlq_count > dlq_before )); then break; fi
  sleep 1
done
if [[ ! "$dlq_count" =~ ^[0-9]+$ ]] || (( dlq_count < 1 )); then
  echo "poison message was not moved to the configured SQS DLQ" >&2
  exit 1
fi
printf 'SQS failure handling: poison message reached DLQ after five receives\n'

metrics="$(curl -fsS "$api_url/metrics")"
for metric in jungle_http_requests_total jungle_wager_results_total jungle_wager_processing_duration_seconds_count; do
  if [[ "$metrics" != *"$metric"* ]]; then echo "metrics endpoint is missing $metric" >&2; exit 1; fi
done
printf 'metrics endpoint: HTTP 200 with request, wager and latency measurements\n'
