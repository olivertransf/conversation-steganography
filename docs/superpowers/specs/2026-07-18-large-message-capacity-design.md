# Large-message capacity design

Date: 2026-07-18  
Status: approved for implementation planning  
Scope: denser generative encoding + auto-chunking so plaintext scales without a fixed size ceiling beyond practical cover length

## Problem

One `Send` today embeds a single sealed blob in one cover. Capacity is limited by:

1. Generative rate (default `top_n: 8`, non-zero `length_bias`) so each token carries few secret bits.
2. Fixed AES-SIV tag (16 bytes) paid once per send — fine for short chat, painful if we naively seal every chunk.
3. No protocol to split a large payload across multiple chat bubbles.

Short messages work; longer plaintext produces oversized or unnatural covers, or fails human-writing trials.

## Goals

- Embed arbitrarily long plaintext by auto-splitting across N covers (practical cover length is the only soft ceiling).
- Increase bits per visible character so N and cover length stay smaller for the same plaintext.
- Preserve existing security properties: local model, AES-SIV authenticity, conversation chain order, model fingerprint.
- One code path for short and long messages (`total == 1` is the short case).

## Non-goals

- Meteor / ANS / DISCOP stego-hardness upgrades.
- Shorter authentication tags or replacing AES-SIV.
- Cloud models, GUI, or changing the copy/paste messaging-app transport.
- Fixing general cover naturalness except where capacity settings interact with trials.

## Approach summary

**Chunking (Approach A):** pack → seal SIV once → split ciphertext → encode each piece as its own chain carrier.

**Density:** a capacity generative profile (`top_n: 32`, `length_bias: 0`) used for this send path; chunk size derived from a max cover character budget.

## Protocol

### Logical send pipeline

```
plaintext
  → packMessage / packMessageDetached (existing packers + dictionaries)
  → sealSIV(key, aad_logical, packed) → sealed
  → split sealed into pieces P[0..t) sized for the cover budget under the capacity profile
  → for i in 0..t-1:
        wire_i = uvarint(i) || uvarint(t) || uvarint(len(P[i])) || P[i]
        if i == 0: wire_0 also prefixes uvarint(len(sealed)) after t
        encode wire_i with GenerativeCodec → cover_i
        commit ChainRecord i (advances index and sender sequence)
```

Part 0 wire layout:

```
uvarint(part=0) || uvarint(total=t) || uvarint(sealed_len) || uvarint(len(P0)) || P0
```

Parts `i > 0`:

```
uvarint(part=i) || uvarint(total=t) || uvarint(len(Pi)) || Pi
```

Constraints:

- `t >= 1`; `part` in `[0, t)`.
- `sum(len(Pi)) == sealed_len`.
- `sealed_len` appears only on part 0; later parts must not carry a conflicting length.
- Empty plaintext remains allowed (`sealed` is tag-only or packed empty per existing packer rules).

### Logical AAD

`aad_logical` binds at least:

- conversation id
- sender name
- chain hash **before** the first chunk of this logical message
- sender sequence **before** the first chunk
- label `decalgo-large-msg-v1` (or the project’s current KDF/label prefix style)

Do not bind per-chunk trial/packing mode into `aad_logical`. Per-chunk generative trials stay as today’s carrier-trial mechanism on each cover encode.

### Why final SIV is enough

Pieces are raw splits of one SIV ciphertext. After ordered reassembly, `openSIV` authenticates the whole logical payload. Swap/drop/reorder fails because:

- chain requires exact carrier order and advances per accepted cover
- explicit `part` / `total` must match expectations for the pending assembly
- wrong bytes fail SIV on the concatenated sealed blob

No per-chunk SIV tag (avoids 16×t byte tax).

### Receive pipeline

1. Decode generative carrier → `wire`.
2. Parse header; reject malformed varints / lengths.
3. Validate against pending assembly for `(conversation, from)` or start a new assembly if `part == 0` and none pending.
4. Commit the chain record for this cover only after header checks pass and the piece is stored (same “accept then commit” discipline as today’s successful `Receive`).
5. When all `t` parts are present and `sum(lengths) == sealed_len`, concatenate in part order → `openSIV` → unpack → return plaintext.
6. If `t == 1`, behave like a single-cover send/receive with no user-visible “waiting” state.

Pending assembly state is persisted with the conversation so restart does not lose received parts.

### Chunk sizing

Inputs:

- `max_cover_chars` (config, default **600**)
- capacity generative profile
- estimated bits/token from coding + `top_n` (conservative lower bound is fine)

Choose max piece bytes so a typical encode of `wire_i` stays near `max_cover_chars`. If a single piece still overshoots after encode (tokenizer / finish tokens), allow one retry with a smaller piece size for that logical send; if still impossible, return an error asking to raise `max_cover_chars` or check the model.

No hard plaintext ceiling. Extremely large messages become large `t`; UX and disk/state are the practical limits.

## Denser encoding (capacity profile)

Applied for the large-message send path (including `t == 1`):

| Setting        | Capacity profile | Rationale                                      |
|----------------|------------------|------------------------------------------------|
| `coding`       | `arithmetic`     | Best rate among existing codecs                |
| `top_n`        | `32`             | More of the distribution → more bits/step      |
| `length_bias`  | `0`              | Prefer rate over forcing short tokens          |
| `candidate_pool` | `max(8, top_n)` with strict_style | Keep enough candidates after filters |

Existing short-chat defaults in `conversation-stenography.local.json` may remain for any legacy single-carrier helpers; interactive chat / simulate should use `SendMessage` / `ReceiveMessage` so one path owns capacity.

Long-text packing: keep current packers; prefer dynamic dictionary / deflate when they win. No new packing mode required for v1.

Out of scope: Meteor/ANS, changing SIV, semantic-judge defaults.

## Library API

```go
type ReceiveStatus struct {
    Waiting      bool
    Part         int // 0-based next expected, or last accepted
    Total        int
    ReceivedMask string // e.g. "1010" or count received
    SyncCode     string
}

func (c *ConversationChain) SendMessage(ctx context.Context, from string, plaintext []byte) ([]ChainRecord, error)

func (c *ConversationChain) ReceiveMessage(ctx context.Context, from, encrypted string) (plaintext []byte, done bool, status ReceiveStatus, err error)
```

- `Send` / `Receive` may remain as thin wrappers around the `t == 1` path or stay for tests; new interactive code must call `SendMessage` / `ReceiveMessage`.
- `EncodingBudget` gains fields: `SealedBytes`, `ChunkCount`, `MaxCoverChars`, and uses the capacity profile for estimates when budgeting a `SendMessage`.

## CLI / UX

- Multiline `/send` and ordinary sends go through `SendMessage`.
- Output:

  ```
  Cover 1/3 — copy into your messaging app:
  ...
  Cover 2/3 — ...
  ```

- `/paste NAME` calls `ReceiveMessage` once per pasted cover.
  - Incomplete: `Waiting for part 2/3 (sync abcd)`.
  - Complete: show plaintext as today.
- `/status` shows pending assembly if any.
- After a successful multi-cover send, optionally print budget line: packed / sealed / chunks / profile `top_n`.

Simulate mode uses the same APIs so multi-cover round-trips are exercised locally.

## Error handling

| Case | Behavior |
|------|----------|
| Malformed chunk header | Error; do not commit |
| `part` != expected next | Error with sync hint; do not commit |
| Inconsistent `total` / `sealed_len` | Error; do not commit |
| Duplicate part | Error; do not commit |
| All parts present but SIV fails | Error; leave chain consistent with committed covers but clear pending assembly and surface “authentication failed / desync” |
| Generative encode fails all trials for a chunk | Fail the whole `SendMessage` before committing any chunk of that logical message (all-or-nothing send) |
| Decode of cover fails | Same as today’s receive errors |

All-or-nothing send: generate all covers first (or generate and buffer records), commit only when every chunk encoded successfully. Avoids leaving a half-sent logical message in the chain.

## Config

Extend local config (names illustrative):

```json
"max_cover_chars": 600,
"capacity_top_n": 32,
"capacity_length_bias": 0
```

Defaults as in the table above when keys are omitted.

## Testing

1. **Split/join unit tests:** random sealed blobs; round-trip; reject swap, drop, reorder, wrong `total`, truncated piece.
2. **Single-chunk compatibility:** short plaintext → `t == 1`; plaintext matches; budget fields sane.
3. **Multi-chunk chain:** fixture model; ~2KiB plaintext; Alice `SendMessage` → N records; Bob receives in order → plaintext; skipping a cover → waiting/error without false plaintext.
4. **All-or-nothing send:** force encode failure on last chunk → zero new records committed.
5. **Density smoke:** same plaintext and model fixture; capacity profile reports fewer chunks or lower total visible chars than `top_n: 8` + `length_bias: 0.1` (allow soft assertion if fixture entropy is tiny).
6. **Persistence:** save conversation with pending parts; reload; finish paste → plaintext.

## Implementation order

1. Wire format + split/join + pending assembly (no generative yet).
2. `SendMessage` / `ReceiveMessage` on `ConversationChain` with capacity profile and all-or-nothing commit.
3. Persist pending assembly; wire CLI + simulate.
4. Budget reporting + tests listed above.
5. README note on multi-cover paste order (short).

## Risks

- Higher `top_n` may increase unnatural tokens; carrier trials still apply per chunk.
- Large `t` is tedious to paste; mitigated by density + `max_cover_chars` tuning, not by silent merging.
- Pending state adds complexity; must be included in conversation encryption-at-rest like other secrets/state.
- Label/prefix strings still say `decalgo-*` in places; new labels should follow existing chain AAD style for consistency unless a rename pass is done separately.
