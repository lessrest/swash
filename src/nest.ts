import {
  Operation,
  Stream,
  Task,
  createContext,
  each,
  ensure,
  once,
  spawn,
} from "effection"

import { html } from "./html.ts"
import { task } from "./task.ts"

export const $node = createContext<HTMLElement>("node")

export function* grow(content: string | Node) {
  const node = yield* $node
  node.append(content)
}

export function* into(element: HTMLElement): Operation<HTMLElement> {
  yield* $node.set(element)
  return element
}

export function* nest(node: HTMLElement) {
  yield* grow(node)
  yield* into(node)

  return node
}

export function* withClassName<T>(
  className: string,
  body: () => Operation<T>,
): Operation<T> {
  const element = yield* $node
  if (element.classList.contains(className)) {
    return yield* body()
  } else {
    element.classList.add(className)
    try {
      return yield* body()
    } finally {
      element.classList.remove(className)
    }
  }
}

export function* seem(className: string): Operation<void> {
  const element = yield* $node
  element.classList.add(className)
  yield* ensure(function* () {
    element.classList.remove(className)
  })
}

export function* waitForButton(label: string): Operation<void> {
  const button = html("button", {}, label)
  yield* grow(button)
  try {
    yield* once(button, "click")
  } finally {
    button.remove()
  }
}

export function* clear(): Operation<void> {
  const node = yield* $node
  node.replaceChildren()
}

export function* replaceChildren(
  ...content: (string | Node)[]
): Operation<void> {
  const node = yield* $node
  node.replaceChildren(...content)
}

export function* querySelectorAll<T extends Element>(
  selector: string,
): Operation<T[]> {
  const node = yield* $node
  return [...node.querySelectorAll(selector)] as unknown as T[]
}

export function* querySelector<T extends Element>(
  selector: string,
): Operation<T | null> {
  const node = yield* $node
  return node.querySelector(selector) as unknown as T | null
}

export function* scrollToBottom() {
  const node = yield* $node
  node.scrollIntoView({ block: "end", inline: "end", behavior: "smooth" })
}

export function* spawnWithElement<T>(
  element: HTMLElement,
  body: (element: HTMLElement) => Operation<T>,
): Operation<Task<T>> {
  return yield* spawn(function* () {
    return yield* body(yield* nest(element))
  })
}

export function* pushFramedWindow(title: string) {
  yield* nest(html("div", { class: "window" }))

  yield* grow(
    html(
      "header",
      { class: "title-bar" },
      html("span", { class: "title-bar-text" }, title),
    ),
  )

  return yield* nest(html("div", { class: "window-body" }))
}

export function* spawnFramedWindow<T>(
  title: string,
  body: (window: HTMLElement) => Operation<T>,
): Operation<Task<T>> {
  return yield* task(title, function* () {
    const window = yield* pushFramedWindow(title)
    try {
      return yield* body(window)
    } finally {
      window.setAttribute("failed", "")
    }
  })
}

export function* spawnFramedWindow2<T>(
  title: string,
  body: (window: HTMLElement) => Operation<T>,
): Operation<Task<T>> {
  return yield* task(title, function* () {
    const window = yield* nest(html("div", { class: "window2" }, title))
    try {
      return yield* body(window)
    } finally {
      window.setAttribute("failed", "")
    }
  })
}

export function* pull<T, R>(
  stream: Stream<T, R>,
  callback: (value: T) => Operation<void | string>,
): Operation<void> {
  for (const event of yield* each(stream)) {
    const x = yield* callback(event)
    if (x === "stop") {
      break
    }
    yield* each.next()
  }
}
