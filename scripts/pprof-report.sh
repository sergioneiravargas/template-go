#!/usr/bin/env bash
set -uo pipefail

# Usage: ./scripts/pprof-report.sh
#
# Fetches heap and goroutine summaries from the pprof endpoints exposed by the
# local containers (APP_PROFILER_ENABLED=true, ports mapped in
# docker-compose.yaml.local) and prints a one-screen health report.

# --- Configuration ---
SERVICES=(
  "server:6060"
  "socket-server:6070"
  "worker:6080"
)

# --- Fetch helpers ---
fetch_heap() {
  local port="$1"
  curl -s --connect-timeout 5 --max-time 10 "http://127.0.0.1:$port/debug/pprof/heap?debug=1" 2>/dev/null || true
}

fetch_goroutines_raw() {
  local port="$1"
  curl -s --connect-timeout 5 --max-time 10 "http://127.0.0.1:$port/debug/pprof/goroutine?debug=1" 2>/dev/null || true
}

extract_stat() {
  local data="$1" field="$2"
  echo "$data" | grep "^# $field = " | awk -F'= ' '{print $2}' || true
}

extract_goroutine_count() {
  local data="$1"
  echo "$data" | head -1 | awk '{print $NF}' || true
}

extract_idle_http() {
  local data="$1"
  local count
  count=$(echo "$data" | grep -c 'http\.(\*conn)\.serve' 2>/dev/null) || true
  echo "${count:-0}"
}

human_bytes() {
  local bytes="${1:-0}"
  if [[ -z "$bytes" || "$bytes" == "0" ]]; then
    echo "n/a"
  elif (( bytes >= 1073741824 )); then
    awk "BEGIN {printf \"%.1f GB\", $bytes/1073741824}"
  elif (( bytes >= 1048576 )); then
    awk "BEGIN {printf \"%.1f MB\", $bytes/1048576}"
  elif (( bytes >= 1024 )); then
    awk "BEGIN {printf \"%.1f KB\", $bytes/1024}"
  else
    echo "${bytes} B"
  fi
}

human_count() {
  local n="${1:-0}"
  if [[ -z "$n" || "$n" == "0" ]]; then
    echo "n/a"
  elif (( n >= 1000000 )); then
    awk "BEGIN {printf \"%.1fM\", $n/1000000}"
  elif (( n >= 1000 )); then
    awk "BEGIN {printf \"%.1fK\", $n/1000}"
  else
    echo "$n"
  fi
}

# --- Collect all data (one curl per endpoint, cache results) ---
declare -A HEAP_DATA GOR_DATA DATA
FIELDS=("Goroutines" "HTTP idle" "HeapAlloc" "HeapInuse" "Sys" "HeapObjects" "NumGC" "MaxRSS")

for svc in "${SERVICES[@]}"; do
  port="${svc##*:}"
  name="${svc%%:*}"
  HEAP_DATA["$name"]=$(fetch_heap "$port")
  GOR_DATA["$name"]=$(fetch_goroutines_raw "$port")

  if [[ -z "${HEAP_DATA["$name"]}" ]]; then
    for field in "${FIELDS[@]}"; do
      DATA["$name:$field"]="unreachable"
    done
    continue
  fi

  DATA["$name:Goroutines"]=$(extract_goroutine_count "${GOR_DATA["$name"]}")
  DATA["$name:HTTP idle"]=$(extract_idle_http "${GOR_DATA["$name"]}")
  DATA["$name:HeapAlloc"]=$(extract_stat "${HEAP_DATA["$name"]}" "HeapAlloc")
  DATA["$name:HeapInuse"]=$(extract_stat "${HEAP_DATA["$name"]}" "HeapInuse")
  DATA["$name:Sys"]=$(extract_stat "${HEAP_DATA["$name"]}" "Sys")
  DATA["$name:HeapObjects"]=$(extract_stat "${HEAP_DATA["$name"]}" "HeapObjects")
  DATA["$name:NumGC"]=$(extract_stat "${HEAP_DATA["$name"]}" "NumGC")
  DATA["$name:MaxRSS"]=$(extract_stat "${HEAP_DATA["$name"]}" "MaxRSS")
done

# --- Report ---
echo ""
echo "========================================"
echo "  pprof Report - $(date '+%Y-%m-%d %H:%M:%S')"
echo "  Host: localhost"
echo "========================================"

printf "\n  %-16s" ""
for svc in "${SERVICES[@]}"; do
  name="${svc%%:*}"
  port="${svc##*:}"
  printf "%-20s" "$name (:$port)"
done
echo ""
printf "  %-16s" ""
for svc in "${SERVICES[@]}"; do
  printf "%-20s" "-------------------"
done
echo ""

for field in "${FIELDS[@]}"; do
  printf "  %-16s" "$field"
  for svc in "${SERVICES[@]}"; do
    name="${svc%%:*}"
    val="${DATA["$name:$field"]:-n/a}"
    if [[ "$val" == "unreachable" ]]; then
      printf "%-20s" "unreachable"
    elif [[ "$field" == "HeapAlloc" || "$field" == "HeapInuse" || "$field" == "Sys" || "$field" == "MaxRSS" ]]; then
      printf "%-20s" "$(human_bytes "$val")"
    elif [[ "$field" == "HeapObjects" ]]; then
      printf "%-20s" "$(human_count "$val")"
    else
      printf "%-20s" "$val"
    fi
  done
  echo ""
done

echo ""
echo "  --- Verdict ---"
has_issues=false
reachable_count=0
for svc in "${SERVICES[@]}"; do
  name="${svc%%:*}"
  val="${DATA["$name:HeapAlloc"]:-0}"
  if [[ "$val" == "unreachable" ]]; then
    echo "  ?  $name: unreachable - profiler disabled or service down"
    has_issues=true
    continue
  fi
  reachable_count=$((reachable_count + 1))
  if [[ -n "$val" ]] && (( val > 104857600 )); then
    echo "  !  $name: HeapAlloc > 100 MB - investigate"
    has_issues=true
  fi
  gor="${DATA["$name:Goroutines"]:-0}"
  if [[ -n "$gor" ]] && (( gor > 200 )); then
    echo "  !  $name: $gor goroutines - possible goroutine leak"
    has_issues=true
  fi
done
if (( reachable_count == 0 )); then
  echo "  No services reachable. Check APP_PROFILER_ENABLED and port mappings."
elif [[ "$has_issues" == false ]]; then
  echo "  All processes healthy."
fi
echo ""
