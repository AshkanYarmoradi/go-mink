// Decode only entities emitted by the static HTML serializer. A single pass
// preserves encoded entity text instead of decoding it for a second time.
const entities = {
  "&amp;": "&",
  "&quot;": '"',
  "&#x27;": "'",
  "&#39;": "'",
  "&lt;": "<",
  "&gt;": ">",
};
function decode(value) {
  return value.replace(
    /&(?:amp|quot|#x27|#39|lt|gt);/g,
    (entity) => entities[entity],
  );
}
module.exports = { decode };
