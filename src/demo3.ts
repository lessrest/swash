import { call, each, main, on, spawn } from "effection"
import { z } from "zod"
import { useWebSocket } from "./sock.ts"
import { Sync, sync, system } from "./sync2.ts"

import "@types/dom-view-transitions"

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

type Sign =
  | ["the socket is connected"]
  | ["some speech was heard", { text: string; isFinal: boolean }]
  | ["document edit requested", (document: Document) => void]

type TagName = Sign[0]

type Payload<T extends TagName> = Extract<Sign, [T, unknown]>[1]

function* once<T extends TagName>(
  tag: T,
  predicate?: (payload: Payload<T>) => boolean,
): Generator<Sync<Sign>, Payload<T>, Sign> {
  return (
    (yield sync<Sign>({
      want: (t) =>
        t[0] === tag && (!predicate || predicate(t[1] as Payload<T>)),
    })) as [T, Payload<T>]
  )[1]
}

const swash = system<Sign>(function* (rule, sync) {
  function* emit<T extends TagName>(tag: T, payload?: Payload<T>) {
    yield* rule(tag, function* () {
      yield sync({ post: [[tag, payload] as Sign] })
    })
  }

  function* post<T extends TagName>(tag: T, payload?: Payload<T>) {
    yield sync({ post: [[tag, payload] as Sign] })
  }

  function* rules(
    rules: Record<string, () => Generator<Sync<Sign>, void, Sign>>,
  ) {
    for (const [name, ruleBody] of Object.entries(rules)) {
      yield* rule(name, ruleBody)
    }
  }

  yield* rules({
    *["Document edits are applied as view transitions."]() {
      for (;;) {
        const thunk = yield* once("document edit requested")
        if (document.startViewTransition) {
          document.startViewTransition(() => thunk(document))
        } else {
          thunk(document)
        }
      }
    },

    *["Once the socket is connected, it is indicated visually."]() {
      yield* once("the socket is connected")
      yield* post("document edit requested", ({ body }) => {
        body.classList.add("ok")
        body.innerHTML += "<span><kbd></kbd><ins></ins></span>"
      })
    },

    *["As interim transcripts arrive, they are shown."]() {
      for (;;) {
        const { text } = yield* once(
          "some speech was heard",
          (x) => !x.isFinal,
        )
        yield* post("document edit requested", ({ body }) => {
          body.querySelector("ins")!.textContent = text
        })
      }
    },

    *["As final transcripts arrive, they are shown."]() {
      for (;;) {
        const { text } = yield* once(
          "some speech was heard",
          (x) => x.isFinal,
        )
        yield* post("document edit requested", ({ body }) => {
          body.querySelector("kbd")!.textContent += text
          body.querySelector("ins")!.textContent = ""
        })
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
      }/transcribe?lang=en`,
    ),
  )

  yield* spawn(function* () {
    yield* emit("the socket is connected")

    for (const { data } of yield* each(socket)) {
      if (typeof data === "string") {
        const json = JSON.parse(data)
        console.info(json)
        const payload = DeepgramResultSchema.parse(json)
        if (payload.channel && payload.channel.alternatives[0].transcript) {
          yield* emit("some speech was heard", {
            text: payload.channel.alternatives[0].transcript,
            isFinal: payload.is_final,
          })
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
