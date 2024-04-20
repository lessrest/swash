import {
  Operation,
  Stream,
  Task,
  call,
  createContext,
  each,
  once,
  resource,
  spawn,
} from "effection"

import { tag } from "./tag.js"

export const Target = createContext("target", {
  node: document.body,
})

export const Widget = createContext("widget", {
  node: document.body,
})

export function* append(content: string | Node) {
  const { node } = yield* Target
  node.append(content)
}

export function* message(...content: (string | Node)[]) {
  yield* append(tag("message", {}, ...content))
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
  return yield* spawn(function* () {
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
  return yield* spawn(function* () {
    const window = yield* pushNode(tag("div", { class: "window2" }, title))
    try {
      return yield* body(window)
    } finally {
      window.setAttribute("failed", "")
    }
  })
}

export function useMediaStream(
  constraints: MediaStreamConstraints,
): Operation<MediaStream> {
  return resource(function* (provide) {
    const stream: MediaStream = yield* call(
      navigator.mediaDevices.getUserMedia(constraints),
    )
    try {
      yield* provide(stream)
    } finally {
      for (const track of stream.getTracks()) {
        yield* message(`stopping ${track.kind}`)
        track.stop()
      }
    }
  })
}

export function useMediaRecorder(
  stream: MediaStream,
  options: MediaRecorderOptions,
): Operation<MediaRecorder> {
  return resource(function* (provide) {
    const recorder = new MediaRecorder(stream, options)
    try {
      yield* provide(recorder)
    } finally {
      if (recorder.state !== "inactive") {
        recorder.stop()
      }
    }
  })
}
export function* foreach<T, R>(
  stream: Stream<T, R>,
  callback: (value: T) => Operation<void>,
): Operation<void> {
  for (let event of yield* each(stream)) {
    yield* callback(event)
    yield* each.next()
  }
}
