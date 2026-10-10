#!/usr/bin/env bash
set -euo pipefail

# SUEZ smart sleep script
#
# Goal:
# - Put the MVP stack to sleep only when it appears idle
# - Restart it on demand
#
# What "idle" means in this script:
# - ECS services have low CPU and memory utilization for a lookback window
# - ALB request count is below a threshold
# - No recent database connections above a threshold
#
# Modes:
#   ./suez-smart-sleep.sh check-and-sleep
#   ./suez-smart-sleep.sh wake
#   ./suez-smart-sleep.sh status
#
# Assumptions:
# - ECS/Fargate services back the app
# - RDS PostgreSQL is used
# - Optional ALB fronts the services
# - CloudWatch metrics exist for these resources
#
# Required IAM permissions:
# - cloudwatch:GetMetricStatistics / cloudwatch:GetMetricData
# - ecs:DescribeServices, ecs:UpdateService
# - rds:DescribeDBInstances, rds:StartDBInstance, rds:StopDBInstance
# - rds:ListTagsForResource
# - elasticloadbalancing:DescribeLoadBalancers
#
# Environment overrides:
#   AWS_PROFILE=suez
#   AWS_REGION=us-east-1
#   CLUSTER_NAME=suez-cluster
#   SERVICES="suez-api suez-agent"
#   DB_INSTANCE=suez-postgres
#   ALB_ARN=arn:aws:elasticloadbalancing:...
#   LOOKBACK_MINUTES=15
#   CPU_IDLE_THRESHOLD=5
#   MEMORY_IDLE_THRESHOLD=10
#   ALB_REQUEST_THRESHOLD=20
#   DB_CONNECTION_THRESHOLD=2
#   DESIRED_COUNT=1
#   DRY_RUN=true

AWS_PROFILE="${AWS_PROFILE:-suez}"
AWS_REGION="${AWS_REGION:-us-east-1}"

CLUSTER_NAME="${CLUSTER_NAME:-suez-cluster}"
SERVICES="${SERVICES:-suez-api suez-agent}"
DB_INSTANCE="${DB_INSTANCE:-suez-postgres}"
ALB_ARN="${ALB_ARN:-}"

LOOKBACK_MINUTES="${LOOKBACK_MINUTES:-15}"
CPU_IDLE_THRESHOLD="${CPU_IDLE_THRESHOLD:-5}"
MEMORY_IDLE_THRESHOLD="${MEMORY_IDLE_THRESHOLD:-10}"
ALB_REQUEST_THRESHOLD="${ALB_REQUEST_THRESHOLD:-20}"
DB_CONNECTION_THRESHOLD="${DB_CONNECTION_THRESHOLD:-2}"
DESIRED_COUNT="${DESIRED_COUNT:-1}"
DRY_RUN="${DRY_RUN:-false}"

_now_utc() {
  date -u +"%Y-%m-%dT%H:%M:%SZ"
}

_start_utc() {
  date -u -d "${LOOKBACK_MINUTES} minutes ago" +"%Y-%m-%dT%H:%M:%SZ"
}

aws_cli() {
  aws --profile "${AWS_PROFILE}" --region "${AWS_REGION}" "$@"
}

log() {
  echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] $*"
}

require_binary() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "Missing required command: $1" >&2
    exit 1
  }
}

json_query_metric_average() {
  local namespace="$1"
  local metric_name="$2"
  local dimensions_json="$3"
  local stat="${4:-Average}"

  aws_cli cloudwatch get-metric-statistics \
    --namespace "${namespace}" \
    --metric-name "${metric_name}" \
    --dimensions "${dimensions_json}" \
    --start-time "$(_start_utc)" \
    --end-time "$(_now_utc)" \
    --period 300 \
    --statistics "${stat}" \
    --output json
}

json_query_metric_sum() {
  local namespace="$1"
  local metric_name="$2"
  local dimensions_json="$3"

  aws_cli cloudwatch get-metric-statistics \
    --namespace "${namespace}" \
    --metric-name "${metric_name}" \
    --dimensions "${dimensions_json}" \
    --start-time "$(_start_utc)" \
    --end-time "$(_now_utc)" \
    --period 300 \
    --statistics Sum \
    --output json
}

metric_max_average_from_json() {
  python3 - <<'PY'
import sys, json
data = json.load(sys.stdin)
points = data.get("Datapoints", [])
if not points:
    print("0")
else:
    vals = [p.get("Average", 0) for p in points]
    print(max(vals) if vals else 0)
PY
}

metric_total_sum_from_json() {
  python3 - <<'PY'
import sys, json
data = json.load(sys.stdin)
points = data.get("Datapoints", [])
total = sum(p.get("Sum", 0) for p in points)
if int(total) == total:
    print(int(total))
else:
    print(total)
PY
}

ecs_service_idle() {
  local service="$1"

  local cpu_dims="Name=ClusterName,Value=${CLUSTER_NAME} Name=ServiceName,Value=${service}"
  local mem_dims="Name=ClusterName,Value=${CLUSTER_NAME} Name=ServiceName,Value=${service}"

  local cpu_json
  local mem_json
  cpu_json="$(json_query_metric_average "AWS/ECS" "CPUUtilization" "${cpu_dims}")"
  mem_json="$(json_query_metric_average "AWS/ECS" "MemoryUtilization" "${mem_dims}")"

  local cpu_max
  local mem_max
  cpu_max="$(printf '%s' "${cpu_json}" | metric_max_average_from_json)"
  mem_max="$(printf '%s' "${mem_json}" | metric_max_average_from_json)"

  log "Service ${service}: max CPU=${cpu_max}% max Memory=${mem_max}% over last ${LOOKBACK_MINUTES}m"

  python3 - <<PY
cpu=float("${cpu_max}")
mem=float("${mem_max}")
cpu_th=float("${CPU_IDLE_THRESHOLD}")
mem_th=float("${MEMORY_IDLE_THRESHOLD}")
print("true" if cpu <= cpu_th and mem <= mem_th else "false")
PY
}

alb_request_count() {
  if [[ -z "${ALB_ARN}" ]]; then
    echo "0"
    return
  fi

  # CloudWatch dimension wants the load balancer suffix, e.g. app/name/hash
  local alb_suffix
  alb_suffix="$(printf '%s' "${ALB_ARN}" | sed 's#.*loadbalancer/##')"
  local dims="Name=LoadBalancer,Value=${alb_suffix}"

  local req_json
  req_json="$(json_query_metric_sum "AWS/ApplicationELB" "RequestCount" "${dims}")"
  printf '%s' "${req_json}" | metric_total_sum_from_json
}

db_connections_max() {
  local dims="Name=DBInstanceIdentifier,Value=${DB_INSTANCE}"
  local db_json
  db_json="$(json_query_metric_average "AWS/RDS" "DatabaseConnections" "${dims}")"
  printf '%s' "${db_json}" | metric_max_average_from_json
}

rds_is_available() {
  local status
  status="$(aws_cli rds describe-db-instances \
    --db-instance-identifier "${DB_INSTANCE}" \
    --query 'DBInstances[0].DBInstanceStatus' \
    --output text)"
  [[ "${status}" == "available" ]]
}

rds_is_stopped() {
  local status
  status="$(aws_cli rds describe-db-instances \
    --db-instance-identifier "${DB_INSTANCE}" \
    --query 'DBInstances[0].DBInstanceStatus' \
    --output text)"
  [[ "${status}" == "stopped" ]]
}

service_desired_count() {
  local service="$1"
  aws_cli ecs describe-services \
    --cluster "${CLUSTER_NAME}" \
    --services "${service}" \
    --query 'services[0].desiredCount' \
    --output text
}

scale_service() {
  local service="$1"
  local count="$2"
  log "Scaling service ${service} to desired count ${count}"
  if [[ "${DRY_RUN}" == "true" ]]; then
    log "DRY_RUN enabled, skipping ecs update-service"
    return
  fi
  aws_cli ecs update-service \
    --cluster "${CLUSTER_NAME}" \
    --service "${service}" \
    --desired-count "${count}" >/dev/null
}

sleep_stack() {
  log "Beginning smart sleep flow"

  local all_idle="true"
  local svc
  for svc in ${SERVICES}; do
    if [[ "$(ecs_service_idle "${svc}")" != "true" ]]; then
      all_idle="false"
    fi
  done

  local req_count
  req_count="$(alb_request_count)"
  log "ALB request count over last ${LOOKBACK_MINUTES}m: ${req_count}"

  local db_conn
  db_conn="$(db_connections_max)"
  log "RDS max database connections over last ${LOOKBACK_MINUTES}m: ${db_conn}"

  local req_idle="false"
  local db_idle="false"

  python3 - <<PY > /tmp/suez_req_idle.txt
req=float("${req_count}")
th=float("${ALB_REQUEST_THRESHOLD}")
print("true" if req <= th else "false")
PY
  req_idle="$(cat /tmp/suez_req_idle.txt)"

  python3 - <<PY > /tmp/suez_db_idle.txt
dbc=float("${db_conn}")
th=float("${DB_CONNECTION_THRESHOLD}")
print("true" if dbc <= th else "false")
PY
  db_idle="$(cat /tmp/suez_db_idle.txt)"

  if [[ "${all_idle}" == "true" && "${req_idle}" == "true" && "${db_idle}" == "true" ]]; then
    log "Stack appears idle. Proceeding with sleep actions."
  else
    log "Stack is not idle enough to sleep."
    log "all_idle=${all_idle} req_idle=${req_idle} db_idle=${db_idle}"
    exit 0
  fi

  for svc in ${SERVICES}; do
    scale_service "${svc}" 0
  done

  if rds_is_available; then
    log "Stopping RDS instance ${DB_INSTANCE}"
    if [[ "${DRY_RUN}" == "true" ]]; then
      log "DRY_RUN enabled, skipping rds stop-db-instance"
    else
      aws_cli rds stop-db-instance --db-instance-identifier "${DB_INSTANCE}" >/dev/null
    fi
  else
    log "RDS instance ${DB_INSTANCE} is not in available state; skipping stop."
  fi

  log "Smart sleep completed."
}

wake_stack() {
  log "Beginning wake flow"

  if rds_is_stopped; then
    log "Starting RDS instance ${DB_INSTANCE}"
    if [[ "${DRY_RUN}" == "true" ]]; then
      log "DRY_RUN enabled, skipping rds start-db-instance"
    else
      aws_cli rds start-db-instance --db-instance-identifier "${DB_INSTANCE}" >/dev/null
      log "Waiting for RDS availability"
      aws_cli rds wait db-instance-available --db-instance-identifier "${DB_INSTANCE}"
    fi
  else
    log "RDS instance ${DB_INSTANCE} is not stopped; continuing."
  fi

  local svc
  for svc in ${SERVICES}; do
    scale_service "${svc}" "${DESIRED_COUNT}"
  done

  log "Wake flow completed."
}

status_stack() {
  log "Status report"
  local svc
  for svc in ${SERVICES}; do
    local desired
    desired="$(service_desired_count "${svc}")"
    echo "ECS service ${svc}: desiredCount=${desired}"
  done

  local db_status
  db_status="$(aws_cli rds describe-db-instances \
    --db-instance-identifier "${DB_INSTANCE}" \
    --query 'DBInstances[0].DBInstanceStatus' \
    --output text)"
  echo "RDS ${DB_INSTANCE}: status=${db_status}"

  if [[ -n "${ALB_ARN}" ]]; then
    echo "ALB request count over ${LOOKBACK_MINUTES}m: $(alb_request_count)"
  fi
  echo "RDS max DB connections over ${LOOKBACK_MINUTES}m: $(db_connections_max)"
}

usage() {
  cat <<EOF
Usage:
  $0 check-and-sleep
  $0 wake
  $0 status

Examples:
  AWS_PROFILE=suez AWS_REGION=us-east-1 $0 check-and-sleep
  AWS_PROFILE=suez AWS_REGION=us-east-1 DESIRED_COUNT=1 $0 wake
  AWS_PROFILE=suez AWS_REGION=us-east-1 $0 status
EOF
}

main() {
  require_binary aws
  require_binary python3

  local mode="${1:-}"
  case "${mode}" in
    check-and-sleep)
      sleep_stack
      ;;
    wake)
      wake_stack
      ;;
    status)
      status_stack
      ;;
    *)
      usage
      exit 1
      ;;
  esac
}

main "$@"
