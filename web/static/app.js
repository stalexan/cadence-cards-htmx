// Cadence Cards — the only hand-written client JS. Everything else is htmx
// attributes. No inline scripts, no hx-on:* (CSP has no 'unsafe-eval' or
// 'unsafe-inline'); all behavior hangs off data-* attributes via delegated
// listeners so it survives htmx swaps.
(function () {
  "use strict";

  // ----- Show/Hide answer panel -----
  document.addEventListener("click", function (e) {
    var btn = e.target.closest("[data-toggle]");
    if (!btn) return;
    var panel = document.querySelector(btn.getAttribute("data-toggle"));
    if (!panel) return;
    var hidden = panel.toggleAttribute("hidden");
    var show = btn.getAttribute("data-label-show") || btn.textContent;
    var hide = btn.getAttribute("data-label-hide") || btn.textContent;
    // Rewrite the label span when present — textContent on the whole button
    // would destroy sibling children like the eye icon SVG.
    var label = btn.querySelector("[data-toggle-label]") || btn;
    label.textContent = hidden ? show : hide;
  });

  // ----- Copy to clipboard with a 2s checkmark -----
  // data-copy carries the text inline; data-copy-target names an element whose
  // current value is copied instead (the share dialog's YAML textarea, which is
  // only populated once htmx has fetched it).
  document.addEventListener("click", function (e) {
    var btn = e.target.closest("[data-copy], [data-copy-target]");
    if (!btn) return;
    var text;
    if (btn.hasAttribute("data-copy-target")) {
      var src = document.querySelector(btn.getAttribute("data-copy-target"));
      if (!src) return;
      text = "value" in src ? src.value : src.textContent;
    } else {
      text = btn.getAttribute("data-copy");
    }
    navigator.clipboard.writeText(text).then(function () {
      var original = btn.innerHTML;
      btn.innerHTML =
        '<svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m4 12.5 5.5 5.5L20 6.5"/></svg>';
      setTimeout(function () { btn.innerHTML = original; }, 2000);
    });
  });

  // ----- Auto-resizing textareas -----
  function autoresize(el) {
    el.style.height = "auto";
    el.style.height = el.scrollHeight + "px";
  }
  document.addEventListener("input", function (e) {
    if (e.target.matches("[data-autoresize]")) autoresize(e.target);
  });
  function initAutoresize(root) {
    (root || document).querySelectorAll("[data-autoresize]").forEach(autoresize);
  }

  // ----- Submit busy state -----
  // Flags the form so CSS can swap .btn-label for .btn-busy. Skipped while a
  // confirm dialog is still pending, otherwise the button would read "Saving…"
  // behind an unanswered "Are you sure?".
  document.addEventListener("submit", function (e) {
    var form = e.target;
    if (!(form instanceof HTMLFormElement)) return;
    if (form.matches("[data-confirm]") && !form.dataset.confirmed) return;
    form.dataset.busy = "1";
  });

  // ----- Confirm dialog for destructive forms (data-confirm) -----
  document.addEventListener("submit", function (e) {
    var form = e.target;
    if (!form.matches("form[data-confirm]") || form.dataset.confirmed) return;
    e.preventDefault();
    var dialog = document.getElementById("confirm-dialog");
    if (!dialog || !dialog.showModal) {
      // Fallback when the shared dialog is unavailable.
      if (window.confirm(form.getAttribute("data-confirm"))) {
        form.dataset.confirmed = "1";
        form.submit();
      }
      return;
    }
    dialog.querySelector("#confirm-dialog-message").textContent = form.getAttribute("data-confirm");
    // Optional per-action heading ("Delete Topic?"); falls back to the generic
    // one so forms without the attribute keep working.
    var title = dialog.querySelector("#confirm-dialog-title");
    if (title) title.textContent = form.getAttribute("data-confirm-title") || "Are you sure?";
    dialog.returnValue = "";
    dialog.showModal();
    var ok = dialog.querySelector("#confirm-dialog-ok");
    var onOk = function () {
      cleanup();
      dialog.close();
      form.dataset.confirmed = "1";
      form.submit();
    };
    var onClose = function () { cleanup(); };
    var cleanup = function () {
      ok.removeEventListener("click", onOk);
      dialog.removeEventListener("close", onClose);
    };
    ok.addEventListener("click", onOk);
    dialog.addEventListener("close", onClose);
  });

  // ----- Generic dialog openers/closers -----
  document.addEventListener("click", function (e) {
    var opener = e.target.closest("[data-open-dialog]");
    if (opener) {
      var dlg = document.querySelector(opener.getAttribute("data-open-dialog"));
      if (dlg) {
        dlg.showModal();
        // Lets dialog content lazy-load via hx-trigger="dialog-opened ..."
        // (the share dialog's YAML preview).
        dlg.dispatchEvent(new Event("dialog-opened"));
      }
      return;
    }
    var closer = e.target.closest("[data-close-dialog]");
    if (closer) {
      var parent = closer.closest("dialog");
      if (parent) parent.close();
    }
  });

  // ----- Export dialogs: rebuild the download href from the toggles -----
  // Scoped to the dialog rather than a fixed element id, so the deck dialog
  // (one toggle) and the topic dialog (two) share one handler. Every checked
  // toggle in the scope contributes a query param, not just the one that
  // changed.
  document.addEventListener("change", function (e) {
    var box = e.target.closest("[data-export-toggle]");
    if (!box) return;
    var scope = box.closest("[data-export-scope]");
    if (!scope) return;
    var link = scope.querySelector("[data-export-download]");
    if (!link) return;
    var base = scope.getAttribute("data-export-base");
    var params = [];
    scope.querySelectorAll("[data-export-toggle]").forEach(function (t) {
      if (t.checked) {
        params.push(encodeURIComponent(t.name) + "=" + encodeURIComponent(t.value));
      }
    });
    link.href = params.length ? base + "?" + params.join("&") : base;
  });

  // ----- Tag chips (card forms) -----
  function renderChips(wrap) {
    var hidden = wrap.querySelector('input[type="hidden"][name="tags"]');
    var chips = wrap.querySelector(".tag-chips");
    var tags = hidden.value.split(",").map(function (t) { return t.trim(); }).filter(Boolean);
    chips.textContent = "";
    tags.forEach(function (tag, i) {
      var chip = document.createElement("span");
      chip.className = "tag-chip";
      chip.textContent = tag;
      var x = document.createElement("button");
      x.type = "button";
      x.setAttribute("aria-label", "Remove tag " + tag);
      x.textContent = "×";
      x.addEventListener("click", function () {
        tags.splice(i, 1);
        hidden.value = tags.join(", ");
        renderChips(wrap);
      });
      chip.appendChild(x);
      chips.appendChild(chip);
    });
  }
  function addTag(wrap) {
    var entry = wrap.querySelector("[data-tag-entry]");
    var hidden = wrap.querySelector('input[type="hidden"][name="tags"]');
    var value = entry.value.replace(/,/g, "").trim();
    if (value) {
      var tags = hidden.value.split(",").map(function (t) { return t.trim(); }).filter(Boolean);
      if (tags.indexOf(value) === -1) tags.push(value);
      hidden.value = tags.join(", ");
      renderChips(wrap);
    }
    entry.value = "";
  }
  document.addEventListener("keydown", function (e) {
    var entry = e.target.closest("[data-tag-entry]");
    if (!entry) return;
    if (e.key === "Enter" || e.key === ",") {
      e.preventDefault();
      addTag(entry.closest("[data-tag-input]"));
    }
  });
  document.addEventListener("focusout", function (e) {
    var entry = e.target.closest && e.target.closest("[data-tag-entry]");
    if (entry && entry.value.trim()) addTag(entry.closest("[data-tag-input]"));
  });
  function initTagInputs(root) {
    (root || document).querySelectorAll("[data-tag-input]").forEach(renderChips);
  }

  // ----- Study setup: select all / clear deck checkboxes -----
  document.addEventListener("click", function (e) {
    var btn = e.target.closest("[data-check-all]");
    if (!btn) return;
    var name = btn.getAttribute("data-check-all");
    var on = btn.getAttribute("data-check-state") === "on";
    btn.closest("form").querySelectorAll('input[type="checkbox"][name="' + name + '"]').forEach(function (box) {
      box.checked = on;
    });
  });

  // ----- Forms that need at least one checked box (study setup decks) -----
  // Registered after the busy-state listener so it can undo data-busy when it
  // blocks the submit.
  document.addEventListener("submit", function (e) {
    var form = e.target;
    if (!(form instanceof HTMLFormElement)) return;
    var name = form.getAttribute("data-require-checked");
    if (!name) return;
    if (!form.querySelector('input[type="checkbox"][name="' + name + '"]:checked')) {
      e.preventDefault();
      delete form.dataset.busy;
      window.alert(form.getAttribute("data-require-checked-message") || "Select at least one option.");
    }
  });

  // ----- Study setup: unchecked box -> enable hidden includeNew=false -----
  document.addEventListener("submit", function (e) {
    var form = e.target;
    form.querySelectorAll("[data-mirror-unchecked]").forEach(function (box) {
      var name = box.getAttribute("data-mirror-unchecked");
      var hidden = form.querySelector('input[type="hidden"][name="' + name + '"]');
      if (hidden) hidden.disabled = box.checked;
    });
  });

  // ----- Chat: clear input after send, autoscroll after swaps -----
  document.addEventListener("htmx:afterRequest", function (e) {
    var form = e.target.closest && e.target.closest("form[data-chat-form]");
    if (form && e.detail.successful) {
      var input = form.querySelector('[name="message"]');
      if (input) { input.value = ""; autoresize(input); input.focus(); }
    }
  });
  // The chat page's panel has a definite height, so .chat-scroll is its scroll
  // container; the study page's panel grows the document instead, so there we
  // bring the composer to the bottom of the viewport, which puts the newest
  // message just above it.
  function scrollChatToNewest() {
    var messages = document.getElementById("chat-messages");
    if (!messages) return;
    var scroller = messages.closest(".chat-scroll");
    if (scroller && scroller.scrollHeight > scroller.clientHeight) {
      scroller.scrollTop = scroller.scrollHeight;
      return;
    }
    var panel = messages.closest(".chat-panel");
    var composer = panel && panel.querySelector(".chat-composer");
    if (composer) composer.scrollIntoView({ block: "end", behavior: "smooth" });
  }
  // Only appends into the message list scroll: the card fragment targets
  // #study-area and the initial question replaces the pending stub, so neither
  // yanks the page down when a card loads.
  document.addEventListener("htmx:afterSwap", function (e) {
    initAutoresize(e.target);
    initTagInputs(e.target);
    if (e.target && e.target.id === "chat-messages") scrollChatToNewest();
  });
  // OOB swaps (question fragment, history updates, grade-conflict bubble).
  document.addEventListener("htmx:oobAfterSwap", function (e) {
    if (e.target && e.target.id === "chat-messages") scrollChatToNewest();
  });

  // ----- Chat: Enter sends, Shift+Enter adds a newline -----
  document.addEventListener("keydown", function (e) {
    if (e.key !== "Enter" || e.shiftKey) return;
    var input = e.target.closest("form[data-chat-form] [name='message']");
    if (!input) return;
    e.preventDefault();
    if (input.value.trim()) input.form.requestSubmit();
  });

  // ----- Progress bar width (CSP forbids inline style attributes) -----
  function initProgress(root) {
    (root || document).querySelectorAll("[data-progress]").forEach(function (el) {
      el.style.width = el.getAttribute("data-progress") + "%";
    });
  }
  document.addEventListener("htmx:afterSwap", function (e) { initProgress(e.target); });
  document.addEventListener("htmx:oobAfterSwap", function (e) { initProgress(e.target); });

  document.addEventListener("DOMContentLoaded", function () {
    initAutoresize();
    initTagInputs();
    initProgress();
  });
})();
