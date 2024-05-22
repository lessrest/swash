import {
  Operation,
  action,
  call,
  createChannel,
  each,
  main,
  on,
  resource,
  suspend,
} from "effection"

import { demo3 } from "./demo3.ts"
import { html } from "./html.ts"
import { ChatMessage, gpt4o, think } from "./mind.ts"
import { into, nest } from "./nest.ts"
import { SocketConnection, rent } from "./sock.ts"
import { redo, task } from "./task.ts"
import { Word } from "./text.ts"

const recorderOptions: MediaRecorderOptions = MediaRecorder.isTypeSupported(
  "audio/webm;codecs=opus",
)
  ? { mimeType: "audio/webm;codecs=opus", audioBitsPerSecond: 128000 }
  : { mimeType: "audio/mp4", audioBitsPerSecond: 128000 }

function* fade(fn: () => void) {
  if ("startViewTransition" in document) {
    return yield* action(function* (resolve, reject) {
      document.startViewTransition(async () => {
        try {
          fn()
          resolve()
        } catch (error) {
          reject(error)
        }
      })
      yield* suspend()
    })
  } else {
    fn()
  }
}

function* demo() {
  yield* into(document.body)

  const audioStream = yield* useMediaStream({
    audio: true,
    video: false,
  })

  const transcriptionPeer: SocketConnection = yield* rent(dial("sv"))

  const mediaRecorder = yield* useMediaRecorder(audioStream, recorderOptions)

  const startTime = Date.now() / 1000

  const audioUploadTask = yield* task("audio uploading", function* () {
    for (const audioChunk of yield* each(
      on(mediaRecorder, "dataavailable"),
    )) {
      yield* transcriptionPeer.send(audioChunk.data)
      yield* each.next()
    }
  })

  const transcriptionChannel = createChannel<"finalize" | Word[]>()

  const transcriptReceiverTask = yield* task(
    "transcript receiver",
    function* () {
      for (const transcriptionEvent of yield* each(transcriptionPeer)) {
        const transcriptionData = JSON.parse(transcriptionEvent.data)

        if (
          transcriptionData.type === "Results" &&
          transcriptionData.channel
        ) {
          const {
            alternatives: [{ transcript, words }],
          } = transcriptionData.channel
          if (transcript) {
            yield* transcriptionChannel.send(words)
            if (transcriptionData.is_final) {
              yield* transcriptionChannel.send("finalize")
            }
          }
        }

        yield* each.next()
      }
    },
  )

  const finalSentenceChannel = createChannel<HTMLSpanElement>()

  const typingTask = yield* task("type", function* () {
    const transcriptionSubscription = yield* transcriptionChannel
    let phraseSpan = html("span.phrase")
    const finalSpan = html("span.final")
    const sentencesSpan = html("span.sentences")

    yield* nest(html("p", {}, sentencesSpan, finalSpan, phraseSpan))

    for (;;) {
      const transcriptionEvent = yield* transcriptionSubscription.next()
      if (transcriptionEvent.done) {
        return
      }
      if (transcriptionEvent.value === "finalize") {
        const newSentences: HTMLSpanElement[] = []
        yield* fade(() => {
          finalSpan.append(...phraseSpan.children)
          phraseSpan = html("span.phrase")
          finalSpan.after(phraseSpan)

          for (const terminator of finalSpan.querySelectorAll(
            ".terminator",
          )) {
            const sentence = html("span.sentence")
            const terminatorOffset = Array.from(
              terminator.parentElement?.children ?? [],
            ).indexOf(terminator)
            // append terminator and all its predecessors to sentence
            sentence.append(
              ...[...(terminator.parentElement?.children ?? [])].slice(
                0,
                terminatorOffset + 1,
              ),
            )
            sentencesSpan.append(sentence)
            newSentences.push(sentence)
          }
        })
        for (const sentence of newSentences) {
          yield* finalSentenceChannel.send(sentence)
        }
      } else {
        const currentTime = Date.now() / 1000 - startTime
        const wordElements = transcriptionEvent.value.map((word) => {
          console.log(
            word.word,
            Math.max(0, Math.abs(word.start - currentTime) - 1),
          )
          return html(
            "span.word",
            {
              data: { t0: word.start, t1: word.end },
              class: word.punctuated_word.match(/[.!?]$/)
                ? ["terminator"]
                : [],
            },
            word.punctuated_word + " ",
          )
        })

        yield* fade(() => phraseSpan.replaceChildren(...wordElements))
      }
    }
  })

  const llmProcessingTask = yield* task(
    "LLM processing of final phrases",
    function* () {
      for (const phraseElement of yield* each(finalSentenceChannel)) {
        const phrase = phraseElement.innerText
        if (phrase) {
          const messages: ChatMessage[] = Array.from(
            phraseElement
              .closest("p")
              ?.querySelectorAll<HTMLSpanElement>(".sentence") ?? [],
          ).map((sentence) => ({
            role: sentence.classList.contains("ai") ? "assistant" : "user",
            content: sentence.innerText,
          }))

          const chat = () =>
            think(gpt4o, {
              systemMessage: [
                "Fix likely transcription errors.",
                "Split run-on sentences and improve punctuation.",
                "Use CAPS only on key words for emphasis and flow.",
                "Prefix each line with a relevant EMOJI.",
              ].join(" "),

              // "This is a rap. User throws a line, you hit back with a rhyme.
              // Match the user's meter and length.
              // Use CAPS only on key words for emphasis and flow.
              // Prefix each line with a relevant EMOJI.",

              messages,
              temperature: 0.4,
              maxTokens: 50,
            })
          yield* redo(function* read() {
            const editedSpan = html("span.sentence.ai", {
              data: {
                t0: phraseElement.dataset.t0,
                t1: phraseElement.dataset.t1,
              },
            })
            phraseElement.after(editedSpan)

            const responseChannel = yield* chat()
            let responseText = ""
            for (;;) {
              const { value, done } = yield* responseChannel.next()
              if (done) break
              responseText += value.content
              yield* fade(() => {
                editedSpan.innerText = responseText
              })
            }
            return responseText
          })
        }

        yield* each.next()
      }
    },
  )

  mediaRecorder.start(100)

  document.body.classList.add("ok")
  try {
    yield* audioUploadTask
    yield* transcriptReceiverTask
    yield* typingTask
    yield* llmProcessingTask
  } catch (e) {
    document.body.classList.add("error")
    document.body.append(
      html(
        "p",
        {
          style: {
            backgroundColor: "red",
            color: "white",
            padding: "1em",
          },
        },
        e.message,
      ),
    )
  } finally {
    document.body.classList.remove("ok")
  }
}

document.addEventListener("DOMContentLoaded", async function () {
  if (document.location.hash === "#nt") {
    await demo3()
  } else {
    await main(() => demo())
  }
})

function dial(lang: string): WebSocket {
  return new WebSocket(
    `${document.location.protocol === "https:" ? "wss:" : "ws:"}//${
      document.location.host
    }/transcribe?lang=${lang}`,
  )
}

function useMediaRecorder(
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

function useMediaStream(
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
