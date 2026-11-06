"use strict";

const flash = (btn) => {
  const label = btn.textContent;
  btn.textContent = "copied";
  btn.classList.add("copied");
  setTimeout(() => {
    btn.textContent = label;
    btn.classList.remove("copied");
  }, 1500);
};

const copyText = (text) => {
  if (navigator.clipboard && window.isSecureContext) {
    return navigator.clipboard.writeText(text);
  }
  const ta = document.createElement("textarea");
  ta.value = text;
  document.body.appendChild(ta);
  ta.select();
  const ok = document.execCommand("copy");
  document.body.removeChild(ta);
  return ok ? Promise.resolve() : Promise.reject(new Error("copy failed"));
};

function copyButtons() {
  for (const btn of document.querySelectorAll("[data-copy]")) {
    btn.addEventListener("click", () => {
      copyText(btn.dataset.copy).then(() => flash(btn), () => {});
    });
  }
}

// NIP-07: let users fill their key from a browser extension.
function extensionButton() {
  const btn = document.getElementById("use-extension");
  const form = document.getElementById("join-form");
  if (!btn || !form) return;
  let tries = 0;
  const detect = setInterval(() => {
    if (window.nostr && typeof window.nostr.getPublicKey === "function") {
      btn.hidden = false;
      clearInterval(detect);
    } else if (++tries > 20) {
      clearInterval(detect);
    }
  }, 150);
  btn.addEventListener("click", async () => {
    try {
      document.getElementById("pkey").value = await window.nostr.getPublicKey();
      form.submit();
    } catch {
      // The user declined in the extension; nothing to do.
    }
  });
}

function countdowns() {
  const els = document.querySelectorAll("[data-expires]");
  if (!els.length) return;
  const tick = () => {
    const now = Date.now() / 1000;
    for (const el of els) {
      const left = Math.round(Number(el.dataset.expires) - now);
      if (left <= 0) {
        el.textContent = "now";
        continue;
      }
      const m = Math.floor(left / 60);
      const s = String(left % 60).padStart(2, "0");
      el.textContent = `in ${m}:${s}`;
    }
  };
  tick();
  setInterval(tick, 1000);
}

const plural = (n, word) => `${n} ${word}${n === 1 ? "" : "s"}`;

// Poll the invoice status and update the page; reload once access is granted.
function invoiceStatus() {
  const box = document.getElementById("invoice");
  if (!box) return;
  const url = box.dataset.statusUrl;
  const title = document.getElementById("status-title");
  const text = document.getElementById("status-text");
  let last = null;

  const poll = async () => {
    let st;
    try {
      const r = await fetch(url, { headers: { Accept: "application/json" }, cache: "no-store" });
      if (!r.ok) return;
      st = await r.json();
    } catch {
      return;
    }
    if (st.member || st.banned) {
      location.reload();
      return;
    }
    const inv = st.invoice;
    if (!inv || !title || !text) return;
    const key = `${inv.seen}/${inv.confirmed}/${inv.expired}`;
    if (key === last) return;
    last = key;
    const seen = Number(inv.seen);
    const amount = Number(inv.amount);
    title.classList.remove("ok", "pending");
    if (seen >= amount) {
      const minutes = 2 * Math.max(1, inv.confirmations_required);
      title.textContent = "Payment received";
      title.classList.add("pending");
      text.textContent = `Waiting for ${plural(inv.confirmations_required, "confirmation")} (about ${minutes} minutes). You can close this page.`;
    } else if (seen > 0) {
      title.textContent = "Partial payment received";
      title.classList.add("pending");
      text.textContent = `Received ${inv.seen} XMR. Send the remaining ${inv.remaining} XMR to the same address.`;
    } else if (inv.expired) {
      location.reload();
    }
  };

  const check = box.dataset.checkUrl;
  if (check) {
    for (const form of document.querySelectorAll(`form[action="${check}"]`)) {
      form.addEventListener("submit", async (e) => {
        e.preventDefault();
        const btn = form.querySelector("button");
        btn.disabled = true;
        btn.textContent = "checking…";
        try {
          await fetch(check, { method: "POST", headers: { Accept: "application/json" } });
        } catch {
          // Polling below still picks up the result.
        }
        setTimeout(() => {
          poll();
          btn.disabled = false;
          btn.textContent = "I've paid, check now";
        }, 6000);
      });
    }
  }

  setInterval(poll, 10000);
}

document.addEventListener("DOMContentLoaded", () => {
  copyButtons();
  extensionButton();
  countdowns();
  invoiceStatus();
});
