#!/usr/bin/env bash
# Owner-invoked backend release (via `task be:deploy`). GitOps model: resolve the
# newest green backend image sha, bump BE_TAG in .doco-cd.yml, then commit + push.
# doco-cd on the VM reconciles the new image within one poll interval — no SSH,
# no scp, no VM-side edit. Secrets stay on the VM (PASS_ENV → /opt/doco-cd/
# secrets.env); this only touches the non-secret image tag in git.
set -euo pipefail

DOCO_CFG=".doco-cd.yml"
WORKFLOW="${BE_WORKFLOW:-Build backend image}"
[ -f "$DOCO_CFG" ] || { echo "missing $DOCO_CFG (run from the repo root)"; exit 1; }

# 1. Image tag — explicit override, else the newest green CI build on main.
if [ -n "${BE_TAG:-}" ]; then
  tag="$BE_TAG"
  echo "→ image tag (pinned): $tag"
else
  read -r sha title < <(gh run list --workflow "$WORKFLOW" --branch main \
    --status success --limit 1 --json headSha,displayTitle \
    -q '.[0] | "\(.headSha) \(.displayTitle)"')
  [ -n "${sha:-}" ] || { echo "no successful '$WORKFLOW' run found on main"; exit 1; }
  tag="sha-$sha"
  echo "→ image tag: $tag  ($title)"
fi

# 2. Bump BE_TAG in .doco-cd.yml (idempotent).
if grep -q "^  BE_TAG: ${tag}\$" "$DOCO_CFG"; then
  echo "✓ $DOCO_CFG already at $tag — nothing to ship"; exit 0
fi
tmp="$(mktemp)"; trap 'rm -f "$tmp"' EXIT
sed -E "s|^(  BE_TAG: ).*|\1${tag}|" "$DOCO_CFG" >"$tmp" && mv "$tmp" "$DOCO_CFG"
grep -q "^  BE_TAG: ${tag}\$" "$DOCO_CFG" || { echo "failed to set BE_TAG in $DOCO_CFG"; exit 1; }

# 3. Commit + push just this file — doco-cd reconciles it on the VM. Staging only
#    $DOCO_CFG keeps any unrelated working-tree changes out of the deploy commit.
git add "$DOCO_CFG"
git commit -q -m "chore(deploy): roll VM backend image to ${tag}"
git push -q origin main
echo "✓ pushed ${tag} — doco-cd reconciles the VM within ~30s (task doco:logs, or on the VM: docker logs \$(docker ps -q --filter label=com.docker.compose.service=doco-cd))"
