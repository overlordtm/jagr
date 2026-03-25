import htmx from "htmx.org";
window.htmx = htmx;
import { marked } from "marked";
import "./main.css";

marked.setOptions({
  breaks: true,
  gfm: true,
});

function renderMarkdown(root) {
  root.querySelectorAll(".msg-content").forEach(function (el) {
    if (el.dataset.rendered) return;
    el.dataset.rendered = "1";
    var raw = el.textContent;
    el.innerHTML = marked.parse(raw);
  });
}

// Preserve <details> open state across HTMX swaps
document.addEventListener("htmx:beforeSwap", function (evt) {
  var target = evt.detail.target;
  var details = target.querySelectorAll("details");
  var open = [];
  details.forEach(function (d, i) {
    if (d.open) open.push(i);
  });
  target._openDetails = open;
});

document.addEventListener("htmx:afterSwap", function (evt) {
  var target = evt.detail.target;
  var open = target._openDetails;
  if (open && open.length) {
    var details = target.querySelectorAll("details");
    open.forEach(function (i) {
      if (details[i]) details[i].open = true;
    });
  }
  renderMarkdown(target);
});

// Sort button toggle for session messages
function applySortMode(sort) {
  var list = document.getElementById("messages-list");
  if (list) {
    var url = list.getAttribute("hx-get");
    if (url) {
      list.setAttribute("hx-get", url.replace(/sort=\w+/, "sort=" + sort));
      htmx.process(list);
    }
  }
  document.querySelectorAll(".msg-sort-btn").forEach(function (b) {
    if (b.dataset.sort === sort) {
      b.classList.add("bg-gray-700", "text-white");
      b.classList.remove("text-gray-400");
    } else {
      b.classList.remove("bg-gray-700", "text-white");
      b.classList.add("text-gray-400");
    }
  });
}

document.addEventListener("click", function (evt) {
  var btn = evt.target.closest(".msg-sort-btn");
  if (!btn) return;
  var sort = btn.dataset.sort || "chronological";
  localStorage.setItem("jagr-msg-sort", sort);
  applySortMode(sort);
});

// Restore sort mode from localStorage after HTMX swaps
document.addEventListener("htmx:load", function () {
  var sort = localStorage.getItem("jagr-msg-sort");
  if (sort) applySortMode(sort);
});

// Initial render on page load
document.addEventListener("DOMContentLoaded", function () {
  renderMarkdown(document.body);
  var sort = localStorage.getItem("jagr-msg-sort");
  if (sort) applySortMode(sort);
  updateLiveRefreshUI();
});

// Live Refresh Toggle
window.isLiveRefreshEnabled = function() {
  return localStorage.getItem("jagr-live-refresh") !== "false";
};

function updateLiveRefreshUI() {
  const indicator = document.getElementById("live-refresh-indicator");
  const status = document.getElementById("live-refresh-status");
  if (!indicator || !status) return;

  const enabled = window.isLiveRefreshEnabled();
  if (enabled) {
    indicator.classList.remove("hidden");
    status.classList.add("text-emerald-400");
    status.classList.remove("text-gray-500");
  } else {
    indicator.classList.add("hidden");
    status.classList.remove("text-emerald-400");
    status.classList.add("text-gray-500");
  }
}

document.addEventListener("click", function (evt) {
  var btn = evt.target.closest("#live-refresh-toggle");
  if (!btn) return;
  const newState = !window.isLiveRefreshEnabled();
  localStorage.setItem("jagr-live-refresh", newState);
  updateLiveRefreshUI();
});
