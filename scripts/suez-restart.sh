#!/bin/bash
set -e

REGION=${AWS_REGION:-us-east-1}
PROFILE=${AWS_PROFILE:-suez}

CLUSTER_NAME="suez-cluster"
SERVICES=("suez-api" "suez-agent")

DB_INSTANCE="suez-postgres"

DESIRED_COUNT=1

echo "Starting SUEZ restart sequence..."

echo "Starting RDS database..."

aws rds start-db-instance   --db-instance-identifier $DB_INSTANCE   --region $REGION   --profile $PROFILE

echo "Waiting for database to become available..."

aws rds wait db-instance-available   --db-instance-identifier $DB_INSTANCE   --region $REGION   --profile $PROFILE

echo "Scaling ECS services back up..."

for SERVICE in "${SERVICES[@]}"; do
  aws ecs update-service     --cluster $CLUSTER_NAME     --service $SERVICE     --desired-count $DESIRED_COUNT     --region $REGION     --profile $PROFILE
done

echo "SUEZ platform restart complete."
