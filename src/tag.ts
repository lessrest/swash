export function tag(
  tagName: string,
  attributes: Record<string, any> = {},
  ...content: (string | Node)[]
) {
  const element = document.createElement(tagName)
  for (const [key, value] of Object.entries(attributes)) {
    if (typeof value === "function") {
      element.addEventListener(key.slice(2), value as EventListener)
    } else if (key === "class") {
      element.classList.add(...value.split(" "))
    } else if (key === "srcObject") {
      ;(element as HTMLVideoElement).srcObject = value
    } else {
      element.setAttribute(key, value)
    }
  }
  element.append(...content)
  return element
}
