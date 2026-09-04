"""Generate docs-site/overview/comparison.mdx from measured benchmark data.

Reads bench_results.json, the output of bench.py, so the page can be
regenerated after any re-run rather than hand-edited.
"""
import json, statistics, datetime, pathlib

rows = json.load(open("bench_results.json"))
FD_RATE = 0.002

ok = [r for r in rows if r["ours"] and r["ours"]["status"] == 200]
paired = [r for r in rows if r["ours"] and r["fd"]
          and r["ours"]["status"] == 200 and r["fd"]["status"] == 200]
total = sum(r["ours_cost"] or 0 for r in ok)
hops = sum(len(r["hops"]) for r in rows)
o_med = statistics.median([r["ours"]["elapsed_cold"] for r in paired]) * 1000
f_med = statistics.median([r["fd"]["elapsed_cold"] for r in paired]) * 1000
fd_ok = sum(1 for r in rows if r["fd"] and r["fd"]["status"] == 200)

lines = []
for r in rows:
    o, f = r["ours"], r["fd"]
    o_ms = f'{o["elapsed_cold"]*1000:.0f} ms' if o else "—"
    f_ms = f'{f["elapsed_cold"]*1000:.0f} ms' if f else "—"
    cost = f'${(r["ours_cost"] or 0):.4f}'
    chain = " → ".join(f'`{h["provider"]}{h["endpoint"]}`' for h in r["hops"]) or "_no new call_"
    lines.append(f'| `{r["path"].split("?")[0]}` | {o_ms} | {f_ms} | {cost} | {chain} |')

body = f'''---
title: "Measured against Financial Datasets"
description: "The same request sent to both gateways, with latency, cost and the provider route each one took."
---

Every number here was measured on 2026-09-04 by sending the same request to both
APIs back to back from the same machine. Our cost comes from the receipts ledger,
so it is the amount actually billed, not an estimate. Regenerate with
`bench.py` and `gen_comparison_mdx.py`.

<CardGroup cols={{4}}>
  <Card title="Routes returning 200" icon="check">
    {len(ok)} of {len(rows)} here, {fd_ok} of {len(rows)} on Financial Datasets
  </Card>
  <Card title="Median latency, cold" icon="timer">
    {o_med:.0f} ms here, {f_med:.0f} ms theirs
  </Card>
  <Card title="Measured spend" icon="receipt">
    ${total:.4f} across {hops} provider calls
  </Card>
  <Card title="Their equivalent" icon="scale-balanced">
    ${len(ok) * FD_RATE:.4f} at $0.002 per request
  </Card>
</CardGroup>

## Read the latency honestly

Financial Datasets is faster, and by a wide margin on most routes. They serve
pre-ingested data from their own store. We compose each response live across
upstream providers on every request, so on raw speed they should win, and they do.

What that column cannot show is which sources a request touched, or what it cost
to serve. That is the trade: you give up latency and get the provenance and the
per-call price. If your workload needs sub-200ms reads, theirs is the better
product and we would rather say so here than have you discover it in production.

Several routes below show single-digit milliseconds and no provider call. Those
are either a static catalog compiled into the binary, or a shared cache an
earlier request in the same run had already populated. Both are genuinely free.
Neither means data is missing.

## Route by route

| Route | Ours, cold | Financial Datasets | Our measured cost | Provider route taken |
|---|---|---|---|---|
{chr(10).join(lines)}

## What the provider route column means

Every upstream call is written to a receipts ledger with its provider, endpoint,
Monid run id and measured cost. The chain above is read straight from that
ledger, so a response's whole path is reconstructable after the fact.

`/institutional-holdings` is the clearest example. It resolves the issuer's SEC
CIK first, then reads the 13F feed:

```
get_institutional_holdings
  1. defillama /equities/v1/filings        $0.0006   resolve ticker to CIK
  2. secform4  /get_institution_holders    $0.0100   the holdings themselves
                                  total    $0.0106
```

Financial Datasets publishes neither the sources a request touched nor what it
cost them to serve.

## Cost, stated fairly

Financial Datasets bills by request count, not per dataset. Build is $200/month
with 100,000 requests included and $10 per additional 1,000; Scale is
$2,000/month with 1,000,000 included and $5 per additional 1,000. Both work out
to $0.002 per included request.

Our costs above are per call and vary by route, from $0.0006 to $0.0100. The
expensive routes are the extraction and scrape-backed ones. A workload made
mostly of those costs more per call than their included rate, and for
high-volume use of them a subscription is genuinely cheaper.

Where we win is the floor. Their $200 is due before the first request. The whole
run above cost ${total:.4f}.

_Generated {datetime.datetime.now().strftime("%Y-%m-%d")} from {len(rows)} routes._
'''
out = pathlib.Path("/Users/belazy/personal/monid-us-finance-mcp-2026/docs-site/overview/comparison.mdx")
out.write_text(body)
print(f"wrote {out} ({len(body):,} bytes, {len(rows)} routes)")
