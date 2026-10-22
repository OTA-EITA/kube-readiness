#!/usr/bin/env bash
# End-to-end test: deploy the example workloads to a real cluster (kind in CI)
# and score them with --cluster.
set -euo pipefail

bin=${BIN:-./bin/kube-readiness}
fail() { echo "e2e: $*" >&2; exit 1; }

kubectl apply -f examples/good.yaml -f examples/bad.yaml
kubectl -n readiness-good rollout status deploy/web --timeout=180s

"$bin" --cluster --namespace readiness-good --min-score 100 --fail-on-danger \
  || fail "the good example must score 100 with no danger findings"

"$bin" --cluster --output json > report.json || true
"$bin" --cluster --output markdown | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}" || true

score() { jq -r --arg id "$1" '.workloads[] | select(.id == $id) | .score' report.json; }
failed() { jq -r --arg id "$1" '[.workloads[] | select(.id == $id) | .results[] | select(.passed | not) | .id] | join(",")' report.json; }

[ "$(score Deployment/readiness-good/web)" = "100" ] || fail "web should score 100"
failed Deployment/readiness-bad/api | grep -q REL-005 || fail "api: the blocking PDB (REL-005) was not reported"
failed Deployment/readiness-bad/api | grep -q REL-007 || fail "api: the latest tag (REL-007) was not reported"
failed DaemonSet/readiness-bad/node-agent | grep -q SEC-001 || fail "node-agent: privileged (SEC-001) was not reported"
failed DaemonSet/readiness-bad/node-agent | grep -q SEC-002 || fail "node-agent: hostNetwork (SEC-002) was not reported"
failed StatefulSet/readiness-bad/cache | grep -q SEC-005 && fail "cache: SEC-005 is ignored by annotation"
jq -e '[.workloads[].id] | any(startswith("DaemonSet/kube-system/")) | not' report.json >/dev/null \
  || fail "kube-system must be excluded by default"

set +e
"$bin" --cluster --namespace readiness-bad --fail-on-danger >/dev/null
code=$?
set -e
[ "$code" -eq 1 ] || fail "danger findings must exit 1, got $code"
echo "e2e: all expectations met"
