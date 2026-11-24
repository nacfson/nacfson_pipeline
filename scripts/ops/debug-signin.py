#!/usr/bin/env python3
import argparse
import datetime as dt
import http.cookiejar
import json
import os
import re
import shlex
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from collections import Counter, defaultdict

NS = "identity"
KC = "deploy/keycloak"
KC_C = "keycloak"
GW = "deploy/gateway"
PG = "postgres-0"
TRACE_LEVEL = ("INFO,org.keycloak.events:DEBUG,org.keycloak.protocol.oidc:DEBUG,"
               "org.keycloak.services.managers:DEBUG,org.keycloak.authentication:DEBUG,"
               "org.keycloak.models.sessions.infinispan:DEBUG")
SECRET_MAP = {
    "gateway-client": "gateway_client_secret",
    "session-revocation-client": "session_revocation_secret",
    "project-pn": "project_pn_client_secret",
    "project-health": "project_health_client_secret",
}
NO_SESSION = ("", "null")
PROBE_CLIENT = "debug-signin-probe"
EVENT_RE = re.compile(r'(\w+)="([^"]*)"')
TS_RE = re.compile(r"^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}),(\d{3})")
LOGIN_EVENTS = ("LOGIN", "IDENTITY_PROVIDER_LOGIN")
SUCCESS_EVENTS = ("LOGIN", "CODE_TO_TOKEN")

ERROR_MEANING = {
    ("invalid_client_credentials", None): ("SECRET", "client secret in Keycloak DB differs from OpenBao (TROUBLESHOOTING §5)"),
    ("user_session_not_found", "null"): ("LINK", "login session found, code valid, gateway-client link missing (race or lifespan edge)"),
    ("user_session_not_found", "set"): ("ACTIVE", "session existed but was no longer active (idle or 10h max)"),
    ("invalid_code", None): ("CODE", "code reused, double callback, session deleted, or redirect_uri mismatch"),
    ("expired_code", None): ("SLOW", "callback arrived after accessCodeLifespan (slow hop or CPU throttling)"),
    ("code_verifier", None): ("PKCE", "PKCE verifier mismatch"),
    ("invalid_code_verifier", None): ("PKCE", "PKCE verifier mismatch"),
}

RESULTS = []
STATE = {"break_glass": None, "traefik": None}


def report(section, status, check, detail=""):
    RESULTS.append((section, status, check, detail))
    mark = status
    if sys.stdout.isatty():
        mark = {"PASS": "\033[32mPASS\033[0m", "FAIL": "\033[31mFAIL\033[0m",
                "WARN": "\033[33mWARN\033[0m", "INFO": "\033[36mINFO\033[0m"}.get(status, status)
    print(f"  [{mark}] {check}" + (f" — {detail}" if detail else ""))


def header(title):
    print(f"\n=== {title} ===")


def kubectl_base():
    return shlex.split(os.environ.get("KUBECTL", "kubectl"))


def kube(args, stdin=None, timeout=90, write=False):
    base = kubectl_base()
    if write and STATE["break_glass"]:
        args = [f"--as=break-glass:{STATE['break_glass']}", "--as-group=platform:break-glass"] + args
    if base and os.path.basename(base[0]) == "ssh":
        args = [shlex.quote(x) for x in args]
    try:
        p = subprocess.run(base + args, input=stdin, capture_output=True, text=True, timeout=timeout)
        return p.returncode, p.stdout, p.stderr
    except subprocess.TimeoutExpired:
        return 124, "", "timeout"
    except FileNotFoundError as e:
        return 127, "", str(e)


def kube_json(args):
    rc, out, _ = kube(args + ["-o", "json"])
    if rc != 0:
        return None
    try:
        return json.loads(out)
    except json.JSONDecodeError:
        return None


def psql(sql):
    rc, out, err = kube(["exec", "-n", NS, PG, "-c", "postgres", "--", "psql", "-U", "postgres", "-d", "keycloak",
                         "-At", "-F", "\t", "-c", sql])
    if rc != 0:
        return None, err.strip()
    return [line.split("\t") for line in out.splitlines() if line], ""


def kc_bash(script, timeout=90):
    return kube(["exec", "-n", NS, KC, "-c", KC_C, "--", "bash", "-c", script], timeout=timeout)


def container_env(deploy_json, name):
    for c in deploy_json["spec"]["template"]["spec"]["containers"]:
        if c["name"] == name:
            return {e["name"]: e.get("value") for e in c.get("env", [])}, c
    return {}, {}


def traefik_target(a):
    if STATE["traefik"]:
        return STATE["traefik"]
    candidates = [a.traefik_ns] if a.traefik_ns else ["platform", "kube-system"]
    for ns in candidates:
        rc, _, _ = kube(["get", "deploy", "traefik", "-n", ns, "-o", "name"])
        if rc == 0:
            STATE["traefik"] = (ns, "deploy/traefik")
            return STATE["traefik"]
    STATE["traefik"] = (candidates[0], "deploy/traefik")
    return STATE["traefik"]


def start_url(a):
    return f"https://{a.auth_host}/oauth/start?return={urllib.parse.quote(a.return_url, safe='')}"


def on_auth_host(url, host):
    p = urllib.parse.urlparse(url)
    return p.scheme == "https" and (p.hostname or "").lower() == host.lower()


def parse_ts(line):
    m = TS_RE.match(line)
    if not m:
        return None
    return dt.datetime.strptime(m.group(1), "%Y-%m-%d %H:%M:%S").replace(
        microsecond=int(m.group(2)) * 1000, tzinfo=dt.timezone.utc)


def parse_event(line):
    if "org.keycloak.events" not in line or 'type="' not in line:
        return None
    ev = dict(EVENT_RE.findall(line))
    if ev.get("clientId") == PROBE_CLIENT:
        return None
    ev["_ts"] = parse_ts(line)
    ev["_line"] = line.rstrip()
    return ev


def classify(ev):
    err = ev.get("error", "")
    uid = "null" if ev.get("userId", "null") in NO_SESSION else "set"
    for key in ((err, uid), (err, None)):
        if key in ERROR_MEANING:
            return ERROR_MEANING[key]
    return ("OTHER", f"unmapped error '{err}'")


def repo_realm_limits():
    path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "deploy", "platform", "identity", "keycloak-realm-config.yaml")
    try:
        with open(path, encoding="utf-8") as f:
            text = f.read()
    except OSError:
        return 1800, 36000
    idle = re.search(r'"ssoSessionIdleTimeout":\s*(\d+)', text)
    mx = re.search(r'"ssoSessionMaxLifespan":\s*(\d+)', text)
    return int(idle.group(1)) if idle else 1800, int(mx.group(1)) if mx else 36000


PROBE_SH = r'''
set +e
T=""; command -v timeout >/dev/null 2>&1 && T="timeout 8"
probe() {
  local name=$1 host=$2 port=$3 req=$4 t0=$EPOCHREALTIME out
  out=$($T bash -c 'exec 3<>"/dev/tcp/$1/$2" || exit 9; printf "%b" "$3" >&3; while IFS= read -r l <&3 || [ -n "$l" ]; do printf "%s\n" "${l%$'"'"'\r'"'"'}"; done' _ "$host" "$port" "$req" 2>/dev/null)
  local t1=$EPOCHREALTIME
  local ms=$(( (10#${t1/./} - 10#${t0/./}) / 1000 ))
  local first=${out%%$'\n'*}; local code=${first#* }; code=${code%% *}
  [[ $code =~ ^[0-9]{3}$ ]] || code=0
  local body=${out##*$'\n'}
  printf 'RESULT\t%s\t%s\t%s\t%s\n' "$name" "$code" "$ms" "${body:0:4000}"
}
tcp() {
  local t0=$EPOCHREALTIME
  if $T bash -c 'exec 3<>"/dev/tcp/$1/443"' _ "$1" 2>/dev/null; then s=OK; else s=FAIL; fi
  local t1=$EPOCHREALTIME
  printf 'TCP\t%s\t%s\t%s\n' "$1" "$s" "$(( (10#${t1/./} - 10#${t0/./}) / 1000 ))"
}
probe kc-token keycloak-service.identity.svc 8080 "GET /realms/platform/protocol/openid-connect/token HTTP/1.0\r\nHost: keycloak-service.identity.svc\r\n\r\n"
probe kc-discovery keycloak-service.identity.svc 8080 "GET /realms/platform/.well-known/openid-configuration HTTP/1.0\r\nHost: keycloak-service.identity.svc\r\n\r\n"
probe gw-healthz gateway.identity.svc 8080 "GET /healthz HTTP/1.0\r\nHost: gateway.identity.svc\r\n\r\n"
for h in oauth2.googleapis.com www.googleapis.com accounts.google.com; do tcp "$h"; done
'''


def cmd_network(a):
    header("Cause: firewall / network path")
    pols = []
    rc, out, err = kube(["get", "networkpolicy", "-n", NS, "-o", "name"])
    if rc != 0:
        report("network", "WARN", "NetworkPolicy list", err.strip()[:160])
    pols += out.split()
    rc2, out2, _ = kube(["get", "ciliumnetworkpolicy", "-n", NS, "-o", "name"])
    if rc2 == 0:
        pols += out2.split()
    if rc == 0:
        report("network", "INFO" if not pols else "WARN", f"Policies in {NS}",
               "none (traffic unrestricted)" if not pols else ", ".join(pols))

    rc, out, err = kc_bash(PROBE_SH, timeout=120)
    results = {}
    for line in out.splitlines():
        parts = line.split("\t", 4)
        if parts[0] == "RESULT" and len(parts) == 5:
            results[parts[1]] = (int(parts[2]), parts[3], parts[4])
        elif parts[0] == "TCP" and len(parts) == 4:
            report("network", "PASS" if parts[2] == "OK" else "FAIL", f"Keycloak pod -> {parts[1]}:443",
                   f"TCP {parts[2]} in {parts[3]}ms")
    if not results:
        report("network", "FAIL", "In-cluster probes from Keycloak pod", (err or "no output").strip()[:200])
    expect = {"kc-token": ("identity -> keycloak token endpoint (GET, no event)", {400, 405}),
              "kc-discovery": ("identity -> keycloak discovery", {200}),
              "gw-healthz": ("identity -> gateway /healthz", {200})}
    for key, (name, ok) in expect.items():
        if key not in results:
            continue
        code, ms, _ = results[key]
        report("network", "PASS" if code in ok else "FAIL", name, f"HTTP {code} in {ms}ms" if code else f"no response after {ms}ms")
    if "kc-discovery" in results and results["kc-discovery"][0] == 200:
        try:
            iss = json.loads(results["kc-discovery"][2]).get("issuer")
        except json.JSONDecodeError:
            iss = None
        want = f"https://{a.auth_host}/realms/platform"
        report("network", "PASS" if iss == want else "FAIL", "Keycloak issuer (KC_HOSTNAME_URL)", f"{iss} (want {want})")

    host = a.auth_host
    tns, _ = traefik_target(a)
    rc, lb, _ = kube(["get", "svc", "-n", tns, "traefik", "-o", "jsonpath={.status.loadBalancer.ingress[0].ip}"])
    try:
        ips = sorted({ai[4][0] for ai in socket.getaddrinfo(host, 443, proto=socket.IPPROTO_TCP)})
    except socket.gaierror as e:
        ips = []
        report("network", "FAIL", f"DNS {host}", str(e))
    if ips and lb:
        report("network", "PASS" if lb in ips and len(ips) == 1 else "FAIL", f"DNS {host}", f"resolves {ips}, Traefik LB ({tns}) {lb}")
    elif ips:
        report("network", "INFO", f"DNS {host}", f"resolves {ips}; no LoadBalancer IP on {tns}/traefik to compare")

    opener = urllib.request.build_opener(NoRedirect)
    status, loc, _ = fetch(opener, start_url(a))
    ok = status == 302 and on_auth_host(loc, host) and "/realms/platform/protocol/openid-connect/auth" in loc and "client_id=gateway-client" in loc
    report("network", "PASS" if ok else "FAIL", "Public /oauth/start redirect", f"HTTP {status} -> {loc[:90]}")

    for ds in ("anetd", "cilium"):
        d = kube_json(["get", "ds", ds, "-n", "kube-system"])
        if not d:
            continue
        sel = ",".join(f"{k}={v}" for k, v in d["spec"]["selector"].get("matchLabels", {}).items())
        rc, names, _ = kube(["get", "pods", "-n", "kube-system", "-l", sel, "-o", "name"])
        total, seen = 0, 0
        for pod in names.split():
            rc, out, _ = kube(["exec", "-n", "kube-system", pod, "-c", "cilium-agent", "--", "hubble", "observe",
                               "--namespace", NS, "--verdict", "DROPPED", "--last", "200", "-o", "compact"], timeout=40)
            if rc == 0:
                seen += 1
                total += len([x for x in out.splitlines() if x.strip()])
        if seen:
            report("network", "PASS" if total == 0 else "FAIL", f"Hubble dropped flows in {NS}", f"{total} dropped across {seen} agent pod(s) of ds/{ds}")
            break
    else:
        report("network", "INFO", "Hubble dropped flows", "hubble CLI not reachable; skipped")


def cmd_config(a):
    header("Cause: configuration drift")
    gw = kube_json(["get", GW, "-n", NS])
    kc = kube_json(["get", KC, "-n", NS])
    if not gw or not kc:
        report("config", "FAIL", "Read deployments", "kubectl get failed")
        return
    genv, _ = container_env(gw, "gateway")
    kenv, kcont = container_env(kc, "keycloak")
    pub = f"https://{a.auth_host}/realms/platform"
    rhost = urllib.parse.urlparse(a.return_url).hostname
    checks = [
        ("gateway KEYCLOAK_PUBLIC_URL", genv.get("KEYCLOAK_PUBLIC_URL") == pub, genv.get("KEYCLOAK_PUBLIC_URL")),
        ("gateway KEYCLOAK_ISSUER_URL internal", (genv.get("KEYCLOAK_ISSUER_URL") or "").endswith("/realms/platform"), genv.get("KEYCLOAK_ISSUER_URL")),
        ("gateway COOKIE_DOMAIN", a.auth_host.endswith((genv.get("COOKIE_DOMAIN") or "#").lstrip(".")), genv.get("COOKIE_DOMAIN")),
        ("gateway GATEWAY_CLIENT_ID", genv.get("GATEWAY_CLIENT_ID") == "gateway-client", genv.get("GATEWAY_CLIENT_ID")),
        ("gateway PROJECT_RETURN_HOSTS has return host", rhost in (genv.get("PROJECT_RETURN_HOSTS") or "").split(","), genv.get("PROJECT_RETURN_HOSTS")),
        ("keycloak KC_HOSTNAME_URL", kenv.get("KC_HOSTNAME_URL") == f"https://{a.auth_host}", kenv.get("KC_HOSTNAME_URL")),
        ("keycloak KC_PROXY_HEADERS", kenv.get("KC_PROXY_HEADERS") in ("xforwarded", "forwarded"), kenv.get("KC_PROXY_HEADERS")),
    ]
    for name, ok, val in checks:
        report("config", "PASS" if ok else "FAIL", name, str(val))
    report("config", "INFO", "keycloak KC_LOG_LEVEL", kenv.get("KC_LOG_LEVEL") or "default (success events hidden)")

    for label, d in (("keycloak", kc), ("gateway", gw)):
        spec, st = d["spec"].get("replicas", 1), d.get("status", {})
        ready = st.get("readyReplicas", 0)
        status = "FAIL" if label == "keycloak" and spec > 1 else ("PASS" if spec == 1 and ready == 1 else "WARN")
        report("config", status, f"{label} replicas", f"spec {spec}, ready {ready}"
               + (" — >1 Keycloak without a shared cache loses sessions between pods" if label == "keycloak" and spec > 1 else ""))
    lim = kcont.get("resources", {}).get("limits", {})
    report("config", "INFO", "keycloak limits", f"cpu {lim.get('cpu')}, memory {lim.get('memory')}, JAVA_OPTS_APPEND {kenv.get('JAVA_OPTS_APPEND')}")

    pods = kube_json(["get", "pods", "-n", NS, "-l", "app.kubernetes.io/name in (keycloak,gateway)"])
    for p in (pods or {}).get("items", []):
        for cs in p.get("status", {}).get("containerStatuses", []):
            last = cs.get("lastState", {}).get("terminated", {})
            st = "FAIL" if last.get("reason") == "OOMKilled" else ("WARN" if cs.get("restartCount", 0) else "PASS")
            report("config", st, f"pod {p['metadata']['name']}/{cs['name']}",
                   f"started {p['status'].get('startTime')}, restarts {cs.get('restartCount', 0)}"
                   + (f", last exit {last.get('reason')} at {last.get('finishedAt')}" if last else ""))
    rc, out, _ = kube(["top", "pod", "-n", NS, "--containers"])
    if rc == 0:
        for line in out.splitlines()[1:]:
            if "keycloak" in line or "gateway" in line:
                report("config", "INFO", "kubectl top", " ".join(line.split()))

    rows, err = psql("select sso_idle_timeout, sso_max_lifespan, sso_idle_timeout_remember_me, sso_max_lifespan_remember_me, "
                     "access_code_lifespan, access_code_lifespan_login, events_enabled from realm where name='platform'")
    if rows is None:
        rows, err = psql("select sso_idle_timeout, sso_max_lifespan, sso_idle_timeout_remember_me, sso_max_lifespan_remember_me, "
                         "'?', '?', '?' from realm where name='platform'")
    if not rows:
        report("config", "FAIL", "Read realm from PostgreSQL", err[:200])
        return
    idle, mx, idle_rm, mx_rm, acl, acl_login, ev = rows[0]
    want_idle, want_max = repo_realm_limits()
    report("config", "PASS" if str(want_idle) == idle else "FAIL", "realm ssoSessionIdleTimeout", f"live {idle}, repo {want_idle}")
    report("config", "PASS" if str(want_max) == mx else "FAIL", "realm ssoSessionMaxLifespan", f"live {mx}, repo {want_max}")
    report("config", "INFO", "realm remember-me / code lifespans",
           f"idleRM {idle_rm}, maxRM {mx_rm}, accessCodeLifespan {acl}s, loginLifespan {acl_login}s, eventsStored {ev}")
    rows, _ = psql("select ra.name, ra.value from realm_attribute ra join realm r on r.id=ra.realm_id "
                   "where r.name='platform' and (ra.name ilike 'clientSession%' or ra.name ilike '%session%idle%')")
    report("config", "PASS" if not rows else "WARN", "realm client-session overrides", str(rows or "none"))
    rows, _ = psql("select c.enabled, c.standard_flow_enabled, c.public_client from client c join realm r on r.id=c.realm_id "
                   "where r.name='platform' and c.client_id='gateway-client'")
    if rows:
        en, sf, pubc = rows[0]
        report("config", "PASS" if en == "t" and sf == "t" and pubc == "f" else "FAIL", "gateway-client flags",
               f"enabled {en}, standardFlow {sf}, public {pubc}")
    rows, _ = psql("select ru.value from redirect_uris ru join client c on c.id=ru.client_id join realm r on r.id=c.realm_id "
                   "where r.name='platform' and c.client_id='gateway-client'")
    uris = [r[0] for r in rows or []]
    want = f"https://{a.auth_host}/oauth/callback"
    report("config", "PASS" if want in uris else "FAIL", "gateway-client redirect URI", f"{want} {'present' if want in uris else 'MISSING'}")
    rows, _ = psql("select ca.name, ca.value from client_attributes ca join client c on c.id=ca.client_id join realm r on r.id=c.realm_id "
                   "where r.name='platform' and c.client_id='gateway-client' and ca.name like 'client.session%'")
    report("config", "PASS" if not rows else "WARN", "gateway-client session overrides", str(rows or "none"))


SECRET_SH = r'''
for f in "$@"; do
  p="/opt/keycloak/conf/secrets/$f"
  if [ ! -r "$p" ]; then echo "MISSING $f"; continue; fi
  if command -v sha256sum >/dev/null 2>&1; then h=$(sha256sum < "$p"); h=${h%% *}
  elif command -v openssl >/dev/null 2>&1; then h=$(openssl dgst -sha256 -r < "$p"); h=${h%% *}
  else echo "NOTOOL $f"; continue; fi
  echo "HASH $f $h"
done
'''


def cmd_secrets(a):
    header("Cause: client secret drift (OpenBao file vs Keycloak DB)")
    rc, out, err = kube(["exec", "-n", NS, KC, "-c", KC_C, "--", "bash", "-c", SECRET_SH, "_"] + list(SECRET_MAP.values()))
    file_hash, problems = {}, {}
    for line in out.splitlines():
        parts = line.split()
        if len(parts) == 3 and parts[0] == "HASH":
            file_hash[parts[1]] = parts[2]
        elif len(parts) == 2:
            problems[parts[1]] = parts[0]
    if rc != 0 and not file_hash:
        report("secrets", "FAIL", "Hash secret files in Keycloak pod", err.strip()[:200])
        return
    rows, err = psql("select c.client_id, encode(sha256(convert_to(c.secret,'UTF8')),'hex') from client c join realm r on r.id=c.realm_id "
                     "where r.name='platform' and c.client_id in ('gateway-client','session-revocation-client','project-pn','project-health')")
    db_hash = {r[0]: r[1] for r in rows or [] if len(r) == 2}
    for client, f in SECRET_MAP.items():
        fh, db = file_hash.get(f), db_hash.get(client)
        if f in problems:
            report("secrets", "WARN", client, "file missing in pod" if problems[f] == "MISSING" else "no sha256sum/openssl in pod; skipped (secrets never leave the pod)")
            continue
        if not fh or not db:
            report("secrets", "WARN", client, "could not read one side")
            continue
        report("secrets", "PASS" if db == fh else "FAIL", client, f"file {fh[:10]}…, db {db[:10]}… ({'match' if db == fh else 'DRIFT'})")


def cmd_history(a):
    header(f"Evidence: Keycloak / gateway / Traefik logs (since {a.since})")
    rc, out, err = kube(["logs", "-n", NS, KC, "-c", KC_C, f"--since={a.since}"], timeout=120)
    if rc != 0:
        report("history", "FAIL", "Keycloak logs", err.strip()[:200])
        return
    lines = out.splitlines()
    events = [e for e in (parse_event(l) for l in lines) if e]
    for (t, e), n in sorted(Counter((e.get("type"), e.get("error", "")) for e in events).items()):
        report("history", "INFO", f"event {t}" + (f" error={e}" if e else ""), f"{n}x")
    if not any(e.get("type") in SUCCESS_EVENTS for e in events):
        report("history", "INFO", "Success events", "none visible: run `trace-on` to log LOGIN/CODE_TO_TOKEN")
    for e in events:
        if e.get("type") == "CODE_TO_TOKEN_ERROR":
            tag, meaning = classify(e)
            report("history", "FAIL", f"{e['_ts']} CODE_TO_TOKEN_ERROR [{tag}]",
                   f"session {e.get('sessionId')}, userId {e.get('userId')}: {meaning}")
    used = [l for l in lines if "already used for userSession" in l or "bound to a different session" in l]
    report("history", "PASS" if not used else "FAIL", "'Code already used' warnings", f"{len(used)}")
    infra = [l for l in lines if re.search(r"ISPN\d+|OutOfMemory|GC overhead|Connection refused|JDBC", l)]
    report("history", "PASS" if not infra else "WARN", "Infinispan / memory / DB errors", f"{len(infra)}" + (f": {infra[-1][:160]}" if infra else ""))

    rc, out, _ = kube(["logs", "-n", NS, GW, "-c", "gateway", f"--since={a.since}"])
    report("history", "INFO", "gateway log lines", f"{len(out.splitlines())} (gateway does not log exchange failures)")

    tns, tdep = traefik_target(a)
    rc, out, _ = kube(["logs", "-n", tns, tdep, f"--since={a.since}"], timeout=120)
    cb = [l for l in out.splitlines() if "/oauth/callback" in l]
    st = [l for l in out.splitlines() if "/oauth/start" in l]
    if not cb and not st:
        report("history", "INFO", f"Traefik access log ({tns})", "no /oauth lines: access log is off by default, duplicate/burst checks skipped")
        return
    codes = Counter(m.group(1) for l in cb for m in [re.search(r"code=([^&\s\"]+)", l)] if m)
    dup = {c: n for c, n in codes.items() if n > 1}
    report("history", "PASS" if not dup else "FAIL", "Duplicate callbacks with same code", f"{len(dup)}")
    report("history", "INFO", "Traefik /oauth/start vs /oauth/callback", f"{len(st)} starts, {len(cb)} callbacks")
    starts = sorted(t for t in (traefik_ts(l) for l in st) if t)
    bursts = sum(1 for x, y in zip(starts, starts[1:]) if (y - x).total_seconds() < 2)
    report("history", "PASS" if bursts == 0 else "WARN", "Parallel /oauth/start bursts (<2s apart)", f"{bursts}")


def traefik_ts(line):
    m = re.search(r"\[(\d{2}/\w{3}/\d{4}:\d{2}:\d{2}:\d{2}) ([+-]\d{4})\]", line)
    if m:
        return dt.datetime.strptime(" ".join(m.groups()), "%d/%b/%Y:%H:%M:%S %z")
    m = re.search(r'"(?:StartUTC|time)":"([^"]+)"', line)
    if m:
        raw = re.sub(r"(\.\d{6})\d+", r"\1", m.group(1).replace("Z", "+00:00"))
        try:
            return dt.datetime.fromisoformat(raw)
        except ValueError:
            return None
    return None


SESSIONS_SH = r'''
set -euo pipefail
D="/tmp/kcadm-$$-$RANDOM"; mkdir -m 700 "$D"; trap 'rm -rf "$D"' EXIT
export HOME="$D"; C="$D/kcadm.config"; K=/opt/keycloak/bin/kcadm.sh
"$K" config credentials --config "$C" --server http://localhost:8080 --realm platform \
  --client session-revocation-client --secret "$(cat /opt/keycloak/conf/secrets/session_revocation_secret)" >/dev/null
Q=(-q max=200); [ -n "${1:-}" ] && Q=(-q "email=$1")
echo "@@USERS"
"$K" get users --config "$C" -r platform "${Q[@]}" --fields id,username,email
for id in $("$K" get users --config "$C" -r platform "${Q[@]}" --fields id --format csv --noquotes); do
  echo "@@SESSIONS $id"
  "$K" get "users/$id/sessions" --config "$C" -r platform
done
'''


def parse_sessions_output(out):
    users, sessions, cur, buf = [], {}, None, []

    def flush():
        if cur is None:
            return
        data = json.loads("\n".join(buf) or "[]")
        if cur == "@@USERS":
            users.extend(data)
        else:
            sessions[cur.split(" ", 1)[1]] = data

    for line in out.splitlines():
        if line.startswith("@@"):
            flush()
            cur, buf = line.strip(), []
        elif cur is not None:
            buf.append(line)
    flush()
    return users, sessions


def cmd_sessions(a):
    header("Cause: 10h max lifespan / missing gateway-client link (live sessions)")
    rc, out, err = kube(["exec", "-n", NS, KC, "-c", KC_C, "--", "bash", "-c", SESSIONS_SH, "_", a.user], timeout=240)
    if rc != 0:
        report("sessions", "FAIL", "kcadm as session-revocation-client", (err or out).strip()[-240:])
        return
    try:
        users, sessions = parse_sessions_output(out)
    except json.JSONDecodeError as e:
        report("sessions", "FAIL", "Parse kcadm output", str(e))
        return
    idle_max, life_max = repo_realm_limits()
    now, total = time.time(), 0
    for u in users:
        for s in sessions.get(u["id"], []):
            total += 1
            age = now - s["start"] / 1000
            idle = now - s["lastAccess"] / 1000
            clients = set((s.get("clients") or {}).values())
            left = life_max - age
            status = "WARN" if left < 1800 or idle > idle_max - 300 or "gateway-client" not in clients else "PASS"
            report("sessions", status, f"{u.get('email') or u.get('username')} session {s['id'][:8]}",
                   f"age {age/3600:.2f}h (max {life_max/3600:.0f}h, {max(left, 0)/60:.0f} min left), idle {idle/60:.1f} min, "
                   f"clients {sorted(clients) or 'NONE'}" + ("" if "gateway-client" in clients else " — gateway-client link missing"))
    report("sessions", "INFO", "Active sessions", f"{total} for {len(users)} user(s)")


def require_break_glass(what):
    if STATE["break_glass"]:
        return True
    report("trace", "FAIL", what, "identity and flux-system are guarded by the manual-change policy: rerun with --break-glass <operator>")
    return False


def trace_off_steps():
    rc1, _, err1 = kube(["set", "env", KC, "-n", NS, "-c", KC_C, "KC_LOG_LEVEL-"], write=True)
    report("trace", "PASS" if rc1 == 0 else "FAIL", "Remove KC_LOG_LEVEL", err1.strip()[:200])
    rc2, _, err2 = kube(["patch", "kustomization", "identity", "-n", "flux-system", "--type", "merge", "-p", '{"spec":{"suspend":false}}'], write=True)
    report("trace", "PASS" if rc2 == 0 else "FAIL", "Resume Flux kustomization identity", err2.strip()[:200])
    kube(["annotate", "kustomization", "identity", "-n", "flux-system", "--overwrite",
          f"reconcile.fluxcd.io/requestedAt={dt.datetime.now(dt.timezone.utc).isoformat()}"], write=True)
    rc3, _, err3 = kube(["rollout", "status", KC, "-n", NS, "--timeout=600s"], timeout=660)
    report("trace", "PASS" if rc3 == 0 else "FAIL", "Keycloak rollout", err3.strip()[:200])
    return rc1 == 0 and rc2 == 0


def cmd_trace_on(a):
    header("Enable Keycloak debug trace (break-glass)")
    if not require_break_glass("trace-on"):
        return 2
    if not a.yes:
        ans = input("This suspends Flux 'identity', restarts Keycloak (all users sign in again), and ships DEBUG logs with "
                    "session IDs and emails to VictoriaLogs. Continue? [y/N] ")
        if ans.strip().lower() != "y":
            print("aborted")
            return 0
    rc, _, err = kube(["patch", "kustomization", "identity", "-n", "flux-system", "--type", "merge", "-p", '{"spec":{"suspend":true}}'], write=True)
    report("trace", "PASS" if rc == 0 else "FAIL", "Suspend Flux kustomization identity", err.strip()[:200])
    if rc != 0:
        return 1
    rc, _, err = kube(["set", "env", KC, "-n", NS, "-c", KC_C, f"KC_LOG_LEVEL={TRACE_LEVEL}"], write=True)
    report("trace", "PASS" if rc == 0 else "FAIL", "Set KC_LOG_LEVEL", err.strip()[:200])
    if rc != 0:
        print("Rolling back: resuming Flux.")
        trace_off_steps()
        return 1
    rc, _, err = kube(["rollout", "status", KC, "-n", NS, "--timeout=600s"], timeout=660)
    report("trace", "PASS" if rc == 0 else "FAIL", "Keycloak rollout", err.strip()[:200])
    if rc != 0:
        print("Rollout failed. Rolling back to the Git state.")
        trace_off_steps()
        return 1
    print("\nTrace is ON and Flux 'identity' is SUSPENDED. Next: `capture`, reproduce, `analyze`, then ALWAYS `trace-off`.")
    return 0


def cmd_trace_off(a):
    header("Disable Keycloak debug trace")
    if not require_break_glass("trace-off"):
        return 2
    return 0 if trace_off_steps() else 1


def cmd_capture(a):
    out_dir = a.out or tempfile.mkdtemp(prefix="signin-trace-")
    os.makedirs(out_dir, mode=0o700, exist_ok=True)
    tns, tdep = traefik_target(a)
    targets = {
        "keycloak.log": ["logs", "-f", "-n", NS, KC, "-c", KC_C, "--since=1s"],
        "gateway.log": ["logs", "-f", "-n", NS, GW, "-c", "gateway", "--since=1s"],
        "traefik.log": ["logs", "-f", "-n", tns, tdep, "--since=1s"],
    }
    procs = []
    for name, args in targets.items():
        fd = os.open(os.path.join(out_dir, name), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        f = os.fdopen(fd, "w", encoding="utf-8")
        procs.append((subprocess.Popen(kubectl_base() + args, stdout=f, stderr=subprocess.STDOUT), f))
    print(f"Capturing into {out_dir}/ (mode 0600; contains session IDs, emails, codes — delete when done).")
    print("Reproduce the sign-in now, press Ctrl-C to stop.")
    try:
        if a.seconds:
            time.sleep(a.seconds)
        else:
            signal.pause()
    except KeyboardInterrupt:
        pass
    finally:
        for p, f in procs:
            p.terminate()
            try:
                p.wait(timeout=10)
            except subprocess.TimeoutExpired:
                p.kill()
            f.close()
    print(f"Done. Next: {sys.argv[0]} analyze {out_dir}/keycloak.log")
    return 0


def analyze_lines(lines, window=5.0, life_max=36000):
    events = [e for e in (parse_event(l) for l in lines) if e]
    by_session = defaultdict(list)
    orphans = []
    for e in events:
        sid = e.get("sessionId", "")
        if sid in NO_SESSION:
            if e.get("type") == "CODE_TO_TOKEN_ERROR":
                orphans.append(e)
        else:
            by_session[sid].append(e)
    used = [l for l in lines if "already used for userSession" in l]
    findings = []
    for e in orphans:
        tag, meaning = classify(e)
        findings.append({"ts": e["_ts"], "session": "none", "tag": tag, "meaning": meaning, "verdicts": [meaning],
                         "timeline": [(e["_ts"], e.get("type"), e.get("clientId"), e.get("error", ""))], "context": []})
    for sid, evs in by_session.items():
        for f in [e for e in evs if e.get("type") == "CODE_TO_TOKEN_ERROR"]:
            tag, meaning = classify(f)
            before = [e for e in evs if e["_ts"] and f["_ts"] and e["_ts"] <= f["_ts"]]
            logins = [e for e in before if e.get("type") in LOGIN_EVENTS and e.get("clientId") == f.get("clientId")]
            recent = [e for e in logins if (f["_ts"] - e["_ts"]).total_seconds() <= window]
            first = min((e["_ts"] for e in evs if e["_ts"]), default=None)
            age = (f["_ts"] - first).total_seconds() if first and f["_ts"] else None
            verdicts = []
            if tag == "LINK" and len(recent) >= 2:
                verdicts.append(f"RACE: {len(recent)} sign-ins for {f.get('clientId')} on this session within {window:.0f}s")
            if age is not None and age >= life_max - 120:
                verdicts.append(f"LIFESPAN: session first seen {age/3600:.2f}h before failure")
            if any(f"'{sid}'" in l for l in used):
                verdicts.append("REUSE: Keycloak logged 'Code already used' for this session")
            if not verdicts:
                verdicts.append("UNPROVEN: see debug context below" if tag == "LINK" else meaning)
            findings.append({"ts": f["_ts"], "session": sid, "tag": tag, "meaning": meaning, "verdicts": verdicts,
                             "timeline": [(e["_ts"], e.get("type"), e.get("clientId"), e.get("error", "")) for e in evs],
                             "context": [l.rstrip() for l in lines if sid in l and "org.keycloak.events" not in l][-40:]})
    return events, findings


def cmd_analyze(a):
    header(f"Analyze {a.file}")
    with open(a.file, encoding="utf-8", errors="replace") as f:
        lines = f.readlines()
    events, findings = analyze_lines(lines, a.window, repo_realm_limits()[1])
    counts = Counter(e.get("type") for e in events)
    report("analyze", "INFO", "Events parsed", ", ".join(f"{k} {v}" for k, v in counts.most_common()) or "none")
    if not any(counts.get(t) for t in SUCCESS_EVENTS):
        report("analyze", "WARN", "Success events missing", "trace not enabled: run `trace-on` before capturing")
    if not findings:
        report("analyze", "PASS", "CODE_TO_TOKEN_ERROR", "none in this capture")
    for fd in findings:
        report("analyze", "FAIL", f"{fd['ts']} session {fd['session'][:8]} [{fd['tag']}]", "; ".join(fd["verdicts"]))
        print("    timeline:")
        for ts, t, c, e in fd["timeline"]:
            print(f"      {ts}  {t:<28} {c or '':<22} {e}")
        if fd["context"]:
            print("    debug lines mentioning this session:")
            for l in fd["context"]:
                print(f"      {l[:220]}")
    return 0


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def fetch(opener, url, headers=None, timeout=20):
    try:
        req = urllib.request.Request(url, headers=headers or {})
        with opener.open(req, timeout=timeout) as r:
            return r.status, r.headers.get("Location", ""), r.read(300).decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, e.headers.get("Location", ""), e.read(300).decode("utf-8", "replace")
    except Exception as e:
        return 0, "", str(e)


def barrier_wait(b):
    if b:
        try:
            b.wait(timeout=60)
        except threading.BrokenBarrierError:
            pass


def race_flow(idx, a, cookie, barrier, r):
    jar = http.cookiejar.CookieJar()
    gw = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar), NoRedirect)
    kc = urllib.request.build_opener(NoRedirect)
    waits = 0
    try:
        st, loc, _ = fetch(gw, start_url(a))
        r["start"] = st
        if st != 302 or not on_auth_host(loc, a.auth_host) or "/protocol/openid-connect/auth" not in loc:
            r["error"] = f"/oauth/start returned {st} -> {loc[:80]} (cookie withheld: not https://{a.auth_host})"
            return
        barrier_wait(barrier)
        waits += 1
        t0 = time.time()
        st, cb, _ = fetch(kc, loc, headers={"Cookie": cookie})
        r["authorize"], r["authorize_ms"] = st, int((time.time() - t0) * 1000)
        if st != 302 or not on_auth_host(cb, a.auth_host) or "/oauth/callback" not in cb or "code=" not in cb:
            r["error"] = f"Keycloak did not issue a code silently (HTTP {st}, Location {cb[:80]}): SSO cookie expired?"
            r["sso_expired"] = True
            return
        barrier_wait(barrier)
        waits += 1
        t1 = time.time()
        st, _, body = fetch(gw, cb)
        r["callback"], r["callback_ms"], r["body"] = st, int((time.time() - t1) * 1000), body.strip()[:120]
        r["utc"] = dt.datetime.now(dt.timezone.utc).strftime("%H:%M:%S.%f")[:-3]
    except Exception as e:
        r["error"] = f"worker crashed: {e}"
    finally:
        while waits < 2:
            barrier_wait(barrier)
            waits += 1


def race_worker(idx, a, cookie, barrier, out):
    r = {"worker": idx}
    race_flow(idx, a, cookie, barrier, r)
    out.append(r)


def shared_auth_cookie(a, sso):
    gw = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()), NoRedirect)
    st, loc, _ = fetch(gw, start_url(a))
    if st != 302 or not on_auth_host(loc, a.auth_host):
        return None, f"/oauth/start returned {st} -> {loc[:80]}"
    req = urllib.request.Request(loc, headers={"Cookie": sso})
    try:
        with urllib.request.build_opener(NoRedirect).open(req, timeout=20) as r:
            return None, f"Keycloak answered {r.status} instead of a redirect: SSO cookie expired?"
    except urllib.error.HTTPError as e:
        sc = e.headers.get_all("Set-Cookie") or []
        e.close()
        shared = "; ".join(c.split(";")[0] for c in sc if c.startswith("AUTH_SESSION_ID"))
        return shared, "" if shared else "Keycloak issued no AUTH_SESSION_ID"
    except Exception as e:
        return None, str(e)


def cmd_race(a):
    header(f"Cause: parallel sign-in race ({a.mode}, {a.workers} workers x {a.rounds} rounds)")
    if a.workers < 1 or a.rounds < 1:
        report("race", "FAIL", "Arguments", "--workers and --rounds must be >= 1")
        return 2
    try:
        with open(a.sso_cookie_file, encoding="utf-8") as f:
            sso = f.read().strip()
    except OSError as e:
        report("race", "FAIL", "Read SSO cookie file", str(e))
        return 2
    if "KEYCLOAK_IDENTITY" not in sso:
        report("race", "FAIL", "SSO cookie file", "must contain KEYCLOAK_IDENTITY=... copied from the browser")
        return 2
    cookie = sso
    if a.shared_auth_session:
        shared, why = shared_auth_cookie(a, sso)
        if shared:
            cookie = f"{sso}; {shared}"
            report("race", "INFO", "Shared AUTH_SESSION_ID", "captured")
        else:
            report("race", "FAIL" if "expired" in why else "WARN", "Shared AUTH_SESSION_ID", why)
            if "expired" in why:
                return 2
    started = dt.datetime.now(dt.timezone.utc)
    fails = total = 0
    for rnd in range(1, a.rounds + 1):
        out = []
        if a.mode == "parallel":
            barrier = threading.Barrier(a.workers)
            ts = [threading.Thread(target=race_worker, args=(i, a, cookie, barrier, out)) for i in range(a.workers)]
            for t in ts:
                t.start()
            for t in ts:
                t.join()
        else:
            for i in range(a.workers):
                race_worker(i, a, cookie, None, out)
        for r in sorted(out, key=lambda x: x["worker"]):
            total += 1
            if "error" in r:
                report("race", "WARN", f"round {rnd} worker {r['worker']}", r["error"])
                if r.get("sso_expired"):
                    return 2
                continue
            bad = r.get("callback") != 302
            fails += bad
            report("race", "FAIL" if bad else "PASS", f"round {rnd} worker {r['worker']} @ {r['utc']} UTC",
                   f"authorize {r['authorize_ms']}ms, callback HTTP {r['callback']} {r['callback_ms']}ms" + (f" body {r['body']}" if bad else ""))
        time.sleep(a.pause)
    since = int((dt.datetime.now(dt.timezone.utc) - started).total_seconds()) + 30
    report("race", "FAIL" if fails else "PASS", f"Race {'REPRODUCED' if fails else 'not reproduced'}", f"{fails}/{total} callbacks failed")
    print(f"\nCorrelate: {sys.argv[0]} history --since {since}s")
    return 3 if fails else 0


def summary():
    header("Summary by cause")
    groups = {
        "Firewall / network": "network", "Configuration drift": "config", "Client secret drift": "secrets",
        "Log evidence": "history", "Session lifespan / link": "sessions", "Parallel sign-in race": "race",
        "Trace": "trace", "Trace analysis": "analyze",
    }
    for label, sec in groups.items():
        rows = [r for r in RESULTS if r[0] == sec]
        if not rows:
            continue
        f = sum(1 for r in rows if r[1] == "FAIL")
        w = sum(1 for r in rows if r[1] == "WARN")
        state = "SUSPECT" if f else ("CHECK" if w else "CLEAR")
        print(f"  {label:<26} {state:<8} ({f} fail, {w} warn, {len(rows)} checks)")


def build_parser():
    p = argparse.ArgumentParser(description="Diagnose the /oauth/callback 'Sign-in service unreachable' failure. See docs/debug-signin.md.")
    p.add_argument("--auth-host", default=os.environ.get("AUTH_HOST", "auth.nacfson.cloud"))
    p.add_argument("--return-url", default=os.environ.get("RETURN_URL", "https://pn.nacfson.cloud/"))
    p.add_argument("--traefik-ns", default=os.environ.get("TRAEFIK_NS", ""))
    p.add_argument("--break-glass", metavar="OPERATOR", default=os.environ.get("BREAK_GLASS_OPERATOR", ""))
    sub = p.add_subparsers(dest="cmd", required=True)
    sub.add_parser("network")
    sub.add_parser("config")
    sub.add_parser("secrets")
    h = sub.add_parser("history")
    h.add_argument("--since", default="24h")
    s = sub.add_parser("sessions")
    s.add_argument("--user", default="")
    al = sub.add_parser("all")
    al.add_argument("--since", default="24h")
    al.add_argument("--user", default="")
    t = sub.add_parser("trace-on")
    t.add_argument("--yes", action="store_true")
    sub.add_parser("trace-off")
    c = sub.add_parser("capture")
    c.add_argument("--out", default="")
    c.add_argument("--seconds", type=int, default=0)
    an = sub.add_parser("analyze")
    an.add_argument("file")
    an.add_argument("--window", type=float, default=5.0)
    r = sub.add_parser("race")
    r.add_argument("--sso-cookie-file", required=True)
    r.add_argument("--workers", type=int, default=2)
    r.add_argument("--rounds", type=int, default=5)
    r.add_argument("--mode", choices=["parallel", "sequential"], default="parallel")
    r.add_argument("--shared-auth-session", action="store_true")
    r.add_argument("--pause", type=float, default=1.0)
    return p


def main():
    a = build_parser().parse_args()
    STATE["break_glass"] = a.break_glass or None
    rc = 0
    if a.cmd == "all":
        for fn in (cmd_network, cmd_config, cmd_secrets, cmd_history, cmd_sessions):
            fn(a)
    else:
        rc = {"network": cmd_network, "config": cmd_config, "secrets": cmd_secrets, "history": cmd_history,
              "sessions": cmd_sessions, "trace-on": cmd_trace_on, "trace-off": cmd_trace_off,
              "capture": cmd_capture, "analyze": cmd_analyze, "race": cmd_race}[a.cmd](a) or 0
    summary()
    if rc:
        return rc
    return 1 if any(r[1] == "FAIL" for r in RESULTS) else 0


if __name__ == "__main__":
    sys.exit(main())
