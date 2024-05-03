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
  suspend,
} from "effection"

import { append, appendNewTarget, foreach, message } from "./kernel.ts"

import { speechInput } from "./speechInput.ts"
import { tag } from "./tag.ts"
import { Epoch, info, task } from "./task.ts"
import { telegramClient } from "./telegramClient.ts"
import { SpokenWord } from "./transcription.ts"
import { WebSocketHandle, useWebSocket } from "./websocket.ts"

const recorderOptions: MediaRecorderOptions = MediaRecorder.isTypeSupported(
  "audio/webm;codecs=opus",
)
  ? { mimeType: "audio/webm;codecs=opus", audioBitsPerSecond: 128000 }
  : { mimeType: "audio/mp4", audioBitsPerSecond: 128000 }

const videoFeatureFlag = document.location.hash.includes("video")

function* app() {
  yield* task("swa.sh user agent", function* () {
    yield* appendNewTarget(tag("app"))

    const stream = yield* useMediaStream({
      audio: true,
      video: false,
    })
    const saveChannel: Channel<string, void> = createChannel<string>()

    if (location.hash.includes("telegram")) {
      yield* telegramClient(saveChannel)
    }

    if (videoFeatureFlag) {
      const videoElement = tag<HTMLVideoElement>("video")
      videoElement.srcObject = yield* useMediaStream({
        audio: false,
        video: true,
      })
      yield* append(videoElement)
      videoElement.play()
    }

    yield* recordingSession(stream, saveChannel)
  })

  yield* suspend()
}

document.addEventListener("DOMContentLoaded", async function () {
  await main(app)
})

const ctxAudioStream = createContext<MediaStream>("audioStream")

function* recordingSession(
  stream: MediaStream,
  saveChannel: Channel<string, void>,
): Operation<void> {
  const audioStream = new MediaStream(stream.getAudioTracks())
  yield* ctxAudioStream.set(audioStream)

  const language = "en"

  const interimChannel = createChannel<SpokenWord[]>()
  const finalChannel = createChannel<SpokenWord[]>()

  yield* Epoch.set(new Date())

  const socket: WebSocketHandle = yield* useWebSocket(
    new WebSocket(`${getWebSocketUrl()}/transcribe?language=${language}`),
  )

  const recorder = yield* useMediaRecorder(audioStream, recorderOptions)

  yield* task("audio packet transmitter", function* () {
    yield* foreach(on(recorder, "dataavailable"), function* ({ data }) {
      try {
        yield* socket.send(data)
      } catch (error) {
        yield* info("error sending data", error)
        throw error
      }
    })
  })

  recorder.start(100)

  yield* info("specifies language with code", language)
  yield* info("has audio format", recorderOptions.mimeType)

  yield* task("socket reader", function* () {
    for (const event of yield* each(socket)) {
      const data = JSON.parse(event.data)

      if (data.type === "Results" && data.channel) {
        const {
          alternatives: [{ transcript, words }],
        } = data.channel
        if (data.is_final) {
          yield* interimChannel.send([])
          if (transcript) {
            yield* info("received final transcript", transcript)
            yield* finalChannel.send(words)
          }
        } else if (transcript) {
          yield* interimChannel.send(words)
        }
      }

      yield* each.next()
    }
  })

  yield* task("speech input", function* () {
    yield* appendNewTarget(tag("article"))
    for (;;) {
      yield* call(function* () {
        const text = yield* speechInput(interimChannel, finalChannel)
        yield* saveChannel.send(text)
      })
    }
  })

  yield* suspend()
}

export function punctuatedConcatenation(speech: SpokenWord[]) {
  return speech.map(({ punctuated_word }) => punctuated_word).join(" ")
}

export function paragraphsToText(x: {
  paragraphs: {
    sentences: {
      text: string
    }[]
  }[]
}) {
  return x.paragraphs
    .map(({ sentences }) => sentences.map(({ text }) => text).join(" "))
    .join("\n\n")
}

export function plainConcatenation(speech: SpokenWord[]) {
  return speech.map(({ word }) => word).join(" ")
}

function getWebSocketUrl(): string {
  return `${document.location.protocol === "https:" ? "wss:" : "ws:"}//${
    document.location.host
  }`
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
        yield* message(`stopping ${track.kind}`)
        track.stop()
      }
    }
  })
}

export const nbsp = String.fromCharCode(160)

export function* useAudioRecorder() {
  const audioStream = yield* ctxAudioStream.get()
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

export function innerTextWithBr(element: HTMLElement): string {
  return [...element.childNodes]
    .map((child) =>
      child.nodeType === Node.TEXT_NODE
        ? child.textContent
        : child.nodeName === "BR"
        ? "\n"
        : "",
    )
    .join("")
}
