import {
  Channel,
  Operation,
  Stream,
  Task,
  call,
  createChannel,
  each,
  main,
  on,
  resource,
  sleep,
  spawn,
} from "effection"
import {
  append,
  clear,
  foreach,
  message,
  pushNode,
  setNode,
  spawnWithElement,
} from "./kernel.js"
import { modelsByName, stream } from "./llm.js"

import { tag } from "./tag.js"
import { WebSocketHandle, useWebSocket } from "./websocket.js"

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

function* spawnVideoStream(
  stream: MediaStream,
  interimChannel: Channel<WordHypothesis[], void>,
  finalWordsChannel: Channel<WordHypothesis[], void>,
): Operation<void> {
  const article = tag(
    "article",
    {},
    tag("video", {
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
    }),
  )
  yield* append(article)
  yield* spawnTextOverlay(article, interimChannel, finalWordsChannel)
}

function* spawnTextOverlay(
  article: HTMLElement,
  interim: Stream<WordHypothesis[], void>,
  phrases: Stream<WordHypothesis[], void>,
): Operation<void> {
  // show the current interim in an aside tag
  yield* spawn(function* () {
    yield* setNode(article)
    yield* pushNode(
      tag("aside", { style: "font-style: italic; display: none" }),
    )
    yield* foreach(interim, function* (words) {
      yield* clear()
      yield* message(words.map(({ word }) => word).join(" "))
    })
  })

  yield* spawn(function* () {
    yield* setNode(article)
    yield* pushNode(tag("p"))

    let t = 0
    let i = 1

    let messages: { role: "user" | "assistant"; content: string }[] = []

    for (let words of yield* each(phrases)) {
      const phrase = words.map(({ word }) => word).join(" ")
      if (phrase === "clear screen") {
        article.querySelectorAll("message").forEach((message) => {
          message.remove()
        })

        yield* each.next()
        continue
      }

      let t1 = Date.now()
      let dt = t ? t1 - t : 0

      if (dt > 5000 || i === 1) {
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
            `❡${i++} `,
          ),
        )
      }

      messages.push({ role: "user", content: phrase })

      const subscription = yield* stream(modelsByName["Claude III Opus"], {
        temperature: 1,
        maxTokens: 512,
        systemMessage: `You are Claude III Opus, a noble and wise interlocutor, patient, concise, and calm. 
           Respond with a single sentence. Hesitate to offer help or advice.
           Primarily, you are here to acknowledge, mirror, and listen.`,
        messages,
      })

      for (const word of words) {
        yield* typeMessage(word.punctuated_word + " ", word.confidence)
      }

      yield* yield* spawn(function* () {
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
          console.info("Received response:", content)
          yield* typeMessage(content, 1)
          next = yield* subscription.next()
        }

        messages.push({ role: "assistant", content: response })
      })

      t = t1

      yield* each.next()
    }
  })
}

function* recordingSession(stream: MediaStream): Operation<void> {
  const audioStream = new MediaStream(stream.getAudioTracks())
  let language = "en"

  const interimChannel = createChannel<WordHypothesis[]>()
  const phraseChannel = createChannel<WordHypothesis[]>()

  yield* spawnVideoStream(stream, interimChannel, phraseChannel)

  for (;;) {
    console.log("Starting session", language)
    const task = yield* spawn(function* () {
      const socket = yield* useWebSocket(
        `${getWebSocketUrl()}/transcribe?language=${language}`,
      )

      const recorder = yield* useMediaRecorder(audioStream, {
        mimeType: "audio/webm;codecs=opus",
        audioBitsPerSecond: 64000,
      })
      recorder.start(100)

      let blobs: Blob[] = []

      yield* spawn(function* () {
        yield* foreach(on(recorder, "dataavailable"), function* ({ data }) {
          blobs.push(data)
          yield* socket.send(data)
        })
      })

      yield* spawnSocketMessageListener(socket, interimChannel, phraseChannel)
      yield* spawnVoiceCommandListener(phraseChannel, blobs)

      for (let words of yield* each(phraseChannel)) {
        const phrase = words.map(({ word }) => word).join(" ")
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

    yield* task
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
  let subscription = yield* finalWordsChannel
  while (true) {
    let { value } = yield* subscription.next()
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
  yield* pushNode(tag("app"))

  const stream = yield* useMediaStream({ audio: true, video: true })

  for (;;) {
    yield* recordingSession(stream)
  }
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
