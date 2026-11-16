# Block explorer API v1

`blockinspector serve` reads one channel's blocks and serves them as JSON,
with a server-sent events (SSE) stream of new blocks. It reads blocks only:
it sends no transaction.

This is the backend of the explorer in luthersystems/blockinspector#25. The
same server serves the web UI (luthersystems/blockinspector#30) on `/`: open
`http://127.0.0.1:8090/`. See "Web UI" below.

## Sources

| Source | Flags | Live |
|---|---|---|
| A peer, over mutual TLS | `--network-dir DIR`, or the explicit TLS and identity flags below | Yes: follows new blocks, and reconnects with backoff when the peer goes away |
| Block files, offline | `--ledger-dir DIR` (`--orderer` for an orderer's) | No: stops after the last block in the files |

### A local network

`--network-dir` is the directory that holds the network's `crypto-config/`,
as luthersystems/fabric-network-builder writes it (a project's `fabric/`
directory). The client uses that network's own identity, so it passes the
peers' `CORE_PEER_TLS_CLIENTAUTHREQUIRED=true`:

| What | File |
|---|---|
| Peer's TLS CA | `crypto-config/peerOrganizations/ORG.DOMAIN/users/USER@ORG.DOMAIN/tls/ca.crt` |
| Client TLS certificate and key | `.../users/USER@ORG.DOMAIN/tls/client.crt`, `client.key` |
| Signing identity (MSP `Org1MSP` for `org1`) | `.../users/USER@ORG.DOMAIN/msp/signcerts/*`, `msp/keystore/*` |

The defaults are the first org, `--peer peer0`, `--user User1` and
`--peer-address 127.0.0.1:7051`, where the builder publishes peer0 of org1.
The TLS server name is `PEER.ORG.DOMAIN`. A member of the org satisfies the
channel's Readers policy.

Each seek request is signed by the user's MSP key, and its channel header
carries the SHA-256 of the client TLS certificate, which a peer with mutual
TLS requires.

For another network, give `--tls-root-ca`, `--tls-cert`, `--tls-key`,
`--msp-id`, `--sign-cert`, `--sign-key` and `--server-name`. They also
override what `--network-dir` finds.

### Make target

```sh
make explorer NETWORK_DIR=../ui-core/fabric
```

`CHANNEL` (default `luther`), `PEER_ADDRESS` and `EXPLORER_LISTEN` (default
`127.0.0.1:8090`) change the rest.

## Memory

The explorer keeps every block of the channel in memory, decoded. It is made
for local networks. It starts from the first block the peer has, so after a
restart it reads the chain again.

## Endpoints

All responses are JSON. An error is `{"error": "..."}` with a 4xx status.

| Endpoint | Returns |
|---|---|
| `GET /v1/status` | Channel, height, first block, and the source's state (`connected`, `last_error`) |
| `GET /v1/blocks?before=N&limit=20` | Block summaries, newest first, below block `before` (the tip when absent). `next_before` is the next page's `before`. `limit` 1 to 500 |
| `GET /v1/blocks/{number}` | A block summary with its transaction summaries |
| `GET /v1/transactions?method=M&valid=true&before=N&limit=50` | Transaction summaries, newest first. `method` also matches a method in a JSON-RPC batch. `limit` 1 to 1000. A full page has `next` (`before`, `before_index`): pass both to get the next page, which can start inside a block (with `before_index`, `before=0` is block 0) |
| `GET /v1/transactions/{id}` | The transaction page |
| `GET /v1/keys?namespace=&prefix=&kind=&space=&limit=100` | Keys that valid transactions wrote, sorted, with their last write. `kind` is `phylum` or `framework`; `space` is one framework space. Page with `after_namespace` and `after_key` from `next` |
| `GET /v1/keys/history?namespace=NS&key=K` | Each valid write of a key, oldest first, with the value before and after |
| `GET /v1/framework-keys` | Key and write counts for each framework key space, and for phylum keys |
| `GET /v1/phylum-versions` | Each phylum version, its status, and each install, update, enable, disable and delete |
| `GET /v1/flows` | Each DefFlow flow in public state: its runs by state and by what they wait on, parked rows and manifests |
| `GET /v1/flows/{flow}/runs?namespace=&state=&kind=&label=&phylum=&caller=&q=&limit=50` | The flow's runs, most recently written first. `kind` is what a waiting run waits on (`input`, `connector`, `flow`); `q` matches part of a run ID. `limit` 1 to 1000 |
| `GET /v1/flows/{flow}/runs/{run}?namespace=` | A run: its header, history, children and parked rows. 404 when the run is not in public state |
| `GET /v1/batches` | Each batch queue: prepared and due items, the oldest due time, pending batches, and whether app control pauses batching |
| `GET /v1/batches/queue?namespace=&name=Q` | One queue: its prepared items (earliest due first), pending batches, and the last 200 items that left it with their outcome. 404 when no key of the queue is live |
| `GET /v1/stream/blocks` | SSE stream of blocks |

Keys go in the query string, not the path, because substrate keys hold `:`
and `/`, and Fabric composite keys hold `\u0000`.

### Values

A byte value (a write, a response payload, an event payload) is

```json
{"encoding": "utf8", "data": "{\"balance\":10}", "size": 14}
```

with `encoding` `base64` when the bytes are not valid UTF-8.

### Block summary

```json
{
  "number": 7,
  "time": "2024-03-07T23:58:51.236935132Z",
  "data_hash": "dea1...",
  "previous_hash": "72ea...",
  "tx_count": 1,
  "valid_tx_count": 1,
  "size": 4261,
  "config": false
}
```

A Fabric block has no time. `time` is the channel header timestamp of the
block's first transaction.

### Transaction

The summary (in blocks and lists) has `id`, `type` (`ENDORSER_TRANSACTION`,
`CONFIG`, ...), `validation_code` (`VALID`, `MVCC_READ_CONFLICT`,
`ENDORSEMENT_POLICY_FAILURE`, ...), `valid`, `creator_msp`, `chaincode_id`,
`method`, `block_number`, `index` and `time`.

`method` is the JSON-RPC method of a substrate call: substrate's gateway
invokes the chaincode with the arguments `Invoke` and the JSON-RPC request.
For a JSON-RPC batch, `method` is `batch` and `batch_methods` lists the
methods.

The transaction page adds:

| Field | Holds |
|---|---|
| `creator` | MSP ID and certificate common name of the creator |
| `endorsers` | MSP ID and certificate common name of each endorser |
| `request` | The JSON-RPC request, for a substrate call |
| `args` | The chaincode arguments, for any other call |
| `response` | Status, message and payload the endorsers signed |
| `event` | The chaincode event (substrate's is named `luther`) |
| `rwsets` | Per namespace: `reads` (key and version), `writes` (key, value or `is_delete`), `range_queries`, and `private_writes` (collection, key hash and value hash: a block holds only the hashes of private data) |

Transient data is not in a block, so it does not show.

### Key history

```json
{
  "namespace": "sandbox",
  "key": "account:1:balance",
  "writes": [
    {"tx_id": "...", "method": "create_account", "block_number": 5, "tx_index": 0,
     "time": "...", "after": {"encoding": "utf8", "data": "100", "size": 3}},
    {"tx_id": "...", "method": "transfer", "block_number": 7, "tx_index": 0,
     "time": "...", "before": {...}, "after": {...}}
  ]
}
```

Only valid transactions change state, so an invalid transaction's write is
not in the history. A delete has `is_delete` and no `after`.

### Framework keys

Each key under `luther:` is in a space of substrate's registry of reserved
keys (luthersystems/substrate `docs/agents/reserved-keys.md`): `luther:flow:`,
`luther:phylum:`, `luther:app_control` (the key) and `luther:app_control:`,
`luther:private:`, `luther:pvtdata:health:`, `luther:batch:`,
`luther:batchreq:`, `luther:cr:`, `luther:ev:`, `luther:auth:`,
`luther:healthcheck:`, the `utl:migration:` spaces, and the `luther:`
catch-all for the rest. The longest match wins. Any other key is a phylum
key. `GET /v1/framework-keys` counts keys, live keys (not deleted) and writes
per space; `GET /v1/keys?space=luther:batch:` lists one space's keys.

### Phylum versions

From the writes of `luther:phylum:desc:VERSION` (the description
`{"phylum_id", "fingerprint", "status", "init_timestamp"}`) and
`luther:phylum:src:VERSION` (its size only):

| Action | When |
|---|---|
| `install` | The version's first description |
| `disable` | The status changes to `DISABLED` |
| `enable` | The status changes to `IN_SERVICE` |
| `update` | Any other change of the description |
| `delete` | The description is deleted |
| `undecodable` | The description is not JSON. The version keeps its status |

Each event also has the transaction's `method` (`init`, `update`, `enable`,
`disable`).

### DefFlow runs

From the keys substrate's DefFlow engine writes with a public store (a
private store writes only hashes, which the explorer cannot read):

| Key | Shown as |
|---|---|
| `luther:flow:run:FLOW:h:RUN` | The run's header. Format 2 is decoded. A header of another format, or one that does not decode, is returned as `raw` with only its plain fields that hold no personal data (`v`, `flow`, `id`, `state`, `label`, `kind`, times and versions) and `omitted`, the number of fields left out. Times are Unix nanoseconds |
| `luther:flow:run:FLOW:hist:RUN:N` | A history row: event, from and to label, sender, time, and the transaction that wrote it |
| `luther:flow:run:FLOW:c:RUN:CHILD` | A child run, with its flow and the label that started it |
| `luther:flow:run:FLOW:r:RUN`, `...:v:RUN` | Only `has_result` and `has_vars`. A result and saved locals can hold personal data, so their values are not shown |
| `luther:flow:park:FLOW:VERSION:RUN` | A parked row and its migration outcome |
| `luther:flow:man:FLOW:HASH` | A manifest hash |

Only writes of valid transactions count, and a deleted key is gone. A run's
error shows its reason, kind, label, attempts and message, but not its data.

### Batches

From substrate's batch keys (`libframework/batch.go`):

| Key | Shown as |
|---|---|
| `luther:batch:queue:NAME` | `created`: init made the queue |
| `luther:batch:item:NAME~TIME~ID` | A prepared item, due at TIME (Unix seconds plus 2^63, in hex). It is `due` once TIME is at or before the latest block's time, not the wall clock |
| `luther:batch:pending:NAME~BATCH` | A pending batch and its item ids |
| `luther:batchreq:ID` | Only `has_request`. A request can hold personal data |
| `luther:app_control` | `paused`, from its `BATCHING_PAUSED` property |

NAME, ID and BATCH are unescaped (`%` and two hex digits). An item key that a
valid transaction deleted is a finished item: `collected` when the same
transaction wrote a pending batch of the queue that lists it, `skipped` when a
`batch_get_requests` call removed it otherwise (its request record was missing
or not a JSON object; only when the transaction calls nothing else), and
`removed` for any other delete, such as a reinit. `GET /v1/batches` also
returns `undecodable_keys`, the live keys under `luther:batch:` of no known
form.

## Stream

`GET /v1/stream/blocks` is a `text/event-stream`. Each block is one event:

```
id: 42
event: block
data: {"number":42,"tx_count":1,...,"transactions":[...]}

```

`data` is the block, as `GET /v1/blocks/{number}` returns it.

| Request | The stream starts at |
|---|---|
| No parameter | The next new block (with no block yet, the first block the explorer reads) |
| `?from=N` | Block N (or the first block the explorer holds), then new blocks |
| `Last-Event-ID: N` header | Block N+1 |

A browser's `EventSource` sends `Last-Event-ID` when it reconnects, so it
misses no block. The stream sends `retry: 3000` first and a `: keepalive`
comment every 15 seconds. A slow client does not lose blocks: each stream
reads from the index at its own pace.

`--allow-origin` sets `Access-Control-Allow-Origin`, for a UI served from
another origin, and answers a preflight `OPTIONS` request. The API has no authentication, so it listens on 127.0.0.1 by
default.

## Web UI

The server serves a web UI on `/`, from files embedded in the binary
(`internal/explorer/ui`). It reads only this API, from its own origin, and
sends no transaction.

| Page | Route | API |
|---|---|---|
| Status bar (every page) | | `GET /v1/status`, every 5 s |
| Blocks, newest first; new blocks appear at the top | `#/` | `GET /v1/blocks`, `GET /v1/stream/blocks?from=` |
| Block and its transactions | `#/blocks/N` | `GET /v1/blocks/{number}` |
| Transactions, filtered by method and validity | `#/transactions?method=&valid=` | `GET /v1/transactions` |
| Transaction | `#/tx/ID` | `GET /v1/transactions/{id}` |
| Keys: all, phylum, framework, or one space | `#/keys?kind=&space=&namespace=&prefix=` | `GET /v1/keys` |
| Key history, with a line diff of JSON values | `#/key?namespace=&key=` | `GET /v1/keys/history` |
| Key spaces | `#/framework` | `GET /v1/framework-keys` |
| Phylum versions | `#/phylum` | `GET /v1/phylum-versions` |
| Flows | `#/flows` | `GET /v1/flows` |
| A flow's runs, filtered | `#/flows/FLOW?namespace=&state=&kind=&label=&phylum=&caller=&q=` | `GET /v1/flows/{flow}/runs` |
| A run, with its history | `#/flows/FLOW/runs/RUN?namespace=` | `GET /v1/flows/{flow}/runs/{run}` |
| Batch queues | `#/batches` | `GET /v1/batches` |
| A batch queue | `#/batch?namespace=&name=` | `GET /v1/batches/queue` |

The box in the header opens a block (a number) or a transaction (an ID).

Values show as formatted JSON, as text, or as a hex dump for base64 (the
first 4 KiB). A key's U+0000 (the Fabric composite key separator) shows as
`␀`. The UI puts ledger values in the page as text only, and its pages carry
a Content-Security-Policy that allows only the UI's own scripts and calls to
its own origin.

Values can hold personal data, so keep the default `--listen` on 127.0.0.1.
