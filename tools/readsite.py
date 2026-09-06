#!/usr/bin/env python3
"""Read a site's pages through Monid and save each one as Markdown.

Every page is fetched by Context.dev through Monid's /v1/run, the same way
this server reaches every other provider, so the pages cost Monid credit
and carry a run id that is checkable in the dashboard.

    MONID_API_KEY=... python3 tools/readsite.py
    MONID_API_KEY=... python3 tools/readsite.py --out site/ <url> [<url> ...]

Any same-site link found in a page is reported at the end rather than
followed: what to read next is a decision, not something a script should
spend a caller's credit on unasked. Pass --follow to read them too.
"""

import argparse
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

MONID = "https://api.monid.ai"
PROVIDER = "context.dev"
SCRAPE = "/web/scrape/markdown"
# Context.dev bills one credit per page it actually returns; a page it
# fails on is free.
TIMEOUT = 300
POLL_SECONDS = 3
POLL_ATTEMPTS = 60

DEFAULT_URLS = [
    "https://hacks.monid.ai/",
    "https://hacks.monid.ai/guide.html",
]


def request(method, path, key, payload=None):
    """One Monid API call. Returns (status, decoded body)."""
    headers = {"Authorization": f"Bearer {key}"}
    data = None
    if payload is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(payload).encode()
    req = urllib.request.Request(MONID + path, headers=headers, data=data, method=method)
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT) as response:
            return response.status, json.loads(response.read())
    except urllib.error.HTTPError as e:
        body = e.read()
        try:
            return e.code, json.loads(body)
        except ValueError:
            return e.code, {"message": body[:300].decode("utf-8", "replace")}


def run(key, endpoint, query):
    """Run one provider endpoint to completion.

    A run may come back already COMPLETED or as a 202 to poll, so both are
    handled here rather than at each call site.
    """
    status, body = request("POST", "/v1/run", key,
                           {"provider": PROVIDER, "endpoint": endpoint,
                            "input": {"queryParams": query}})
    if status >= 400:
        return None, body.get("message", f"HTTP {status}")

    run_id = body.get("runId")
    for _ in range(POLL_ATTEMPTS):
        state = (body.get("status") or "").upper()
        if state in ("COMPLETED", "SUCCEEDED"):
            return body, None
        if state in ("FAILED", "CANCELLED"):
            return None, f"run {state.lower()}"
        if not run_id:
            return None, f"run has no id and no terminal status ({state or 'none'})"
        time.sleep(POLL_SECONDS)
        status, body = request("GET", f"/v1/runs/{run_id}", key)
        if status >= 400:
            return None, body.get("message", f"HTTP {status}")
    return None, "run did not finish in time"


def cost_of(body):
    """What one run actually cost: the unit price times the units billed.

    A failed page is not billed, so this is the measured figure rather than
    the list price of what was asked for.
    """
    price = (body.get("price") or {}).get("amount") or {}
    return float(price.get("value") or 0) * float(body.get("billedUnits") or 0)


def filename_for(url):
    """A readable file name for one page, unique within a site."""
    parsed = urllib.parse.urlparse(url)
    path = parsed.path.strip("/")
    if not path:
        path = "index"
    name = re.sub(r"[^A-Za-z0-9._-]+", "-", path).strip("-")
    if not name.endswith(".md"):
        name = re.sub(r"\.(html?|php|aspx?)$", "", name) + ".md"
    return name


def same_site_links(markdown, base):
    """Same-site links a page points at, absolute and de-duplicated."""
    host = urllib.parse.urlparse(base).netloc
    found = set()
    for target in re.findall(r"\]\(([^)\s]+)", markdown):
        absolute = urllib.parse.urljoin(base, target)
        parsed = urllib.parse.urlparse(absolute)
        if parsed.scheme in ("http", "https") and parsed.netloc == host:
            found.add(parsed._replace(fragment="").geturl())
    return found


def read_page(key, url):
    """Fetch one page as Markdown. Returns (markdown, metadata, cost, error)."""
    # includeLinks is validated as a real boolean, not the string "true",
    # so the links this script reports back survive the scrape.
    body, error = run(key, SCRAPE, {"url": url, "includeLinks": True})
    if error:
        return "", {}, 0.0, error
    output = body.get("output") or {}
    markdown = output.get("markdown") or ""
    if not markdown:
        return "", output.get("metadata") or {}, cost_of(body), "no markdown returned"
    return markdown, output.get("metadata") or {}, cost_of(body), None


def main():
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("urls", nargs="*", default=DEFAULT_URLS,
                        help=f"pages to read (default: {', '.join(DEFAULT_URLS)})")
    parser.add_argument("--out", default="site", help="directory to write the Markdown into")
    parser.add_argument("--follow", action="store_true",
                        help="also read the same-site links these pages point at")
    args = parser.parse_args()

    key = os.environ.get("MONID_API_KEY")
    if not key:
        sys.exit("MONID_API_KEY is not set; every page is read through Monid")

    os.makedirs(args.out, exist_ok=True)
    queue = list(dict.fromkeys(args.urls or DEFAULT_URLS))
    done, pages, spent, failures = set(), [], 0.0, []

    while queue:
        url = queue.pop(0)
        if url in done:
            continue
        done.add(url)

        markdown, metadata, cost, error = read_page(key, url)
        spent += cost
        if error:
            failures.append((url, error))
            print(f"  FAILED  {url}  {error}", flush=True)
            continue

        name = filename_for(url)
        path = os.path.join(args.out, name)
        title = metadata.get("title") or ""
        with open(path, "w") as f:
            f.write(f"<!-- {url} -->\n")
            if title:
                f.write(f"<!-- {title} -->\n")
            f.write("\n" + markdown.rstrip() + "\n")

        links = same_site_links(markdown, url)
        pages.append({"url": url, "file": name, "title": title,
                      "chars": len(markdown), "cost_usd": round(cost, 6),
                      "links": sorted(links)})
        print(f"  read    {url}  {len(markdown):,} chars  ${cost:.4f}  -> {name}", flush=True)

        if args.follow:
            queue.extend(sorted(links - done))

    index = os.path.join(args.out, "pages.json")
    with open(index, "w") as f:
        json.dump({"pages": pages, "failures": failures,
                   "cost_usd": round(spent, 6)}, f, indent=1)

    print(f"\n{len(pages)} pages, {len(failures)} failed, ${spent:.4f} of Monid credit")
    print(f"wrote {args.out}/ and {index}")

    unread = sorted({link for page in pages for link in page["links"]} - done)
    if unread and not args.follow:
        print(f"\n{len(unread)} same-site links not read (pass --follow to read them):")
        for link in unread[:20]:
            print("  ", link)
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
