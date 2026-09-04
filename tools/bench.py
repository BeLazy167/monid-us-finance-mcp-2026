"""Side-by-side benchmark: our Monid-backed gateway vs Financial Datasets.

Sends the SAME request to both gateways, measures wall-clock latency,
response size, record count and measured cost, then renders an HTML
comparison (ours left, Financial Datasets right).

Cost accounting:
  ours - read from the receipts ledger delta around each call, so it is the
         real measured Monid spend, never an estimate.
  FD   - $0.002 per request, their effective rate on included volume
         ($200/mo / 100,000 requests). Their overage rate is $0.010.

Usage:
  OURS_BASE=http://localhost:8111 MONID_KEY=... FD_API_KEY=... python3 bench.py
"""
import json, os, statistics, time, urllib.error, urllib.request, html, pathlib

OURS_BASE = os.environ.get("OURS_BASE", "http://localhost:8111")
FD_BASE = "https://api.financialdatasets.ai"
MONID_KEY = os.environ.get("MONID_KEY", "")
FD_KEY = os.environ.get("FD_API_KEY", "")
LEDGER = os.environ.get("RECEIPTS_PATH", "")
REPEATS = int(os.environ.get("REPEATS", "3"))

BROWSER_UA = ("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "
              "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")

# FD's effective per-request rate on included volume, and its overage rate.
FD_INCLUDED_RATE = 0.002
FD_OVERAGE_RATE = 0.010

CASES = [
    ("SEC filings",            "/filings?ticker=AAPL&limit=5",                              "filings"),
    ("Filing types catalog",   "/filings/types",                                            "filing_types"),
    ("Filings CIK universe",   "/filings/ciks",                                             "ciks"),
    ("Income statement",       "/financials/income-statements?ticker=AAPL&period=annual&limit=1", "income_statements"),
    ("Balance sheet",          "/financials/balance-sheets?ticker=AAPL&period=annual&limit=1",    "balance_sheets"),
    ("Cash flow",              "/financials/cash-flow-statements?ticker=AAPL&period=annual&limit=1", "cash_flow_statements"),
    ("Financial metrics",      "/financial-metrics/snapshot?ticker=AAPL",                   "snapshot"),
    ("Historical prices",      "/prices?ticker=AAPL&interval=day&interval_multiplier=1&start_date=2025-06-01&end_date=2025-06-30", "prices"),
    ("Price snapshot",         "/prices/snapshot?ticker=AAPL",                              "snapshot"),
    ("Market snapshot",        "/prices/snapshot/market",                                   "snapshots"),
    ("Company facts",          "/company/facts?ticker=AAPL",                                "company_facts"),
    ("Earnings",               "/earnings?ticker=AAPL&limit=2",                             "earnings"),
    ("News",                   "/news?ticker=AAPL&limit=5",                                 "news"),
    ("Insider trades",         "/insider-trades?ticker=AAPL&limit=5",                       "insider_trades"),
    ("Insider ownership",      "/insider-ownership?ticker=AAPL&limit=5",                    "insider_ownership"),
    ("Beneficial ownership",   "/beneficial-ownership?ticker=AAPL&limit=5",                 "beneficial_owners"),
    ("Institutional holdings", "/institutional-holdings?ticker=AAPL&limit=5",               "institutional_holdings"),
    ("Institutional investors","/institutional-holdings/investors?name=berk",               "investors"),
    ("Interest rates",         "/macro/interest-rates/snapshot",                            "interest_rates"),
    ("Interest rate banks",    "/macro/interest-rates/banks",                               "banks"),
    ("Segmented financials",   "/financials/segments?ticker=AAPL&period=annual&limit=1",    "segmented_financials"),
    ("KPI metrics",            "/kpi/metrics?ticker=DAL&limit=2",                           "kpi_metrics"),
    ("IPO registrations",      "/ipos?ticker=RDDT&limit=3",                                 "ipos"),
    ("Company facts CIKs",     "/company/facts/ciks",                                       "ciks"),
    ("Segments (income)",      "/financials/income-statements/segments?ticker=AAPL&period=annual&limit=1", "income_statement_segments"),
    ("Screener filters",       "/financials/search/screener/filters",                       "metrics"),
]


def ledger_lines():
    """Every receipt currently in the ledger, as parsed objects."""
    if not LEDGER or not os.path.exists(LEDGER):
        return []
    out = []
    with open(LEDGER) as fh:
        for line in fh:
            line = line.strip()
            if line:
                try:
                    out.append(json.loads(line))
                except Exception:
                    pass
    return out


def receipts_since(n):
    """The receipts written after index n: one per Monid call made.

    This is the actual provider route a request took. Each entry names the
    provider and endpoint, its measured cost, and the Monid run id, so the
    call chain behind any response is reconstructable rather than asserted.
    """
    return ledger_lines()[n:]


def fetch(base, path, key, ua=None):
    url = base + path
    headers = {"X-API-KEY": key}
    if ua:
        headers["User-Agent"] = ua
    req = urllib.request.Request(url, headers=headers)
    started = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=180) as r:
            body = r.read()
            status = r.status
    except urllib.error.HTTPError as e:
        body, status = e.read(), e.code
    except Exception as e:
        return {"status": 0, "elapsed": time.perf_counter() - started,
                "bytes": 0, "error": f"{type(e).__name__}: {e}", "json": None}
    return {"status": status, "elapsed": time.perf_counter() - started,
            "bytes": len(body), "error": None,
            "json": _safe_json(body)}


def _safe_json(body):
    try:
        return json.loads(body)
    except Exception:
        return None


def records_and_fields(payload, key):
    """Record count and the field set of the first record, for any envelope."""
    if not isinstance(payload, dict):
        return 0, []
    node = payload.get(key)
    if node is None:
        for k, v in payload.items():
            if k not in ("next_page_url", "resource") and isinstance(v, (list, dict)):
                node = v
                break
    if isinstance(node, list):
        if not node:
            return 0, []
        return len(node), (list(node[0].keys()) if isinstance(node[0], dict) else ["<scalar>"])
    if isinstance(node, dict):
        return 1, list(node.keys())
    return 0, []


def measure(base, path, key, ua, repeats):
    """Measure cold and warm latency separately.

    Our gateway holds a 5-minute TTL response cache, so a second identical
    request is served from memory. Reporting only a best-of would credit us
    with cache hits and read as a latency win we did not earn. The cold
    number is the first call after the cache is cold; warm is the immediate
    repeat. Both are reported, and the cold number is the honest headline.
    """
    first = fetch(base, path, key, ua)
    warm_runs = []
    last = first
    for _ in range(max(0, repeats - 1)):
        time.sleep(0.15)
        last = fetch(base, path, key, ua)
        warm_runs.append(last["elapsed"])
    result = dict(first)
    result["elapsed_cold"] = first["elapsed"]
    result["elapsed_warm"] = min(warm_runs) if warm_runs else None
    result["elapsed_best"] = first["elapsed"]
    result["json"] = first["json"] or last.get("json")
    return result


def main():
    rows = []
    for name, path, key in CASES:
        # Pace the run. Firing every case back to back rate-limits the
        # upstream and produces 502s that measure our burst behaviour
        # rather than each route's real latency.
        time.sleep(3.0)
        # Snapshot the ledger, make ONE cold call, then read exactly the
        # receipts that call produced. Warm repeats hit our response cache
        # and make no Monid call, so they must not be counted.
        before_n = len(ledger_lines())
        cold = fetch(OURS_BASE, path, MONID_KEY, None)
        hops = receipts_since(before_n)

        warm = None
        if REPEATS > 1:
            time.sleep(0.2)
            warm = fetch(OURS_BASE, path, MONID_KEY, None)

        ours = dict(cold)
        ours["elapsed_cold"] = cold["elapsed"]
        ours["elapsed_warm"] = warm["elapsed"] if warm else None

        fd = measure(FD_BASE, path, FD_KEY, BROWSER_UA, REPEATS) if FD_KEY else None

        o_n, o_fields = records_and_fields(ours["json"], key)
        f_n, f_fields = records_and_fields(fd["json"], key) if fd else (0, [])
        ours_cost = sum((h.get("measured_cost") or {}).get("value") or 0 for h in hops)

        rows.append({
            "name": name, "path": path, "key": key,
            "ours": ours, "fd": fd,
            "ours_records": o_n, "fd_records": f_n,
            "ours_fields": o_fields, "fd_fields": f_fields,
            "ours_cost": ours_cost,
            "hops": [{
                "provider": h.get("provider"),
                "endpoint": h.get("endpoint"),
                "cost": (h.get("measured_cost") or {}).get("value"),
                "run_id": h.get("run_id"),
                "status": h.get("lifecycle_status"),
                "http": h.get("provider_http_status"),
                "tool": h.get("tool"),
            } for h in hops],
        })
        o_ms = f"{ours['elapsed_cold']*1000:.0f}ms"
        f_ms = f"{fd['elapsed_cold']*1000:.0f}ms" if fd else "-"
        print(f"{name:<24} ours {ours['status']:<4} {o_ms:<9} ${ours_cost:.4f} "
              f"{len(hops)} hop(s) | fd {fd['status'] if fd else '-':<4} {f_ms}")

    out = pathlib.Path("bench_results.json")
    out.write_text(json.dumps(rows, indent=1, default=str))
    print(f"\nwrote {out}")


if __name__ == "__main__":
    main()
