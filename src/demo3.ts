import {
  Stream,
  call,
  createSignal,
  each,
  main,
  on,
  resource,
  spawn,
} from "effection"
import { z } from "zod"
import { useWebSocket } from "./sock.ts"
import { Sync, sync, system } from "./sync2.ts"

import "@types/dom-view-transitions"
import { html } from "./html.ts"

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
  | ["animation frame begins", number]
  | ["request document mutation", (document: Document) => void]
  | ["transcription begins"]
  | ["conclusive phrase", { text: string }]
  | ["tentative phrase", { text: string }]

type TagName = Sign[0]

type Payload<T extends TagName> = Extract<Sign, [T, unknown]>[1]

function* want<T extends TagName>(
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

  function _byTag<T extends TagName>(tag: T) {
    return (x: Sign) => x[0] === tag
  }

  yield* spawn(function* () {
    for (const timestamp of yield* each(useAnimationFrames)) {
      yield* emit("animation frame begins", timestamp)
      yield* each.next()
    }
  })

  const mutationQueue: ((document: Document) => void)[] = []

  yield* rules({
    *["Document mutations are queued."]() {
      for (;;) {
        mutationQueue.push(yield* want("request document mutation"))
      }
    },

    *["View transitions are applied to the DOM document."]() {
      for (;;) {
        yield* want("request document mutation")
        yield* want("animation frame begins")
        document.startViewTransition(() => {
          for (const thunk of mutationQueue) {
            thunk(document)
          }
          mutationQueue.length = 0
        })
      }
    },
  })

  yield* rules({
    *["Events are logged."]() {
      for (;;) {
        const [tag, payload] = yield sync({ want: () => true })
        console.info(tag, payload)
      }
    },

    *["Once transcription begins, the transcription view is shown."]() {
      yield* want("transcription begins")
      yield* post("request document mutation", ({ body }) => {
        body.classList.add("ok")
        body.innerHTML += "<span><kbd></kbd> <ins></ins></span>"
      })
    },

    *["The latest tentative phrase is shown in the insertion buffer."]() {
      for (;;) {
        const { text } = yield* want("tentative phrase")
        yield* post("request document mutation", ({ body }) => {
          body.querySelector("ins")!.textContent = text
        })
      }
    },

    *["All conclusive phrases are shown in the transcript element."]() {
      for (;;) {
        const { text } = yield* want("conclusive phrase")
        yield* post("request document mutation", ({ body }) => {
          body.querySelector("kbd")!.append(html("span", {}, text), " ")
        })
      }
    },

    *["The insertion buffer is cleared when a conclusive phrase arrives."]() {
      for (;;) {
        yield* want("conclusive phrase")
        yield* post("request document mutation", ({ body }) => {
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
    yield* emit("transcription begins")

    for (const { data } of yield* each(socket)) {
      if (typeof data === "string") {
        const json = JSON.parse(data)
        console.info(json)
        const payload = DeepgramResultSchema.parse(json)
        if (payload.channel && payload.channel.alternatives[0].transcript) {
          if (payload.is_final) {
            yield* emit("conclusive phrase", {
              text: payload.channel.alternatives[0].transcript,
            })
          } else {
            yield* emit("tentative phrase", {
              text: payload.channel.alternatives[0].transcript,
            })
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
})

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
