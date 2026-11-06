(() => {
  function pad(value) {
    return String(value).padStart(2, "0");
  }

  function formatLocalTime(element) {
    const date = new Date(element.getAttribute("datetime"));
    if (Number.isNaN(date.getTime())) return;

    element.textContent = [
      `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`,
      `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`,
    ].join(" ");
  }

  function formatLocalTimes() {
    document.querySelectorAll("time[datetime]").forEach(formatLocalTime);
  }

  formatLocalTimes();
  document.addEventListener("htmx:after:swap", formatLocalTimes);
})();
