# Block explorer API v1

`blockinspector serve` reads one channel's blocks and serves them as JSON,
with a server-sent events (SSE) stream of new blocks. It reads blocks only:
it sends no transaction.

This is the backend of the explorer in luthersystems/blockinspector#25. The
UI is luthersystems/blockinspector#30.

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
| `GET /v1/transactions?method=M&valid=true&before=N&limit=50` | Transaction summaries, newest first. `method` also matches a method in a JSON-RPC batch. `limit` 1 to 1000 |
| `GET /v1/transactions/{id}` | The transaction page |
| `GET /v1/keys?namespace=&prefix=&kind=&space=&limit=100` | Keys that valid transactions wrote, sorted, with their last write. `kind` is `phylum` or `framework`; `space` is one framework space. Page with `after_namespace` and `after_key` from `next` |
| `GET /v1/keys/history?namespace=NS&key=K` | Each valid write of a key, oldest first, with the value before and after |
| `GET /v1/framework-keys` | Key and write counts for each framework key space, and for phylum keys |
| `GET /v1/phylum-versions` | Each phylum version, its status, and each install, update, enable, disable and delete |
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

Each event also has the transaction's `method` (`init`, `update`, `enable`,
`disable`).

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
| No parameter | The next new block |
| `?from=N` | Block N (or the first block the explorer holds), then new blocks |
| `Last-Event-ID: N` header | Block N+1 |

A browser's `EventSource` sends `Last-Event-ID` when it reconnects, so it
misses no block. The stream sends `retry: 3000` first and a `: keepalive`
comment every 15 seconds. A slow client does not lose blocks: each stream
reads from the index at its own pace.

`--allow-origin` sets `Access-Control-Allow-Origin`, for a UI served from
another origin. The API has no authentication, so it listens on 127.0.0.1 by
default.
