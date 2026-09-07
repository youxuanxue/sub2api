(() => {
  const buttons = document.querySelectorAll(".lang button[data-lang]");
  const locales = document.querySelectorAll(".locale[data-locale]");
  if (!buttons.length || !locales.length) return;

  const apply = (lang) => {
    const next = lang === "zh" ? "zh" : "en";
    locales.forEach((el) => {
      el.hidden = el.getAttribute("data-locale") !== next;
    });
    buttons.forEach((btn) => {
      btn.setAttribute("aria-pressed", btn.getAttribute("data-lang") === next ? "true" : "false");
    });
    document.documentElement.lang = next === "zh" ? "zh-CN" : "en";
    try {
      localStorage.setItem("tokenkey-legal-lang", next);
    } catch (_) {}
  };

  buttons.forEach((btn) => {
    btn.addEventListener("click", () => apply(btn.getAttribute("data-lang")));
  });

  let initial = "en";
  try {
    const saved = localStorage.getItem("tokenkey-legal-lang");
    if (saved === "zh" || saved === "en") initial = saved;
  } catch (_) {}
  if (location.hash === "#zh") initial = "zh";
  if (location.hash === "#en") initial = "en";
  apply(initial);
})();
