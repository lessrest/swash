import { call, each, main, on, spawn } from "effection"
import { useWebSocket } from "./sock.ts"
import { Sync, sync, system } from "./sync2.ts"

interface TranscriptionResultMessage {
  type: string
  channel: {
    alternatives: { transcript: string }[]
  }
  is_final: boolean
}

enum Tag {
  SocketConnected = "SocketConnected",
  DeepgramResult = "DeepgramResult",
  Transcript = "Transcript",
}

type Sign =
  | { tag: Tag.SocketConnected }
  | { tag: Tag.DeepgramResult; message: TranscriptionResultMessage }
  | { tag: Tag.Transcript; transcript: string; final: boolean }

function* waitFor<T extends Tag>(
  tag: T,
): Generator<Sync<Sign>, Extract<Sign, { tag: T }>, Sign> {
  const sign = yield sync<Sign>({ want: (t) => t.tag === tag })
  return sign as Extract<Sign, { tag: T }>
}

const swash = system<Sign>(function* (thread) {
  function* emit<T extends Sign>(sign: T) {
    yield* thread("emit", function* () {
      yield sync<Sign>({ post: [sign] })
    })
  }

  yield* thread("splash", function* () {
    yield* waitFor(Tag.SocketConnected)
    document.body.classList.add("ok")
  })

  yield* thread("logger", function* () {
    for (;;) {
      const sign = yield sync<Sign>({
        want: (t) => t.tag !== Tag.DeepgramResult,
      })
      console.info(sign)
    }
  })

  yield* thread("render", function* () {
    let text = ""
    for (;;) {
      const { transcript, final } = yield* waitFor(Tag.Transcript)
      document.body.textContent = text + transcript + " "
      if (final) {
        text += transcript + " "
      }
    }
  })

  yield* thread("parse transcript message", function* () {
    for (;;) {
      const { message } = yield* waitFor(Tag.DeepgramResult)

      if (message.type === "Results" && message.channel) {
        const {
          alternatives: [{ transcript }],
        } = message.channel
        if (transcript) {
          yield sync<Sign>({
            post: [
              {
                tag: Tag.Transcript,
                transcript,
                final: message.is_final,
              },
            ],
          })
        }
      }
    }
  })

  const conn = yield* useWebSocket(
    new WebSocket(
      `${document.location.protocol === "https:" ? "wss:" : "ws:"}//${
        document.location.host
      }/transcribe?lang=en`,
    ),
  )

  const mediaStream = yield* call(
    navigator.mediaDevices.getUserMedia({ audio: true, video: false }),
  )

  const recorder = new MediaRecorder(mediaStream, {
    mimeType: "audio/webm;codecs=opus",
    audioBitsPerSecond: 64000,
  })

  yield* spawn(function* () {
    yield* emit({ tag: Tag.SocketConnected })

    for (const { data } of yield* each(conn)) {
      if (typeof data === "string") {
        yield* emit({
          tag: Tag.DeepgramResult,
          message: JSON.parse(data),
        })
      } else {
        throw new Error("unexpected message type")
      }

      yield* each.next()
    }
  })

  return yield* spawn(function* () {
    recorder.start(500)

    for (const chunk of yield* each(on(recorder, "dataavailable"))) {
      yield* conn.send(chunk.data)
      yield* each.next()
    }
  })
})

export async function swash3() {
  await main(() => swash)
}
