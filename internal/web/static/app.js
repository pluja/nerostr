"use strict";

// Optional enhancements. Every page works without this script.

const copyText = (text) => {
  if (navigator.clipboard && window.isSecureContext) {
    return navigator.clipboard.writeText(text);
  }
  return Promise.reject(new Error("clipboard unavailable"));
};

function copyButtons() {
  if (!(navigator.clipboard && window.isSecureContext)) return;
  for (const btn of document.querySelectorAll("button[data-copy]")) {
    btn.hidden = false;
    btn.addEventListener("click", () => {
      copyText(btn.dataset.copy).then(() => {
        btn.textContent = "Copied";
        btn.classList.add("copied");
        setTimeout(() => {
          btn.textContent = "Copy";
          btn.classList.remove("copied");
        }, 1500);
      }, () => {});
    });
  }
}

// NIP-07: fill the key from a browser extension.
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
      // Declined in the extension.
    }
  });
}

// Check the status in the background and reload only when it changes,
// instead of the full-page refresh used without JavaScript.
function watchStatus() {
  const url = document.querySelector("[data-status-url]")?.dataset.statusUrl;
  if (!url) return;
  let last = null;
  const poll = async () => {
    if (document.hidden) return;
    try {
      const r = await fetch(url, { headers: { Accept: "application/json" }, cache: "no-store" });
      if (!r.ok) return;
      const st = await r.json();
      const inv = st.invoice || {};
      const key = [st.member, st.banned, inv.seen, inv.confirmed, inv.expired].join("/");
      if (last !== null && key !== last) location.reload();
      last = key;
    } catch {
      // Try again on the next tick.
    }
  };
  poll();
  setInterval(poll, 10000);
}

document.addEventListener("DOMContentLoaded", () => {
  copyButtons();
  extensionButton();
  watchStatus();
});
