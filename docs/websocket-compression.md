# WebSocket transport compression

## Scope and release status

This change adds optional RFC 7692 `permessage-deflate` to `/ws` (`realtime`)
and `/api/daemon/ws` (`daemon`). Both use the existing Gorilla WebSocket v1.5.3.
Business JSON, event routing, authorization, and frontend message decoding are
unchanged. Nginx HTTP gzip and Sub2API are outside this change.

The implementation is local and **server compression is off by default**.
The local base is commit `3660d3391` (changelog v0.5.13); the deployed commit,
server topology, and installed client versions have not been verified. Local
tests do not authorize deployment or establish production savings. Deployment
and environment changes require a separate reviewed plan and confirmation.

## Protocol and safety

- Both directions negotiate `server_no_context_takeover` and
  `client_no_context_takeover`; dictionaries are not reused across messages.
- Only compatible offers are accepted. Unknown/duplicate parameters and
  window sizes other than 15 fall back to plain communication. Bare
  `client_max_window_bits` is accepted. Unsupported clients need no upgrade.
- Default compression level is 1, with configurable levels 1–3. The initial
  threshold is 1,024 serialized UTF-8 bytes, inclusive; smaller server messages
  stay plain. This counts bytes, not characters.
- Compression state changes and data writes run in the existing sole writer.
  Level changes apply to new connections only. No concurrent compression
  toggles or new business-message writers are introduced.
- Data remains WebSocket text JSON. Applications never receive custom gzip
  binary payloads. Control frames remain uncompressed.
- Inbound reads are bounded after inflation as well as on the wire: 64 KiB at
  both server endpoints and the existing 64 MiB limit at the Go daemon. Oversize
  messages close with code 1009.

Compression uses a schema allowlist and a second check of every decoded JSON
string for URLs, data URIs, and common credential markers. Unknown events or
fields remain plain. The current reviewed event families are:

| Direction | Potentially compressed, subject to threshold and safety checks |
| --- | --- |
| Server `/ws` | `task:progress`, the plain-text subset of `task:message`, `chat:message`, `chat:done`, `chat:quick_actions`, inbox batch-read/batch-archived ID/count payloads |
| Server `/api/daemon/ws` | Heartbeat ack, task-available, runtime-profiles-changed, pending-work, empty workspaces-changed and DingTalk-personal-message-available hints |
| Go daemon outbound | Heartbeat requests and the strict `tasks.claim` request schema |

All daemon RPC responses stay plain: `tasks.claim` responses can contain
`AuthToken` and `RemoteMCPDaemonToken`. Authentication, tool input/output,
provider configuration, invitations, attachments, binary data, and media do not
enter the allowlist. URL/data URI checks also exclude common encoded media.
Existing permission-safe projections are preserved; payloads are not expanded
to increase compression savings.

Schema review is the security boundary. Marker checks cannot prove arbitrary
user text contains no secret. A single message can still mix a secret pasted
into prose with attacker-controlled text, even without context takeover. Do
not enable a sensitive cohort without reviewing its actual message schemas and
threat model. Changes to shared protocol structs used by the allowlist require
the same review; adding a field to those structs can expand the accepted schema.

## Configuration and hot reload

See [the default-off example](examples/websocket-compression.yaml). Set
`MULTICA_WS_COMPRESSION_FILE` to an absolute policy path to load it at startup.
An empty environment variable keeps both server endpoints plain and starts no
watcher. Setting/changing this environment variable requires a process restart.
The Compose change only passes through this optional variable; it does not
mount a policy file or enable compression.

| Field | Meaning |
| --- | --- |
| `threshold` | Minimum serialized message bytes; omitted/0 means 1024, valid 1–67108864 |
| `level` | Compression level; omitted/0 means 1, valid 1–3; new connections only |
| `instance` | Trusted operator-set identity for instance targeting |
| `realtime`, `daemon` | Independently configured endpoint policies |
| `negotiate` | Permit extension negotiation on eligible new connections |
| `send` | Permit safe, sufficiently large outbound messages on already negotiated connections |
| `pre_auth` | Permit `/ws` negotiation before first-message authentication on a controlled instance; ignored for daemon |
| `rule.default` | Fallback eligibility; false for a limited rollout |
| `rule.allow`, `allow_by` | Explicit eligible identifiers and their attribute |
| `rule.deny`, `deny_by` | Explicit excluded identifiers and their attribute |
| `rule.percent.percent`, `by` | Stable rollout percentage, 0–100, and bucketing attribute |

Target attributes are `user_id`, `workspace_id`, and `instance`; an omitted
attribute defaults to `user_id`. Identities come from existing authentication,
not client-supplied rollout headers. A missing required targeting identity
fails closed. Daemon identities without a user ID can use a trusted instance
cohort where appropriate.

Rules reuse the existing evaluator: **deny > allow > percentage > default**.
A percentage miss falls through to `default`. Set `default: false` for a 5%
rollout; `default: true` would also enable users outside the percentage. Use a
stable attribute across stages. An explicit allow list remains enabled even
when the percentage is zero unless denied.

For cookie-authenticated `/ws`, eligibility is known during the handshake.
For first-message authentication, the handshake occurs before the user is
known. User bucketing cannot prevent those handshake negotiations: enable
`pre_auth` only on a controlled instance. Outbound compression is still checked
against the authenticated user's rule after authentication. Browser outbound
compression is controlled by the browser once the extension is negotiated,
including the first auth message; the server cannot apply its outbound schema
allowlist or threshold to browser-originated frames. Browser authentication
frames do not enter the server's outbound compression allowlist.

Files are limited to 64 KiB, use strict known-field YAML decoding, and contain
exactly one document. Invalid initial configuration fails startup. The watcher
revalidates every two seconds; invalid or missing replacement files retain the
last good policy, increment reload-error metrics, and emit a generic warning
on the transition to an invalid state. No file contents are logged.

Mount the **containing directory**, not a single file, when using containers.
Write a complete replacement beside the live policy and rename it atomically
inside that mounted directory. Single-file bind mounts can keep the old inode
and defeat atomic replacement. Confirm each instance has loaded the new
behavior through controlled connections and metrics. Deleting or corrupting
the file is **not** a rollback because the last good policy remains active.

The Go daemon requests compression by default. Set
`MULTICA_DAEMON_WS_COMPRESSION=false` to opt out on its next connection. Its
existing sole writer uses level 1 and a 1,024-byte threshold. It accepts servers
that decline compression. If Gorilla rejects a post-101 takeover negotiation,
the daemon retries once without extensions; authentication, TLS, network, and
other HTTP errors retain their normal reconnect behavior.

## Metrics and cost accounting

New metrics use `multica_ws_compression_`, with only the fixed endpoint labels
`realtime` and `daemon`. Reload errors are unlabeled. No token, key, payload,
URL, user ID, or event-name labels are recorded by this instrumentation. The
wire observer parses headers and skips payloads without retaining copies.

| Suffix | Scope |
| --- | --- |
| `connections_total`, `negotiated_total` | Successful handshakes, and those with compression |
| `active`, `active_negotiated` | Current observed connections |
| `original_bytes_total`, `sent_bytes_total` | Matched serialized bytes and actual frame payload bytes for successful Session data writes, including skipped/plain messages |
| `compressed_original_bytes_total` | Original bytes of successful data messages actually selected for compression |
| `messages_total`, `compressed_messages_total` | Successful Session data writes and completed observed data messages carrying RSV1 |
| `write_seconds` | Histogram of write duration, including policy/schema checks, compression, and network backpressure |
| `write_errors_total`, `inbound_too_large_total` | Failed writes and inbound size-limit rejections |
| `transport_payload_bytes_total`, `transport_header_bytes_total`, `transport_control_bytes_total` | All accepted outgoing frame payload/header/control bytes, including pre-auth and partial failed writes |
| `failed_write_bytes_total` | Accepted bytes from network writes returning errors; overlaps transport totals |
| `reload_errors_total` | Rejected reload attempts while keeping the last good policy |

`auth_ack` is a plain Session write and enters paired totals. Raw `auth_error`
writes are transport-only. The completed RSV1 observer counter can include a
complete frame even if a later write error prevents successful Session
accounting; use the paired byte counters for savings. These counters cover
server egress, not Go daemon outbound traffic.

Calculate weighted savings per endpoint over a representative window:

```promql
1 - (
  sum by (endpoint) (increase(multica_ws_compression_sent_bytes_total[1h]))
  /
  sum by (endpoint) (increase(multica_ws_compression_original_bytes_total[1h]))
)
```

Ignore zero-denominator windows. To aggregate endpoints, sum both byte totals
first and divide once. Never average per-message compression percentages.
Compression byte coverage is `sum(increase(compressed_original_bytes_total)) /
sum(increase(original_bytes_total))` using the same prefix, window and scope.
It measures bytes actually compressed, not hypothetical eligibility.

Payload savings exclude WebSocket headers/control, HTTP handshake, TLS, TCP,
retransmissions, and other services' traffic. Compare transport totals and cloud
egress accounting separately. Estimated avoided egress cost is measured billable
GB saved × approximately CNY 0.8/GB, using the provider's billing units. Local
payload bytes alone are not exact billable savings. Daily bill changes also
reflect business volume; compare equivalent cohorts/traffic and report absolute
bytes plus weighted ratios instead of attributing the entire daily decrease to
compression.

Observe `process_cpu_seconds_total`, `process_resident_memory_bytes`, Go
allocation/GC metrics, the write histogram, and existing
`multica_realtime_disconnects_total` / `multica_daemonws_disconnects_total`.
Normalize disconnects by connection exposure/traffic and distinguish expected
reconnects from errors. Compare canary/control instances through scrape target
labels; application metrics do not add sensitive or high-cardinality labels.
Write duration is not compressor-only CPU or end-user event latency; include
synthetic end-to-end probes for the latter.

## Local verification and measured results

Tests cover both endpoint transports and actual hubs: negotiated/plain clients,
RSV1 and raw inflate equality, long Chinese JSON, threshold boundaries, skipped
sensitive payloads, continuous delivery, ping/pong, reconnects, hot rollback,
read bounds, and 16 mixed concurrent connections per endpoint while changing
send policy. The real Go daemon writer/reader is tested, including a 32-task
claim response of about 4 MiB and rejection of an inflated response over 64 MiB.
Targeted packages and daemon regression tests passed with the race detector.

A real browser's native `WebSocket` verified both paths against the shared
transport fixture. Both reported `permessage-deflate;
server_no_context_takeover; client_no_context_takeover`, decoded ten messages,
and produced ten actual RSV1 messages per path. Observed original/sent payload
bytes were 40,960/800 (`realtime`) and 60,820/1,260 (`daemon`). This highly
repetitive fixture uses artificially long daemon notification IDs and validates
protocol behavior, not a savings forecast. It is
not the full authenticated application E2E or an installed desktop/mobile test.

Synthetic mixed-traffic benchmark: Apple M1 Max, darwin/arm64, Go 1.26.6,
2,000 messages per mode, three repetitions. Realtime mixes small/large Chinese
text and an ID list. Daemon mixes normal-length notification IDs and heartbeat
acks containing 0, 1, 4, 12, 48, or 192 pending skill-import entries. Both mixes
include intentionally uncompressed sensitive RPC responses. Batch sizes and
equal fixture frequencies are synthetic; they are not measured production
distributions. The final benchmark includes the compressed-original byte
counter and follows the collector snapshot correction.

| Endpoint | Original total | Sent payload total | Weighted saving | Compressed messages |
| --- | ---: | ---: | ---: | ---: |
| realtime | 10,778,250 B | 4,861,500 B | 54.90% | 1,000/2,000 |
| daemon | 9,828,750 B | 5,253,750 B | 46.55% | 750/2,000 |

| Endpoint/mode | p95 loopback latency | Process CPU per message | Allocated bytes/message | Allocations/message |
| --- | ---: | ---: | ---: | ---: |
| realtime plain | 38.29–53.17 µs | 28.59–32.29 µs | 13,267–13,273 | 8 |
| realtime compressed | 340.8–343.0 µs | 139.66–143.98 µs | 157,700–174,900 | 61 |
| daemon plain | 31.88–32.83 µs | 22.03–22.74 µs | 12,107 | 7 |
| daemon compressed | 448.5–455.5 µs | 140.35–149.25 µs | 137,078–163,040 | 128–129 |

Loopback p95 grows roughly 0.30 ms for realtime and 0.42 ms for daemon. Median
process CPU grows about 4.9× for realtime and 6.7× for daemon in this fixture.
CPU includes both local peers,
schema checks, compression/decompression, and GC; it is not production server
CPU percentage. Allocated bytes are cumulative allocation, not RSS or retained
memory per connection. The short benchmark establishes no long-run disconnect
rate. Existing safe projections often produce small hints, so production
compression coverage and savings may be substantially lower.

Run all Go checks through `scripts/go-test-with-agent-cli-guard.sh` to prevent
execution of installed agent CLIs. On this machine set
`GOCACHE=/private/tmp/multica-ws-go-cache`. From `server/`:

```bash
env GOCACHE=/private/tmp/multica-ws-go-cache \
  bash ../scripts/go-test-with-agent-cli-guard.sh -- \
  go test -race ./internal/wscompress ./internal/realtime ./internal/daemonws ./internal/metrics ./cmd/server

env GOCACHE=/private/tmp/multica-ws-go-cache \
  bash ../scripts/go-test-with-agent-cli-guard.sh -- \
  go test -race ./internal/daemon \
  -run 'Test(WakeupCompression|DaemonSingleWriter|DaemonRejectsInflated|DaemonReadsCompressed32|ReadTaskWakeupMessagesAcceptsLargeRPCResponse)' -count=1

env GOCACHE=/private/tmp/multica-ws-go-cache \
  bash ../scripts/go-test-with-agent-cli-guard.sh -- \
  go test ./internal/wscompress -run '^$' \
  -bench '^BenchmarkTransport$' -benchtime=2000x -count=3 -benchmem
```

Required `make test` was attempted but stopped because Docker is unavailable;
managed database/migration checks remain unverified. The broader guarded race
suite passed other packages but failed `pkg/agent` tests due to sandbox process
management restrictions and forbidden Pi/OMP session-directory writes; it was
not an all-green suite. No race report was found. Database-dependent skips do
not establish database verification.

## Proposed deployment and rollback (requires separate confirmation)

1. Read-only audit: identify deployed commit/image and both endpoint handlers,
   proxy Upgrade/extension forwarding, routing/drain topology, daemon versions,
   installed desktop/native socket behavior, and representative safe traffic.
   Do not assume the local checkout is the deployed build.
2. Complete managed database checks in an environment with Docker. Deploy a test
   instance with both policies off, then verify both endpoints with a browser,
   installed desktop, real daemon, and an old non-offering client. Inspect
   handshake extensions **and actual RSV1/frame bytes**, not decoded DevTools
   JSON lengths alone. Use synthetic test content for frame capture.
3. Test Chinese/long/small messages, sustained output, heartbeat, reconnect,
   concurrency, size bounds, policy reload, and plain compatibility. Establish
   CPU, RSS, p95 event latency, disconnect, throughput, and byte baselines.
4. Enable only an internal controlled instance/cohort, then consider stable 5%,
   20%, 50%, and 100% stages independently for each endpoint. A stage is not an
   automatic approval for the next. First-message-auth browser negotiation
   requires instance isolation as described above. Observe a representative
   traffic period at every stage before changing threshold or level.
5. Agree abort thresholds against that baseline before activation. Candidate
   values for discussion are CPU +10 percentage points, p95 event latency
   +5 ms or +20%, and disconnect incidence +0.5 percentage points. Specify the
   denominator, traffic minimum, observation window, and RSS budget first;
   these are proposals, not validated production SLOs.
6. Report actual endpoint byte-weighted savings, compression coverage, absolute
   bytes, measured CPU/RSS/latency/disconnect impact, and cost estimate with
   traffic-volume qualifications. Installed desktop/mobile behavior and real
   server performance remain open until tested in this phase.

For rollback, atomically replace the valid policy with `send: false` and
`negotiate: false` for the affected endpoint. Within the reload interval, current
connections stop server outbound compression and new handshakes stay plain.
Keep metrics enabled to confirm the change. Current connections must continue
accepting compressed inbound messages until they naturally close; the extension
cannot be removed mid-connection. Browser outbound compression also persists
until reconnect. Do not mass-disconnect clients to renegotiate. Old plain
connections only gain compression on a future handshake.

If code rollback is needed, use the verified topology's approved rolling drain
and prior image. A single Compose container recreation is not a zero-disconnect
rolling deployment. Do not execute deployment, restart, policy activation, or
traffic changes from this local implementation approval.
