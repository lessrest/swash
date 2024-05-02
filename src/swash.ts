import {
  Channel,
  Operation,
  Stream,
  Subscription,
  call,
  createChannel,
  createContext,
  createSignal,
  each,
  ensure,
  main,
  on,
  race,
  resource,
  sleep,
  spawn,
  suspend,
} from "effection"

import {
  append,
  clear,
  foreach,
  getTarget,
  message,
  pushNode,
  scrollToBottom,
  setNode,
} from "./kernel.ts"

import { ChatMessage, modelsByName, stream } from "./llm.ts"
import { tag } from "./tag.ts"
import { Epoch, info, task } from "./task.ts"
import { useWebSocket } from "./websocket.ts"

import GraphemeSplitter from "grapheme-splitter"
import { TelegramService } from "./telegram-service.ts"

const recorderOptions: MediaRecorderOptions = MediaRecorder.isTypeSupported(
  "audio/webm;codecs=opus",
)
  ? { mimeType: "audio/webm;codecs=opus", audioBitsPerSecond: 64000 }
  : { mimeType: "audio/mp4", audioBitsPerSecond: 128000 }

function* app() {
  yield* task("swa.sh user agent", function* () {
    yield* pushNode(tag("app"))

    const stream = yield* useMediaStream({ audio: true, video: false })
    const saveChannel: Channel<string, void> = createChannel<string>()

    if (location.hash.includes("telegram")) {
      yield* telegramClient(saveChannel)
    }

    for (;;) {
      yield* recordingSession(stream, saveChannel)
    }
  })

  yield* suspend()
}

const splitter = new GraphemeSplitter()

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

  const interimChannel = createChannel<WordHypothesis[]>()
  const phraseChannel = createChannel<WordHypothesis[]>()

  let i = 1
  for (;;) {
    yield* Epoch.set(new Date())
    const task1 = yield* task(`transcription session ${i++}`, function* () {
      yield* task("a dialogue", function* () {
        const article = yield* pushNode(tag("article"))
        yield* dialogue(article, phraseChannel, saveChannel)
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

      yield* spawn(function* () {
        yield* foreach(on(recorder, "dataavailable"), function* ({ data }) {
          try {
            yield* socket.send(data)
          } catch (error) {
            yield* info("error sending data", error)
            throw error
          }
        })
      })

      yield* task("socket reader", function* () {
        // Read incoming JSON events from the transcription service.
        for (const event of yield* each(socket)) {
          const data = JSON.parse(event.data)

          // Does the event have new transcription results?
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

// This is like modal editing, a subprocess for entering a section of speech.
// When the user says "over", it returns with its full transcript, which it
// has also inserted into the current target.
function* readSpeech(
  subscription: Subscription<WordHypothesis[], void>,
  quick: boolean = false,
): Operation<string> {
  const audioStream = yield* ctxAudioStream.get()
  if (!audioStream) {
    throw new Error("No audio stream")
  }

  const gotData = createSignal<void, Blob>()
  const recorder = yield* useMediaRecorder(audioStream, recorderOptions)
  recorder.start()

  recorder.ondataavailable = (event) => {
    gotData.close(event.data)
  }

  yield* ensure(() => {
    if (recorder.state !== "inactive") {
      recorder.stop()
    }
  })

  yield* info("started reading at", new Date())
  let next = yield* subscription.next()
  let result = ""
  while (!next.done) {
    const phrase = next.value
    const text = punctuatedConcatenation(phrase)
    const plain = plainConcatenation(phrase)

    yield* info("heard", { text })

    if (plain === "over") {
      break
    } else if (plain === "edit") {
      yield* edit(subscription)
    } else if (plain === "retranscribe") {
      yield* retranscribe()
    } else {
      result += plain + " "
      yield* insertGraphemesSlowly(text)

      // if text doesn't end with punctuation, add an em dash
      if (!text.match(/[.!?]$/)) {
        yield* append("— ")
      } else {
        yield* append(" ")
      }

      if (quick) {
        break
      }
    }

    yield* info("reading next phrase")
    if (recorder.state !== "recording") {
      recorder.start()
    }
    next = yield* subscription.next()
  }

  return result.trim()

  function* retranscribe() {
    yield* withClassName("retranscribing", function* () {
      recorder.stop()
      const subscription = yield* gotData
      const { value } = yield* subscription.next()
      if (value) {
        const { words } = yield* transcribe([value], "en")
        yield* clear()
        yield* append(punctuatedConcatenation(words.slice(0, -1)))
      }
    })
  }
}

function* withClassName<T>(
  className: string,
  body: () => Operation<T>,
): Operation<T> {
  const element = yield* getTarget()
  if (element.classList.contains(className)) {
    return yield* body()
  } else {
    element.classList.add(className)
    try {
      return yield* body()
    } finally {
      element.classList.remove(className)
    }
  }
}

function* edit(subscription: Subscription<WordHypothesis[], void>) {
  // There is a phrase written in the target paragraph as a text node.

  const task = yield* spawn(function* () {
    const target = yield* getTarget()
    target.classList.toggle("editing", true)
    const gadget = yield* pushNode(tag("kbd"))

    try {
      const desire = yield* readSpeech(subscription, false)
      const response = yield* stream(modelsByName["Claude III Opus"], {
        temperature: 0,
        maxTokens: 1024,
        systemMessage: "User requested edit to auto-transcribed paragraph.",
        messages: [
          {
            role: "user",
            content: `<p>${target.innerHTML}</p>
<edit>${desire}</edit>`,
          },
          {
            role: "assistant",
            content: "<p edited=true>",
          },
        ],
        stopSequences: ["</p>"],
      })

      const samp = yield* pushNode(tag("samp"))

      let next = yield* response.next()
      while (!next.done && next.value) {
        const { content } = next.value
        yield* append(content)
        next = yield* response.next()
      }

      const kbd2 = yield* pushNode(tag("kbd"))
      const decision = yield* readSpeech(subscription, true)
      kbd2.remove()
      console.log({ decision })
      if (decision.match(/ok/)) {
        target.innerHTML = samp.innerText
      }
    } finally {
      target.classList.toggle("editing", false)
      gadget.remove()
    }
  })

  yield* task
}

function* insertGraphemesSlowly(text: string) {
  const graphemes = splitter.splitGraphemes(text)
  for (const g of graphemes) {
    yield* append(g)
    yield* scrollToBottom()
    if (g.match(/[.!?]/)) {
      yield* sleep(500)
    } else if (g === ",") {
      yield* sleep(300)
    } else {
      yield* sleep(Math.random() * 10)
    }
  }
}

function* dialogue(
  article: HTMLElement,
  phrases: Stream<WordHypothesis[], void>,
  saveChannel: Channel<string, void>,
): Operation<void> {
  yield* info("has document", article)

  for (;;) {
    {
      yield* setNode(article)
      const p = yield* pushNode(tag("p", { class: "user speaking" }))

      const subscription = yield* phrases
      yield* readSpeech(subscription)

      p.classList.remove("speaking")
      const text = p.innerText
      yield* info("user said", { text })
      if (text === "Over and out.") {
        return
      }

      yield* saveChannel.send(text)
    }

    const messages = articleMessages(article)

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

        if (result && result.value) {
          const interruption = plainConcatenation(result.value)
          p.innerText += `—[user interrupts: ${interruption}] `
          break
        }

        next = yield* response.next()
      }
      p.classList.remove("speaking")

      yield* saveChannel.send("[assistant] " + p.innerText)
    }
  }
}

function articleMessages(article: HTMLElement): ChatMessage[] {
  return [...article.querySelectorAll("p")].map((paragraph) => {
    const { innerHTML: content, classList } = paragraph
    const role = classList.contains("user") ? "user" : "assistant"
    return { role, content }
  })
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

function* telegramClient(saveChannel: Stream<string, void>) {
  yield* task("telegram client", function* () {
    const service = new TelegramService()
    const updateSignal = createSignal<AnyUpdate>()
    service.subscribe((update) => {
      updateSignal.send(update)
    })

    const updateSubscription = yield* updateSignal

    service.start()

    yield* task("saver", function* () {
      yield* foreach(saveChannel, function* (text) {
        yield* info("saving", text)
        service.send({
          "@type": "sendMessage",
          "chat_id": service.self.id,
          "input_message_content": {
            "@type": "inputMessageText",
            "text": { "@type": "formattedText", "text": text },
          },
        })
      })
    })

    let next = yield* updateSubscription.next()
    while (!next.done) {
      yield* info("received update", next.value)
      next = yield* updateSubscription.next()
    }
  })
}
