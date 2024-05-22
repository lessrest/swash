import { call, each, main, on, spawn } from "effection"
import { rent } from "./sock.ts"
import { sync, system } from "./sync2.ts"

type Sign =
  | { tag: "socket connected" }
  | {
      tag: "transcription result"
      message: {
        type: string
        channel: {
          alternatives: { transcript: string; words: string }[]
        }
        is_final: boolean
      }
    }
  | { tag: "transcript"; transcript: string; final: boolean }
  | { tag: "blob"; blob: Blob }

function* syncEvent<T extends Sign>(tag: T["tag"]) {
  return (yield sync<Sign>({ want: (t) => t.tag === tag })) as Extract<
    Sign,
    { tag: T["tag"] }
  >
}

export async function demo3() {
  await main(() =>
    system<Sign>(function* (thread) {
      yield* thread({
        name: "splash",
        prio: 1,
        init: function* () {
          yield sync({ want: (t) => t.tag === "socket connected" })
          document.body.classList.add("ok")
        },
      })

      yield* thread({
        name: "logger",
        prio: 0,
        init: function* () {
          for (;;) {
            const sign = yield sync<Sign>({
              want: (t) =>
                t.tag !== "blob" && t.tag !== "transcription result",
            })
            console.info(sign)
          }
        },
      })

      yield* thread({
        name: "render",
        prio: 0,
        init: function* () {
          let transcript = ""
          for (;;) {
            const { transcript: partialTranscript, final } =
              yield* syncEvent<{
                tag: "transcript"
                transcript: string
                final: boolean
              }>("transcript")
            document.body.textContent = transcript + partialTranscript + " "
            if (final) {
              transcript += partialTranscript + " "
            }
          }
        },
      })

      yield* thread({
        name: "parse transcript message",
        prio: 0,
        init: function* () {
          for (;;) {
            const { message: transcriptionData } = yield* syncEvent<{
              tag: "transcription result"
              message: {
                type: string
                channel: {
                  alternatives: { transcript: string; words: string }[]
                }
                is_final: boolean
              }
            }>("transcription result")

            if (
              transcriptionData.type === "Results" &&
              transcriptionData.channel
            ) {
              const {
                alternatives: [{ transcript }],
              } = transcriptionData.channel
              if (transcript) {
                yield sync<Sign>({
                  post: [
                    {
                      tag: "transcript",
                      transcript,
                      final: transcriptionData.is_final,
                    },
                  ],
                })
              }
            }
          }
        },
      })

      const conn = yield* rent(
        new WebSocket(
          `${document.location.protocol === "https:" ? "wss:" : "ws:"}//${
            document.location.host
          }/transcribe?lang=en`,
        ),
      )

      yield* spawn(function* () {
        const mediaStream = yield* call(
          navigator.mediaDevices.getUserMedia({ audio: true, video: false }),
        )

        const recorder = new MediaRecorder(mediaStream, {
          mimeType: "audio/webm;codecs=opus",
          audioBitsPerSecond: 64000,
        })

        recorder.start(500)

        for (const chunk of yield* each(on(recorder, "dataavailable"))) {
          yield* conn.send(chunk.data)
          yield* thread({
            name: "audio recv",
            prio: 0,
            init: function* () {
              yield sync<Sign>({ post: [{ tag: "blob", blob: chunk.data }] })
            },
          })
          yield* each.next()
        }
      })

      return yield* spawn(function* () {
        yield* thread({
          name: "socket",
          prio: 0,
          init: function* () {
            yield sync<Sign>({ post: [{ tag: "socket connected" }] })
          },
        })

        for (const msg of yield* each(conn)) {
          yield* thread({
            name: "socket recv",
            prio: 0,
            init: function* () {
              if (typeof msg.data === "string") {
                yield sync<Sign>({
                  post: [
                    {
                      tag: "transcription result",
                      message: JSON.parse(msg.data),
                    },
                  ],
                })
              } else {
                throw new Error("unexpected message type")
              }
            },
          })
          yield* each.next()
        }
      })
    }),
  )
}
