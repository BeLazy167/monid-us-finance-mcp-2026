#!/usr/bin/env python3
"""Run every Financial Datasets route against both APIs and record the result.

Both sides are called with the same path and the same parameters, one call at a
time, with a fixed pause between calls so neither API sees a burst. Our side is
asked for provenance (X-Monid-Trace), so each row carries the Monid providers
that answered it and what they cost.

    MONID_API_KEY=... FD_API_KEY=... python3 tools/headtohead.py out.json

Both APIs are called directly from this machine, so neither carries a proxy hop.
The browser demo has to reach Financial Datasets through this server's
comparison proxy instead, because their API sends no CORS headers, and that
path is slower by one hop; the numbers here are the fairer, direct ones.
"""

import json
import os
import sys
import time
import urllib.error
import urllib.request

OURS = os.environ.get("OURS_BASE", "https://monid-finance-api.fly.dev")
FD = "https://api.financialdatasets.ai"
PAUSE_SECONDS = float(os.environ.get("PAUSE_SECONDS", "2.5"))
TIMEOUT = 120

# Financial Datasets answers 403 to the default Python user-agent, so both
# sides are called with the same ordinary one. Measured 2026-09-05: the same
# request is 200 with a browser agent and 403 with "Python-urllib/3.13".
USER_AGENT = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) monid-headtohead/1.0"

TICKER = "AAPL"
# A fixed, recent window so both sides answer the same question.
PRICE_START, PRICE_END = "2026-08-24", "2026-09-02"

# Every path the spec registers, with parameters chosen so the call is
# answerable. Grouped for the page's own sections.
MATRIX = [
    ("Financial statements", "/financials/income-statements", f"ticker={TICKER}&period=annual&limit=2"),
    ("Financial statements", "/financials/balance-sheets", f"ticker={TICKER}&period=annual&limit=2"),
    ("Financial statements", "/financials/cash-flow-statements", f"ticker={TICKER}&period=annual&limit=2"),
    ("Financial statements", "/financials", f"ticker={TICKER}&period=annual&limit=1"),
    ("Financial statements", "/financials/as-reported", f"ticker={TICKER}&period=annual&limit=1"),
    ("Financial statements", "/financials/income-statements/as-reported", f"ticker={TICKER}&period=annual&limit=1"),
    ("Financial statements", "/financials/balance-sheets/as-reported", f"ticker={TICKER}&period=annual&limit=1"),
    ("Financial statements", "/financials/cash-flow-statements/as-reported", f"ticker={TICKER}&period=annual&limit=1"),
    ("Segments", "/financials/segments", f"ticker={TICKER}&period=annual&limit=1"),
    ("Segments", "/financials/income-statements/segments", f"ticker={TICKER}&period=annual&limit=1"),
    ("Segments", "/financials/balance-sheets/segments", f"ticker={TICKER}&period=annual&limit=1"),
    ("Segments", "/financials/cash-flow-statements/segments", f"ticker={TICKER}&period=annual&limit=1"),
    ("Metrics", "/financial-metrics", f"ticker={TICKER}&period=annual&limit=2"),
    ("Metrics", "/financial-metrics/snapshot", f"ticker={TICKER}"),
    ("Prices", "/prices", f"ticker={TICKER}&interval=day&interval_multiplier=1&start_date={PRICE_START}&end_date={PRICE_END}"),
    ("Prices", "/prices/snapshot", f"ticker={TICKER}"),
    ("Prices", "/prices/snapshot/market", ""),
    ("Prices", "/prices/tickers", "limit=5"),
    ("Company", "/company/facts", f"ticker={TICKER}"),
    ("Company", "/company/facts/tickers", "limit=5"),
    ("Company", "/company/facts/ciks", "limit=5"),
    ("Filings", "/filings", f"ticker={TICKER}&limit=3"),
    ("Filings", "/filings/types", "limit=5"),
    ("Filings", "/filings/tickers", "limit=5"),
    ("Filings", "/filings/ciks", "limit=5"),
    # Financial Datasets accepts only the hyphenated item form; ours takes
    # either, so the shared call uses theirs.
    ("Filings", "/filings/items", f"ticker={TICKER}&filing_type=10-K&year=2025&item=Item-1"),
    ("Filings", "/filings/items/types", ""),
    ("Earnings", "/earnings", f"ticker={TICKER}"),
    ("Earnings", "/earnings/tickers", "limit=5"),
    ("News", "/news", f"ticker={TICKER}&limit=3"),
    ("Ownership", "/insider-trades", f"ticker={TICKER}&limit=3"),
    ("Ownership", "/insider-ownership", f"ticker={TICKER}&limit=3"),
    ("Ownership", "/beneficial-ownership", f"ticker={TICKER}&limit=3"),
    ("Ownership", "/activist-ownership", f"ticker={TICKER}&limit=3"),
    ("Ownership", "/institutional-holdings", f"ticker={TICKER}&limit=3"),
    ("Ownership", "/institutional-holdings/investors", f"ticker={TICKER}&limit=3"),
    ("Ownership", "/institutional-holdings/tickers", "limit=5"),
    ("KPI", "/kpi/metrics", f"ticker={TICKER}&period=quarterly&limit=1"),
    ("KPI", "/kpi/guidance", f"ticker={TICKER}&period=quarterly&limit=1"),
    ("KPI", "/kpi/non-gaap", f"ticker={TICKER}&period=quarterly&limit=1"),
    ("KPI", "/kpi/metrics/tickers", "limit=5"),
    ("KPI", "/kpi/metrics/sectors", ""),
    # Financial Datasets requires bank on this route; ours makes it optional.
    ("Macro", "/macro/interest-rates", "bank=FED"),
    ("Macro", "/macro/interest-rates/snapshot", ""),
    ("Macro", "/macro/interest-rates/banks", ""),
    ("Funds", "/index-funds", "ticker=SPY&limit=5"),
    ("Funds", "/index-funds/tickers", ""),
    ("Screener", "/financials/search/screener/filters", ""),
    ("IPOs", "/ipos", "ticker=RDDT&limit=2"),
    ("Metrics", "/financial-metrics/snapshot/tickers", "limit=5"),
    ("Prices", "/prices/snapshot/tickers", "limit=5"),
    ("Filings", "/filings/items/requests/does-not-exist", ""),
]

# The two routes Financial Datasets defines as POST.
POST_MATRIX = [
    ("Screener", "/financials/search/screener",
     {"filters": [{"field": "market_cap", "operator": "eq", "value": 3000000000000}], "limit": 2}),
    ("Line items", "/financials/search/line-items",
     {"tickers": ["AAPL"], "line_items": ["revenue", "net_income"], "period": "annual", "limit": 2}),
]


def fetch(url, headers, timeout=TIMEOUT):
    """One HTTP GET. Returns (status, body_bytes, lowercased headers, seconds)."""
    headers = dict(headers, **{"User-Agent": USER_AGENT})
    req = urllib.request.Request(url, headers=headers)
    started = time.perf_counter()

    def norm(msg):
        # HTTP/2 lowercases header names; match case-insensitively.
        return {k.lower(): v for k, v in msg.items()}

    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            body = r.read()
            return r.status, body, norm(r.headers), time.perf_counter() - started
    except urllib.error.HTTPError as e:
        body = e.read()
        return e.code, body, norm(e.headers), time.perf_counter() - started
    except Exception as e:  # network-level failure
        return 0, json.dumps({"error": str(e)[:200]}).encode(), {}, time.perf_counter() - started


def post(url, headers, payload, timeout=TIMEOUT):
    """One HTTP POST. Returns (status, body_bytes, lowercased headers, seconds)."""
    headers = dict(headers, **{"User-Agent": USER_AGENT, "Content-Type": "application/json"})
    req = urllib.request.Request(url, headers=headers, data=json.dumps(payload).encode(), method="POST")
    started = time.perf_counter()
    norm = lambda m: {k.lower(): v for k, v in m.items()}
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read(), norm(r.headers), time.perf_counter() - started
    except urllib.error.HTTPError as e:
        return e.code, e.read(), norm(e.headers), time.perf_counter() - started
    except Exception as e:
        return 0, json.dumps({"error": str(e)[:200]}).encode(), {}, time.perf_counter() - started


def verdict(row):
    """A blunt grade per route, so a 200 with an empty body cannot pass as working."""
    o, f = row["ours"], row["fd"]
    if o["status"] != 200:
        return "OURS_FAILS"
    if f["status"] != 200:
        return "THEIRS_FAILS"
    if o["key"] and f["key"] and o["key"] != f["key"]:
        return "SHAPE_DIFFERS"
    on, fn = o["records"], f["records"]
    if on is None and fn is None:
        return "BOTH_OBJECT"
    if (on or 0) > 0 and (fn or 0) > 0:
        return "BOTH_DATA"
    if (on or 0) > 0 and (fn or 0) == 0:
        return "ONLY_OURS"
    if (on or 0) == 0 and (fn or 0) > 0:
        return "ONLY_THEIRS"
    return "BOTH_EMPTY"


def envelope_count(body):
    """How many records the response carried, when it is a list envelope."""
    try:
        d = json.loads(body)
    except Exception:
        return None, None
    if not isinstance(d, dict) or not d:
        return None, None
    key = next(iter(d))
    value = d[key]
    return key, (len(value) if isinstance(value, list) else None)


def main():
    out_path = sys.argv[1] if len(sys.argv) > 1 else "headtohead.json"
    monid_key = os.environ["MONID_API_KEY"]
    fd_key = os.environ["FD_API_KEY"]

    rows = []
    for i, (group, path, query) in enumerate(MATRIX, 1):
        url_q = f"?{query}" if query else ""

        ours_status, ours_body, ours_headers, ours_secs = fetch(
            f"{OURS}{path}{url_q}", {"X-API-KEY": monid_key, "X-Monid-Trace": "1"}
        )
        trace = []
        raw_trace = ours_headers.get("x-monid-trace")
        if raw_trace:
            try:
                trace = json.loads(raw_trace)
            except Exception:
                trace = []
        try:
            ours_cost = float(ours_headers.get("x-monid-cost-usd") or 0)
        except ValueError:
            ours_cost = 0.0
        time.sleep(PAUSE_SECONDS)

        fd_status, fd_body, _, fd_secs = fetch(f"{FD}{path}{url_q}", {"X-API-KEY": fd_key})
        time.sleep(PAUSE_SECONDS)

        ours_key, ours_n = envelope_count(ours_body)
        fd_key_name, fd_n = envelope_count(fd_body)
        row = {
            "group": group,
            "path": path,
            "query": query,
            "ours": {
                "status": ours_status,
                "ms": round(ours_secs * 1000),
                "bytes": len(ours_body),
                "key": ours_key,
                "records": ours_n,
                "cost_usd": ours_cost,
                "trace": trace,
                "body": ours_body[:4000].decode("utf-8", "replace"),
            },
            "fd": {
                "status": fd_status,
                "ms": round(fd_secs * 1000),
                "bytes": len(fd_body),
                "key": fd_key_name,
                "records": fd_n,
                "body": fd_body[:4000].decode("utf-8", "replace"),
            },
        }
        rows.append(row)
        route = " -> ".join(f"{s['provider']}{s['endpoint']}" for s in trace) or "-"
        print(
            f"{i:3}/{len(MATRIX)} {path:46} ours {ours_status} {row['ours']['ms']:>6}ms ${ours_cost:<8.4f}"
            f" | fd {fd_status} {row['fd']['ms']:>6}ms | {route[:60]}",
            flush=True,
        )
        with open(out_path, "w") as f:
            json.dump(rows, f)

    for i, (group, path, payload) in enumerate(POST_MATRIX, 1):
        ours_status, ours_body, ours_headers, ours_secs = post(
            f"{OURS}{path}", {"X-API-KEY": monid_key, "X-Monid-Trace": "1"}, payload)
        trace = []
        try:
            trace = json.loads(ours_headers.get("x-monid-trace") or "[]")
        except Exception:
            trace = []
        try:
            ours_cost = float(ours_headers.get("x-monid-cost-usd") or 0)
        except ValueError:
            ours_cost = 0.0
        time.sleep(PAUSE_SECONDS)
        fd_status, fd_body, _, fd_secs = post(f"{FD}{path}", {"X-API-KEY": fd_key}, payload)
        time.sleep(PAUSE_SECONDS)
        ours_key, ours_n = envelope_count(ours_body)
        fd_key_name, fd_n = envelope_count(fd_body)
        rows.append({
            "group": group, "path": path, "query": json.dumps(payload), "method": "POST",
            "ours": {"status": ours_status, "ms": round(ours_secs * 1000), "bytes": len(ours_body),
                     "key": ours_key, "records": ours_n, "cost_usd": ours_cost, "trace": trace,
                     "body": ours_body[:4000].decode("utf-8", "replace")},
            "fd": {"status": fd_status, "ms": round(fd_secs * 1000), "bytes": len(fd_body),
                   "key": fd_key_name, "records": fd_n, "body": fd_body[:4000].decode("utf-8", "replace")},
        })
        print(f"POST {path:46} ours {ours_status} | fd {fd_status}", flush=True)
        with open(out_path, "w") as f:
            json.dump(rows, f)

    for r in rows:
        r["verdict"] = verdict(r)
    with open(out_path, "w") as f:
        json.dump(rows, f)

    from collections import Counter
    tally = Counter(r["verdict"] for r in rows)
    print("\n=== verdicts ===")
    for k, v in tally.most_common():
        print(f"  {k:14} {v}")
    for bad in ("OURS_FAILS", "ONLY_THEIRS", "SHAPE_DIFFERS", "BOTH_EMPTY"):
        for r in rows:
            if r["verdict"] == bad:
                print(f"  {bad}: {r['path']}  ours={r['ours']['status']}/{r['ours']['records']} theirs={r['fd']['status']}/{r['fd']['records']}")

    ok = lambda side: sum(1 for r in rows if r[side]["status"] == 200)
    total_cost = sum(r["ours"]["cost_usd"] for r in rows)
    print(f"\nours 200s: {ok('ours')}/{len(rows)}   fd 200s: {ok('fd')}/{len(rows)}")
    print(f"ours measured cost: ${total_cost:.4f}")
    print(f"wrote {out_path}")


if __name__ == "__main__":
    main()
