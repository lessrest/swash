import { call, each, main, on, spawn } from "effection"
import { useWebSocket } from "./sock.ts"
import { sync, system } from "./sync2.ts"

interface TranscriptionResultMessage {
  type: string
  channel: {
    alternatives: { transcript: string }[]
  }
  is_final: boolean
}

const SocketConnected = Symbol("SocketConnected")
const DeepgramResult = Symbol("DeepgramResult")
const Transcript = Symbol("Transcript")
const AudioBlob = Symbol("AudioBlob")

type Sign =
  | { tag: typeof SocketConnected }
  | { tag: typeof DeepgramResult; message: TranscriptionResultMessage }
  | { tag: typeof Transcript; transcript: string; final: boolean }
  | { tag: typeof AudioBlob; blob: globalThis.Blob }

function* waitFor<T extends Sign>(tag: T["tag"]) {
  return (yield sync<Sign>({ want: (t) => t.tag === tag })) as Extract<
    Sign,
    { tag: T["tag"] }
  >
}

export async function demo3() {
  await main(() =>
    system<Sign>(function* (thread) {
      function* emit<T extends Sign>(sign: T) {
        yield* thread("emit", function* () {
          yield sync<Sign>({ post: [sign] })
        })
      }

      yield* thread("splash", function* () {
        yield* waitFor(SocketConnected)
        document.body.classList.add("ok")
      })

      yield* thread({
        name: "logger",
        prio: 0,
        init: function* () {
          for (;;) {
            const sign = yield sync<Sign>({
              want: (t) => t.tag !== AudioBlob && t.tag !== DeepgramResult,
            })
            console.info(sign)
          }
        },
      })

      yield* thread("render", function* () {
        let transcript = ""
        for (;;) {
          const { transcript: partialTranscript, final } = yield* waitFor<{
            tag: typeof Transcript
            transcript: string
            final: boolean
          }>(Transcript)
          document.body.textContent = transcript + partialTranscript + " "
          if (final) {
            transcript += partialTranscript + " "
          }
        }
      })

      yield* thread("parse transcript message", function* () {
        for (;;) {
          const { message } = yield* waitFor<{
            tag: typeof DeepgramResult
            message: {
              type: string
              channel: {
                alternatives: { transcript: string }[]
              }
              is_final: boolean
            }
          }>(DeepgramResult)

          if (message.type === "Results" && message.channel) {
            const {
              alternatives: [{ transcript }],
            } = message.channel
            if (transcript) {
              yield sync<Sign>({
                post: [
                  {
                    tag: Transcript,
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
        yield* emit({ tag: SocketConnected })

        for (const msg of yield* each(conn)) {
          if (typeof msg.data === "string") {
            yield* emit({
              tag: DeepgramResult,
              message: JSON.parse(msg.data),
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
          yield* emit({ tag: AudioBlob, blob: chunk.data })
          yield* each.next()
        }
      })
    }),
  )
}
