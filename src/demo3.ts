import { call, each, main, on, spawn } from "effection"
import { z } from "zod"
import { useWebSocket } from "./sock.ts"
import { Sync, sync, system } from "./sync2.ts"

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
            start: z.number(),
            end: z.number(),
            confidence: z.number(),
          }),
        ),
      }),
    ),
  }),
})

type DeepgramPayload = z.infer<typeof DeepgramResultSchema>

type Sign =
  | ["SocketConnected"]
  | ["DeepgramMessage", DeepgramPayload]
  | ["Transcript", { text: string; finality: boolean }]

type TagName = Sign[0]

type Payload<T extends TagName> = Extract<Sign, [T, unknown]>[1]

function* want<T extends TagName>(
  tag: T,
): Generator<Sync<Sign>, Payload<T>, Sign> {
  return (
    (yield sync<Sign>({ want: (t) => t[0] === tag })) as [T, Payload<T>]
  )[1]
}

const swash = system<Sign>(function* (rule, sync) {
  function* emit<T extends TagName>(tag: T, payload?: Payload<T>) {
    yield* rule(tag, function* () {
      yield sync({ post: [[tag, payload] as Sign] })
    })
  }

  yield* rule("splash", function* () {
    yield* want("SocketConnected")
    document.body.classList.add("ok")
  })

  yield* rule("logger", function* () {
    for (;;) {
      const sign = yield sync({
        want: ([tag, _]) => tag !== "DeepgramMessage",
      })
      console.info(sign)
    }
  })

  yield* rule("render", function* () {
    let sum = ""
    for (;;) {
      const { text, finality } = yield* want("Transcript")
      document.body.textContent = sum + text + " "
      if (finality) {
        sum += text + " "
      }
    }
  })

  yield* rule("parse transcript message", function* () {
    for (;;) {
      const { type, channel, is_final } = yield* want("DeepgramMessage")

      if (type === "Results" && channel) {
        const {
          alternatives: [{ transcript }],
        } = channel
        if (transcript) {
          yield sync({
            post: [["Transcript", { text: transcript, finality: is_final }]],
          })
        }
      }
    }
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
      }/transcribe?lang=en`,
    ),
  )

  yield* spawn(function* () {
    yield* emit("SocketConnected")

    for (const { data } of yield* each(socket)) {
      if (typeof data === "string") {
        const json = JSON.parse(data)
        console.info(json)
        const payload = DeepgramResultSchema.parse(json)
        yield* emit("DeepgramMessage", payload)
      } else {
        throw new Error("unexpected message type")
      }

      yield* each.next()
    }
  })

  return yield* spawn(function* () {
    recorder.start(500)

    for (const chunk of yield* each(on(recorder, "dataavailable"))) {
      yield* socket.send(chunk.data)
      yield* each.next()
    }
  })
})

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
