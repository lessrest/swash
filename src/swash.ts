import {
  Channel,
  Operation,
  Stream,
  Subscription,
  Task,
  call,
  createChannel,
  each,
  main,
  on,
  resource,
  sleep,
  spawn,
  suspend,
} from "effection"

import {
  append,
  foreach,
  message,
  pushNode,
  setNode,
  spawnWithElement,
} from "./kernel.ts"

import { modelsByName, stream } from "./llm.ts"
import { tag } from "./tag.ts"
import { Epoch, info, task } from "./task.ts"
import { WebSocketHandle, useWebSocket } from "./websocket.ts"

function* typeMessage(text: string, confidence = 1): Operation<void> {
  yield* yield* spawnWithElement(
    tag("message", {
      style: `opacity: ${confidence}; text-decoration: ${
        confidence < 0.6 ? "line-through" : "none"
      }`,
    }),

    function* (self) {
      self.scrollIntoView({
        behavior: "smooth",
        block: "center",
        inline: "center",
      })

      requestAnimationFrame(() => {
        self.classList.add("started")
      })

      for (const char of text) {
        self.textContent += char

        let delay = 20
        if (char === "." || char === "!" || char === "?") {
          delay = 200 * Math.sin(Date.now() / 100) + 150
        } else if (char === ",") {
          delay = 70 * Math.sin(Date.now() / 50) + 75
        }

        yield* sleep(delay)
      }

      self.classList.add("finished")
    },
  )
}

function* spawnSpeechDocument(
  stream: MediaStream,
  interimChannel: Channel<WordHypothesis[], void>,
  finalWordsChannel: Channel<WordHypothesis[], void>,
): Operation<void> {
  const article = tag("article", {}, videoTag(stream))
  yield* append(article)
  yield* spawnSpeechDocumentProcess(
    article,
    interimChannel,
    finalWordsChannel,
  )
}

function videoTag(stream: MediaStream): HTMLElement {
  return tag("video", {
    srcObject: stream,
    autoplay: true,
    oncanplay: function () {
      this.muted = true
    },
    onClick: function () {
      if (this.requestFullscreen) {
        this.requestFullscreen()
      }
    },
  })
}

function* spawnSpeechDocumentProcess(
  article: HTMLElement,
  _interim: Stream<WordHypothesis[], void>,
  phrases: Stream<WordHypothesis[], void>,
): Operation<void> {
  yield* spawn(function* () {
    yield* setNode(article)
    yield* pushNode(tag("p"))

    let t = 0
    let i = 1

    const messages: { role: "user" | "assistant"; content: string }[] = []
    let nextMessage = ""

    for (const words of yield* each(phrases)) {
      const phrase = words.map(({ word }) => word).join(" ")
      if (phrase === "clear screen") {
        article.querySelectorAll("message").forEach((message) => {
          message.remove()
        })
        yield* each.next()
        continue
      }

      const punctuatedPhrase = words
        .map(({ punctuated_word }) => punctuated_word)
        .join(" ")

      const t1 = Date.now()
      const dt = t ? t1 - t : 0

      if (dt > 5000 || i === 1) {
        yield* info("decides to add message separator at", new Date())
        yield* addMessageSeparator(article, i++)
      }

      //     const systemMessage = `1. Assistant Role and Characteristics
      //     1.1 You are a helpful, thoughtful, and creative assistant.
      //     1.2 Your role is to engage in conversation with the human user to help them develop a quest log dashboard system for managing their home, family life, and personal goals.
      //     1.3 The system should be imbued with beauty, story, and respect for the meaningful objects and centers that make up their life.

      //  2. Conversation Medium and Considerations
      //     2.1 The conversation takes place via a live transcription system, so the user's input may contain some inaccuracies or anomalies.
      //     2.2 When you notice these, tactfully clarify the user's intended meaning.

      //  3. System Definition and Inspiration
      //     3.1 In this conversation, we will work to define a system of interlinked cards representing meaningful centers and the quests, tasks, and aspirations associated with them.
      //     3.2 The system should have qualities reminiscent of a quest log and inventory in a beautifully-crafted action RPG video game.
      //     3.3 The system should evoke the ideas of thinkers like Christopher Alexander, Graham Nelson, and Hubert Dreyfus through mood and atmosphere, rather than direct reference.
      //     3.4 The cards are reminiscent of the patterns in a pattern language, and are hypertext or hypermedia in nature.

      //  5. Tone and Collaboration
      //     5.1 Throughout, adopt a gentle, playful, and literary tone while maintaining focus on the practical goal of creating an aspirational yet functional system for this family's domestic life.
      //     5.2 Engage the user collaboratively and draw connections between the various concepts and values they express.
      //     5.3 Be tactful and mindful of timing, cadence, concision; take it easy, one thing at a time.
      //  `

      const systemMessage =
        "Help the user formulate their thoughts. Be concise. Just rephrase, clarify, summarize."

      if (phrase === "over") {
        yield* info("believed user was done speaking at", new Date())
        messages.push({ role: "user", content: nextMessage })
        nextMessage = ""

        const response = yield* stream(modelsByName["Claude III Opus"], {
          temperature: 0.6,
          maxTokens: 1024,
          systemMessage,
          messages,
        })

        yield* info("began displaying transcription at", new Date())
        for (const word of words) {
          yield* typeMessage(word.punctuated_word + " ", word.confidence)
        }

        yield* info("began displaying LLM response at", new Date())
        yield* streamModelResponse(response, messages)
      } else {
        nextMessage += punctuatedPhrase
        for (const word of words) {
          yield* typeMessage(word.punctuated_word + " ", word.confidence)
        }
      }

      t = t1

      yield* each.next()
    }
  })
}

function* addMessageSeparator(
  article: HTMLElement,
  index: number,
): Operation<void> {
  // if the last message didn't end with punctuation,
  // add an em dash
  const lastMessage = article.querySelector("message:last-child")
  if (lastMessage && lastMessage.textContent) {
    const text = lastMessage.textContent.trim()
    if (!text.match(/[.!?]/)) {
      lastMessage.textContent = text + "—"
    }
  }

  yield* append(tag("hr"))
  yield* append(
    tag(
      "message",
      {
        class: "started finished",
        style:
          "font-size: 80%; padding: 0 1rem; opacity: 0.7; font-weight: bold;",
      },
      `❡${index} `,
    ),
  )
}

export function* streamModelResponse(
  subscription: Subscription<{ content: string }, void>,
  messages: { role: "user" | "assistant"; content: string }[],
): Operation<void> {
  yield* yield* task("a streaming chat response view", function* () {
    yield* pushNode(
      tag("aside", {
        style:
          "font-style: italic; white-space: pre-wrap;  padding: .25em .5em; font-size: 80%;",
      }),
    )
    let next = yield* subscription.next()
    let response = ""
    while (!next.done && next.value) {
      const { content } = next.value
      response += content
      yield* typeMessage(content, 1)
      next = yield* subscription.next()
    }
    yield* info("received response", { text: response })

    messages.push({ role: "assistant", content: response })
  })
}

function* recordingSession(stream: MediaStream): Operation<void> {
  const audioStream = new MediaStream(stream.getAudioTracks())
  let language = "en"

  const interimChannel = createChannel<WordHypothesis[]>()
  const phraseChannel = createChannel<WordHypothesis[]>()

  yield* spawnSpeechDocument(stream, interimChannel, phraseChannel)

  let i = 1
  for (;;) {
    yield* Epoch.set(new Date())
    const task1 = yield* task(`transcription session ${i++}`, function* () {
      const socket = yield* useWebSocket(
        new WebSocket(`${getWebSocketUrl()}/transcribe?language=${language}`),
      )

      yield* info("specifies language with code", language)
      yield* info("established service connection at", new Date())

      const recorder = yield* useMediaRecorder(audioStream, {
        mimeType: "audio/webm;codecs=opus",
        audioBitsPerSecond: 64000,
      })
      recorder.start(300)

      yield* info("started recording at", new Date())
      yield* info("has audio format", "Opus-compressed WebM container")
      yield* info("has audio bitrate", recorder.audioBitsPerSecond)
      yield* info("has audio timeslice", "300ms")

      const blobs: Blob[] = []

      yield* spawn(function* () {
        yield* foreach(on(recorder, "dataavailable"), function* ({ data }) {
          blobs.push(data)
          yield* socket.send(data)
        })
      })

      yield* spawnSocketMessageListener(socket, interimChannel, phraseChannel)
      yield* spawnVoiceCommandListener(phraseChannel, blobs)

      for (const words of yield* each(phraseChannel)) {
        const phrase = words.map(({ word }) => word).join(" ")
        yield* info("phrase at", new Date(), `"${phrase}"`)
        if (phrase === "swedish please") {
          language = "sv"
          break
        } else if (phrase === "engelska tack") {
          language = "en"
          break
        }

        yield* each.next()
      }
    })

    yield* task1
  }
}

function* spawnVoiceCommandListener(
  finalWordsChannel: Channel<WordHypothesis[], void>,
  blobs: Blob[],
): Operation<void> {
  yield* spawn(function* () {
    while (true) {
      yield* waitForSpecificPhrase(finalWordsChannel, "fix it up")
      const { words } = yield* transcribe(blobs)
      yield* message(
        words.map(({ punctuated_word }) => punctuated_word).join(" "),
      )
    }
  })

  yield* spawn(function* () {
    yield* waitForSpecificPhrase(finalWordsChannel, "reload")
    document.location.reload()
  })
}

function* waitForSpecificPhrase(
  finalWordsChannel: Channel<WordHypothesis[], void>,
  phrase: string,
): Operation<void> {
  const subscription = yield* finalWordsChannel
  while (true) {
    const { value } = yield* subscription.next()
    if (!value) {
      throw new Error("Channel closed")
    } else {
      const spokenPhrase = value.map(({ word }) => word).join(" ")
      if (spokenPhrase === phrase) {
        return
      }
    }
  }
}

function* spawnSocketMessageListener(
  socket: WebSocketHandle,
  interimChannel: Channel<WordHypothesis[], void>,
  finalWordsChannel: Channel<WordHypothesis[], void>,
): Operation<Task<void>> {
  return yield* spawn(function* () {
    for (const event of yield* each(socket)) {
      const data = JSON.parse(event.data)
      if (data.type === "Results" && data.channel) {
        const {
          alternatives: [{ transcript, words }],
        } = data.channel
        if (data.is_final) {
          if (transcript) {
            yield* finalWordsChannel.send(words)
          }
          yield* interimChannel.send([])
        } else if (transcript) {
          yield* interimChannel.send(words)
        }
      }
      yield* each.next()
    }
  })
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
