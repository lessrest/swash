import { z } from "zod"
import { html } from "./html.ts"
import { useWebSocket } from "./sock.ts"
import { Sync, sync, system } from "./sync2.ts"

import "@types/dom-view-transitions"

import {
  Stream,
  call,
  createSignal,
  each,
  main,
  on,
  resource,
  spawn,
  useScope,
} from "effection"

export type Step =
  | ["animation frame began", number]
  | ["document mutation requested", (document: Document) => void]
  | ["document mutation applied"]
  | ["live transcription began"]
  | ["phrase heard conclusively", Word[]]
  | ["phrase heard tentatively", Word[]]
  | ["known text is now", string]
  | ["shown text is now", string]
  | ["typing speed is now", number]
  | ["show one more letter"]

type TagName = Step[0]
type Payload<T extends TagName> = Extract<Step, [T, unknown]>[1]

function* wait<T extends TagName>(
  tag: T,
  predicate?: (payload: Payload<T>) => boolean,
): Generator<Sync<Step>, Payload<T>, Step> {
  return (
    (yield sync<Step>({
      wait: (t) =>
        t[0] === tag && (!predicate || predicate(t[1] as Payload<T>)),
    })) as [T, Payload<T>]
  )[1]
}

const swash = system<Step>(function* (rule, sync) {
  yield* spawn(function* () {
    for (const timestamp of yield* each(useAnimationFrames)) {
      if (mutationQueue.length > 0) {
        yield* emit("animation frame began", timestamp)
      }
      yield* each.next()
    }
  })

  const scope = yield* useScope()

  function start(
    name: string,
    body: () => Generator<Sync<Step>, void, Step>,
  ) {
    run(function* () {
      yield* rule(name, body)
    })
  }

  function run(body: () => Operation<void>) {
    scope.run(body)
  }

  const mutationQueue: ((document: Document) => void)[] = []

  yield* rules({
    *["Document mutations are queued."]() {
      for (;;) {
        mutationQueue.push(yield* wait("document mutation requested"))
      }
    },

    *["The mutation queue is applied in animation frames."]() {
      for (;;) {
        yield* wait("document mutation requested")
        yield* wait("animation frame began")
        const x = applyMutationQueue(mutationQueue)
        x.updateCallbackDone.then(() => {
          run(function* () {
            yield* emit("document mutation applied")
          })
        })
      }
    },

    *["Events are logged to the console."]() {
      for (;;) {
        const [tag, payload] = yield sync({ wait: () => true })
        console.info(tag, payload)
      }
    },

    *["Once transcription begins, a transcription view is shown."]() {
      yield* wait("live transcription began")
      yield* post("document mutation requested", ({ body }) => {
        body.classList.add("ok")
        body.append(
          html(
            "kbd",
            {
              style: {
                opacity: 0.5,
                fontSize: "60%",
                lineHeight: "1.5",
              },
            },
            "",
          ),
        )
      })
    },

    *["The latest tentative phrase is shown."]() {
      for (;;) {
        const words = yield* wait("phrase heard tentatively")
        yield* post("document mutation requested", ({ body }) => {
          body.querySelectorAll(".tentative").forEach((x) => x.remove())
          body
            .querySelector("kbd")!
            .append(
              ...words.map(({ punctuated_word }) =>
                html("span.word.tentative", {}, punctuated_word + " "),
              ),
            )
        })
      }
    },

    *["All conclusive phrases are shown."]() {
      for (;;) {
        const words = yield* wait("phrase heard conclusively")
        yield* post("document mutation requested", ({ body }) => {
          body
            .querySelector("kbd")!
            .append(
              ...words.map(({ punctuated_word }) =>
                html("span.word.conclusive", {}, punctuated_word + " "),
              ),
            )
        })
      }
    },

    *["The insertion buffer is cleared when a conclusive phrase arrives."]() {
      for (;;) {
        yield* wait("phrase heard conclusively")
        yield* post("phrase heard tentatively", [])
      }
    },

    *["The known text is tracked."]() {
      for (;;) {
        yield* wait("document mutation applied")
        const next = document.querySelector("kbd")?.textContent ?? ""
        if (next !== knownText) {
          yield* post("known text is now", next)
          knownText = next
        }
      }
    },

    *["The shown text is updated."]() {
      for (;;) {
        yield* wait("show one more letter")
        yield* post(
          "shown text is now",
          knownText.slice(0, shownText.length + 1),
        )
        shownText = knownText.slice(0, shownText.length + 1)
      }
    },

    *["shown text is shown"]() {
      const p = html("p")
      document.body.append(p)
      for (;;) {
        const text = yield* wait("shown text is now")
        yield* post("document mutation requested", ({ body }) => {
          console.info(text)
          p.replaceChildren(html("span", {}, text))
        })
      }
    },

    *["The typing speed is dynamically adjusted."]() {
      for (;;) {
        yield* wait("document mutation applied")
        const lettersLeft = knownText.length - shownText.length
        if (lettersLeft > 0) {
          const lettersPerSecond = Math.min(lettersLeft, 5) * 10
          yield* post("typing speed is now", lettersPerSecond)
        } else {
          yield* post("typing speed is now", 0)
        }
      }
    },

    *["The typing speed is used."]() {
      let interval = null
      let speed = 0
      for (;;) {
        const nextSpeed = yield* wait("typing speed is now")
        if (nextSpeed !== speed) {
          speed = nextSpeed
          clearInterval(interval)
          if (nextSpeed > 0) {
            interval = setInterval(() => {
              run(function* () {
                yield* emit("show one more letter")
              })
            }, 1000 / speed)
          }
        }
      }
    },
  })

  let knownText = ""
  let shownText = ""

  const mediaStream = yield* call(
    navigator.mediaDevices.getUserMedia({ audio: true, video: false }),
  )

  const recorder = new MediaRecorder(mediaStream, {
    mimeType: "audio/webm;codecs=opus",
    audioBitsPerSecond: 64000,
  })

  const socket = yield* useWebSocket(
    new WebSocket(
      `${document.location.protocol === "https:" ? "wss:" : "ws:"}//${
        document.location.host
      }/transcribe?language=en-US`,
    ),
  )

  yield* spawn(function* () {
    yield* emit("live transcription began")

    for (const { data } of yield* each(socket)) {
      if (typeof data === "string") {
        const json = JSON.parse(data)
        const { channel, is_final } = DeepgramResultSchema.parse(json)
        if (channel && channel.alternatives[0].transcript) {
          const { words } = channel.alternatives[0]
          if (is_final) {
            yield* emit("phrase heard conclusively", words)
          } else {
            yield* emit("phrase heard tentatively", words)
          }
        }
      } else {
        throw new Error("unexpected message type")
      }

      yield* each.next()
    }
  })

  return yield* spawn(function* () {
    recorder.start(100)

    for (const chunk of yield* each(on(recorder, "dataavailable"))) {
      yield* socket.send(chunk.data)
      yield* each.next()
    }
  })

  function* emit<T extends TagName>(tag: T, payload?: Payload<T>) {
    yield* rule(tag, function* () {
      yield sync({ post: [[tag, payload] as Step] })
    })
  }

  function* post<T extends TagName>(tag: T, payload?: Payload<T>) {
    yield sync({ post: [[tag, payload] as Step] })
  }

  function* rules(
    rules: Record<string, () => Generator<Sync<Step>, void, Step>>,
  ) {
    for (const [name, ruleBody] of Object.entries(rules)) {
      yield* rule(name, ruleBody)
    }
  }

  function _byTag<T extends TagName>(tag: T) {
    return (x: Step) => x[0] === tag
  }
})

const DeepgramResultSchema = z.object({
  metadata: z.object({
    request_id: z.string(),
  }),
  type: z.literal("Results"),
  channel_index: z.tuple([z.number(), z.number()]),
  duration: z.number(),
  start: z.number(),
  is_final: z.boolean(),
  speech_final: z.boolean().optional(),
  channel: z.object({
    alternatives: z.array(
      z.object({
        transcript: z.string(),
        confidence: z.number(),
        words: z.array(
          z.object({
            word: z.string(),
            punctuated_word: z.string(),
            start: z.number(),
            end: z.number(),
            confidence: z.number(),
          }),
        ),
      }),
    ),
  }),
})

type DeepgramResult = z.infer<typeof DeepgramResultSchema>
type Word = DeepgramResult["channel"]["alternatives"][0]["words"][0]

export const useAnimationFrames: Stream<number, never> = resource(function* (
  provide,
) {
  const signal = createSignal<number, never>()
  let id = 0
  const callback: FrameRequestCallback = (timestamp) => {
    signal.send(timestamp)
    id = requestAnimationFrame(callback)
  }
  id = requestAnimationFrame(callback)
  try {
    yield* provide(yield* signal)
  } finally {
    cancelAnimationFrame(id)
  }
})

function applyMutationQueue(queue: ((document: Document) => void)[]) {
  return document.startViewTransition(() => {
    for (const thunk of queue) {
      thunk(document)
    }
    queue.length = 0
  })
}

export async function swash3() {
  await main(() => swash)

  // document.body.append(
  //   html(
  //     "div",
  //     {
  //       style: {
  //         display: "flex",
  //         flexFlow: "column  wrap",
  //         marginLeft: "4em",
  //         fontFamily: "graphik",
  //       },
  //     },
  //     ...words
  //       .filter(({ kind }) => kind === "noun")
  //       .map(({ word, text }) => html("span", {}, html("span", {}, word))),
  //   ),
  // )
}
