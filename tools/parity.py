#!/usr/bin/env python3
"""Compare this server's responses with Financial Datasets', route by route.

Two earlier hand-checks got this wrong in two different ways, so both are
closed here. Keys are compared as sets, never by which one serializes
first, because a Go map orders its keys alphabetically and theirs do not.
An unparseable body fails its route loudly instead of being skipped, which
is how twenty routes once passed without being looked at.

    MONID_API_KEY=... FD_API_KEY=... python3 tools/parity.py report.json
"""

import json
import os
import sys
import time
import urllib.error
import urllib.request

OURS = os.environ.get("OURS_BASE", "https://monid-finance-api.fly.dev")
FD = "https://api.financialdatasets.ai"
PAUSE = float(os.environ.get("PAUSE_SECONDS", "2.0"))
TIMEOUT = 180
# Financial Datasets answers 403 to the default Python agent.
UA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) monid-parity/1.0"

T = "AAPL"
# Each row is (page group, path, query). The group is what the comparison
# page filters by; keeping it here means the benchmark and the parity
# checker can never disagree about which routes exist.
GET_ROUTES = [
    ("Financial statements", "/financials/income-statements", f"ticker={T}&period=annual&limit=1"),
    ("Financial statements", "/financials/balance-sheets", f"ticker={T}&period=annual&limit=1"),
    ("Financial statements", "/financials/cash-flow-statements", f"ticker={T}&period=annual&limit=1"),
    ("Financial statements", "/financials", f"ticker={T}&period=annual&limit=1"),
    ("Financial statements", "/financials/as-reported", f"ticker={T}&period=annual&limit=1"),
    ("Financial statements", "/financials/income-statements/as-reported", f"ticker={T}&period=annual&limit=1"),
    ("Financial statements", "/financials/balance-sheets/as-reported", f"ticker={T}&period=annual&limit=1"),
    ("Financial statements", "/financials/cash-flow-statements/as-reported", f"ticker={T}&period=annual&limit=1"),
    ("Segments", "/financials/segments", f"ticker={T}&period=annual&limit=1"),
    ("Segments", "/financials/income-statements/segments", f"ticker={T}&period=annual&limit=1"),
    ("Segments", "/financials/balance-sheets/segments", f"ticker={T}&period=annual&limit=1"),
    ("Segments", "/financials/cash-flow-statements/segments", f"ticker={T}&period=annual&limit=1"),
    ("Metrics", "/financial-metrics", f"ticker={T}&period=annual&limit=1"),
    ("Metrics", "/financial-metrics/snapshot", f"ticker={T}"),
    ("Metrics", "/financial-metrics/snapshot/tickers", "limit=2"),
    ("Prices", "/prices", f"ticker={T}&interval=day&interval_multiplier=1&start_date=2026-08-31&end_date=2026-09-02"),
    ("Prices", "/prices/snapshot", f"ticker={T}"),
    ("Prices", "/prices/snapshot/market", ""),
    ("Prices", "/prices/snapshot/tickers", "limit=2"),
    ("Prices", "/prices/tickers", "limit=2"),
    ("Company", "/company/facts", f"ticker={T}"),
    ("Company", "/company/facts/tickers", "limit=2"),
    ("Company", "/company/facts/ciks", "limit=2"),
    ("Filings", "/filings", f"ticker={T}&limit=2"),
    ("Filings", "/filings/types", "limit=2"),
    ("Filings", "/filings/tickers", "limit=2"),
    ("Filings", "/filings/ciks", "limit=2"),
    ("Filings", "/filings/items", f"ticker={T}&filing_type=10-K&year=2025&item=Item-1"),
    ("Filings", "/filings/items/types", ""),
    ("Filings", "/filings/items/requests/does-not-exist", ""),
    ("Earnings", "/earnings", f"ticker={T}"),
    ("Earnings", "/earnings/tickers", "limit=2"),
    ("News", "/news", f"ticker={T}&limit=2"),
    ("Ownership", "/insider-trades", f"ticker={T}&limit=2"),
    ("Ownership", "/insider-trades/names", f"ticker={T}"),
    ("Ownership", "/insider-ownership", f"ticker={T}&limit=2"),
    ("Ownership", "/beneficial-ownership", f"ticker={T}&limit=2"),
    ("Ownership", "/activist-ownership", f"ticker={T}&limit=2"),
    ("Ownership", "/institutional-holdings", f"ticker={T}&limit=2"),
    ("Ownership", "/institutional-holdings/investors", f"ticker={T}&limit=2"),
    ("Ownership", "/institutional-holdings/tickers", "limit=2"),
    ("KPI", "/kpi/metrics", f"ticker={T}&period=quarterly&limit=1"),
    ("KPI", "/kpi/guidance", f"ticker={T}&period=quarterly&limit=1"),
    ("KPI", "/kpi/non-gaap", f"ticker={T}&period=quarterly&limit=1"),
    ("KPI", "/kpi/metrics/tickers", "limit=2"),
    ("KPI", "/kpi/metrics/sectors", ""),
    ("Macro", "/macro/interest-rates", "bank=FED"),
    ("Macro", "/macro/interest-rates/snapshot", "bank=FED"),
    ("Macro", "/macro/interest-rates/banks", ""),
    ("Funds", "/index-funds", "ticker=SPY&limit=2"),
    ("Funds", "/index-funds/tickers", ""),
    ("Screener", "/financials/search/screener/filters", ""),
    ("IPOs", "/ipos", "ticker=RDDT&limit=2"),
]
POST_ROUTES = [
    ("Screener", "/financials/search/screener",
     {"filters": [{"field": "market_cap", "operator": "eq", "value": 3000000000000}], "limit": 2}),
    ("Line items", "/financials/search/line-items",
     {"tickers": [T], "line_items": ["revenue", "net_income"], "period": "annual", "limit": 2}),
]


def fetch(url, key, payload=None):
    headers = {"X-API-KEY": key, "User-Agent": UA, "Accept": "application/json"}
    data = None
    if payload is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(payload).encode()
    req = urllib.request.Request(url, headers=headers, data=data,
                                 method="POST" if payload is not None else "GET")
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()
    except Exception as e:
        return 0, json.dumps({"transport_error": str(e)[:200]}).encode()


def shape(body):
    """Top-level keys, plus the union of record keys inside the list envelope."""
    doc = json.loads(body)
    if not isinstance(doc, dict):
        raise ValueError(f"top level is {type(doc).__name__}, not an object")
    top = set(doc)
    fields, listkey = set(), None
    # An empty list still names the envelope. Requiring a first element
    # hid the key whenever a side returned nothing, which read as a shape
    # mismatch three separate times.
    for k, v in doc.items():
        if isinstance(v, list):
            listkey = k
            for row in v:
                if isinstance(row, dict):
                    fields |= set(row)
            break
    return top, fields, listkey, doc


def check(path, query, payload, mk, fk):
    suffix = f"?{query}" if query else ""
    so, bo = fetch(f"{OURS}{path}{suffix}", mk, payload)
    time.sleep(PAUSE)
    sf, bf = fetch(f"{FD}{path}{suffix}", fk, payload)
    time.sleep(PAUSE)

    row = {"path": path, "query": query or json.dumps(payload or {}),
           "ours_status": so, "fd_status": sf}
    try:
        otop, ofields, olist, odoc = shape(bo)
    except Exception as e:
        row["verdict"] = "OURS_UNREADABLE"
        row["detail"] = f"{e}: {bo[:160].decode('utf-8', 'replace')}"
        return row
    try:
        ftop, ffields, flist, fdoc = shape(bf)
    except Exception as e:
        row["verdict"] = "THEIRS_UNREADABLE"
        row["detail"] = f"{e}: {bf[:160].decode('utf-8', 'replace')}"
        return row

    row["envelope_missing"] = sorted(ftop - otop)
    row["envelope_extra"] = sorted(otop - ftop)
    row["list_key_ours"] = olist
    row["list_key_theirs"] = flist
    row["fields_missing"] = sorted(ffields - ofields)
    row["fields_extra"] = sorted(ofields - ffields)
    row["records_ours"] = len(odoc.get(olist, [])) if olist else None
    row["records_theirs"] = len(fdoc.get(flist, [])) if flist else None

    if row["envelope_missing"]:
        row["verdict"] = "ENVELOPE_MISSING"
    elif olist != flist:
        row["verdict"] = "LIST_KEY_DIFFERS"
    elif row["envelope_extra"]:
        row["verdict"] = "ENVELOPE_EXTRA"
    elif row["fields_missing"]:
        row["verdict"] = "FIELDS_MISSING"
    else:
        row["verdict"] = "MATCH"
    return row


def main():
    out_path = sys.argv[1] if len(sys.argv) > 1 else "parity.json"
    mk, fk = os.environ["MONID_API_KEY"], os.environ["FD_API_KEY"]
    rows = []
    todo = [(p, q, None) for _, p, q in GET_ROUTES] + [(p, "", b) for _, p, b in POST_ROUTES]
    for i, (path, query, payload) in enumerate(todo, 1):
        row = check(path, query, payload, mk, fk)
        rows.append(row)
        print(f"{i:3}/{len(todo)} {row['verdict']:18} {path:46} "
              f"{row['ours_status']}/{row['fd_status']} "
              f"miss={row.get('envelope_missing') or ''} extra={row.get('envelope_extra') or ''}",
              flush=True)
        with open(out_path, "w") as f:
            json.dump(rows, f, indent=1)

    from collections import Counter
    print("\n=== verdicts ===")
    for k, v in Counter(r["verdict"] for r in rows).most_common():
        print(f"  {k:18} {v}")
    broken = [r for r in rows if r["verdict"] not in ("MATCH", "FIELDS_MISSING")]
    print(f"\ncontract defects: {len(broken)}")
    for r in broken:
        print(f"  {r['path']:46} {r['verdict']:18} "
              f"missing={r.get('envelope_missing')} extra={r.get('envelope_extra')} "
              f"{r.get('detail','')[:80]}")
    print(f"\nwrote {out_path}")
    return 1 if broken else 0


if __name__ == "__main__":
    sys.exit(main())
