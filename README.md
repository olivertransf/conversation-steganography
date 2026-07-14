# decalgo

`decalgo` is a small streaming chat codec. Each message is encrypted as it is
sent, and every message cryptographically commits to the preceding chain.
Deleting, reordering, modifying, using the wrong key, or switching the
conversation identifier makes decoding fail.

The authenticated-encryption wire format begins with `enc:DEC1.` so that mode is
explicitly marked. The repository also includes an experimental generative
data codec described below. Model-generated carriers are not guaranteed to be
natural, robust, or undetectable; use the feature only where you are authorized
to transmit encoded data.

## Messaging-app workflow

The general-purpose entry point is `chat`. Two people first meet physically and
agree on all three of these values:

- a long secret phrase (prefer six or more randomly chosen words);
- one conversation name, including capitalization;
- the exact local model/runtime configuration.

The phrase can be entered invisibly when the client starts, or supplied through
`DECALGO_SECRET`. It is stretched with PBKDF2-HMAC-SHA-256 (600,000 rounds),
separated by conversation name, and never written to disk.

```sh
./decalgo-cli chat -conversation samir-and-alex -me alex
```

The client automatically stores Alex's encrypted state under
`~/.decalgo/conversations/`. When a carrier arrives in WhatsApp, Signal,
Telegram, email, or another text channel:

```text
alex> /paste samir
Paste the exact received carrier, then type /end on a new line:
<paste the complete message here>
/end
```

The plaintext is displayed only after model decoding and AES-SIV
authentication succeed. To reply, type a one-line plaintext directly or use
`/send` for multiple lines. Copy only the ordinary prose between the local UI
markers into the messaging app. The markers, sender, sequence, and time are
never part of the message sent through the platform:

```text
alex> Hi Samir, yes, I finished the maths homework.
Generating carrier…

SEND THIS TEXT in the messaging app as alex:
----- BEGIN DECALGO MESSAGE -----
<generated carrier text>
----- END DECALGO MESSAGE -----
```

The state file—including locally decrypted history—is itself encrypted with
AES-GCM under the phrase-derived key and written atomically with mode `0600`.
Using the wrong phrase fails state decryption. Every participant must paste all
messages in exactly the order shown by the messaging platform before sending a
reply. Editing, formatting, translation, whitespace normalization, or a missed
message breaks synchronization and is rejected.

For shell automation rather than an invisible prompt:

```sh
export DECALGO_SECRET='six genuinely random words shared in person'
./decalgo-cli chat -conversation samir-and-alex -me alex
```

Environment variables may be readable by other processes or shell tooling on
some systems, so the hidden prompt is preferable on a personal computer.

List local conversation/identity state names with:

```sh
./decalgo-cli conversations
```

Inside `chat`, `/status` shows the current identity, automatic state path, and
next global message index plus a short synchronization code; `/show` displays
locally decrypted history. Before exchanging a message, both participants'
`/status` output must show the same `next_index` and `sync` value. If they do
not, do not send: process the missing carriers in order or have both people
start a new conversation ID. Deleting/resetting only one participant's state
does not repair a fork. A carrier must be pasted with the exact sender name and
without autocorrect, formatting, or whitespace changes.

## Protocol sketch

- A shared high-entropy secret derives the AES-256-GCM message key and initial
  chain state using domain-separated HMAC-SHA-256.
- Each call to `Encode` gets a fresh 96-bit random nonce.
- The zero-based message index and current chain hash are authenticated as
  associated data.
- After a successful encode/decode, both peers advance the chain with SHA-256
  over the previous state, index, nonce, and ciphertext.
- The decoder advances state only after authentication succeeds.
- Unmarked input is rejected; applications should preserve the `enc:` prefix
  when copying messages into or out of a chat service.

The API is stateful, so create one `Encoder` and one `Decoder` per conversation
and retain them for the life of that ordered stream. A production messenger
would additionally need secure key exchange, durable atomic state, multi-device
session management, and a ratcheting protocol such as Signal's Double Ratchet.

Run the verification suite with:

```sh
go test ./...
```

## Interactive testing

Build the terminal application and create a temporary test key:

```sh
go build -o decalgo-cli ./cmd/decalgo
export DECALGO_KEY="$(openssl rand -base64 32)"
./decalgo-cli demo
```

In `demo` mode, every line typed at `plain>` is immediately displayed as its
`enc:DEC1.` wire representation and decoded again at `clear>`.

For a two-terminal experiment, both terminals must use the same
`DECALGO_KEY` and conversation identifier. Start `encode` in one and `decode`
in the other, then copy each `wire >` value in order:

```sh
./decalgo-cli encode -conversation chat-42
./decalgo-cli decode -conversation chat-42
```

The current test CLI keeps chain state in memory. Restart both sides together
before beginning a new test conversation.

## Generative data codec

The separate generative codec embeds arbitrary bytes in choices among a causal
language model's next tokens. Its default arithmetic distribution matcher
quantizes the model probabilities deterministically and makes selected tokens
follow that distribution. This is substantially more natural than assigning a
fixed number of bits uniformly to top-N tokens. The old `uniform` mode and an
intermediate `huffman` mode remain available for comparison.

Install the optional model runtime (prefer a dedicated virtual environment):

```sh
python3 -m pip install torch transformers
```

Build the CLI, then generate text and recover its payload:

```sh
go build -o decalgo-cli ./cmd/decalgo
printf 'binary payload' | ./decalgo-cli generate \
  -model openai-community/gpt2 -revision main -top-n 8 \
  -prompt 'The weather today is' > generated.txt
./decalgo-cli extract \
  -model openai-community/gpt2 -revision main -top-n 8 \
  -prompt 'The weather today is' < generated.txt > recovered.bin
```

On this machine, `decalgo.local.json` is already configured for the cached
MLX `Meta-Llama-3.1-8B-Instruct-4bit` snapshot and its working Python virtual
environment. From the repository root the short form is therefore:

```sh
export DECALGO_KEY="$(openssl rand -base64 32)"
printf 'binary payload' | ./decalgo-cli generate > generated.txt
./decalgo-cli extract < generated.txt > recovered.bin
```

Command-line flags override the local configuration. `DECALGO_MODEL`,
`DECALGO_REVISION`, `DECALGO_RUNTIME`, and `DECALGO_PYTHON` can also override
it. The MLX backend is implemented by `mlx_model.py` and maintains a KV cache
while candidate tokens are selected. Local defaults use arithmetic coding over
top 256, the model's native temperature, a Llama chat-template prompt, and a
short verified greedy suffix to land on sentence-ending punctuation.

Secure mode is enabled locally. AES-256-GCM encrypts and authenticates the
payload before it reaches the model; its associated data binds the conversation
identifier, direction, and sequence number. Peers must use the same key and
flags. Increment `-sequence` for every message in a direction, and use distinct
direction names for the two sides, for example `alice-to-bob` and
`bob-to-alice`. Reusing a sequence is rejected only if the receiving application
tracks its expected value, so durable sequence state is required in a real app.

For a live conversation, both peers can pass `-prompt-file transcript.prompt`
to use an identical rolling model context. Update that file identically and
atomically after each accepted carrier; a missing, reordered, or normalized
message will otherwise desynchronize all following token probabilities. The
prompt file must contain the complete tokenizer-ready context, including the
Llama chat-template control tokens when using an instruct checkpoint.

For reproducible communication, pin `-revision` to an immutable Hugging Face
commit rather than `main`. Both peers must use the same model revision,
tokenizer, prompt, top-N, PyTorch/Transformers versions, dtype, and device. The
backend fingerprint includes these synchronization inputs and the resolved
model commit. Candidate scores are sorted descending with token ID as a stable
tie-breaker. Reserved tokenizer control tokens are excluded from candidates.

Generated text must be transported byte-for-byte. Whitespace normalization,
smart-quote replacement, or adding/removing a trailing newline can change its
token sequence and make extraction fail. The encoder checks that its generated
token IDs survive a detokenize/tokenize round trip before returning text.

The Go API is `NewGenerativeCodec` with a `LanguageModel` implementation. The
included `ProcessModel` speaks a small line-delimited JSON protocol to
`hf_model.py`, so applications can substitute another deterministic local model
runtime without changing the codec.

## Multi-party conversation chains

`chain-send` and `chain-receive` maintain a durable ordered group conversation
instead of treating each carrier independently. Every participant keeps a
private state file. A record contains the visible sender and encrypted carrier;
JSON escaping preserves multiline carrier text exactly.

```sh
export DECALGO_KEY='<the same base64 group key on every device>'

# Samir creates the first transport record and sends record-1.json to the group.
printf 'hi alex' | ./decalgo-cli chain-send \
  -conversation friends -state samir.state -from samir > record-1.json

# Alex accepts that exact record, then creates the next one.
./decalgo-cli chain-receive \
  -conversation friends -state alex.state < record-1.json
printf 'hi samir, did you do your homework?' | ./decalgo-cli chain-send \
  -conversation friends -state alex.state -from alex > record-2.json

# Samir accepts Alex's reply.
./decalgo-cli chain-receive \
  -conversation friends -state samir.state < record-2.json
```

Continue in the same way for Samir, Alex, or any additional participant. Sender
sequence numbers are tracked independently, while `index` defines one global
message order. A new member can start from an authenticated copy of the public
record history, although they cannot decrypt earlier messages unless those
records are replayed through their state in order.

View the locally known equivalent of `from | decrypted | encrypted` as JSONL:

```sh
./decalgo-cli chain-show -conversation friends -state alex.state
./decalgo-cli chain-show -conversation friends -state alex.state -format table
```

Each encrypted payload authenticates all of the following:

- group conversation identifier;
- exact sender identity;
- sender-specific sequence number;
- global record index;
- hash of every preceding sender and carrier.

Consequently, deletion, insertion, reordering, chain forks, wrong keys, and
carrier edits fail before state is committed. State is
written through a temporary `0600` file and atomically renamed. Applications
must still serialize local sends/receives and prevent restoring an old state
backup, since concurrent writers or rollback can fork a conversation.

The shared group key authenticates a sender name to the group, but it does not
provide non-repudiation between group members: anyone holding that same key can
construct a record claiming another member's name. A production group messenger
must add per-member signing keys and authenticated membership changes if insider
impersonation is in scope.

There is an unavoidable capacity tradeoff, but the chain carrier contains no
sender label, timestamp, index, conversation name, or random nonce. Ordinary
chat text is first compressed with a shared static dictionary. AES-SIV then
adds only its required 16-byte authentication tag. The model frame adds a
variable-length payload count (normally one byte) and arithmetic coding adds a
four-byte termination guard. Sender identity, ordering, and prior chain hash
are synchronized associated data rather than encoded payload bytes. Before
each interactive send, the local UI prints this complete byte budget; that
diagnostic line is never part of the messaging-platform carrier.

### Interactive tester

Keep Llama loaded and exercise a whole multi-person chain in one terminal:

```sh
./decalgo-cli chain-chat \
  -conversation friends \
  -state interactive.state \
  -from samir
```

At `samir>` type any plaintext message. The tester prints a one-line JSON
transport record after generating its carrier. Useful commands are:

```text
/as alex          switch the simulated sender
/show             print from|decrypted|encrypted history
/record 0         print a prior transport record again
/receive {JSON}   accept a record copied from another participant
/help             show commands
/quit             save and exit
```

For a local simulation, use `/as` to move between Samir, Alex, and other names.
For a real two-terminal test, give each terminal its own state file and paste
the entire single-line `record>` JSON after `/receive `. The JSON wrapper is
important because it preserves newlines and whitespace inside generated text.
