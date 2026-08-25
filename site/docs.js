// Shared docs behaviour. Extracted from the inline script the single-page
// docs carried, because with many pages an inline copy per page is a thing to
// keep in sync for no benefit.

// Copy buttons on code blocks.
document.querySelectorAll(".copy-btn").forEach(function (btn) {
  btn.addEventListener("click", function () {
    var block = btn.closest(".code-block");
    if (!block) return;
    var code = block.querySelector("code").innerText;
    navigator.clipboard.writeText(code).then(function () {
      var original = btn.textContent;
      btn.textContent = "Copied";
      btn.classList.add("copied");
      setTimeout(function () {
        btn.textContent = original;
        btn.classList.remove("copied");
      }, 1600);
    });
  });
});

// Keep the active sidebar entry in view. The nav scrolls independently and is
// long enough that the current page can start below the fold, which reads as
// "my page isn't in the nav".
(function () {
  var active = document.querySelector(".docs-nav a.active");
  var nav = document.getElementById("docs-nav");
  if (!active || !nav) return;
  var top = active.offsetTop - nav.clientHeight / 2;
  if (top > 0) nav.scrollTop = top;
})();
