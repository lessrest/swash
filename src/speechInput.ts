import { Operation, Stream, sleep } from "effection"
import { graphemesOf } from "./graphemes.ts"

import {
  appendNewTarget,
  foreach,
  replaceChildren,
  useClassName,
} from "./kernel.ts"

import { nbsp, useAudioRecorder } from "./swash.ts"
import {
  SpokenWord,
  paragraphsToText,
  plainConcatenation,
  punctuatedConcatenation,
  transcribe,
} from "./transcription.ts"

import { tag } from "./tag.ts"
import { info, task } from "./task.ts"

export function* speechInput(
  interimStream: Stream<SpokenWord[], void>,
  finalStream: Stream<SpokenWord[], void>,
): Operation<string> {
  yield* appendNewTarget(tag("ins.user"))
  yield* useClassName("listening")

  let finalText = ""
  let interimText = ""

  yield* task("recorder", function* () {
    const blobs: Blob[] = yield* useAudioRecorder()

    for (;;) {
      yield* (yield* finalStream).next()
      try {
        const result = yield* transcribe(blobs, "en")
        finalText = paragraphsToText(result.paragraphs) + " "
      } catch (error) {
        yield* info("error transcribing", error)
      }
    }
  })

  let done = false

  const typingAnimationTask = yield* task("typing animation", function* () {
    yield* appendNewTarget(tag("p.typing-animation", {}, nbsp))

    let limit = 0
    for (;;) {
      const text = finalText + interimText
      const graphemes = graphemesOf(text)
      const remaining = graphemes.length - limit
      const textToShow = graphemes.slice(0, limit).join("")

      if (done && remaining <= 0) {
        yield* replaceChildren(finalText)
        break
      } else {
        if (textToShow !== text) {
          yield* info("replacing text", { textToShow, text })
          yield* replaceChildren(textToShow)
        }

        yield* sleep(20)
        limit = Math.min(graphemes.length, limit + 1)
      }
    }
  })

  const finalTask = yield* task("final", function* () {
    yield* foreach(finalStream, function* (phrase) {
      if (plainConcatenation(phrase) === "over") {
        return "stop"
      }
      finalText += punctuatedConcatenation(phrase) + " "
    })
  })

  yield* task("interim", function* () {
    yield* foreach(interimStream, function* (phrase) {
      interimText = punctuatedConcatenation(phrase)
    })
  })

  yield* finalTask
  done = true
  yield* typingAnimationTask

  return finalText
}
