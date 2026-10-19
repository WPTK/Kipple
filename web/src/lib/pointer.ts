/** "Click" when the device's main pointer is a mouse or trackpad, "Tap" otherwise (touch screens, and test environments with no matchMedia). */
export function tapVerb(): "Click" | "Tap" {
  return typeof window.matchMedia === "function" && window.matchMedia("(pointer: fine)").matches ? "Click" : "Tap";
}
