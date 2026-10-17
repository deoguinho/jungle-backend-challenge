#!/bin/sh
set -eu

endpoint="${AWS_ENDPOINT_URL:-http://ministack:4566}"
create_fifo_queue() {
  name="$1"
  attributes="$2"
  if aws --endpoint-url "$endpoint" sqs get-queue-url --queue-name "$name" >/dev/null 2>&1; then
    echo "SQS queue $name already exists"
  else
    aws --endpoint-url "$endpoint" sqs create-queue --queue-name "$name" --attributes "$attributes" >/dev/null
    echo "Created SQS queue $name"
  fi
}

create_fifo_queue wager-transactions-dlq.fifo '{"FifoQueue":"true","ContentBasedDeduplication":"false"}'
dlq_arn="$(aws --endpoint-url "$endpoint" sqs get-queue-attributes --queue-url "$endpoint/000000000000/wager-transactions-dlq.fifo" --attribute-names QueueArn --query 'Attributes.QueueArn' --output text)"
redrive="$(printf '{"deadLetterTargetArn":"%s","maxReceiveCount":"5"}' "$dlq_arn")"
create_fifo_queue wager-transactions.fifo "{\"FifoQueue\":\"true\",\"ContentBasedDeduplication\":\"false\",\"RedrivePolicy\":\"$(printf '%s' "$redrive" | sed 's/"/\\"/g')\"}"
create_fifo_queue wager-events.fifo '{"FifoQueue":"true","ContentBasedDeduplication":"false"}'
