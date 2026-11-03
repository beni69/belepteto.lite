(() => {
  const armTimeoutMs = 8000;
  let armTimer = null;
  let finishTimer = null;
  let feedbackTimer = null;
  let requestPending = false;
  let accessDenied = false;

  function layer() {
    return document.getElementById("door-open-control");
  }

  function button() {
    return document.getElementById("door-open-button");
  }

  function feedback() {
    return document.getElementById("door-open-feedback");
  }

  function setPageInert(inert) {
    document.querySelectorAll("nav.site-nav, main.app-main").forEach((element) => {
      element.inert = inert;
    });
    document.body.classList.toggle("door-open-armed", inert);
  }

  function clearTimers() {
    window.clearTimeout(armTimer);
    window.clearTimeout(finishTimer);
    window.clearTimeout(feedbackTimer);
    armTimer = null;
    finishTimer = null;
    feedbackTimer = null;
  }

  function clearFeedback() {
    const result = feedback();
    if (!result) return;
    result.textContent = "";
    result.removeAttribute("data-state");
  }

  function resetButton(control, hide) {
    const currentButton = button();
    if (!currentButton) return;

    currentButton.disabled = false;
    currentButton.hidden = Boolean(hide);
    currentButton.removeAttribute("aria-busy");
    currentButton.setAttribute("aria-expanded", "false");
    currentButton.setAttribute("aria-label", "Open door. Press once to confirm, then press again to send.");
    const label = currentButton.querySelector(".door-open-label");
    if (label) label.textContent = "Open door";
    const icon = currentButton.querySelector("use");
    if (icon) icon.setAttribute("href", "#icon-door");
    if (control) {
      control.classList.remove("is-armed", "is-success", "is-error");
      control.removeAttribute("role");
      control.removeAttribute("aria-modal");
      control.removeAttribute("aria-labelledby");
      control.removeAttribute("aria-describedby");
    }
    setPageInert(false);
  }

  function disarm({ hideButton = false, keepFeedback = false, restoreFocus = true } = {}) {
    clearTimers();
    const control = layer();
    const currentButton = button();
    resetButton(control, hideButton || accessDenied);
    requestPending = false;
    accessDenied = false;

    if (!keepFeedback) {
      clearFeedback();
    } else {
      feedbackTimer = window.setTimeout(clearFeedback, 4000);
    }

    if (restoreFocus && currentButton && !currentButton.hidden) {
      currentButton.focus({ preventScroll: true });
    } else if (restoreFocus && currentButton?.hidden) {
      feedback()?.focus({ preventScroll: true });
    }
  }

  function arm() {
    const control = layer();
    const currentButton = button();
    if (!control || !currentButton || requestPending) return;

    clearTimers();
    accessDenied = false;
    clearFeedback();
    control.classList.remove("is-success", "is-error");
    control.classList.add("is-armed");
    control.setAttribute("role", "dialog");
    control.setAttribute("aria-modal", "true");
    control.setAttribute("aria-labelledby", "door-open-button");
    control.setAttribute("aria-describedby", "door-open-feedback");
    currentButton.setAttribute("aria-expanded", "true");
    currentButton.setAttribute("aria-label", "Tap again to send the door open request");
    const label = currentButton.querySelector(".door-open-label");
    if (label) label.textContent = "Tap again to open";
    setPageInert(true);
    currentButton.focus({ preventScroll: true });

    armTimer = window.setTimeout(() => disarm(), armTimeoutMs);
  }

  function showResult(state, message) {
    const result = feedback();
    if (!result) return;
    result.dataset.state = state;
    result.textContent = message;
  }

  async function sendOpenRequest() {
    const control = layer();
    const currentButton = button();
    if (!control || !currentButton || requestPending) return;

    window.clearTimeout(armTimer);
    armTimer = null;
    requestPending = true;
    currentButton.disabled = true;
    currentButton.setAttribute("aria-busy", "true");
    currentButton.setAttribute("aria-label", "Sending door open request");
    const label = currentButton.querySelector(".door-open-label");
    if (label) label.textContent = "Sending request…";

    try {
      const response = await fetch("/htmx/door/open", {
        method: "POST",
        credentials: "same-origin",
        headers: { Accept: "application/json" },
      });
      const payload = await response.json();

      if (response.ok && payload.ok) {
        control.classList.remove("is-error");
        control.classList.add("is-success");
        const icon = currentButton.querySelector("use");
        if (icon) icon.setAttribute("href", "#icon-check-circle");
        currentButton.setAttribute("aria-label", "Door open request sent");
        if (label) label.textContent = "Request sent";
        showResult("sent", payload.message || "Open request sent.");
        requestPending = false;
        finishTimer = window.setTimeout(() => disarm({ keepFeedback: true }), 1400);
        return;
      }

      control.classList.remove("is-success");
      control.classList.add("is-error");
      currentButton.setAttribute("aria-label", "Door open request was not sent");
      if (label) label.textContent = "Not sent";
      const denied = response.status === 403 || payload.code === "access_denied";
      accessDenied = denied;
      showResult(denied ? "denied" : "error", payload.message || "Could not send the open request.");
      requestPending = false;
      finishTimer = window.setTimeout(() => disarm({ hideButton: denied, keepFeedback: true }), 1400);
    } catch (_error) {
      control.classList.remove("is-success");
      control.classList.add("is-error");
      currentButton.setAttribute("aria-label", "Door open request was not sent");
      if (label) label.textContent = "Not sent";
      showResult("error", "Could not contact the server. No confirmation was received.");
      requestPending = false;
      finishTimer = window.setTimeout(() => disarm({ keepFeedback: true }), 1400);
    }
  }

  document.addEventListener("click", (event) => {
    const currentButton = event.target.closest("#door-open-button");
    if (currentButton) {
      event.preventDefault();
      const control = layer();
      if (!control || requestPending) return;
      if (control.classList.contains("is-armed")) {
        sendOpenRequest();
      } else {
        arm();
      }
      return;
    }

    if (!requestPending && event.target.closest("[data-door-open-cancel]") && layer()?.classList.contains("is-armed")) {
      disarm();
    }
  });

  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && layer()?.classList.contains("is-armed") && !requestPending) {
      event.preventDefault();
      disarm();
    }
  });

  document.body.addEventListener("htmx:beforeRequest", (event) => {
    const control = layer();
    if (event.detail.elt?.id === "door-open-control" && control?.classList.contains("is-armed")) {
      event.preventDefault();
    }
  });

  document.body.addEventListener("htmx:beforeSwap", (event) => {
    const control = layer();
    if (event.detail.target?.id === "door-open-control" && control?.classList.contains("is-armed")) {
      event.preventDefault();
    }
  });
})();
