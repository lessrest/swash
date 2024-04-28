import {
  Operation,
  Stream,
  Task,
  createContext,
  each,
  once,
  spawn,
} from "effection"

import { tag } from "./tag.ts"
import { task } from "./task.ts"

export const Target = createContext("target", {
  node: document.body,
})

export const Widget = createContext("widget", {
  node: document.body,
})

export function* append(content: string | Node) {
  const { node } = yield* Target
  node.append(content)
  yield* scrollToBottom()
}

export function* message(...content: (string | Node)[]) {
  yield* append(tag("message", {}, ...content))
}

export function* getTarget() {
  const { node } = yield* Target
  return node
}

export function* setNode(element: HTMLElement): Operation<HTMLElement> {
  yield* Target.set({ node: element })
  return element
}

export function* pushNode(node: HTMLElement) {
  yield* append(node)
  yield* setNode(node)

  return node
}

export function* waitForButton(label: string): Operation<void> {
  const button = tag("button", {}, label)
  yield* append(button)
  yield* once(button, "click")
  button.setAttribute("disabled", "")
}

export function* clear(): Operation<void> {
  const { node } = yield* Target
  node.replaceChildren()
}

export function* scrollToBottom() {
  const { node } = yield* Target
  node.scrollIntoView({ block: "end", inline: "end", behavior: "smooth" })
}

export function* spawnWithElement<T>(
  element: HTMLElement,
  body: (element: HTMLElement) => Operation<T>,
): Operation<Task<T>> {
  return yield* spawn(function* () {
    return yield* body(yield* pushNode(element))
  })
}

export function* pushFramedWindow(title: string) {
  yield* pushNode(tag("div", { class: "window" }))

  yield* append(
    tag(
      "header",
      { class: "title-bar" },
      tag("span", { class: "title-bar-text" }, title),
    ),
  )

  return yield* pushNode(tag("div", { class: "window-body" }))
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
    const window = yield* pushNode(tag("div", { class: "window2" }, title))
    try {
      return yield* body(window)
    } finally {
      window.setAttribute("failed", "")
    }
  })
}

export function* foreach<T, R>(
  stream: Stream<T, R>,
  callback: (value: T) => Operation<void>,
): Operation<void> {
  for (const event of yield* each(stream)) {
    yield* callback(event)
    yield* each.next()
  }
}
