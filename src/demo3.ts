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
import { gpt4o, think } from "./mind.ts"
import { into } from "./nest.ts"

export type Step =
  | ["animation frame began", number]
  | ["document mutation requested", (document: Document) => void]
  | ["document mutation applied"]
  | ["live transcription began"]
  | ["phrase heard conclusively", Word[]]
  | ["phrase heard tentatively", Word[]]
  | ["known text changed", string]
  | ["shown text is now", string]
  | ["typing speed is now", number]
  | ["show one more letter"]
  | ["LLM done"]
  | ["LLM starting for", string]
  | ["LLM text", string]

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
  yield* into(document.body)

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

  let sentences = ""
  let conclusive = ""
  let tentative = ""

  const wordsToText = (words: Word[]) =>
    words
      .map((x) => x.punctuated_word)
      .join(" ")
      .trim()
      .replaceAll(/([.!?])\s?/g, "$1\n")

  const knownText = () =>
    (sentences + conclusive + tentative).replaceAll(/^\s+/g, "")

  let shownText = ""

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
      })
    },

    *["The latest tentative phrase is tracked."]() {
      for (;;) {
        const words = yield* wait("phrase heard tentatively")
        tentative = wordsToText(words).trim()
        yield* post("known text changed")
      }
    },

    *["The conclusive phrases are tracked."]() {
      for (;;) {
        const words = yield* wait("phrase heard conclusively")
        conclusive += wordsToText(words)
        yield* post("known text changed")
      }
    },

    *["The insertion buffer is cleared when a conclusive phrase arrives."]() {
      for (;;) {
        yield* wait("phrase heard conclusively")
        yield* post("phrase heard tentatively", [])
      }
    },

    *["The shown text is updated a letter at a time."]() {
      for (;;) {
        yield* wait("show one more letter")
        shownText = knownText().slice(0, shownText.length + 1)
        yield* post("shown text is now", shownText)
      }
    },

    *["The shown text is updated immediately in a certain case."]() {
      for (;;) {
        yield* wait("known text changed")
        if (knownText().slice(0, shownText.length) !== shownText) {
          shownText = knownText().slice(0, shownText.length)
          yield* post("shown text is now", shownText)
        }
      }
    },

    *["The shown text is shown in a paragraph."]() {
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
        yield sync({
          wait: ([tag]) =>
            tag === "known text changed" || tag === "shown text is now",
        })
        const lettersLeft = knownText().length - shownText.length
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

    *["The conclusive text is enhanced with GPT-4o."]() {
      for (;;) {
        yield* wait("phrase heard conclusively")
        yield* wait("known text changed")

        const lines = conclusive.split("\n")
        if (!lines.length) {
          continue
        }

        sentences = sentences + lines.slice(0, -1).join("\n")
        conclusive = lines.pop()

        console.log({ sentences, conclusive, lines })

        yield* post("LLM starting for", sentences)
        run(function* () {
          const response = yield* think(gpt4o, {
            systemMessage: [
              "Fix likely transcription errors.",
              "Split run-on sentences and improve punctuation.",
              //              "Translate to Swedish.",
              "Use CAPS on key salient words for emphasis and flow.",
              //              "Prefix each sentence with a relevant EMOJI.",
            ].join(" "),

            messages: [{ role: "user", content: sentences }],
            temperature: 0.4,
            maxTokens: 200,
          })

          for (;;) {
            const { value, done } = yield* response.next()
            if (done) {
              break
            }
            console.info(value)
            yield* emit("LLM text", value.content)
          }

          yield* emit("LLM done")
        })
        yield* wait("LLM done")
      }
    },

    *["LLM text is used to update the known text."]() {
      for (;;) {
        const n = yield* wait("LLM starting for")
        let orig = sentences
        let llm = ""
        for (;;) {
          const [tag, text] = yield sync({
            wait: ([tag]) => tag === "LLM text" || tag === "LLM done",
          })
          if (tag === "LLM done") {
            break
          }
          llm += text.replaceAll(/([.!?])\s*/g, "$1\n")
          sentences = (llm + orig.slice(llm.length)).trim()

          yield* post("known text changed")
        }

        sentences = llm
        yield* post("known text changed")
      }
    },
  })

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
    recorder.start(60)

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
