# AWS deploy scripts (iterable)

This folder provides an **iterable** deployment workflow for the Suez SaaS MVP CloudFormation templates using the **AWS CLI**, plus a lightweight **bash test harness**.

## Prerequisites

- AWS CLI v2 installed and authenticated
- Permission to create/modify CloudFormation stacks, IAM (bootstrap), ECS/ALB/RDS/Route53/ACM resources used by the templates

## Files

- `env.example`: environment variables expected by the scripts
- `iter-deploy.sh`: validate → deploy → wait → run tests (repeatable)
- `iter-destroy.sh`: delete stacks and wait for completion (repeatable)
- `tests.sh`: test cases (bash) that verify stack health using `aws --query ...`

The scripts wrap existing templates at repo root:

- `scripts/suez-iam-bootstrap.yaml`
- `scripts/suez-saas-app-auto-cert.yaml`

## Quick start

1. Copy and edit env file:

```bash
cp scripts/aws/env.example .env.aws
chmod 600 .env.aws
```

2. Deploy + test:

```bash
set -a
source .env.aws
set +a
./scripts/aws/iter-deploy.sh
```

3. Destroy (optional):

```bash
set -a
source .env.aws
set +a
./scripts/aws/iter-destroy.sh
```

