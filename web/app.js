// Chat client. No dependencies: fetch + a small SSE reader.
//
// The server streams one event per *sentence*, not per token, because the
// pipeline gates on complete sentences (and TTS will want them later). So the
// UI appends sentences as they clear, which reads as natural pacing.
//
// Answers arrive as markdown and are rendered into real DOM nodes below. The
// renderer only ever calls createElement and textContent — never innerHTML —
// so model output cannot inject markup no matter what it emits.

import { createAvatar } from "/ask-about/avatar.js";
import { backgroundLabel } from "/ask-about/backgrounds.js";

const thread = document.getElementById("thread");
const empty = document.getElementById("empty");
const form = document.getElementById("composer");
const input = document.getElementById("input");
const send = document.getElementById("send");

/** Full conversation, replayed to the server each turn — it holds no state. */
const history = [];
let busy = false;

// --- Server config ----------------------------------------------------
//
// Title, tagline, and the avatar all come from the server so the config
// file is the only place they are set. The avatar itself is driven by
// pipeline state rather than audio, so it works before TTS exists.

// Marks an error whose text came from the server and is meant to be read by a
// visitor, as opposed to a network or parsing failure that is not.
class ServerMessage extends Error {}

let avatar = null;
// Set once the server refuses this visitor; nothing re-opens the composer.
let accessDenied = false;
// True once the first sentence of an answer has arrived. The bubble's own dots
// already say "thinking"; the pill means "still writing", so it waits for text
// rather than doubling up on the same signal.
let streaming = false;

async function applyServerConfig() {
  try {
    const response = await fetch("/ask-about/config");
    if (!response.ok) return;
    const {
      avatar: enabled,
      background,
      photo,
      name,
      tagline,
      questions,
      disclaimer,
      admitted,
      denied,
      noLink,
    } = await response.json();

    // The subject's name comes from config, not the markup, so one build
    // serves any corpus.
    if (name) {
      document.title = `Ask about ${name}`;
      const title = document.getElementById("title");
      if (title) title.textContent = `Ask about ${name}`;
    }
    if (tagline) {
      const el = document.getElementById("tagline");
      if (el) el.textContent = tagline;
    }
    if (disclaimer) {
      const el = document.getElementById("disclaimer");
      if (el) el.textContent = disclaimer;
    }
    renderSuggestions(questions);
    // Told on arrival rather than after they have typed a question and had it
    // refused. The avatar still runs — the page is not broken, it is closed.
    if (admitted === false) showDenied(denied, noLink);

    // With the synthetic avatar switched off, the photograph is the avatar
    // rather than an intro to it: shown plainly, kept, no flicker or dissolve.
    if (!enabled) {
      if (photo) showPhoto();
      return;
    }

    const canvas = document.getElementById("avatar");
    canvas.hidden = false;
    avatar = createAvatar(canvas, { background });

    if (photo) playIntro();

    const announce = () => {
      const label = backgroundLabel(avatar.background);
      canvas.title = `Backdrop: ${label} — click to change`;
      canvas.setAttribute(
        "aria-label",
        `Animated synthetic presenter. Backdrop: ${label}. Activate to change it.`,
      );
    };
    announce();

    canvas.addEventListener("click", () => {
      avatar.nextBackground();
      announce();
    });

    // A canvas has no native activation, so the keyboard equivalent is manual.
    canvas.addEventListener("keydown", (e) => {
      if (e.key !== "Enter" && e.key !== " ") return;
      e.preventDefault(); // Space would otherwise scroll the page
      avatar.nextBackground();
      announce();
    });
  } catch {
    // The avatar is decoration; never let it break the chat.
  }
}

function setAvatarState(state) {
  avatar?.setState(state);
}

/**
 * Builds the starter prompts from config.
 *
 * The empty state stays hidden until they arrive — showing "Try one of these:"
 * above nothing, however briefly, looks broken. If the fetch fails there are
 * no suggestions and the composer alone carries the page, which is fine.
 */
function renderSuggestions(questions) {
  const list = document.getElementById("suggestions");
  if (!list || !Array.isArray(questions) || questions.length === 0) return;

  for (const question of questions) {
    const chip = document.createElement("button");
    chip.type = "button";
    chip.className = "chip";
    chip.textContent = question;
    list.appendChild(chip);
  }
  document.getElementById("empty")?.removeAttribute("hidden");
}

/**
 * Shows a real photograph over the avatar, which flickers and dissolves to
 * reveal the synthetic one underneath.
 *
 * The element is only revealed once the image has actually decoded — starting
 * the animation against a half-loaded image would eat the flicker and dissolve
 * a blank box instead.
 */
/** Shows the photograph and leaves it there. */
function showPhoto() {
  const img = document.getElementById("avatar-photo");
  if (!img) return;
  // "still" turns off the intro animation, which would otherwise fade the
  // photo out and leave an empty box with no canvas behind it.
  img.classList.add("still");
  img.addEventListener("load", () => (img.hidden = false), { once: true });
  img.addEventListener("error", () => img.remove(), { once: true });
  img.src = "/ask-about/avatar-photo";
}

function playIntro() {
  const img = document.getElementById("avatar-photo");
  if (!img) return;

  img.addEventListener(
    "load",
    () => {
      img.hidden = false;
      // Remove rather than leave it transparent: an invisible element over the
      // canvas is a hit-testing hazard waiting to be reintroduced.
      //
      // The zoom and flicker tracks each fire animationend, so wait for the
      // one that actually takes the photo to zero. Removing on whichever
      // finishes first would cut the intro short if the durations ever drift.
      img.addEventListener("animationend", (e) => {
        if (e.animationName === "avatar-zoom") return;
        img.remove();
      });
    },
    { once: true },
  );

  // A missing or unreadable file just means no intro.
  img.addEventListener("error", () => img.remove(), { once: true });

  img.src = "/ask-about/avatar-photo";
}

// --- UI helpers -------------------------------------------------------

function addMessage(role, text = "") {
  empty?.remove();
  const el = document.createElement("div");
  el.className = `msg ${role}`;
  el.textContent = text;
  thread.appendChild(el);
  // Only the visitor's own message pulls the view. Scrolling for anything
  // arriving on its own is what moves the page out from under a reader.
  if (role === "user") scrollToEnd();
  updateJump();
  return el;
}

function showTyping() {
  const el = addMessage("bot");
  const typing = document.createElement("span");
  typing.className = "typing";
  for (let i = 0; i < 3; i++) typing.appendChild(document.createElement("span"));
  el.appendChild(typing);
  return el;
}

// --- Markdown rendering ----------------------------------------------
//
// A deliberately small subset: paragraphs, flat bullet lists, headings, and
// bold. That is everything the persona permits, and each maps to a structure
// a reader can scan. Anything else falls through as plain text.

/** Applies inline formatting (**bold**) into `el` as text and <strong> nodes. */
function renderInline(el, text) {
  for (const part of text.split(/(\*\*[^*]+\*\*)/g)) {
    if (!part) continue;
    if (part.length > 4 && part.startsWith("**") && part.endsWith("**")) {
      const strong = document.createElement("strong");
      strong.textContent = part.slice(2, -2);
      el.appendChild(strong);
    } else {
      el.appendChild(document.createTextNode(part));
    }
  }
}

/** Replaces the contents of `container` with `markdown` rendered as nodes. */
function renderMarkdown(container, markdown) {
  container.textContent = "";
  let list = null;

  for (const line of markdown.split("\n")) {
    const trimmed = line.trim();

    if (!trimmed) {
      list = null; // a blank line closes any open list
      continue;
    }

    const bullet = /^[-*]\s+(.*)$/.exec(trimmed);
    if (bullet) {
      if (!list) {
        list = document.createElement("ul");
        container.appendChild(list);
      }
      const item = document.createElement("li");
      renderInline(item, bullet[1]);
      list.appendChild(item);
      continue;
    }

    list = null;

    const heading = /^#{1,6}\s+(.*)$/.exec(trimmed);
    const block = document.createElement("p");
    if (heading) {
      block.className = "section";
      renderInline(block, heading[1]);
    } else {
      renderInline(block, trimmed);
    }
    container.appendChild(block);
  }
}

const jump = document.getElementById("jump");

// How close to the end still counts as "at the end". A couple of lines of
// slack, so a stray pixel from a rounded scroll height does not leave the
// arrow showing when you are already at the bottom.
const AT_END_SLACK = 48;

function atEnd() {
  return thread.scrollHeight - thread.scrollTop - thread.clientHeight <= AT_END_SLACK;
}

function scrollToEnd() {
  thread.scrollTop = thread.scrollHeight;
}

/**
 * Updates the jump-to-latest pill.
 *
 * The thread deliberately does not follow the answer as it streams. Text that
 * moves while you are reading it costs you your place on every sentence, which
 * is worse than having to scroll yourself. So the pill reports instead: dots
 * while the answer is still being written, an arrow once it is finished and
 * there is a bottom worth jumping to.
 */
function updateJump() {
  if (!jump) return;
  // Nothing to jump to before the first message. The empty state's prompts can
  // overflow a short window, but "scroll to the latest" means nothing when
  // there is no conversation yet — and an arrow on a fresh page reads as a
  // control that does not work.
  if (!thread.querySelector(".msg")) {
    jump.hidden = true;
    return;
  }
  if (busy && streaming) {
    // Shown even at the bottom: with the thread held still, this is the only
    // sign that more is on its way.
    jump.classList.add("is-writing");
    jump.hidden = false;
    return;
  }
  jump.classList.remove("is-writing");
  jump.hidden = atEnd();
}

/**
 * Closes the composer for a visitor who cannot ask anything.
 *
 * Two situations, deliberately styled differently. Someone who arrived with no
 * link is at the front door and has done nothing wrong — this is the only
 * thing the site ever says to them, so it reads as an invitation. Someone whose
 * link stopped working gets a refusal, and it never says which of expired,
 * revoked or unknown applies: they hold a token, and naming which would tell
 * anyone probing which guess was closest.
 */
function showDenied(message, noLink) {
  const empty = document.getElementById("empty");
  if (empty) {
    const note = document.createElement("p");
    // A bot bubble either way: this is the avatar speaking, not a form error.
    note.className = noLink ? "msg bot welcome" : "msg error";
    note.textContent = message || "This link is no longer active.";
    empty.replaceChildren(note);
    empty.removeAttribute("hidden");
  }

  input.disabled = true;
  send.disabled = true;
  input.placeholder = "";
  form.setAttribute("hidden", "");
  accessDenied = true;
}

function setBusy(state) {
  // Nothing re-enables the composer once the link is refused.
  if (accessDenied) return;
  busy = state;
  send.disabled = state;
  input.disabled = state;
  updateJump();
  if (!state) input.focus();
}

// Grow the textarea with its content, up to the CSS max-height.
function autoGrow() {
  input.style.height = "auto";
  input.style.height = `${input.scrollHeight}px`;
}

// --- Streaming --------------------------------------------------------

/**
 * Reads an SSE body and yields each decoded `data:` payload.
 * Frames are separated by a blank line; a frame may span chunk boundaries.
 */
async function* readEvents(response) {
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;

    buffer += decoder.decode(value, { stream: true });

    let split;
    while ((split = buffer.indexOf("\n\n")) !== -1) {
      const frame = buffer.slice(0, split);
      buffer = buffer.slice(split + 2);

      const payload = frame
        .split("\n")
        .filter((line) => line.startsWith("data:"))
        .map((line) => line.slice(5).trim())
        .join("");

      if (payload) {
        try {
          yield JSON.parse(payload);
        } catch {
          // A malformed frame shouldn't kill the stream.
        }
      }
    }
  }
}

async function ask(question) {
  history.push({ role: "user", text: question });
  addMessage("user", question);

  setBusy(true);
  setAvatarState("thinking");
  const bubble = showTyping();
  // The last scroll of the turn: it puts the question and the waiting bubble
  // in view. From here the thread stays put and the pill reports instead.
  scrollToEnd();
  let answer = "";

  try {
    const response = await fetch("/ask-about/chat", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ messages: history }),
    });

    if (!response.ok || !response.body) {
      // The server sends a readable sentence for a refusal (a spent cap, a
      // link that no longer works), so show that rather than a status code.
      // Anything else is a fault the visitor cannot act on.
      const body = (await response.text()).trim();
      throw new ServerMessage(body || "Something went wrong. Try again.");
    }

    for await (const event of readEvents(response)) {
      switch (event.kind) {
        case "sentence":
          // `sep` is the whitespace the model actually produced before this
          // sentence. Using it verbatim — rather than joining with a space —
          // is what preserves list and paragraph structure for the renderer.
          answer += (answer ? event.sep || " " : "") + event.text;
          renderMarkdown(bubble, answer);
          setAvatarState("speaking");
          avatar?.pulse();
          if (!streaming) {
            streaming = true;
            updateJump();
          }
          break;
        case "blocked":
          // A sentence was withheld. Nothing is rendered for it.
          break;
        case "error":
          // Already written for a visitor by the pipeline; the underlying
          // vendor error stays in the server log.
          throw new ServerMessage(event.text || "Something went wrong. Try again.");
        case "done":
          break;
      }
    }

    if (answer) {
      history.push({ role: "assistant", text: answer });
    } else {
      bubble.remove();
      addMessage("error", "No answer came back. Try rephrasing?");
    }
  } catch (err) {
    bubble.remove();
    // A message the server wrote is shown as-is. Anything else is a browser or
    // network failure, whose text is for a console rather than a reader.
    addMessage("error", err instanceof ServerMessage
      ? err.message
      : "Could not reach the server. Check your connection and try again.");
  } finally {
    streaming = false;
    setBusy(false);
    setAvatarState("idle");
  }
}

// --- Events -----------------------------------------------------------

form.addEventListener("submit", (e) => {
  e.preventDefault();
  const question = input.value.trim();
  if (!question || busy) return;
  input.value = "";
  autoGrow();
  ask(question);
});

// Enter sends; Shift+Enter makes a newline.
input.addEventListener("keydown", (e) => {
  if (e.key === "Enter" && !e.shiftKey) {
    e.preventDefault();
    form.requestSubmit();
  }
});

input.addEventListener("input", autoGrow);

// The pill only ever reflects two things: whether an answer is still arriving,
// and whether the reader has scrolled away from the end.
thread.addEventListener("scroll", updateJump, { passive: true });
window.addEventListener("resize", updateJump);

jump?.addEventListener("click", () => {
  // Inert while writing — there is no stable bottom to land on yet.
  if (busy) return;
  // Assigning scrollTop rather than scrollTo({behavior:"smooth"}): the smooth
  // variant silently does nothing in some browsers, and a jump button that
  // sometimes fails to jump is worse than one that always arrives at once.
  scrollToEnd();
  updateJump();
});

thread.addEventListener("click", (e) => {
  const chip = e.target.closest(".chip");
  if (chip && !busy) ask(chip.textContent.trim());
});

input.focus();
applyServerConfig();
