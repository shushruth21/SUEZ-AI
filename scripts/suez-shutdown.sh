#!/bin/bash
set -e

REGION=${AWS_REGION:-us-east-1}
PROFILE=${AWS_PROFILE:-suez}

CLUSTER_NAME="suez-cluster"
SERVICES=("suez-api" "suez-agent")

DB_INSTANCE="suez-postgres"
CACHE_CLUSTER="suez-redis"

echo "Starting SUEZ shutdown sequence..."

echo "Scaling ECS services to zero..."

for SERVICE in "${SERVICES[@]}"; do
  aws ecs update-service     --cluster $CLUSTER_NAME     --service $SERVICE     --desired-count 0     --region $REGION     --profile $PROFILE
done

echo "Stopping RDS database..."

aws rds stop-db-instance   --db-instance-identifier $DB_INSTANCE   --region $REGION   --profile $PROFILE

echo "Stopping ElastiCache cluster (if applicable)..."

aws elasticache describe-replication-groups   --replication-group-id $CACHE_CLUSTER   --region $REGION   --profile $PROFILE >/dev/null 2>&1 || true

echo "SUEZ MVP shutdown complete."
