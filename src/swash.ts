import {
  Channel,
  Operation,
  Stream,
  call,
  createChannel,
  createContext,
  each,
  main,
  on,
  resource,
  sleep,
  suspend,
  useAbortSignal,
} from "effection"

import {
  append,
  appendNewTarget,
  foreach,
  message,
  replaceChildren,
  useClassName,
} from "./kernel.ts"

import { graphemesOf } from "./graphemes.ts"
import { tag } from "./tag.ts"
import { Epoch, info, task } from "./task.ts"
import { telegramClient } from "./telegramClient.ts"
import { WebSocketHandle, useWebSocket } from "./websocket.ts"

export interface SpokenWord {
  word: string
  punctuated_word: string
  confidence: number
  start: number
  end: number
}

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

function punctuatedConcatenation(speech: SpokenWord[]) {
  return speech.map(({ punctuated_word }) => punctuated_word).join(" ")
}

function paragraphsToText(x: {
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

function plainConcatenation(speech: SpokenWord[]) {
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

export function* transcribe(
  blobs: Blob[],
  language: string = "en",
): Operation<{
  words: SpokenWord[]
  paragraphs: {
    paragraphs: {
      sentences: {
        text: string
        start: number
        end: number
      }[]
    }[]
  }
}> {
  const formData = new FormData()
  formData.append("file", new Blob(blobs))

  const response = yield* call(
    fetch(`/whisper-deepgram?language=${language}`, {
      method: "POST",
      body: formData,
      signal: yield* useAbortSignal(),
    }),
  )

  if (!response.ok) {
    throw new Error(`HTTP error! status: ${response.status}`)
  }

  const result = yield* call(response.json())
  console.log(result)
  return result.results.channels[0].alternatives[0]
}

const nbsp = String.fromCharCode(160)

function* speechInput(
  interimStream: Stream<SpokenWord[], void>,
  finalStream: Stream<SpokenWord[], void>,
): Operation<string> {
  const root = yield* appendNewTarget(
    tag("ins.user", {
      style: {
        textDecoration: "none",
      },
    }),
  )

  yield* useClassName("listening")

  yield* task("recorder", function* () {
    const blobs: Blob[] = yield* useAudioRecorder()

    for (;;) {
      yield* (yield* finalStream).next()
      try {
        const result = yield* transcribe(blobs, "en")
        root.querySelector<HTMLElement>(".final")!.innerText =
          paragraphsToText(result.paragraphs) + " "
      } catch (error) {
        yield* info("error transcribing", error)
      }
    }
  })

  let done = false

  const typingAnimationTask = yield* task("typing animation", function* () {
    yield* appendNewTarget(
      tag(
        "p",
        {
          style: {
            whiteSpace: "pre-wrap",
          },
        },
        nbsp,
      ),
    )

    let limit = 0
    for (;;) {
      const spans = [
        ...root.querySelectorAll<HTMLElement>(".final, .interim"),
      ]
      const text = spans
        .map((element) =>
          [...element.childNodes]
            .map((child) =>
              child.nodeType === Node.TEXT_NODE
                ? child.textContent
                : child.nodeName === "BR"
                ? "\n"
                : "",
            )
            .join(""),
        )
        .join("")
      const graphemes = graphemesOf(text)
      const remaining = graphemes.length - limit
      const textToShow = graphemes.slice(0, limit).join("")

      if (done && remaining <= 0) {
        const finalSpan = root.querySelector<HTMLElement>(".final")
        if (finalSpan) {
          yield* replaceChildren(...finalSpan.children)
        }
        break
      } else {
        if (textToShow !== text) {
          yield* info("replacing text", { textToShow, text })
          yield* replaceChildren(textToShow)
        }
        const graphemesPerSecond = 20 + 30 * Math.exp(-remaining / 10)
        yield* sleep(1000 / graphemesPerSecond)
        limit = Math.min(graphemes.length, limit + 1)
      }
    }
  })

  const finalTask = yield* task("final", function* () {
    yield* appendNewTarget(tag("span.final"))

    yield* foreach(finalStream, function* (phrase) {
      if (plainConcatenation(phrase) === "over") {
        return "stop"
      }
      yield* append(punctuatedConcatenation(phrase) + " ")
    })
  })

  yield* task("interim", function* () {
    const span = yield* appendNewTarget(tag("span.interim"))

    try {
      yield* foreach(interimStream, function* (phrase) {
        yield* replaceChildren(punctuatedConcatenation(phrase))
      })
    } finally {
      span.remove()
    }
  })

  yield* finalTask
  done = true
  yield* typingAnimationTask

  return root.querySelector("p")!.innerText
}

function* useAudioRecorder() {
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
