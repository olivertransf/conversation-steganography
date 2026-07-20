# Conversation Stenography

**Hide secret messages inside normal-looking chat text.**

Conversation Stenography lets two people talk privately through any messaging app (WhatsApp, Telegram, Signal, iMessage, email, Instagram DMs, …). Your secrets are encrypted, then disguised as ordinary chat text from a local AI model. The messaging app only ever sees the cover text.

## Why

- Private messaging is under more scanning pressure
- “Obviously encrypted” traffic can itself look suspicious

I’m 18, and I’m not the first person on this idea. LLM steganography has been around for years — even GPT-2 shipped in 2019. This project is a practical local demo of that pattern.

**Example:** you type `meet me at the coffee shop at 3pm` and get something like `Hey, traffic was light for once. Want to grab dinner later?` for the chat app. Your friend runs it through Conversation Stenography and gets `meet me at the coffee shop at 3pm` back.

<img src="images/image.png"/>

Your plaintext never leaves your device unencrypted.

## Quick start

### 1. Install

```sh
git clone https://github.com/olivertransf/conversation-steganography.git
cd conversation-steganography
go build -o conversation-stenography ./cmd/conversation-stenography
```

### 2. Setup wizard

```sh
./conversation-stenography
```

First run walks you through picking a model, downloading it, and writing `conversation-stenography.local.json`.

### 3. Practice on one machine

```sh
# Auto decode (quick check)
./conversation-stenography simulate -dev-secret

# Manual copy/paste (closer to real chat)
./conversation-stenography simulate -dev-secret -manual
```

`-dev-secret` skips the phrase prompt with a local-only test phrase. For a real phrase:

```sh
./conversation-stenography simulate -secret 'my test phrase' -manual
```

In `-manual` mode you get a `----- copy -----` block, then a `paste>` prompt — copy the cover from the terminal and paste it back (blank line or `/end`).

| Command | What it does |
|---|---|
| `/paste [SENDER]` | Paste a cover to decode |
| `/switch` | Switch active user |
| `/show` | Plaintext history |
| `/help` | Help |
| `/quit` | Exit (nothing saved) |

```sh
./conversation-stenography simulate -user-a Alex -user-b Samir -conversation test-chat -dev-secret -manual
```

### 4. Real chat

```sh
./conversation-stenography chat -conversation coffee-plans -me alex
```

Or just `./conversation-stenography` and answer the prompts.

```
  Conversation:  coffee-plans
  You are:       alex

alex> Hey, can we meet tomorrow at noon?
  Encoding [##############..............]  52%

----- copy -----
Traffic was light for once. Want to try that place downtown?
----- end copy -----
  send as alex
```

Copy into your messaging app. Longer secrets can be several paragraphs — send each as its own bubble, in order.

### 5. Receive

```
alex> /paste bob
  Paste the exact message received from bob below.
  Then type /end on a new line when done:

Traffic was light for once. Want to try that place downtown?
/end

  📩 Message from bob:
  Sure, noon works!
```

Multi-cover messages: `/paste` each paragraph in order. You’ll see `Waiting for part 2/3…` until the last one. `/status` shows pending assemblies.

## Two people on different machines

Both sides must match:

1. **Same secret phrase** (agree in person; don’t send it over chat)
2. **Same conversation name** (e.g. `coffee-plans`)
3. **Same model + revision** (same setup-wizard choice)
4. **Same generative settings** (easiest: both run setup the same way)

Each person uses their own name:

```sh
# Machine A
./conversation-stenography chat -conversation coffee-plans -me alex

# Machine B
./conversation-stenography chat -conversation coffee-plans -me samir
```

Then exchange cover text through any app. Process covers in the same order they appear in the chat.

## How it works

Interactive walkthrough (pipeline stages, public vs private, token-selection lab):

```sh
open website/index.html
# or serve: python3 -m http.server -d website 8080
```

```
Your secret message
        ↓
   AES-SIV seal
        ↓
   Local model embeds ciphertext
   in next-token choices
        ↓
Cover text  →  messaging app  →  friend’s tool
        ↓
   Recover bytes from tokens
        ↓
   AES-SIV open + unpack
        ↓
Original message
```

- **AES-SIV** — confidentiality + authenticity  
- **Conversation chain** — order and prior covers are bound; reordering/drops show up  
- **Local model** — no cloud API  
- **Shared phrase** — PBKDF2-HMAC-SHA-256, 600k iterations; not stored on disk  

## Commands

| Command | What it does |
|---|---|
| `./conversation-stenography` | Setup or chat |
| `./conversation-stenography setup` | Re-run setup |
| `./conversation-stenography simulate` | Two users on one device |
| `./conversation-stenography conversations` | List saved conversations |
| `./conversation-stenography chat -conversation NAME -me NAME` | Chat with flags |

### In chat

| Command | What it does |
|---|---|
| _(type)_ | Send (may emit multiple cover paragraphs) |
| `/paste NAME` | Decode one cover from NAME |
| `/send` | Multiline secret (`/end`) |
| `/show` | History |
| `/status` | Sync + pending multi-cover state |
| `/help` | Help |
| `/quit` | Save and exit |

## Models

| Model | Size | Runtime | Best for |
|---|---|---|---|
| **Llama 3.2 3B (4-bit)** | ~2 GB | MLX | Apple Silicon (recommended) |
| **Llama 3.2 1B (4-bit)** | ~1 GB | MLX | Apple Silicon (light) |
| **Llama 3.1 8B (4-bit)** | ~5 GB | MLX | Apple Silicon (quality) |
| **GPT-2** | ~500 MB | Transformers | Any machine |
| **GPT-2 Medium** | ~1.5 GB | Transformers | Any machine |

Needs **Go 1.22+** and **Python 3.9+**. Setup can create a venv and install `mlx-lm` or `torch` + `transformers`.

## Environment

| Variable | Purpose |
|---|---|
| `CONVERSATION_STENOGRAPHY_SECRET` | Shared phrase (skip prompt) |
| `CONVERSATION_STENOGRAPHY_KEY` | Legacy base64 key |
| `CONVERSATION_STENOGRAPHY_CONFIG` | Config path |
| `CONVERSATION_STENOGRAPHY_MODEL` | Model override |
| `CONVERSATION_STENOGRAPHY_PYTHON` | Python override |
| `CONVERSATION_STENOGRAPHY_RUNTIME` | `mlx` or `transformers` |

Don’t commit `conversation-stenography.local.json` (model paths, etc.). Use `conversation-stenography.example.json` as a template.

## Tips

- Copy covers exactly — no autocorrect, smart quotes, or reformatting  
- Paste received covers in chat order  
- Both sides need the same model and phrase  
- Re-enter the phrase each run, or set `CONVERSATION_STENOGRAPHY_SECRET`  
- `-dev-secret` is for local simulate only, not real chats  

## Scripted usage

```sh
export CONVERSATION_STENOGRAPHY_SECRET='purple elephant dances under crimson moonlight'
./conversation-stenography chat -conversation coffee-plans -me alex
```

Multi-party chain helpers:

```sh
export CONVERSATION_STENOGRAPHY_KEY="$(openssl rand -base64 32)"

printf 'hi alex' | ./conversation-stenography chain-send \
  -conversation friends -state bob.state -from bob > record-1.json

./conversation-stenography chain-receive \
  -conversation friends -state alex.state < record-1.json
```

## Build / test

```sh
git clone https://github.com/olivertransf/conversation-steganography.git
cd conversation-steganography
go build -o conversation-stenography ./cmd/conversation-stenography
go test ./...
```

## License

See [LICENSE](LICENSE).
