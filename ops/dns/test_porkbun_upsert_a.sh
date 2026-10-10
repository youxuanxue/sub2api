#!/usr/bin/env bash
# Unit tests for porkbun-upsert-a.sh (fake curl; no live Porkbun / no secrets printed).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT}/ops/dns/porkbun-upsert-a.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
mkdir -p "${tmp}/bin"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

cat >"${tmp}/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${FAKE_CURL_LOG}"
body=""
prev=""
for arg in "$@"; do
  if [[ "$prev" == "-d" ]]; then
    body="$arg"
  fi
  prev="$arg"
done
# Never echo request body (contains secrets in real runs).
url=""
for arg in "$@"; do
  case "$arg" in
    https://*|http://*) url="$arg" ;;
  esac
done
case "$url" in
  */ping)
    echo '{"status":"SUCCESS","yourIp":"203.0.113.1","scopes":["dns"]}'
    ;;
  */dns/retrieve/*)
    case "${FAKE_RETRIEVE_MODE}" in
      empty) echo '{"status":"SUCCESS","records":[]}' ;;
      same)
        echo '{"status":"SUCCESS","records":[{"id":"11","name":"api-hz.tokenkey.dev","type":"A","content":"167.233.211.115","ttl":"600","prio":"0"}]}'
        ;;
      different)
        echo '{"status":"SUCCESS","records":[{"id":"11","name":"api-hz.tokenkey.dev","type":"A","content":"1.2.3.4","ttl":"600","prio":"0"}]}'
        ;;
      dup)
        echo '{"status":"SUCCESS","records":[{"id":"11","name":"api-hz.tokenkey.dev","type":"A","content":"1.2.3.4","ttl":"600"},{"id":"12","name":"api-hz.tokenkey.dev","type":"A","content":"5.6.7.8","ttl":"600"}]}'
        ;;
      *)
        echo "unexpected retrieve mode: ${FAKE_RETRIEVE_MODE}" >&2
        exit 90
        ;;
    esac
    ;;
  */dns/create/*)
    echo "$body" >"${FAKE_CREATE_BODY}"
    echo '{"status":"SUCCESS","id":"99"}'
    ;;
  */dns/edit/*)
    echo "$url" >"${FAKE_EDIT_URL}"
    echo "$body" >"${FAKE_EDIT_BODY}"
    echo '{"status":"SUCCESS"}'
    ;;
  *)
    echo "unexpected curl url: ${url}" >&2
    exit 90
    ;;
esac
EOF
chmod +x "${tmp}/bin/curl"

run_script() {
  local out_file="$1"
  shift
  PATH="${tmp}/bin:${PATH}" \
    PORKBUN_CURL="${tmp}/bin/curl" \
    PORKBUN_API_KEY='pk1_test_not_real' \
    PORKBUN_SECRET_API_KEY='sk1_test_not_real' \
    FAKE_CURL_LOG="${tmp}/curl.log" \
    FAKE_CREATE_BODY="${tmp}/create.body" \
    FAKE_EDIT_URL="${tmp}/edit.url" \
    FAKE_EDIT_BODY="${tmp}/edit.body" \
    FAKE_RETRIEVE_MODE="${FAKE_RETRIEVE_MODE}" \
    bash "$SCRIPT" "$@" >"$out_file" 2>"${out_file}.err" || return $?
}

# Negative: missing credentials
if PATH="${tmp}/bin:${PATH}" PORKBUN_CURL="${tmp}/bin/curl" \
  env -u PORKBUN_API_KEY -u PORKBUN_SECRET_API_KEY \
  bash "$SCRIPT" api-hz 1.2.3.4 >"${tmp}/missing.out" 2>"${tmp}/missing.err"; then
  fail "missing credentials must exit non-zero"
fi
grep -q 'missing PORKBUN_API_KEY' "${tmp}/missing.err" || fail "missing-cred message"
! grep -E 'pk1_|sk1_' "${tmp}/missing.out" "${tmp}/missing.err" >/dev/null || fail "secrets leaked on missing-cred"

# Negative: invalid IPv4
if FAKE_RETRIEVE_MODE=empty run_script "${tmp}/badip.out" api-hz not-an-ip; then
  fail "invalid ipv4 must fail"
fi
grep -q 'ipv4 must look like A.B.C.D or' "${tmp}/badip.out.err" || fail "bad ipv4 message"

# Negative: preserve-ip with no existing record
if FAKE_RETRIEVE_MODE=empty run_script "${tmp}/preserve-empty.out" api-hz - --ttl 300 --apply; then
  fail "preserve-ip without record must fail"
fi
grep -q "ipv4 '-' requires an existing A record" "${tmp}/preserve-empty.out.err" || fail "preserve-empty message"

# Positive dry-run create plan
rm -f "${tmp}/curl.log" "${tmp}/create.body"
FAKE_RETRIEVE_MODE=empty run_script "${tmp}/plan-create.out" api-hz 167.233.211.115 \
  || fail "dry-run create plan should succeed"
grep -q 'action   : create' "${tmp}/plan-create.out" || fail "expected create plan"
grep -q '(dry run — pass --apply to execute)' "${tmp}/plan-create.out" || fail "expected dry-run marker"
test ! -e "${tmp}/create.body" || fail "dry-run must not create"

# Positive apply create
rm -f "${tmp}/curl.log" "${tmp}/create.body"
FAKE_RETRIEVE_MODE=empty run_script "${tmp}/apply-create.out" api-hz 167.233.211.115 --apply \
  || fail "apply create should succeed"
grep -q 'created: api-hz.tokenkey.dev A 167.233.211.115' "${tmp}/apply-create.out" || fail "create summary"
test -f "${tmp}/create.body" || fail "create body missing"
jq -e '.type=="A" and .content=="167.233.211.115" and .name=="api-hz" and .ttl=="600"' \
  "${tmp}/create.body" >/dev/null || fail "create payload fields"
# Secrets must be in body for API but must not appear in script stdout/stderr.
! grep -E 'pk1_test_not_real|sk1_test_not_real' \
  "${tmp}/apply-create.out" "${tmp}/apply-create.out.err" >/dev/null || fail "secrets leaked on create"

# Positive apply noop (same IP + default ttl 600)
rm -f "${tmp}/create.body" "${tmp}/edit.body"
FAKE_RETRIEVE_MODE=same run_script "${tmp}/noop.out" api-hz.tokenkey.dev 167.233.211.115 --apply \
  || fail "noop should succeed"
grep -q 'action   : noop' "${tmp}/noop.out" || fail "expected noop plan"
grep -q 'noop: api-hz.tokenkey.dev already A 167.233.211.115 ttl=600' "${tmp}/noop.out" || fail "noop summary"
test ! -e "${tmp}/create.body" || fail "noop must not create"
test ! -e "${tmp}/edit.body" || fail "noop must not edit"

# Positive apply TTL-only edit via explicit IP (same IP, ttl 600 -> 300)
rm -f "${tmp}/edit.body" "${tmp}/edit.url"
FAKE_RETRIEVE_MODE=same run_script "${tmp}/ttl.out" api-hz 167.233.211.115 --ttl 300 --apply \
  || fail "ttl-only edit should succeed"
grep -q 'action   : edit' "${tmp}/ttl.out" || fail "expected ttl edit plan"
grep -q 'ttl 600 -> 300' "${tmp}/ttl.out" || fail "ttl edit summary"
jq -e '.ttl=="300" and .content=="167.233.211.115"' "${tmp}/edit.body" >/dev/null || fail "ttl edit payload"

# Positive apply TTL-only via ipv4 "-" (preserve content; never invent IP)
rm -f "${tmp}/edit.body" "${tmp}/edit.url"
FAKE_RETRIEVE_MODE=same run_script "${tmp}/preserve.out" api-hz - --ttl 300 --apply \
  || fail "preserve-ip ttl edit should succeed"
grep -q 'action   : edit' "${tmp}/preserve.out" || fail "expected preserve edit plan"
grep -q '(preserve)' "${tmp}/preserve.out" || fail "preserve marker in plan"
jq -e '.ttl=="300" and .content=="167.233.211.115"' "${tmp}/edit.body" >/dev/null || fail "preserve edit payload"

# Positive apply edit
rm -f "${tmp}/edit.body" "${tmp}/edit.url"
FAKE_RETRIEVE_MODE=different run_script "${tmp}/edit.out" api-hz 167.233.211.115 --apply \
  || fail "edit should succeed"
grep -q 'action   : edit' "${tmp}/edit.out" || fail "expected edit plan"
grep -q 'edited: api-hz.tokenkey.dev A 1.2.3.4 -> 167.233.211.115' "${tmp}/edit.out" || fail "edit summary"
grep -q '/dns/edit/tokenkey.dev/11' "${tmp}/edit.url" || fail "edit url id"
jq -e '.type=="A" and .content=="167.233.211.115" and .name=="api-hz"' \
  "${tmp}/edit.body" >/dev/null || fail "edit payload"

# Negative: duplicate A records
if FAKE_RETRIEVE_MODE=dup run_script "${tmp}/dup.out" api-hz 167.233.211.115 --apply; then
  fail "duplicate records must fail"
fi
grep -q 'found 2 A records' "${tmp}/dup.out.err" || fail "duplicate message"

echo 'test_porkbun_upsert_a: ok'
