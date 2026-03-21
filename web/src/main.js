import "htmx.org";
import "./main.css";

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
});
