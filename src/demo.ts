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
  sleep,
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
import { graphemesOf } from "./text.ts"

declare global {
  interface AudioEncoderConfig {
    opus?: OpusEncoderConfig
  }

  interface OpusEncoderConfig {
    format?: "opus" | "ogg"
    signal?: "auto" | "music" | "voice"
    application?: "voip" | "audio" | "lowdelay"
    frameDuration?: number
    /**
     * Encoder complexity (0-10). 10 is highest.
     * Default: 5 on mobile, 9 on other platforms.
     */
    complexity?: number
    /** Configures the encoder's expected packet loss percentage (0 to 100). */
    packetlossperc?: number
    /** Enables Opus in-band Forward Error Correction (FEC) */
    useinbandfec?: boolean
    /** Enables Discontinuous Transmission (DTX) */
    usedtx?: boolean
  }
}

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

function* halt<T extends TagName>(
  tag: T,
  predicate?: (payload: Payload<T>) => boolean,
): Generator<Sync<Step>, void, Step> {
  yield sync<Step>({
    halt: (t) =>
      t[0] === tag && (!predicate || predicate(t[1] as Payload<T>)),
  })
}

type Step =
  | ["live transcription began"]
  | ["request animation frame"]
  | ["animation frame began", number]
  | ["document mutation requested", (document: Document) => void]
  | ["document mutation applied"]
  | ["add final phrase", Word[]]
  | ["new interim phrase", Word[]]
  | ["known text changed", string]
  | ["shown text is now", string]
  | ["typing speed is now", number]
  | ["show one more letter"]
  | ["LLM starting for", Word[][]]
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

  function* exec<
    T extends Step,
  >(body: () => Operation<T>, spec: Partial<SyncSpec<Step>> = {}): Generator<Sync<Step>, T, Step> {
    const step = yield sync({ ...spec, exec: body })
    return step as T
  }

  const documentMutations: ((document: Document) => void)[] = []

  let sentences: Word[][] = []
  let conclusive: Word[] = []
  let tentative: Word[] = []

  const wordsToText = (words: Word[]) =>
    words
      .map((x) => x.punctuated_word)
      .join(" ")
      .replaceAll(/([.!?])\s?/g, "$1\n")

  const knownText = () =>
    [
      ...sentences.map(wordsToText),
      wordsToText(conclusive),
      wordsToText(tentative),
    ]
      .join(" ")
      .replaceAll(/\n\s*/g, "\n")

  yield* rules({
    *["The latest tentative phrase is tracked."]() {
      for (;;) {
        const words = yield* wait("new interim phrase")
        tentative = words
        yield* post("known text changed")
      }
    },

    *["The conclusive phrases are tracked."]() {
      for (;;) {
        const words = yield* wait("add final phrase")
        conclusive.push(...words)
        tentative = []
        yield* post("known text changed")
      }
    },

    *["The shown text is shown in a paragraph."]() {
      const p = html("p")
      document.body.append(html("type-writer", {}, p))
      for (;;) {
        yield* wait("known text changed")
        yield* post("document mutation requested", () => {
          p.replaceChildren(html("span", {}, knownText()))
        })
      }
    },
  })

  yield* rules({
    *["The conclusive text is enhanced with GPT-4o."]() {
      for (;;) {
        yield* wait("add final phrase")
        yield* wait("known text changed")

        if (conclusive.length > 0) {
          const newSentences = []
          let sentence = []
          for (const word of conclusive) {
            sentence.push(word)
            if (/[.!?]$/.test(word.punctuated_word)) {
              newSentences.push(sentence)
              sentence = []
            }
          }

          conclusive = sentence
          sentences = [...sentences, ...newSentences]
          yield* post("known text changed")
        }

        if (sentences.length < 3) {
          // prevent hallucination by waiting for more sentences
          continue
        }

        const image = document.querySelector("img")
        const content: ContentPart[] = [
          { type: "text", text: sentences.map(wordsToText).join("\n") },
        ]
        if (image) {
          content.push({ type: "image_url", image_url: { url: image.src } })
        }

        yield* post("making LLM request", {
          systemMessage: [
            "Fix likely transcription errors.",
            "Split run-on sentences and improve punctuation.",
            "Use CAPS where a speaker would put stress.",
            "Use varying EMOJIS before each sentence for visual interest.",
            "Respond ONLY with the edited transcript.",
            "Use em dashes liberally, for a more interesting rhythm.",
          ].join(" "),
          messages: [{ role: "user", content }],
          temperature: 0.4,
          maxTokens: 500,
        })

        yield* wait("LLM done")
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
          const [tag, payload] = yield sync({
            wait: ([tag]) => tag === "LLM text" || tag === "LLM done",
          })
          if (tag === "LLM done") {
            break
          } else if (tag === "LLM text") {
            llm += payload
          }
        }

        const llmSentences = llm
          .replaceAll(/([.!?])\s*/g, "$1\n")
          .split("\n")
          .map((line) => line.trim())
          .filter(Boolean)
          .map((line) =>
            line.split(/\s+/).map((word) => ({
              word,
              punctuated_word: word,
              start: 0,
              end: 0,
              confidence: 0,
            })),
          )

        sentences = [...llmSentences, ...sentences.slice(orig.length)]

        yield* post("known text changed")
      }
    },
  })

  yield* rules({
    *["Document mutations are queued."]() {
      for (;;) {
        documentMutations.push(yield* wait("document mutation requested"))
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
          yield* call(applyMutationQueue(documentMutations))
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

    // *["A video stream is acquired."]() {
    //   yield* wait("live transcription began")
    //   yield* exec(function* () {
    //     const mediaStream = yield* call(
    //       navigator.mediaDevices.getUserMedia({ audio: false, video: true }),
    //     )
    //     return ["acquired video stream", mediaStream]
    //   })
    // },

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

    *["Request an image when the user says 'Look here'."]() {
      for (;;) {
        yield* wait(
          "add final phrase",
          (words) => wordsToText(words).trim() === "Look here.",
        )
        yield* post("request video image")
      }
    },

    *["Images are captured regularly."]() {
      yield* wait("video started")
      yield* post("request video image")
      for (;;) {
        yield* exec(function* () {
          yield* sleep(1000)
          return ["request video image"]
        })
      }
    },

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
  })

  const mediaStream = yield* call(
    navigator.mediaDevices.getUserMedia({ audio: true, video: false }),
  )

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
              yield* emit("add final phrase", words)
            } else {
              yield* emit("new interim phrase", words)
            }
          }
        }
      } else {
        throw new Error("unexpected message type")
      }

      yield* each.next()
    }
  })

  const packets = createSignal<ArrayBuffer>()
  const onPacket = (packet: ArrayBuffer) => {
    packets.send(packet)
  }

  yield* spawn(function* () {
    yield* recordAudioPackets(mediaStream, onPacket)
  })

  return yield* spawn(function* () {
    for (const packet of yield* each(packets)) {
      yield* socket.send(packet)
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

function* recordAudioPackets(
  mediaStream: MediaStream,
  onPacket: (packet: ArrayBuffer) => void,
) {
  const audioContext = new AudioContext()
  const sourceNode = audioContext.createMediaStreamSource(mediaStream)
  const streamChannels = mediaStream.getAudioTracks()[0].getSettings()
    .channelCount!
  const processor = audioContext.createScriptProcessor(
    16384,
    streamChannels,
    1,
  )

  const audioEncoder = new AudioEncoder({
    output: (encodedPacket: EncodedAudioChunk) => {
      const arrayBuffer = new ArrayBuffer(encodedPacket.byteLength)
      encodedPacket.copyTo(arrayBuffer)
      onPacket(arrayBuffer)
    },
    error: (error: Error) => {
      console.error("AudioEncoder error:", error)
    },
  })

  audioEncoder.configure({
    codec: "opus",
    sampleRate: 48000,
    numberOfChannels: 1,
    bitrate: 16000,
    opus: {
      application: "lowdelay",
      signal: "voice",
    },
  })

  sourceNode.connect(processor)
  processor.connect(audioContext.destination)

  processor.addEventListener("audioprocess", (event) => {
    const numberOfFrames = event.inputBuffer.length
    const inputBuffer = new ArrayBuffer(numberOfFrames * 4 * 1)
    const inputView = new DataView(inputBuffer)

    const inputData = event.inputBuffer.getChannelData(0)
    for (let i = 0; i < numberOfFrames; i++) {
      inputView.setFloat32(i * 4, inputData[i], true)
    }

    audioEncoder.encode(
      new AudioData({
        data: inputBuffer,
        timestamp: event.playbackTime * 1000000,
        format: "f32",
        numberOfChannels: 1,
        numberOfFrames,
        sampleRate: 48000,
      }),
    )
  })

  try {
    yield* suspend()
  } finally {
    audioEncoder.close()
    processor.disconnect()
    sourceNode.disconnect()
  }
}

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

class TypeWriter extends HTMLElement {
  limit = 0
  range = new Range()
  timer?: number
  snitch = new MutationObserver(() => {
    this.update()
    if (!this.timer) this.proceed()
  })

  connectedCallback() {
    this.range.selectNodeContents(this)

    CSS.highlights.set(
      "hidden",
      (CSS.highlights.get("hidden") ?? new Highlight()).add(this.range),
    )

    this.snitch.observe(this, {
      childList: true,
      subtree: true,
      characterData: true,
    })

    this.proceed()
  }

  disconnectedCallback() {
    this.snitch.disconnect()
    CSS.highlights.get("hidden")?.delete(this.range)
    clearTimeout(this.timer)
  }

  update() {
    const walk = document.createTreeWalker(this, NodeFilter.SHOW_TEXT)
    let node: Text | null = null

    while (walk.nextNode()) {
      node = walk.currentNode as Text
      const { length } = node.data.slice(0, this.limit)
      if ((this.limit -= length) <= 0) {
        this.range.setStart(node, length)
        break
      }
    }

    if (this.limit > 0) this.range.setStart(this, 0)

    this.range.setEndAfter(this)
  }

  proceed() {
    if (this.range.toString().trim() === "") {
      this.timer = undefined
      return
    }

    this.limit = Math.min(this.limit + 1, this.innerText.length)
    this.update()

    const delay = adjustSpeed(this.innerText.length, this.range.toString())
    this.timer = setTimeout(() => this.proceed(), delay)
  }
}

function adjustSpeed(length: number, suffix: string) {
  const maxSpeed = 80
  const minSpeed = 30
  const speedRange = maxSpeed - minSpeed
  const speedFactor = 1 - suffix.length / length
  const base = Math.round(minSpeed + speedRange * speedFactor ** 2)

  return delayForGrapheme(suffix[0], base)

  function delayForGrapheme(grapheme: string, baseDelay: number) {
    const factors: Record<string, number> = {
      // TODO: justify these arbitrary numbers with pseudoscience
      " ": 3,
      "–": 7,
      ",": 8,
      ";": 8,
      ":": 9,
      ".": 10,
      "—": 12,
      "!": 15,
      "?": 15,
      "\n": 20,
    }
    return baseDelay * (factors[grapheme] ?? 1) * 0.8
  }
}

customElements.define("type-writer", TypeWriter)

document.addEventListener("DOMContentLoaded", () => {
  document.body.append(
    html("style", {}, `::highlight(hidden) { color: transparent }`),
  )
  const poem = html(
    "type-writer",
    {},
    html(
      "p",
      {},
      `
    Two roads diverged in a wood, and I—
    I took the one less traveled by,
    And that has made all the difference.
  `,
    ),
  )
  //  document.body.append(poem)
})
