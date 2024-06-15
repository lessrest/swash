import { z } from "zod"

import "@types/dom-view-transitions"

import {
  Operation,
  Subscription,
  action,
  call,
  createSignal,
  each,
  main,
  sleep,
  spawn,
  suspend,
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

import "./TypeWriter.ts"

document.addEventListener("DOMContentLoaded", async function () {
  await main(() => swash)
})

type AnimationStep =
  | ["request animation frame"]
  | ["animation frame began", number]

type DocumentMutationStep =
  | ["document mutation requested", (document: Document) => void]
  | ["document mutation applied"]

type TranscriptionStep =
  | ["live transcription began"]
  | ["add final phrase", Word[]]
  | ["new interim phrase", Word[]]
  | ["known text changed", string]

type LLMStep =
  | ["LLM starting for", Word[][]]
  | ["LLM request", ChatCompletionRequest]
  | ["LLM subscription", Subscription<ChatMessage, void>]
  | ["LLM text", string]
  | ["LLM done"]

type VideoStep =
  | ["acquired video stream", MediaStream]
  | ["video started"]
  | ["request video image"]
  | ["captured video image", string]

type Step =
  | AnimationStep
  | DocumentMutationStep
  | TranscriptionStep
  | LLMStep
  | VideoStep

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

  function* exec<
    T extends Step,
  >(body: () => Operation<T>, spec: Partial<SyncSpec<Step>> = {}): Generator<Sync<Step>, T, Step> {
    const step = yield sync({ ...spec, exec: body })
    return step as T
  }

  const documentMutations: ((document: Document) => void)[] = []

  let glaciers: Word[][] = []
  let sentences: Word[][] = []
  let conclusive: Word[] = []
  let tentative: Word[] = []

  function wordsToText(words: Word[]) {
    return words.map((word) => word.punctuated_word).join("")
  }

  function render() {
    return [
      ...glaciers.flat(),
      ...sentences.flat(),
      ...conclusive,
      ...tentative,
    ].map((word) =>
      html(
        "span",
        {
          style: {
            color: word.confidence > 0.85 ? "inherit" : "red",
            fontWeight: word.punctuated_word.match(/[A-Z]{3}/)
              ? "bold"
              : "normal",
            paddingRight: word.punctuated_word.match(/[.!?]$/) ? "1em" : "0",
          },
        },
        word.punctuated_word,
        " ",
      ),
    )
  }

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
          p.replaceChildren(...render())
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

        if (glaciers.length + sentences.length < 2) {
          // prevent hallucination by waiting for more sentences
          continue
        }

        const image = document.querySelector("img")
        const content: ContentPart[] = [
          {
            type: "text",
            text: [
              "[begin old transcripts]",
              glaciers.slice(-3, -1).map(wordsToText).join("\n"),
              "[end old transcripts]",
              "[begin new sentences]",
              sentences.map(wordsToText).join("\n"),
              "[end new sentences]",
              "Task: reply with only the reformatted NEW sentences.",
              "They will be replaced. (Don't include any of the old sentences.)",
            ].join("\n"),
          },
        ]
        if (image) {
          content.push({ type: "image_url", image_url: { url: image.src } })
        }

        yield* post("LLM request", {
          systemMessage: [
            "Fix likely transcription errors in the NEW sentences.",
            "Split run-on sentences, improve punctuation, omit nervous repetitive filler, etc.",
            "Use CAPS where a speaker would put stress or emphasis.",
            "Use varying EMOJIS before each sentence, with spaces around—for visual interest.",
            "Respond ONLY with the edited new sentences.",
            "Use em dashes liberally, for a more interesting rhythm.",
            "You may also use parentheses and other punctuation to clarify.",
            "Only respond with the edited new sentences.",
          ].join(" "),
          messages: [{ role: "user", content }],
          temperature: 0.7,
          maxTokens: 500,
        })

        yield* wait("LLM done")
      }
    },

    *["LLM requests are made serially using GPT-4o."]() {
      for (;;) {
        const request = yield* wait("LLM request")
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
          { halt: ([tag]) => tag === "LLM request" },
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
              confidence: 1,
            })),
          )

        sentences = sentences.slice(orig.length)
        glaciers = [...glaciers, ...llmSentences]

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
        yield* post(
          "captured video image",
          captureVideoImage(document.querySelector("video")!),
        )
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

function captureVideoImage(video: HTMLVideoElement) {
  const canvas = document.createElement("canvas")
  const context = canvas.getContext("2d")
  if (!context) throw new Error("no canvas context")

  const maxDimension = 768
  const { videoWidth, videoHeight } = video
  const aspectRatio = videoWidth / videoHeight

  if (videoWidth > videoHeight) {
    canvas.width = maxDimension
    canvas.height = maxDimension / aspectRatio
  } else {
    canvas.width = maxDimension * aspectRatio
    canvas.height = maxDimension
  }

  context.drawImage(
    video,
    0,
    0,
    videoWidth,
    videoHeight,
    0,
    0,
    canvas.width,
    canvas.height,
  )

  return canvas.toDataURL("image/png")
}
