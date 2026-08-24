# Local OpenSearch + Dashboards (flowlog dev log search)

Single-node OpenSearch 3.7.0 + OpenSearch Dashboards 3.7.0 installed via Homebrew
on macOS (Apple Silicon, `brew --prefix` = `/opt/homebrew`). Plain HTTP, no auth,
no TLS - the Homebrew "minimal distribution" ships with NO security plugin
(verified: `opensearch-plugin list` is empty and the plugins dirs are empty for
both OpenSearch and Dashboards). This is intended for LOCAL development only.

Endpoints when running:
- OpenSearch:            http://localhost:9200
- OpenSearch Dashboards: http://localhost:5601

## 1. Install

```bash
# Both are in homebrew-core at 3.7.0. opensearch-dashboards pulls node@22,
# opensearch pulls openjdk@25. Installs can take several minutes.
brew install opensearch opensearch-dashboards
```

## 2. Configure OpenSearch

Config lives under `$(brew --prefix)/etc/opensearch/` (i.e. `/opt/homebrew/etc/opensearch/`).

`opensearch.yml` - add the single-node discovery type so the node forms a cluster
by itself and skips the bootstrap checks:

```yaml
discovery.type: single-node
```

`jvm.options` - cap the heap at 512m (was 1g by default):

```
-Xms512m
-Xmx512m
```

No security settings are needed: the minimal Homebrew build has no security
plugin, so it serves plain HTTP with no auth. (If a future build DID bundle the
security plugin, you would instead add `plugins.security.disabled: true` to
`opensearch.yml` rather than configuring TLS.)

## Shortcut: steps 3-6 in one command

Once steps 1 and 2 are done and both services are started:

```bash
flowlog setup     # or ./bootstrap.sh, which just calls it
```

That applies the index template, creates today's daily index, and creates the
index pattern with a populated field cache, the saved search, and the
sample-size setting. It is idempotent, so it doubles as the repair command when
Discover misbehaves. `flowlog doctor` reports the same checks without changing
anything.

The rest of this document is the manual version, and explains why each step
looks the way it does.

## 3. Start OpenSearch and wait for health

```bash
brew services start opensearch

# Poll until yellow or green (single-node reports green here since it has no replicas to assign):
until curl -s localhost:9200/_cluster/health | grep -qE '"status":"(yellow|green)"'; do sleep 5; done
curl -s localhost:9200/_cluster/health
```

## 4. Create the `flowlog` index template

Template body is in `template.json` (next to this file). It matches index
pattern `flowlog*` and maps (field names mirror the QA `qa-logs-*` index so
Discover muscle memory transfers 1:1):
- `time` -> date (format `epoch_millis`) - the Discover time field
- `timestamp` (epoch ms), `timestamp_ns` (ns; sub-ms digits synthesized from
  arrival order since the log only has ms precision) -> long
- `service`, `level`, `operation_id`, `instance_id` (hostname) -> keyword
- `message`, `params` -> text

```bash
curl -s -X PUT localhost:9200/_index_template/flowlog \
  -H 'Content-Type: application/json' \
  --data-binary @template.json

# Verify:
curl -s localhost:9200/_index_template/flowlog
```

Roundtrip sanity check (index a doc, read it back, delete it):

```bash
TS=$(python3 -c 'import time;print(int(time.time()*1000))')
curl -s -X POST "localhost:9200/flowlog/_doc/testdoc1?refresh=true" \
  -H 'Content-Type: application/json' \
  -d "{\"time\": $TS, \"timestamp\": $TS, \"timestamp_ns\": ${TS}000000, \"service\": \"opensearch-setup\", \"level\": \"info\", \"operation_id\": \"roundtrip-test\", \"message\": \"hello flowlog\", \"params\": \"k=v\", \"instance_id\": \"setup\"}"
curl -s "localhost:9200/flowlog/_search" -H 'Content-Type: application/json' \
  -d '{"query":{"term":{"operation_id":"roundtrip-test"}}}'
curl -s -X DELETE "localhost:9200/flowlog/_doc/testdoc1?refresh=true"
```

## 5. Configure and start OpenSearch Dashboards

Config lives under `$(brew --prefix)/etc/opensearch-dashboards/`
(i.e. `/opt/homebrew/etc/opensearch-dashboards/opensearch_dashboards.yml`).
Append the local dev settings:

```yaml
server.host: "localhost"
server.port: 5601
opensearch.hosts: ["http://localhost:9200"]
```

No security/TLS settings are needed (OpenSearch has no security plugin, and the
Dashboards build has no security-dashboards plugin either).

IMPORTANT one-time fix: the shipped config sets
`pid.file: /opt/homebrew/var/run/opensearchDashboards.pid`, but that directory
does not exist on a fresh install, so Dashboards exits immediately with
`ENOENT ... opensearchDashboards.pid`. Create the dir before starting:

```bash
mkdir -p /opt/homebrew/var/run
brew services start opensearch-dashboards

# First boot can take a minute or two. Wait for HTTP 200:
until [ "$(curl -s -o /dev/null -w '%{http_code}' localhost:5601/api/status)" = "200" ]; do sleep 5; done
curl -s localhost:5601/api/status   # overall state should be "green"
```

## 6. Create the Dashboards index pattern

Create an index pattern for `flowlog*` with `time` as the time field. IMPORTANT:
creating it via the bare saved-objects API stores NO field cache, and Discover's
histogram then fails with "Could not locate that index-pattern-field (id: time)".
The pattern must be created with its `fields` attribute populated from the
`_fields_for_wildcard` endpoint.

That endpoint needs a matching index that has **mappings** - but it does not need
one that has **documents**. So create today's daily index empty first and the
whole setup completes in one pass, with no "ship some logs, then come back"
detour:

```bash
curl -s -X PUT "localhost:9200/flowlog-$(date +%Y.%m.%d)"
```

```bash
python3 - <<'EOF'
import json, urllib.request
def req(method, url, body=None):
    r = urllib.request.Request(url, method=method, data=json.dumps(body).encode() if body else None, headers={'osd-xsrf': 'true', 'Content-Type': 'application/json'})
    return json.load(urllib.request.urlopen(r))
fields = req('GET', 'http://localhost:5601/api/index_patterns/_fields_for_wildcard?pattern=flowlog*&meta_fields=_source&meta_fields=_id&meta_fields=_type&meta_fields=_index&meta_fields=_score')['fields']
print(req('POST', 'http://localhost:5601/api/saved_objects/index-pattern/flowlog', {'attributes': {'title': 'flowlog*', 'timeFieldName': 'time', 'fields': json.dumps(fields)}})['id'])
EOF
```

(After reindexing with NEW fields, delete and recreate the pattern the same way
so the cache picks them up.)

Also create the "flowlog" saved search that reproduces the QA Discover layout
(columns service | level | message | timestamp_ns, sorted by time desc):

```bash
curl -s -X POST "localhost:5601/api/saved_objects/search/flowlog-flow" \
  -H 'osd-xsrf: true' -H 'Content-Type: application/json' \
  -d '{"attributes":{"title":"flowlog","columns":["service","level","message","timestamp_ns"],"sort":[["time","desc"]],"kibanaSavedObjectMeta":{"searchSourceJSON":"{\"query\":{\"query\":\"\",\"language\":\"kuery\"},\"filter\":[],\"indexRefName\":\"kibanaSavedObjectMeta.searchSourceJSON.index\"}"}},"references":[{"id":"flowlog","name":"kibanaSavedObjectMeta.searchSourceJSON.index","type":"index-pattern"}]}'
```

Then open http://localhost:5601 -> Discover -> Open -> "flowlog".

## Retention (daily indices)

flowlog ships to daily indices (`flowlog-YYYY.MM.DD`) with content-hash doc ids
(re-shipping overwrites, never duplicates). The template and the `flowlog*`
index pattern match them automatically. On every `--ship` run flowlog deletes
daily indices older than `--retain` days (default 7; the minimal Homebrew
build has no ISM plugin, so flowlog does its own cleanup). If an old
pre-daily-indices `flowlog` index exists, remove it once:

```bash
curl -s -X DELETE localhost:9200/flowlog
```

## Stopping (reclaim RAM)

These services run in the background across reboots via `brew services`. To free
RAM when you are not using them, stop them (order does not matter):

```bash
brew services stop opensearch-dashboards
brew services stop opensearch

# confirm both show "none":
brew services list | grep -i opensearch
```

Data persists in `/opt/homebrew/var/lib/opensearch/` between stop/start, so the
`flowlog` index, template, and the Dashboards saved objects survive a restart -
you do NOT need to re-run steps 4 and 6 after a plain stop/start.
