import {
  Channel,
  Operation,
  call,
  createChannel,
  createContext,
  each,
  main,
  on,
  resource,
  sleep,
  suspend,
} from "effection"

import { grow, into, nest, pull } from "./nest.ts"

import { tele } from "./chat.ts"
import { dank } from "./dank.ts"
import { html } from "./html.ts"
import { SocketConnection, useWebSocket } from "./sock.ts"
import { talk } from "./talk.ts"
import { dawn, info, task } from "./task.ts"
import { Word } from "./text.ts"

const recorderOptions: MediaRecorderOptions = MediaRecorder.isTypeSupported(
  "audio/webm;codecs=opus",
)
  ? { mimeType: "audio/webm;codecs=opus", audioBitsPerSecond: 128000 }
  : { mimeType: "audio/mp4", audioBitsPerSecond: 128000 }

const videoFeatureFlag = false // document.location.hash.includes("video")
const dankFeatureFlag = document.location.hash.includes("dank")

function* demo() {
  yield* dawn()
  yield* into(document.body)
  yield* task("swa.sh", function* () {
    const stream = yield* useMediaStream({
      audio: true,
      video: false,
    })
    const saveChannel: Channel<string, void> = createChannel<string>()

    if (location.hash.includes("telegram")) {
      yield* tele(saveChannel)
    }

    if (videoFeatureFlag) {
      const videoElement = html<HTMLVideoElement>("video")
      videoElement.srcObject = yield* useMediaStream({
        audio: false,
        video: true,
      })
      yield* grow(videoElement)
      videoElement.play()
    }

    yield* sesh(stream, saveChannel)
  })

  yield* suspend()
}

document.addEventListener("DOMContentLoaded", async function () {
  await main(dankFeatureFlag ? dank : demo)
})

const Cast = createContext<MediaStream>("audioStream")
const lang = "en"

function* sesh(
  src: MediaStream,
  saveChannel: Channel<string, void>,
): Operation<void> {
  yield* dawn()

  const cast = yield* Cast.set(new MediaStream(src.getAudioTracks()))
  const saas: SocketConnection = yield* useWebSocket(dial(lang))

  const flux = createChannel<Word[]>()
  const firm = createChannel<Word[]>()

  const tape = yield* useMediaRecorder(cast, recorderOptions)

  yield* task("audio transmitter", function* () {
    yield* pull(on(tape, "dataavailable"), function* ({ data }) {
      yield* saas.send(data)
    })
  })

  tape.start(100)

  yield* info("specifies language with code", lang)
  yield* info("has audio format", recorderOptions.mimeType)

  yield* task("transcript receiver", function* () {
    for (const event of yield* each(saas)) {
      const data = JSON.parse(event.data)

      if (data.type === "Results" && data.channel) {
        const {
          alternatives: [{ transcript, words }],
        } = data.channel
        if (data.is_final) {
          yield* flux.send([])
          if (transcript) {
            yield* info("firm", transcript)
            yield* firm.send(words)
          }
        } else if (transcript) {
          yield* flux.send(words)
        }
      }

      yield* each.next()
    }
  })

  yield* task("talk loop", function* () {
    yield* nest(html("article"))
    let i = 1
    for (;;) {
      try {
        yield* talk(flux, firm, function* (x) {
          yield* saveChannel.send(x)
        })
      } catch (error) {
        yield* info("error in speech input", error)
        yield* sleep(500)
      }
    }
  })

  yield* suspend()
}

function dial(lang: string): WebSocket {
  return new WebSocket(
    `${document.location.protocol === "https:" ? "wss:" : "ws:"}//${
      document.location.host
    }/transcribe?lang=${lang}`,
  )
}

export function useMediaRecorder(
  stream: MediaStream,
  options: MediaRecorderOptions,
): Operation<MediaRecorder> {
  return resource(function* (provide) {
    const recorder = new MediaRecorder(stream, options)
    try {
      yield* provide(recorder)
    } finally {
      if (recorder.state !== "inactive") {
        recorder.stop()
      }
    }
  })
}

export function useMediaStream(
  constraints: MediaStreamConstraints,
): Operation<MediaStream> {
  return resource(function* (provide) {
    const stream: MediaStream = yield* call(
      navigator.mediaDevices.getUserMedia(constraints),
    )
    try {
      yield* provide(stream)
    } finally {
      for (const track of stream.getTracks()) {
        track.stop()
      }
    }
  })
}

export const nbsp = String.fromCharCode(160)

export function* useAudioRecorder() {
  const audioStream = yield* Cast.get()
  if (!audioStream) {
    throw new Error("No audio stream")
  }

  const blobs: Blob[] = []
  const recorder = yield* useMediaRecorder(audioStream, recorderOptions)
  recorder.start(100)

  recorder.ondataavailable = (event) => {
    blobs.push(event.data)
  }

  return blobs
}
