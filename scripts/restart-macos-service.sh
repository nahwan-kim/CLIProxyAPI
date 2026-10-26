#!/bin/sh

set -eu

label="${CLIPROXY_LAUNCHD_LABEL:-com.9ooniq.cliproxyapi}"
plist="${CLIPROXY_LAUNCHD_PLIST:-$HOME/Library/LaunchAgents/$label.plist}"
host="${CLIPROXY_HOST:-127.0.0.1}"
port="${CLIPROXY_PORT:-8317}"
timeout="${CLIPROXY_RESTART_TIMEOUT:-30}"
domain="gui/$(id -u)"
service="$domain/$label"

die() {
  printf '%s\n' "error: $*" >&2
  exit 1
}

service_loaded() {
  launchctl print "$service" >/dev/null 2>&1
}

service_running() {
  launchctl print "$service" 2>/dev/null |
    grep -q '^[[:space:]]*state = running$'
}

port_listening() {
  nc -z "$host" "$port" >/dev/null 2>&1
}

wait_until() {
  description=$1
  predicate=$2
  elapsed=0

  while ! "$predicate"; do
    if [ "$elapsed" -ge "$timeout" ]; then
      die "timed out waiting for $description after ${timeout}s"
    fi
    sleep 1
    elapsed=$((elapsed + 1))
  done
}

service_unloaded() {
  ! service_loaded
}

port_released() {
  ! port_listening
}

[ -f "$plist" ] || die "launchd plist not found: $plist"
plutil -lint "$plist" >/dev/null || die "invalid launchd plist: $plist"
case "$timeout" in
  ''|*[!0-9]*) die "CLIPROXY_RESTART_TIMEOUT must be a positive integer" ;;
  0) die "CLIPROXY_RESTART_TIMEOUT must be greater than zero" ;;
esac

program=$(/usr/libexec/PlistBuddy -c 'Print :ProgramArguments:0' "$plist" 2>/dev/null || true)
[ -n "$program" ] || die "ProgramArguments[0] is missing from $plist"
[ -x "$program" ] || die "configured executable is not executable: $program"

if service_loaded; then
  if ! launchctl bootout "$service"; then
    printf '%s\n' "warning: bootout returned an error; waiting for launchd state to settle" >&2
  fi
fi

wait_until "launchd service removal" service_unloaded
wait_until "$host:$port to be released" port_released

attempt=1
while ! launchctl bootstrap "$domain" "$plist"; do
  if service_loaded; then
    break
  fi
  if [ "$attempt" -ge "$timeout" ]; then
    die "launchctl bootstrap failed after $attempt attempts"
  fi
  sleep 1
  attempt=$((attempt + 1))
done

wait_until "launchd service startup" service_running
wait_until "$host:$port to accept connections" port_listening

state=$(launchctl print "$service" | awk -F ' = ' '/^[[:space:]]*state = / { print $2; exit }')
[ "$state" = "running" ] || die "service is loaded but not running (state: ${state:-unknown})"

status=$(curl -sS --max-time 5 -o /dev/null -w '%{http_code}' "http://$host:$port/v1/models" || true)
case "$status" in
  200|401) ;;
  *) die "health check returned HTTP ${status:-000}" ;;
esac

printf '%s\n' "ok: $service is running on $host:$port (HTTP $status)"
