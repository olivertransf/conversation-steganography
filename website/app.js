const STEPS = [
  {
    id: "phrase",
    title: "Setup and shared phrase",
    summary:
      "Before any secret is sent, both machines must agree on three things: the same local model, the same conversation name, and the same long phrase. Only the phrase is secret.",
    detail: `
      <p><strong>What you actually do:</strong> run setup, pick Llama (or GPT-2), then later start chat with <code>-conversation coffee-plans</code> and type a phrase (≥16 characters) when prompted.</p>
      <ol class="micro">
        <li>Setup writes <code>conversation-stenography.local.json</code> (model path, prompts, top_n, etc.) and starts a local Python process that loads weights. No cloud API.</li>
        <li>On send/receive, <code>DeriveKeyFromPhrase(phrase, conversation)</code> runs PBKDF2-HMAC-SHA-256 with <strong>600,000</strong> iterations.</li>
        <li>Salt = <code>"decalgo-shared-phrase-v1\\0" + conversation</code>. Same phrase + different conversation name → different key.</li>
        <li>Output is a 32-byte key kept in memory for that session. The phrase is not written to disk.</li>
        <li>Separately, the chain starts at <code>sha256("decalgo-group-chain-v1\\0" + conversation)</code> so both sides share a rolling transcript hash.</li>
      </ol>
      <p>Everything that shapes generation (model revision, <code>chain_system</code>, coding mode) is <em>protocol</em>, not secret — but both peers must match it or decode breaks.</p>
    `,
    example: `
      <p>Phrase: <code>purple elephant dances…</code></p>
      <p>Conversation: <code>coffee-plans</code></p>
      <p>→ Key<sub>A</sub> = PBKDF2(phrase, salt=<code>…coffee-plans</code>)</p>
      <p>If Samir typed <code>coffee-plan</code> (missing s), Key<sub>B</sub> ≠ Key<sub>A</sub>. AES-SIV open fails. No partial leak of plaintext.</p>
    `,
    why: `
      <p>Cryptography needs a shared secret. Using a memorable phrase + public conversation name gives you a key without shipping key files. Wrong phrase = hard fail, not “almost readable.”</p>
    `,
    files: ["phrase.go", "cmd/.../setup.go", "cmd/.../keys.go"],
    viz: "phrase",
  },
  {
    id: "pack",
    title: "Pack the plaintext",
    summary:
      "Your secret string becomes a shorter byte blob before encryption. This step is ordinary compression with chat-specific dictionaries — still fully private.",
    detail: `
      <p><strong>Input:</strong> UTF-8 plaintext like <code>meet at noon</code>.</p>
      <p><strong>Output:</strong> one mode byte + packed body. The mode is remembered because decryption must use the same unpacker.</p>
      <ol class="micro">
        <li><code>packMessageDetached</code> tries several encodings in parallel.</li>
        <li>Examples: raw copy, DEFLATE with a shared <code>chatDictionary</code>, phrase/fragment tables, “dynamic” DEFLATE that also feeds prior <em>public covers</em> into the dictionary.</li>
        <li>Whichever encoding is shortest wins.</li>
        <li>Later, that mode is glued into authenticated data as <code>'p','a','c','k' || mode</code>, so an attacker cannot swap modes.</li>
      </ol>
      <p>Packing does <em>not</em> hide the message by itself. It only shrinks how many cover paragraphs you will need.</p>
    `,
    example: `
      <p>Plaintext length: 12 bytes (<code>meet at noon</code>).</p>
      <p>Suppose DEFLATE+dictionary wins → packed length 9 bytes, <code>mode = 1</code>.</p>
      <p>You now hold <code>[1][9 packed bytes]</code> in memory. WhatsApp has seen nothing yet.</p>
    `,
    why: `
      <p>Each cover bubble can only carry a limited number of secret bits (roughly related to how many token choices you make). Smaller packed payload → fewer bubbles → less typing/pasting.</p>
    `,
    files: ["message_compression.go"],
    viz: "pack",
  },
  {
    id: "seal",
    title: "Seal once with AES-SIV",
    summary:
      "Packed bytes are encrypted and authenticated in one shot. You get a single sealed blob for the whole logical message — even if it later becomes three chat bubbles.",
    detail: `
      <p><strong>Primitive:</strong> AES-SIV (RFC 5297). Deterministic: same key + same AAD + same plaintext → same ciphertext. No random nonce to sync.</p>
      <ol class="micro">
        <li>From the 32-byte conversation key, derive a MAC key and an ENC key (HMAC with fixed labels).</li>
        <li>Build <code>logicalAAD</code>: protocol label, conversation id, sender name, <em>chain hash before this send</em>, sender sequence number, optional trial id, pack mode.</li>
        <li><code>sealSIV(key, aad, packed)</code> → <code>tag (16 bytes) || ciphertext</code>.</li>
        <li>That whole thing is the “logical message.” Chunks later are slices of <em>this</em> blob, not separate encryptions.</li>
      </ol>
      <p><strong>Why snapshot chain hash?</strong> While you generate cover 2 and 3, the tool may already plan commits that advance the chain. Opening still uses the AAD from <em>before</em> the send started.</p>
      <p><strong>Trials:</strong> if a cover looks unnatural, reseal with a different trial byte in AAD so ciphertext bits change and a new cover can be sampled.</p>
    `,
    example: `
      <p>Packed: 9 bytes. Sealed: 16 + 9 = 25 bytes (toy sizes).</p>
      <p>AAD includes <code>coffee-plans</code>, <code>alex</code>, chainHash=<code>a3f1…</code>, seq=4, pack mode=1.</p>
      <p>Friend must reconstruct the same AAD or the SIV tag check fails — even if they somehow recovered the ciphertext bytes.</p>
    `,
    why: `
      <p>Confidentiality (chat apps never see plaintext) + authenticity (wrong phrase / wrong order / wrong packing mode fails closed). One seal for N covers keeps the message atomic.</p>
    `,
    files: ["siv.go", "conversation_chain_message.go"],
    viz: "seal",
  },
  {
    id: "chunk",
    title: "Split into wire chunks",
    summary:
      "If the sealed blob is larger than one cover can carry, it is sliced. Each slice is wrapped with tiny headers (part numbers) that will also be hidden inside cover text.",
    detail: `
      <p><strong>Still private:</strong> these headers never appear as visible “Part 1/3” in WhatsApp. They live inside the bit payload that stego will embed.</p>
      <ol class="micro">
        <li>Estimate how many payload bytes one cover can hold from <code>max_cover_chars</code> and <code>capacity_top_n</code>.</li>
        <li><code>splitSealed</code> cuts the sealed blob into pieces P0, P1, …</li>
        <li>Part 0 wire = uvarint(part) || uvarint(total) || uvarint(sealedLen) || uvarint(len(P0)) || P0</li>
        <li>Later parts omit sealedLen (already known from part 0).</li>
        <li>Each <code>wire_i</code> is what the generative encoder will turn into cover paragraph <em>i</em>.</li>
      </ol>
      <p>If encoding produces a cover that is still too long, the tool shrinks the piece budget and retries.</p>
    `,
    example: `
      <p>Sealed = 25 bytes. Suppose max piece ≈ 12 bytes → need 3 pieces.</p>
      <pre>wire0: part=0 total=3 sealedLen=25 len=12 | P0
wire1: part=1 total=3 len=12 | P1
wire2: part=2 total=3 len=1  | P2</pre>
      <p>You will paste/send <strong>three</strong> ordinary-looking bubbles, in order.</p>
    `,
    why: `
      <p>Human chat bubbles are short. Capacity per bubble is limited. Chunking is how long secrets become a short sequence of natural messages.</p>
    `,
    files: ["message_chunks.go", "conversation_chain_message.go"],
    viz: "chunk",
  },
  {
    id: "encode",
    title: "Embed bits in next-token choices",
    summary:
      "This is the steganography. Ciphertext bits do not become weird characters. They choose which fluent next-token the local model is allowed to write.",
    detail: `
      <p><strong>Mental model:</strong> imagine the model offers a menu of likely next words. Your secret bits are a finger pointing at one menu item. The word looks normal; the <em>choice</em> carries information.</p>
      <ol class="micro">
        <li>Build a prompt: system instructions (casual texting) + earlier public covers + “write the next message.”</li>
        <li>Ask the local model for the top-N next tokens with log-probabilities.</li>
        <li><code>makeFrequencies</code> turns those scores into integer slot counts totaling 32768 (arithmetic coding).</li>
        <li>An arithmetic decoder reads bits from <code>wire_i</code> and lands in one slot → that token is appended to the cover.</li>
        <li>Repeat: new prompt context includes the tokens already chosen, model proposes again, more bits spent.</li>
        <li>When bits are done, finish the sentence with greedy top-1 tokens so it ends cleanly.</li>
        <li>Filters: strict style (no digit junk / meta words), must look human, optional semantic judge, max length.</li>
      </ol>
      <p><strong>Why the friend can recover bits:</strong> same model + same prompt + same history ⇒ same candidate menu ⇒ seeing which token you picked reveals which slot ⇒ reveals bits.</p>
      <p>Scroll to <a href="#token-lab">The core trick</a> for an interactive version of this idea.</p>
    `,
    example: `
      <p>Prompt ends with prior chat. Model top tokens might be:</p>
      <pre>" yeah" 22%
" traffic" 18%
" honestly" 14%
…</pre>
      <p>Next secret bits fall in the <code>" traffic"</code> slice → cover grows by that token.</p>
      <p>After many steps: <code>yeah traffic was light for once…</code> — fluent English whose token path uniquely encodes <code>wire0</code>.</p>
    `,
    why: `
      <p>Observers see chat. Endpoints see a bit channel. That gap is the whole product.</p>
    `,
    files: ["generative.go", "arithmetic.go", "conversation_chain.go", "python/mlx_model.py"],
    viz: "encode",
  },
  {
    id: "commit",
    title: "Multi-cover continuity and chain commit",
    summary:
      "Generate every cover for this logical send first. Only if all succeed, append them to the conversation chain hash. Same-sender parts are prompted to continue one thought.",
    detail: `
      <ol class="micro">
        <li>While encoding part 1, the prompt already includes cover text from part 0 (buffered, not yet committed).</li>
        <li>If the last public record’s sender is you, instructions say: advance the thought, don’t rephrase, don’t reuse the opener.</li>
        <li>Human/length checks run per cover. Any failure can trigger another carrier trial (reseal + re-encode).</li>
        <li>Only when every part succeeds: for each cover, create a <code>ChainRecord{index, from, senderSequence, encrypted: coverText}</code> and <code>commit</code>.</li>
        <li>Commit updates <code>chain = sha256(label || oldChain || index || seq || from || cover)</code>.</li>
      </ol>
      <p>Sync code in the UI is a short hex prefix of <code>chain</code> — a quick “are we desynced?” check.</p>
    `,
    example: `
      <p>Three covers generated successfully → three commits.</p>
      <p>Transcript now publicly contains those three strings in order.</p>
      <p>Next send’s AAD will include the <em>new</em> chain hash. Reordering covers later breaks open.</p>
    `,
    why: `
      <p>All-or-nothing avoids “friend decoded part 1 but part 2 never existed.” Continuation keeps multi-bubble covers reading like one person double-texting.</p>
    `,
    files: ["conversation_chain.go", "conversation_chain_message.go"],
    viz: "commit",
  },
  {
    id: "channel",
    title: "Public channel: just chat text",
    summary:
      "You manually copy each cover into any messaging app. That app is treated as an insecure bulletin board for strings.",
    detail: `
      <ol class="micro">
        <li>CLI prints a <code>----- copy -----</code> block per cover.</li>
        <li>You paste into WhatsApp/Signal/iMessage/email/etc. as yourself.</li>
        <li>Send bubbles in order. Do not edit, autocorrect, or “fix” punctuation.</li>
        <li>Friend copies the exact text back into their tool with <code>/paste yourname</code>.</li>
      </ol>
      <p>Nothing cryptographic is marked in the bubble. No <code>enc:</code> prefix, no zero-width watermark required for the main path — the information is in the token sequence of ordinary words.</p>
    `,
    example: `
      <p>What WhatsApp stores:</p>
      <pre>alex: yeah traffic was light for once…
alex: also that place downtown opened late seats
alex: want to try Thursday?</pre>
      <p>What it does <em>not</em> store: keys, tags, pack mode, part numbers as visible headers.</p>
    `,
    why: `
      <p>Threat model: metadata and “this looks encrypted” can be as dangerous as content. Here the observable is casual chat.</p>
    `,
    files: ["cmd/.../interactive.go", "cmd/.../simulate.go"],
    viz: "channel",
  },
  {
    id: "decode",
    title: "Paste, reassemble, open, unpack",
    summary:
      "Decoding reverses each step with the same shared state. Wrong phrase, model, edit, or order → authentication failure or “waiting for parts,” not garbage English secrets.",
    detail: `
      <ol class="micro">
        <li>Friend runs <code>/paste alex</code> and pastes cover text exactly, then <code>/end</code>.</li>
        <li>Tool rebuilds the generative prompt from the shared transcript <em>before</em> this cover (and continuation rules if same sender).</li>
        <li>Tokenizer walks the cover; at each step rebuilds top-N, sees which token matched, emits bits (arithmetic encode side of the same interval math).</li>
        <li>Recovered bytes are parsed as chunk wire → <code>acceptChunk</code>.</li>
        <li>Part 0 opens a <code>PendingAssembly</code> storing chainHashBefore + senderSeqBefore + sealedLen.</li>
        <li>UI may say <code>Waiting for part 2/3…</code> until all pieces arrive; each accepted cover is committed to the chain.</li>
        <li>When complete: <code>joinSealed</code> → try <code>openSIV</code> across packing modes / trials with reconstructed <code>logicalAAD</code> → <code>unpack</code> → show plaintext.</li>
      </ol>
      <p>Local history can save decrypted text inside an AES-GCM <code>DCS1</code> file on disk. That is separate from what the messaging app sees.</p>
    `,
    example: `
      <p>After three pastes: pieces reassemble to the 25-byte sealed blob.</p>
      <p>openSIV with Key<sub>A</sub> + AAD succeeds → packed bytes → unpack mode 1 → <code>meet at noon</code>.</p>
      <p>If cover 2 had a smart-quote change (<code>'</code> vs <code>’</code>), token path diverges → wrong bits → wire parse/auth fails.</p>
    `,
    why: `
      <p>Security lives in openSIV + matched generative state. Stego only transports bytes; it does not replace cryptography.</p>
    `,
    files: ["conversation_chain_message.go", "generative.go", "siv.go", "cmd/.../chain.go"],
    viz: "decode",
  },
];

const CANDIDATES = [
  { token: " yeah", p: 0.22 },
  { token: " traffic", p: 0.18 },
  { token: " honestly", p: 0.14 },
  { token: " that", p: 0.12 },
  { token: " weird", p: 0.1 },
  { token: " I", p: 0.09 },
  { token: " still", p: 0.08 },
  { token: " maybe", p: 0.07 },
];

let stepIndex = 0;
let labBits = "10110010";
let labCursor = 0;
let coverTokens = [];

const stepList = document.getElementById("step-list");
const flowStrip = document.getElementById("flow-strip");
const panel = document.getElementById("step-panel");
const kicker = document.getElementById("step-kicker");
const title = document.getElementById("step-title");
const summary = document.getElementById("step-summary");
const detail = document.getElementById("step-detail");
const example = document.getElementById("step-example");
const why = document.getElementById("step-why");
const files = document.getElementById("step-files");
const viz = document.getElementById("step-viz");

function renderNav() {
  stepList.innerHTML = STEPS.map(
    (step, i) => `
      <li>
        <button type="button" data-step="${i}" class="${i === stepIndex ? "active" : ""}">
          <strong>${String(i + 1).padStart(2, "0")} · ${step.id}</strong>
          <span>${step.title}</span>
        </button>
      </li>
    `
  ).join("");
}

function renderFlow() {
  [...flowStrip.children].forEach((li, i) => {
    li.classList.toggle("active", i === stepIndex);
  });
}

function vizPhrase() {
  return `
    <div class="anno">
      <p class="anno-title">What must match on both laptops</p>
      <div class="bits-row">
        <span class="chip private">phrase → key (secret)</span>
        <span class="chip public">conversation name (public salt)</span>
        <span class="chip public">model + prompts (public protocol)</span>
      </div>
    </div>
    <div class="pipeline" style="margin-top:1rem">
      <div class="pipe-node on"><strong>PBKDF2</strong><span>600k × SHA-256</span></div>
      <span class="pipe-arrow">→</span>
      <div class="pipe-node on"><strong>32-byte key</strong><span>RAM only</span></div>
      <span class="pipe-arrow">+</span>
      <div class="pipe-node on"><strong>chain seed</strong><span>sha256(conversation)</span></div>
    </div>
  `;
}

function vizPack() {
  return `
    <div class="anno">
      <p class="anno-title">Still on your device — WhatsApp sees nothing</p>
    </div>
    <div class="pipeline">
      <div class="pipe-node on"><strong>"meet at noon"</strong><span>12 UTF-8 bytes</span></div>
      <span class="pipe-arrow">→</span>
      <div class="pipe-node on"><strong>try encoders</strong><span>raw / deflate / fragments…</span></div>
      <span class="pipe-arrow">→</span>
      <div class="pipe-node on"><strong>winner</strong><span>mode byte + packed body</span></div>
    </div>
  `;
}

function vizSeal() {
  return `
    <div class="anno">
      <p class="anno-title">Authenticated data (must match to open)</p>
      <div class="aad-stack">
        <span class="aad-item">label large-msg-v1</span>
        <span class="aad-item">coffee-plans</span>
        <span class="aad-item">alex</span>
        <span class="aad-item">chainHashBefore</span>
        <span class="aad-item">senderSeq</span>
        <span class="aad-item">pack mode</span>
      </div>
    </div>
    <div class="pipeline" style="margin-top:1rem">
      <div class="pipe-node on"><strong>packed</strong><span>private bytes</span></div>
      <span class="pipe-arrow">→</span>
      <div class="pipe-node on"><strong>AES-SIV</strong><span>one seal for whole message</span></div>
      <span class="pipe-arrow">→</span>
      <div class="pipe-node on"><strong>tag||ct</strong><span>still private</span></div>
    </div>
  `;
}

function vizChunk() {
  return `
    <div class="anno">
      <p class="anno-title">Headers travel inside stego bits, not as visible “1/3”</p>
    </div>
    <div class="chunk-row">
      <span class="chunk">sealed 25 B</span>
      <span class="pipe-arrow">→</span>
      <span class="chunk"><strong>wire0</strong> part0 total3 + sealedLen + P0</span>
      <span class="chunk"><strong>wire1</strong> part1 + P1</span>
      <span class="chunk"><strong>wire2</strong> part2 + P2</span>
    </div>
  `;
}

function vizEncode() {
  const ranked = frequencies(CANDIDATES);
  return `
    <div class="anno">
      <p class="anno-title">Secret bits = a point on this line; token = which slice it hits</p>
      <div class="ruler static-ruler">
        ${ranked
          .map(
            (c, i) =>
              `<span class="slice ${i === 1 ? "hit" : ""}" style="flex:${c.p}">${c.token.trim()}</span>`
          )
          .join("")}
      </div>
    </div>
    <div class="candidate-viz" style="margin-top:0.85rem">
      ${CANDIDATES.map(
        (c, i) => `
        <div class="row ${i === 1 ? "picked" : ""}">
          <span><code>${c.token}</code></span>
          <span class="bar"><i style="width:${(c.p / CANDIDATES[0].p) * 100}%"></i></span>
          <span>${i === 1 ? "bits landed here" : Math.round(c.p * 100) + "%"}</span>
        </div>
      `
      ).join("")}
    </div>
  `;
}

function vizCommit() {
  return `
    <div class="cover-bubbles">
      <div class="bubble"><strong>1/3</strong> Traffic was light for once…</div>
      <div class="bubble" style="animation-delay:120ms"><strong>2/3</strong> Also that place downtown opened late seats.</div>
      <div class="bubble" style="animation-delay:240ms"><strong>3/3</strong> Want to try Thursday?</div>
    </div>
    <p class="viz-caption">Labels 1/3 are for this diagram only — real WhatsApp bubbles do not show them.</p>
    <div class="pipeline" style="margin-top:0.75rem">
      <div class="pipe-node on"><strong>all covers OK</strong><span>then commit</span></div>
      <span class="pipe-arrow">→</span>
      <div class="pipe-node on"><strong>chain hash updates</strong><span>order is bound</span></div>
    </div>
  `;
}

function vizChannel() {
  return `
    <div class="bits-row">
      <span class="chip private">sealed bits on laptop</span>
      <span class="pipe-arrow">→</span>
      <span class="chip public">only cover strings in the app</span>
      <span class="pipe-arrow">→</span>
      <span class="chip private">bits recovered on friend’s laptop</span>
    </div>
  `;
}

function vizDecode() {
  return `
    <div class="pipeline">
      <div class="pipe-node on"><strong>exact paste</strong><span>/paste alex</span></div>
      <span class="pipe-arrow">→</span>
      <div class="pipe-node on"><strong>replay model</strong><span>same menus</span></div>
      <span class="pipe-arrow">→</span>
      <div class="pipe-node on"><strong>bits out</strong><span>wire bytes</span></div>
      <span class="pipe-arrow">→</span>
      <div class="pipe-node on"><strong>join + openSIV</strong><span>auth check</span></div>
      <span class="pipe-arrow">→</span>
      <div class="pipe-node on"><strong>meet at noon</strong><span>plaintext</span></div>
    </div>
  `;
}

const VIZ = {
  phrase: vizPhrase,
  pack: vizPack,
  seal: vizSeal,
  chunk: vizChunk,
  encode: vizEncode,
  commit: vizCommit,
  channel: vizChannel,
  decode: vizDecode,
};

function renderStep() {
  const step = STEPS[stepIndex];
  panel.classList.remove("swap");
  void panel.offsetWidth;
  panel.classList.add("swap");

  kicker.textContent = `Stage ${stepIndex + 1} of ${STEPS.length}`;
  title.textContent = step.title;
  summary.textContent = step.summary;
  detail.innerHTML = step.detail;
  example.innerHTML = step.example;
  why.innerHTML = step.why;
  files.innerHTML = step.files.map((f) => `<li>${f}</li>`).join("");
  viz.innerHTML = VIZ[step.viz]();

  document.getElementById("prev-step").disabled = stepIndex === 0;
  document.getElementById("next-step").textContent =
    stepIndex === STEPS.length - 1 ? "Back to start" : "Next";

  renderNav();
  renderFlow();

  requestAnimationFrame(() => {
    viz.querySelectorAll(".pipe-node").forEach((node, i) => {
      node.classList.remove("on");
      setTimeout(() => node.classList.add("on"), 70 * i);
    });
  });
}

function goTo(index) {
  stepIndex = (index + STEPS.length) % STEPS.length;
  renderStep();
}

function frequencies(list) {
  const total = list.reduce((s, c) => s + c.p, 0);
  let acc = 0;
  return list.map((c) => {
    const start = acc;
    acc += c.p / total;
    return { ...c, start, end: acc };
  });
}

function pickFromBits(bits, cursor) {
  const ranked = frequencies(CANDIDATES);
  let lo = 0;
  let hi = 1;
  let used = 0;
  for (let i = cursor; i < bits.length && hi - lo > 1 / 64; i++) {
    const mid = (lo + hi) / 2;
    if (bits[i] === "1") lo = mid;
    else hi = mid;
    used++;
  }
  const target = (lo + hi) / 2;
  const picked = ranked.find((c) => target >= c.start && target < c.end) || ranked[ranked.length - 1];
  return { picked, used, ranked, target };
}

function renderRuler(ranked, target) {
  const ruler = document.getElementById("bit-ruler");
  if (!ruler) return;
  ruler.innerHTML = ranked
    .map((c) => {
      const hit = target >= c.start && target < c.end;
      return `<span class="slice ${hit ? "hit" : ""}" style="flex:${Math.max(c.p, 0.04)}">${c.token.trim()}</span>`;
    })
    .join("");
  const marker = document.createElement("i");
  marker.className = "marker";
  marker.style.left = `${target * 100}%`;
  ruler.appendChild(marker);
}

function renderLab() {
  const { picked, ranked, target } = pickFromBits(labBits, labCursor);
  renderRuler(ranked, target);
  const list = document.getElementById("candidate-list");
  list.innerHTML = ranked
    .map(
      (c) => `
      <li class="${c.token === picked.token ? "picked" : ""}">
        <span><code>${c.token}</code></span>
        <span>${(c.p * 100).toFixed(0)}% of line</span>
        <span>${c.token === picked.token ? "bits point here" : ""}</span>
      </li>
    `
    )
    .join("");
  document.getElementById("cover-so-far").textContent =
    coverTokens.length === 0 ? "…" : coverTokens.join("");
  document.getElementById("lab-status").textContent =
    coverTokens.length === 0
      ? `Unread bits start at index ${labCursor}. The marker shows where the next bits land on the candidate line.`
      : `Next unread bit index: ${labCursor}. Each click spends bits and appends one token — that is encoding.`;
}

function stepLab() {
  labBits = (document.getElementById("bit-input").value || "").replace(/[^01]/g, "") || "10110010";
  document.getElementById("bit-input").value = labBits;
  if (labCursor >= labBits.length) labCursor = 0;
  const { picked, used } = pickFromBits(labBits, labCursor);
  coverTokens.push(picked.token);
  labCursor += Math.max(1, used);
  if (labCursor >= labBits.length) labCursor = 0;
  renderLab();
  const pickedRow = document.querySelector("#candidate-list li.picked");
  if (pickedRow && pickedRow.animate) {
    pickedRow.animate(
      [{ transform: "scale(1)" }, { transform: "scale(1.02)" }, { transform: "scale(1)" }],
      { duration: 280, easing: "ease" }
    );
  }
}

function resetLab() {
  labCursor = 0;
  coverTokens = [];
  labBits = (document.getElementById("bit-input").value || "").replace(/[^01]/g, "") || "10110010";
  renderLab();
}

function bind() {
  stepList.addEventListener("click", (e) => {
    const btn = e.target.closest("button[data-step]");
    if (!btn) return;
    goTo(Number(btn.dataset.step));
  });

  flowStrip.addEventListener("click", (e) => {
    const li = e.target.closest("li[data-step]");
    if (!li) return;
    goTo(Number(li.dataset.step));
    document.getElementById("walkthrough").scrollIntoView({ behavior: "smooth", block: "start" });
  });

  document.getElementById("prev-step").addEventListener("click", () => goTo(stepIndex - 1));
  document.getElementById("next-step").addEventListener("click", () => goTo(stepIndex + 1));
  document.getElementById("run-lab").addEventListener("click", stepLab);
  document.getElementById("reset-lab").addEventListener("click", resetLab);
  document.getElementById("bit-input").addEventListener("input", () => {
    labBits = document.getElementById("bit-input").value.replace(/[^01]/g, "");
    document.getElementById("bit-input").value = labBits;
    resetLab();
  });

  document.addEventListener("keydown", (e) => {
    if (e.target.matches("input, textarea")) return;
    if (e.key === "ArrowRight") goTo(stepIndex + 1);
    if (e.key === "ArrowLeft") goTo(stepIndex - 1);
  });
}

renderNav();
renderStep();
renderLab();
bind();
