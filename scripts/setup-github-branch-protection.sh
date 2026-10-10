#!/usr/bin/env bash
# Protect master and develop on GitHub: require pull requests (no direct pushes).
# GitHub uses Pull Requests (PRs), not GitLab "Merge Requests" — same idea.
#
# Prerequisites:
#   - A classic PAT with repo scope, OR a fine-grained token with
#     Administration: write on this repository (branch protection).
#   - export GITHUB_TOKEN='ghp_...'
#
# Usage:
#   ./scripts/setup-github-branch-protection.sh
#
set -euo pipefail

OWNER="${GITHUB_OWNER:-AcroSystems-AI}"
REPO="${GITHUB_REPO:-suez}"
TOKEN="${GITHUB_TOKEN:?Set GITHUB_TOKEN (PAT with admin permissions on the repo)}"

API="https://api.github.com/repos/${OWNER}/${REPO}/branches"

protect_branch() {
  local branch="$1"
  echo "PUT ${API}/${branch}/protection"
  curl -sS -f -X PUT \
    -H "Accept: application/vnd.github+json" \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "X-GitHub-Api-Version: 2022-11-28" \
    "${API}/${branch}/protection" \
    -d "$(cat <<JSON
{
  "required_status_checks": null,
  "enforce_admins": true,
  "required_pull_request_reviews": {
    "dismiss_stale_reviews": true,
    "require_code_owner_reviews": false,
    "required_approving_review_count": 1
  },
  "restrictions": null,
  "required_linear_history": false,
  "allow_force_pushes": false,
  "allow_deletions": false,
  "block_creations": false,
  "required_conversation_resolution": true
}
JSON
)"
  echo ""
  echo "OK: protection applied to ${branch}"
}

for b in master develop; do
  protect_branch "$b"
done

echo "Done. Verify: https://github.com/${OWNER}/${REPO}/settings/branches"
