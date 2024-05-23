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
} from "effection"

type Sign =
  | ["animation frame began", number]
  | ["document mutation requested", (document: Document) => void]
  | ["live transcription began"]
  | ["phrase heard conclusively", Word[]]
  | ["phrase heard tentatively", Word[]]

type TagName = Sign[0]
type Payload<T extends TagName> = Extract<Sign, [T, unknown]>[1]

function* wait<T extends TagName>(
  tag: T,
  predicate?: (payload: Payload<T>) => boolean,
): Generator<Sync<Sign>, Payload<T>, Sign> {
  return (
    (yield sync<Sign>({
      wait: (t) =>
        t[0] === tag && (!predicate || predicate(t[1] as Payload<T>)),
    })) as [T, Payload<T>]
  )[1]
}

const swash = system<Sign>(function* (rule, sync) {
  yield* spawn(function* () {
    for (const timestamp of yield* each(useAnimationFrames)) {
      if (mutationQueue.length > 0) {
        yield* emit("animation frame began", timestamp)
      }
      yield* each.next()
    }
  })

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
        applyMutationQueue(mutationQueue)
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
        body.innerHTML +=
          "<p><span class=transcript></span> <span class=insertion></span></p>"
      })
    },

    *["The latest tentative phrase is shown in the insertion buffer."]() {
      for (;;) {
        const words = yield* wait("phrase heard tentatively")
        yield* post("document mutation requested", ({ body }) => {
          body
            .querySelector(".insertion")!
            .replaceChildren(
              ...words.map(({ word }) => html("span.word", {}, word)),
            )
        })
      }
    },

    *["All conclusive phrases are shown in the transcript element."]() {
      for (;;) {
        const words = yield* wait("phrase heard conclusively")
        yield* post("document mutation requested", ({ body }) => {
          body
            .querySelector(".transcript")!
            .append(...words.map(({ word }) => html("span.word", {}, word)))
        })
      }
    },

    *["The insertion buffer is cleared when a conclusive phrase arrives."]() {
      for (;;) {
        yield* wait("phrase heard conclusively")
        yield* post("document mutation requested", ({ body }) => {
          body.querySelector(".insertion")!.replaceChildren()
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
  document.startViewTransition(() => {
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
