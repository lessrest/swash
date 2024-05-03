export function tag<T extends HTMLElement>(
  tagName: string,
  // deno-lint-ignore no-explicit-any
  attributes: Record<string, any> = {},
  ...content: (string | Node)[]
): T {
  const [tag, ...classes] = tagName.split(".")
  const element = document.createElement(tag)

  if (classes.length > 0) {
    element.classList.add(...classes)
  }

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
  return element as T
}

export const nbsp = String.fromCharCode(160)

export function innerTextWithBr(element: HTMLElement): string {
  return [...element.childNodes]
    .map((child) =>
      child.nodeType === Node.TEXT_NODE
        ? child.textContent
        : child.nodeName === "BR"
        ? "\n"
        : "",
    )
    .join("")
}
