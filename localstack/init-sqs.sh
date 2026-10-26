#!/bin/bash
set -e

echo "Initializing LocalStack SQS FIFO queues for Jungle Gaming Wager Service..."

# 1. Create Dead Letter Queue (DLQ)
awslocal sqs create-queue \
  --queue-name wager-transactions-dlq.fifo \
  --attributes '{
    "FifoQueue": "true",
    "ContentBasedDeduplication": "false"
  }'

DLQ_ARN="arn:aws:sqs:us-east-1:000000000000:wager-transactions-dlq.fifo"

# 2. Create Main Transactions FIFO Queue with RedrivePolicy (maxReceiveCount = 5)
awslocal sqs create-queue \
  --queue-name wager-transactions.fifo \
  --attributes "{
    \"FifoQueue\": \"true\",
    \"ContentBasedDeduplication\": \"false\",
    \"VisibilityTimeout\": \"30\",
    \"RedrivePolicy\": \"{\\\"deadLetterTargetArn\\\":\\\"${DLQ_ARN}\\\",\\\"maxReceiveCount\\\":\\\"5\\\"}\"
  }"

# 3. Create Domain Events FIFO Queue (Transactional Outbox Target)
awslocal sqs create-queue \
  --queue-name wager-events.fifo \
  --attributes '{
    "FifoQueue": "true",
    "ContentBasedDeduplication": "false"
  }'

echo "SQS FIFO queues initialized successfully:"
awslocal sqs list-queues
