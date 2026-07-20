const STEPS = [
  {
    id: "phrase",
    title: "Setup and shared phrase",
    summary:
      "Both people run setup, pick the same local model, and agree on a long phrase plus a conversation name. The phrase never goes on disk.",
    detail: `
      <p>First run writes <code>conversation-stenography.local.json</code> and starts a local Python model process (MLX or Transformers). Nothing about covers is sent to a cloud API.</p>
      <p>The shared phrase is stretched into a 32-byte conversation key with PBKDF2-HMAC-SHA-256 at 600,000 iterations. Salt is the public conversation id, labeled <code>decalgo-shared-phrase-v1</code>.</p>
      <p>A separate chain seed is hashed from the conversation name so both sides begin with the same rolling transcript commitment.</p>
    `,
    why: `
      <p>If the phrase or conversation name differ, keys and authenticated data diverge and decode fails. The phrase should be agreed in person, not typed into the chat you are trying to hide inside.</p>
    `,
    files: ["phrase.go", "cmd/.../setup.go", "cmd/.../keys.go"],
    viz: "phrase",
  },
  {
    id: "pack",
    title: "Pack the plaintext",
    summary:
      "Before encryption, the secret is compressed with chat-oriented dictionaries so more meaning fits into fewer cover paragraphs.",
    detail: `
      <p><code>packMessageDetached</code> tries several encodings and keeps the smallest: raw, DEFLATE with a shared chat dictionary, fragment dictionaries, compact/dense codes, dynamic DEFLATE seeded by prior public covers, and exact common-message matches.</p>
      <p>The chosen mode byte is later bound into authenticated data as <code>pack || mode</code>, so a tampered packing claim cannot silently open.</p>
    `,
    why: `
      <p>Ordinary chat text has limited stego capacity per bubble. Packing reduces how many covers a longer secret needs without changing what the messaging app sees.</p>
    `,
    files: ["message_compression.go", "conversation_chain_message.go"],
    viz: "pack",
  },
  {
    id: "seal",
    title: "Seal once with AES-SIV",
    summary:
      "The packed bytes are sealed once with deterministic AES-SIV. Confidentiality and authenticity come from the same primitive.",
    detail: `
      <p><code>sealSIV</code> produces <code>tag(16) || ciphertext</code>. MAC and encryption keys are derived from the conversation key with fixed labels.</p>
      <p>Authenticated data (<code>logicalAAD</code>) binds conversation id, sender, the chain hash before this logical send, sender sequence, optional carrier-trial id, and packing mode. That snapshot is kept so later chunks still open after the chain advances.</p>
      <p>Carrier trials reseal with different trial bytes so the tool can regenerate cover candidates without a random nonce.</p>
    `,
    why: `
      <p>Sealing once for the whole logical message means multi-cover sends are fragments of one authenticated blob, not independent messages that could be reordered silently.</p>
    `,
    files: ["siv.go", "conversation_chain_message.go"],
    viz: "seal",
  },
  {
    id: "chunk",
    title: "Split into wire chunks",
    summary:
      "Large sealed payloads are split into pieces sized for cover capacity. Each piece becomes its own chat bubble.",
    detail: `
      <p><code>splitSealed</code> cuts the sealed blob into pieces. Each piece is wrapped by <code>encodeChunkWire</code>:</p>
      <ul>
        <li>Part 0 carries <code>part</code>, <code>total</code>, full sealed length, and the first piece.</li>
        <li>Later parts carry <code>part</code>, <code>total</code>, and their piece bytes.</li>
      </ul>
      <p>Piece size comes from <code>estimateMaxPieceBytes(max_cover_chars, capacity_top_n)</code>. If a generated cover is still too long, the encoder shrinks the budget and retries.</p>
    `,
    why: `
      <p>Messaging apps and human-looking paragraphs both prefer short bubbles. Chunking keeps covers fluent while letting longer secrets through as an ordered sequence.</p>
    `,
    files: ["message_chunks.go", "conversation_chain_message.go"],
    viz: "chunk",
  },
  {
    id: "encode",
    title: "Embed bits in next-token choices",
    summary:
      "A local language model proposes likely next tokens. An arithmetic coder spends secret bits to choose among the top candidates, producing ordinary-looking text.",
    detail: `
      <p>Interactive sends use a capacity profile: arithmetic coding, higher <code>top_n</code>, and style filters. For each wire chunk the tool builds a prompt from prior public covers plus casual texting instructions.</p>
      <p>Loop: model returns top candidates → frequencies → arithmetic decode step spends secret bits → pick a token → append visible text. Strict style drops odd tokens (digits, meta words). Optional human-written and semantic judges reject unnatural covers.</p>
      <p>Unframed arithmetic encoding omits a length prefix; finish tokens are greedy. Decode enumerates candidates and authenticity selects the real one.</p>
    `,
    why: `
      <p>This is the steganography step. Ciphertext never appears as Base64 or funny Unicode. Observers only see fluent chat that a local model could have written.</p>
    `,
    files: ["generative.go", "arithmetic.go", "conversation_chain.go", "python/mlx_model.py"],
    viz: "encode",
  },
  {
    id: "commit",
    title: "Multi-cover continuity and chain commit",
    summary:
      "If one secret needs several bubbles, later covers continue the same casual thought. Nothing is committed until every cover succeeds.",
    detail: `
      <p>While encoding part <em>i</em>, already-generated covers from this send are appended into the prompt. If the last public record is from the same sender, continuation instructions tell the model to advance the thought instead of rephrasing.</p>
      <p>Only after all covers encode does the tool <code>commit</code> each <code>ChainRecord</code>: index, sender, sequence, and cover text update a rolling SHA-256 chain hash. Sync codes are a short prefix of that hash.</p>
    `,
    why: `
      <p>All-or-nothing commit prevents half-sent state. Continuation cues come from the public transcript so encoder and decoder stay aligned without putting part numbers in the visible prompt.</p>
    `,
    files: ["conversation_chain.go", "conversation_chain_message.go"],
    viz: "commit",
  },
  {
    id: "channel",
    title: "Public channel: just chat text",
    summary:
      "You copy each cover into WhatsApp, Signal, iMessage, email, or any other app. The app only transports ordinary paragraphs.",
    detail: `
      <p>The CLI prints <code>----- copy -----</code> blocks. Longer secrets become multiple bubbles; send them in order as the same person.</p>
      <p>There are no visible headers, stego markers, or ciphertext prefixes on the wire. Order matters because chain index, sender sequence, and chunk part numbers are recovered from the covers themselves.</p>
    `,
    why: `
      <p>The threat model is “obviously encrypted traffic looks suspicious.” Here the observable artifact is casual conversation, while cryptography stays on the endpoints.</p>
    `,
    files: ["cmd/.../interactive.go", "cmd/.../simulate.go"],
    viz: "channel",
  },
  {
    id: "decode",
    title: "Paste, reassemble, open, unpack",
    summary:
      "Your friend pastes each cover with /paste. The tool recovers wire bytes, waits until all parts arrive, then opens the sealed blob and unpacks the plaintext.",
    detail: `
      <p><code>ReceiveMessage</code> builds the same generative prompt from the shared transcript, enumerates decode candidates, and parses chunk wire. Part 0 starts a <code>PendingAssembly</code> that stores the chain hash and sender sequence from before the logical send.</p>
      <p>Each accepted cover is committed. When <code>NextPart == Total</code>, pieces are joined, <code>openSIV</code> tries packing modes and carrier trials against <code>logicalAAD</code>, then <code>unpackMessageWithDictionary</code> restores plaintext.</p>
      <p>Local history can keep decrypted text in an AES-GCM <code>DCS1</code> state file. Peers may differ in what they have decrypted locally while still sharing the same ordered covers.</p>
    `,
    why: `
      <p>Authenticity rejects wrong phrases, wrong models, edited covers, and reordered parts. Incomplete multi-cover sends surface as “Waiting for part x/y” instead of garbage plaintext.</p>
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
    <div class="bits-row">
      <span class="chip private">phrase (private)</span>
      <span class="pipe-arrow">+</span>
      <span class="chip public">conversation id (salt)</span>
      <span class="pipe-arrow">→</span>
      <span class="chip private">PBKDF2 → 32-byte key</span>
    </div>
    <div class="pipeline" style="margin-top:1rem">
      <div class="pipe-node on"><strong>Local model</strong><span>MLX / Transformers</span></div>
      <span class="pipe-arrow">→</span>
      <div class="pipe-node on"><strong>Config</strong><span>prompts, top_n, coding</span></div>
      <span class="pipe-arrow">→</span>
      <div class="pipe-node on"><strong>Chain seed</strong><span>sha256(conversation)</span></div>
    </div>
  `;
}

function vizPack() {
  const modes = ["raw", "deflate", "fragments", "dynamic", "dense"];
  return `
    <div class="pipeline">
      <div class="pipe-node on"><strong>plaintext</strong><span>meet at noon</span></div>
      <span class="pipe-arrow">→</span>
      ${modes
        .map(
          (m, i) =>
            `<div class="pipe-node ${i < 3 ? "on" : ""}"><strong>${m}</strong><span>candidate</span></div>${
              i < modes.length - 1 ? '<span class="pipe-arrow">/</span>' : ""
            }`
        )
        .join("")}
      <span class="pipe-arrow">→</span>
      <div class="pipe-node on"><strong>best packed</strong><span>mode bound in AAD</span></div>
    </div>
  `;
}

function vizSeal() {
  return `
    <div class="aad-stack">
      <span class="aad-item">decalgo-large-msg-v1</span>
      <span class="aad-item">conversation</span>
      <span class="aad-item">from</span>
      <span class="aad-item">chainHashBefore</span>
      <span class="aad-item">senderSeq</span>
      <span class="aad-item">trial?</span>
      <span class="aad-item">pack mode</span>
    </div>
    <div class="pipeline" style="margin-top:1rem">
      <div class="pipe-node on"><strong>packed</strong><span>bytes</span></div>
      <span class="pipe-arrow">→</span>
      <div class="pipe-node on"><strong>AES-SIV</strong><span>seal once</span></div>
      <span class="pipe-arrow">→</span>
      <div class="pipe-node on"><strong>tag || ciphertext</strong><span>logical message</span></div>
    </div>
  `;
}

function vizChunk() {
  return `
    <div class="chunk-row">
      <span class="chunk">sealed blob</span>
      <span class="pipe-arrow">→</span>
      <span class="chunk">wire 0/3 · sealed_len · P0</span>
      <span class="chunk">wire 1/3 · P1</span>
      <span class="chunk">wire 2/3 · P2</span>
    </div>
    <p style="margin:0.9rem 0 0;color:var(--muted);font-size:0.9rem">Each wire chunk is encoded into its own cover paragraph.</p>
  `;
}

function vizEncode() {
  const max = Math.max(...CANDIDATES.map((c) => c.p));
  return `
    <div class="candidate-viz">
      ${CANDIDATES.map(
        (c, i) => `
        <div class="row ${i === 1 ? "picked" : ""}">
          <span><code>${c.token}</code></span>
          <span class="bar"><i style="width:${(c.p / max) * 100}%"></i></span>
          <span>${i === 1 ? "selected by bits" : (c.p * 100).toFixed(0) + "%"}</span>
        </div>
      `
      ).join("")}
    </div>
  `;
}

function vizCommit() {
  return `
    <div class="cover-bubbles">
      <div class="bubble">Traffic was light for once…</div>
      <div class="bubble" style="animation-delay:120ms">Also that place downtown finally opened late seats.</div>
      <div class="bubble" style="animation-delay:240ms">Want to try Thursday?</div>
    </div>
    <div class="pipeline" style="margin-top:1rem">
      <div class="pipe-node on"><strong>all covers ok</strong><span>human + length gates</span></div>
      <span class="pipe-arrow">→</span>
      <div class="pipe-node on"><strong>commit all</strong><span>rolling chain hash</span></div>
    </div>
  `;
}

function vizChannel() {
  return `
    <div class="bits-row">
      <span class="chip private">your device</span>
      <span class="pipe-arrow">→</span>
      <span class="chip public">messaging app sees only covers</span>
      <span class="pipe-arrow">→</span>
      <span class="chip private">friend’s device</span>
    </div>
    <div class="cover-bubbles" style="margin-top:1rem">
      <div class="bubble">WhatsApp / Signal / iMessage / email / Instagram DMs</div>
    </div>
  `;
}

function vizDecode() {
  return `
    <div class="pipeline">
      <div class="pipe-node on"><strong>paste cover</strong><span>/paste sender</span></div>
      <span class="pipe-arrow">→</span>
      <div class="pipe-node on"><strong>decode candidates</strong><span>same model prompt</span></div>
      <span class="pipe-arrow">→</span>
      <div class="pipe-node on"><strong>PendingAssembly</strong><span>wait for parts</span></div>
      <span class="pipe-arrow">→</span>
      <div class="pipe-node on"><strong>openSIV</strong><span>+ unpack</span></div>
      <span class="pipe-arrow">→</span>
      <div class="pipe-node on"><strong>plaintext</strong><span>meet at noon</span></div>
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

function renderLab() {
  const { picked, ranked } = pickFromBits(labBits, labCursor);
  const list = document.getElementById("candidate-list");
  list.innerHTML = ranked
    .map(
      (c) => `
      <li class="${c.token === picked.token ? "picked" : ""}">
        <span><code>${c.token}</code></span>
        <span>${(c.p * 100).toFixed(0)}%</span>
        <span>${c.token === picked.token ? "would pick" : ""}</span>
      </li>
    `
    )
    .join("");
  document.getElementById("cover-so-far").textContent =
    coverTokens.length === 0 ? "…" : coverTokens.join("");
  document.getElementById("lab-status").textContent =
    coverTokens.length === 0
      ? "Press Step once to spend bits and append a cover token."
      : `Used bits through position ${labCursor}. Cover is growing from model candidates.`;
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
  document.getElementById("bit-input").addEventListener("change", () => {
    labBits = document.getElementById("bit-input").value.replace(/[^01]/g, "");
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
