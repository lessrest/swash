import { z } from "zod"

import "@types/dom-view-transitions"

import {
  Operation,
  Stream,
  Subscription,
  action,
  call,
  createSignal,
  each,
  main,
  on,
  resource,
  spawn,
  suspend,
  useScope,
} from "effection"

import { html } from "./html.ts"
import { useWebSocket } from "./sock.ts"
import { Sync, SyncSpec, sync, system } from "./sync.ts"
import {
  ChatCompletionRequest,
  ChatMessage,
  ContentPart,
  gpt4o,
  think,
} from "./mind.ts"
import { into } from "./nest.ts"

import { pong } from "./pong.ts"

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

type Step =
  | ["live transcription began"]
  | ["request animation frame"]
  | ["animation frame began", number]
  | ["document mutation requested", (document: Document) => void]
  | ["document mutation applied"]
  | ["phrase heard conclusively", Word[]]
  | ["phrase heard tentatively", Word[]]
  | ["known text changed", string]
  | ["shown text is now", string]
  | ["typing speed is now", number]
  | ["show one more letter"]
  | ["LLM starting for", string]
  | ["making LLM request", ChatCompletionRequest]
  | ["LLM subscription", Subscription<ChatMessage, void>]
  | ["LLM text", string]
  | ["LLM done"]
  | ["acquired video stream", MediaStream]
  | ["video started"]
  | ["request video image"]
  | ["captured video image", string]

document.addEventListener("DOMContentLoaded", async function () {
  if (document.location.hash.includes("pong")) {
    await main(() => pong)
  } else {
    await main(() => swash)
  }
})

const swash = system<Step>(function* (rule, sync) {
  yield* into(document.body)

  const scope = yield* useScope()

  function run(body: () => Operation<void>) {
    scope.run(body)
  }

  const mutationQueue: ((document: Document) => void)[] = []

  function* exec<
    T extends Step,
  >(body: () => Operation<T>, spec: Partial<SyncSpec<Step>> = {}): Generator<Sync<Step>, T, Step> {
    const step = yield sync({ ...spec, exec: body })
    return step as T
  }

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
    [sentences, conclusive, tentative]
      .join(" ")
      //      .replaceAll(/ +/g, " ")
      // remove consecutive newlines
      .replaceAll(/\n+/g, "\n")

  let shownText = ""

  yield* rules({
    *["Document mutations are queued."]() {
      for (;;) {
        mutationQueue.push(yield* wait("document mutation requested"))
      }
    },

    *["Animation frames are triggered on request."]() {
      for (;;) {
        yield* wait("request animation frame")
        yield* exec(() =>
          action(function* (resolve) {
            const id = requestAnimationFrame((x) => {
              resolve(["animation frame began", x])
            })
            try {
              yield* suspend()
            } finally {
              cancelAnimationFrame(id)
            }
          }),
        )
      }
    },

    *["The mutation queue is applied in animation frames."]() {
      for (;;) {
        yield* wait("document mutation requested")
        yield* post("request animation frame")
        yield* wait("animation frame began")
        yield* exec(function* () {
          yield* call(applyMutationQueue(mutationQueue))
          return ["document mutation applied"]
        })
      }
    },

    *["Once transcription begins, a transcription view is shown."]() {
      yield* wait("live transcription began")
      yield* post("document mutation requested", ({ body }) => {
        body.classList.add("ok")
      })
    },

    *["A video stream is acquired."]() {
      yield* wait("live transcription began")
      yield* exec(function* () {
        const mediaStream = yield* call(
          navigator.mediaDevices.getUserMedia({ audio: false, video: true }),
        )
        return ["acquired video stream", mediaStream]
      })
    },

    *["The video stream is shown."]() {
      const video = html<HTMLVideoElement>("video", {
        srcObject: yield* wait("acquired video stream"),
        controls: false,
      })
      document.body.append(video)
      video.play()
      yield* post("video started")
    },

    *["Capture video images on request."]() {
      for (;;) {
        yield* wait("request video image")
        const canvas = document.createElement("canvas")
        const context = canvas.getContext("2d")
        const video = document.querySelector("video")
        if (context && video) {
          const maxDimension = 768
          const aspectRatio = video.videoWidth / video.videoHeight

          if (video.videoWidth > video.videoHeight) {
            canvas.width = maxDimension
            canvas.height = maxDimension / aspectRatio
          } else {
            canvas.height = maxDimension
            canvas.width = maxDimension * aspectRatio
          }

          context.drawImage(
            video,
            0,
            0,
            video.videoWidth,
            video.videoHeight,
            0,
            0,
            canvas.width,
            canvas.height,
          )
          const imageData = canvas.toDataURL("image/png")
          yield* post("captured video image", imageData)
        }
      }
    },

    // *["Request an image when the user says 'Look here'."]() {
    //   for (;;) {
    //     yield* wait(
    //       "phrase heard conclusively",
    //       (words) => wordsToText(words).trim() === "Look here.",
    //     )
    //     yield* post("request video image")
    //   }
    // },

    // *["Images are captured regularly."]() {
    //   yield* wait("video started")
    //   yield* post("request video image")
    //   for (;;) {
    //     yield* exec(function* () {
    //       yield* sleep(1000)
    //       return ["request video image"]
    //     })
    //   }
    // },

    *["Captured images are shown."]() {
      for (;;) {
        const img = html("img", {
          src: yield* wait("captured video image"),
          style: {
            width: "12em",
            height: "auto",
            margin: "1em 0",
            borderRadius: ".5em",
            boxShadow: "0 0 1em 1em #fff3",
            display: "none",
          },
        })
        yield* post("document mutation requested", ({ body }) => {
          body.querySelectorAll("img").forEach((x) => x.remove())
          body.prepend(img)
        })
      }
    },

    // *["Send images to GPT-4o."]() {
    //   for (;;) {
    //     const imageData = yield* wait("captured video image")
    //     yield* post("making LLM request", {
    //       systemMessage: "Generate a caption for this image.",
    //       messages: [
    //         {
    //           role: "user",
    //           content: [{ type: "image_url", image_url: { url: imageData } }],
    //         },
    //       ],
    //       temperature: 0.4,
    //       maxTokens: 200,
    //     })
    //   }
    // },

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
        conclusive += wordsToText(words) + " "
        yield* post("known text changed")
      }
    },

    *["The tentative phrase is cleared when heard conclusively."]() {
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

    *["When the visible prefix of the known text changes, the shown text is updated."]() {
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
        yield* post("document mutation requested", () => {
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
      let interval: number | undefined = undefined
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
        conclusive = lines.pop() || ""

        const image = document.querySelector("img")
        const content: ContentPart[] = [{ type: "text", text: sentences }]
        if (image) {
          content.push({ type: "image_url", image_url: { url: image.src } })
        }

        yield* post("making LLM request", {
          systemMessage: [
            "This is a swa.sh live transcription session.",
            "Fix likely transcription errors.",
            "Split run-on sentences and improve punctuation.",
            "Use CAPS on key salient words for emphasis and flow.",
            "Use varying PREFIX EMOJIS before each sentence for visual interest.",
            "Respond ONLY with the edited transcript.",
            "If the user seems to request some change, do perform that change.",
            "When performing a change, omit the user request from the transcript.",
          ].join(" "),
          messages: [{ role: "user", content }],
          temperature: 0.4,
          maxTokens: 200,
        })
      }
    },

    *["LLM requests are made serially using GPT-4o."]() {
      for (;;) {
        const request = yield* wait("making LLM request")
        yield* exec(
          function* () {
            const response = yield* think(gpt4o, request)
            yield* emit("LLM starting for", sentences)
            for (;;) {
              const { value, done } = yield* response.next()
              if (done) {
                return ["LLM done"]
              }
              yield* emit("LLM text", value.content as string)
            }
          },
          { halt: ([tag]) => tag === "making LLM request" },
        )
      }
    },

    *["LLM text is used to update the known text."]() {
      for (;;) {
        const orig = yield* wait("LLM starting for")
        let llm = ""

        for (;;) {
          const p = yield sync({
            wait: ([tag]) => tag === "LLM text" || tag === "LLM done",
          })
          if (p[0] === "LLM done") {
            break
          } else if (p[0] === "LLM text") {
            llm += p[1].replaceAll(/([.!?])\s*/g, "$1\n")
            sentences = (llm + orig.slice(llm.length)).trim()

            yield* post("known text changed")
          }
        }

        sentences = llm
        yield* post("known text changed")
      }
    },
  })

  let recorder: MediaRecorder | undefined

  try {
    const mediaStream = yield* call(
      navigator.mediaDevices.getUserMedia({ audio: true, video: false }),
    )

    const options = [
      { mimeType: "audio/webm;codecs=opus", audioBitsPerSecond: 128000 },
      { mimeType: "audio/mp4", audioBitsPerSecond: 128000 },
    ]

    for (const option of options) {
      if (MediaRecorder.isTypeSupported(option.mimeType)) {
        recorder = new MediaRecorder(mediaStream, option)
        console.log("recorder", recorder, option.mimeType)
        break
      }
    }
  } catch (e) {
    console.error(e)
    document.body.append(html("aside", {}, `Error: ${e}`))
  }

  if (!recorder) {
    alert("Media codec trouble. Sorry.")
    throw new Error(
      "Unsupported mimeType: MediaRecorder cannot function with the available options.",
    )
  }

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
        const result = DeepgramResultSchema.safeParse(json)
        if (result.success) {
          const { channel, is_final } = result.data
          if (channel && channel.alternatives[0].transcript) {
            const { words } = channel.alternatives[0]
            if (is_final) {
              yield* emit("phrase heard conclusively", words)
            } else {
              yield* emit("phrase heard tentatively", words)
            }
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
  const f = () => {
    for (const thunk of queue) {
      thunk(document)
    }
    queue.length = 0
  }

  if (document.startViewTransition) {
    return document.startViewTransition(f).updateCallbackDone
  } else {
    f()
    return Promise.resolve()
  }
}
