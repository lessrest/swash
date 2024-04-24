import {
  Operation,
  Stream,
  Subscription,
  call,
  createChannel,
  each,
  main,
  on,
  race,
  resource,
  sleep,
  spawn,
  suspend,
} from "effection"

import { append, foreach, message, pushNode, setNode } from "./kernel.ts"

import { modelsByName, stream } from "./llm.ts"
import { tag } from "./tag.ts"
import { Epoch, info, task } from "./task.ts"
import { useWebSocket } from "./websocket.ts"

import GraphemeSplitter from "grapheme-splitter"

const splitter = new GraphemeSplitter()

function* readSpeech(
  phrases: Stream<WordHypothesis[], void>,
): Operation<WordHypothesis[]> {
  let spokenMessage: WordHypothesis[] = []

  yield* info("started reading at", new Date())
  for (const phrase of yield* each(phrases)) {
    spokenMessage = [...spokenMessage, ...phrase]
    const text = punctuatedConcatenation(phrase)
    yield* info("heard", {
      text,
    })
    if (phrase.every((w) => w.word === "over")) {
      break
    } else {
      yield* info("inserting")
      yield* insertGraphemesSlowly(text)
      yield* info("insertied")

      // if text doesn't end with punctuation, add an em dash
      if (!text.match(/[.!?]$/)) {
        yield* append("— ")
      } else {
        yield* append(" ")
      }

      yield* info("reading next phrase")

      yield* each.next()
    }
  }

  return spokenMessage
}

function* insertGraphemesSlowly(text: string) {
  const graphemes = splitter.splitGraphemes(text)
  for (const g of graphemes) {
    yield* append(g)
    if (g.match(/[.!?]/)) {
      yield* sleep(700)
    } else if (g === ",") {
      yield* sleep(400)
    } else {
      yield* sleep(20 + Math.random() * 10)
    }
  }
}

function* dialogue(
  article: HTMLElement,
  phrases: Stream<WordHypothesis[], void>,
): Operation<void> {
  yield* info("has document", article)

  for (;;) {
    {
      yield* setNode(article)
      const p = yield* pushNode(tag("p", { class: "user speaking" }))
      const speech = yield* readSpeech(phrases)
      p.classList.remove("speaking")
      const text = punctuatedConcatenation(speech)
      yield* info("interlocutor said", {
        text,
      })
      if (plainConcatenation(speech) === "over and out") {
        return
      }
    }

    let messages: { role: "user" | "assistant"; content: string }[] = []
    for (const paragraph of [...article.querySelectorAll("p")]) {
      yield* info("has paragraph", paragraph)
      const content = paragraph.innerHTML
      const role = paragraph.classList.contains("user") ? "user" : "assistant"
      messages = [...messages, { role, content }]
    }

    {
      yield* setNode(article)
      const p = yield* pushNode(tag("p", { class: "assistant speaking" }))
      const response = yield* useChatResponse(messages)
      let next = yield* response.next()
      while (!next.done && next.value) {
        const { content } = next.value
        const phraseSubscription = yield* phrases

        const result = yield* race([
          insertGraphemesSlowly(content),
          phraseSubscription.next(),
        ])

        if (result) {
          p.innerText += "—[interruption]"
          break
        }

        next = yield* response.next()
      }
      p.classList.remove("speaking")
    }
  }
}

function punctuatedConcatenation(speech: WordHypothesis[]) {
  return speech.map(({ punctuated_word }) => punctuated_word).join(" ")
}

function plainConcatenation(speech: WordHypothesis[]) {
  return speech.map(({ word }) => word).join(" ")
}

function useChatResponse(
  messages: { role: "user" | "assistant"; content: string }[],
): Operation<Subscription<{ content: string }, void>> {
  return stream(modelsByName["Claude III Opus"], {
    temperature: 0.6,
    maxTokens: 1024,
    systemMessage:
      "Help the user formulate their thoughts. Be concise. Just rephrase, clarify, summarize.",
    messages,
  })
}

const recorderOptions = // check if we can do webm/opus
  MediaRecorder.isTypeSupported("audio/webm;codecs=opus")
    ? { mimeType: "audio/webm;codecs=opus", audioBitsPerSecond: 64000 }
    : { mimeType: "audio/mp4", audioBitsPerSecond: 128000 }

function* recordingSession(stream: MediaStream): Operation<void> {
  const audioStream = new MediaStream(stream.getAudioTracks())
  const language = "en"

  const interimChannel = createChannel<WordHypothesis[]>()
  const phraseChannel = createChannel<WordHypothesis[]>()

  let i = 1
  for (;;) {
    yield* Epoch.set(new Date())
    const task1 = yield* task(`transcription session ${i++}`, function* () {
      yield* task("a dialogue", function* () {
        const article = yield* pushNode(tag("article"))
        yield* dialogue(article, phraseChannel)
      })

      const socket = yield* useWebSocket(
        new WebSocket(`${getWebSocketUrl()}/transcribe?language=${language}`),
      )

      yield* info("specifies language with code", language)
      yield* info("established service connection at", new Date())

      const recorder = yield* useMediaRecorder(audioStream, recorderOptions)
      recorder.start(300)

      yield* info("started recording at", new Date())
      yield* info("has audio format", recorderOptions.mimeType)
      yield* info("has audio bitrate", recorderOptions.audioBitsPerSecond)
      yield* info("has audio timeslice", "300ms")

      const blobs: Blob[] = []

      yield* spawn(function* () {
        yield* foreach(on(recorder, "dataavailable"), function* ({ data }) {
          blobs.push(data)
          try {
            yield* socket.send(data)
          } catch (error) {
            yield* info("error sending data", error)
          }
        })
      })

      yield* task("socket reader", function* () {
        for (const event of yield* each(socket)) {
          const data = JSON.parse(event.data)
          yield* info("received data", data)
          if (data.type === "Results" && data.channel) {
            const {
              alternatives: [{ transcript, words }],
            } = data.channel
            if (data.is_final) {
              if (transcript) {
                yield* info("final phrase", transcript)
                yield* phraseChannel.send(words)
              }
              yield* interimChannel.send([])
            } else if (transcript) {
              yield* interimChannel.send(words)
            }
          }
          yield* each.next()
        }
      })

      yield* suspend()
    })

    yield* task1
  }
}

function* app() {
  yield* task("swa.sh user agent", function* () {
    yield* pushNode(tag("app"))

    const stream = yield* useMediaStream({ audio: true, video: true })

    for (;;) {
      yield* recordingSession(stream)
    }
  })

  yield* suspend()
}

document.addEventListener("DOMContentLoaded", async function () {
  await main(app)
})
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

export interface WordHypothesis {
  word: string
  punctuated_word: string
  confidence: number
  start: number
  end: number
}

export function* transcribe(
  blobs: Blob[],
  language: string = "en",
): Operation<{ words: WordHypothesis[] }> {
  const formData = new FormData()
  formData.append("file", new Blob(blobs))

  const response = yield* call(
    fetch(`/whisper-deepgram?language=${language}`, {
      method: "POST",
      body: formData,
    }),
  )

  if (!response.ok) {
    throw new Error(`HTTP error! status: ${response.status}`)
  }

  const result = yield* call(response.json())
  console.log(result)
  return result.results.channels[0].alternatives[0]
}
