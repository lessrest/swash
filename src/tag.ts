export function tag(
  tagName: string,
  // deno-lint-ignore no-explicit-any
  attributes: Record<string, any> = {},
  ...content: (string | Node)[]
): HTMLElement {
  const element = document.createElement(tagName)

  for (const [key, value] of Object.entries(attributes)) {
    if (key.startsWith("on") && typeof value === "function") {
      const eventName = key.slice(2).toLowerCase()
      element.addEventListener(eventName, value as EventListener)
    } else if (key === "class") {
      if (Array.isArray(value)) {
        element.classList.add(...value)
      } else if (typeof value === "string") {
        element.classList.add(...value.split(" "))
      }
    } else if (key === "style" && typeof value === "object") {
      Object.assign(element.style, value)
    } else if (typeof value !== "string") {
      // deno-lint-ignore no-explicit-any
      const it = element as any
      it[key] = value
    } else if (typeof value === "boolean") {
      if (value) {
        element.setAttribute(key, "")
      }
    } else {
      element.setAttribute(key, value)
    }
  }

  element.append(...content)
  return element
}
