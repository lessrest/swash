import { Operation, Stream, sleep } from "effection"
import { graphemesOf } from "./graphemes.ts"

import {
  appendNewTarget,
  foreach,
  replaceChildren,
  useClassName,
} from "./kernel.ts"

import { useAudioRecorder } from "./swash.ts"
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

  const blobs: Blob[] = yield* useAudioRecorder()
  // yield* task("recorder", function* () {
  //   for (;;) {
  //     yield* info("waiting for final stream")
  //     yield* (yield* finalStream).next()
  //     yield* info("got final stream, transcribing")
  //     try {
  //       const result = yield* transcribe(blobs, "en")
  //       yield* info("transcription result", result)
  //       finalText = paragraphsToText(result.paragraphs) + " "
  //       lastChangeTime = Date.now()
  //       yield* info("updated finalText", finalText)
  //     } catch (error) {
  //       yield* info("error transcribing", error)
  //     }
  //   }
  // })

  let done = false

  const typingAnimationTask = yield* task("typing animation", function* () {
    const self = yield* appendNewTarget(tag("p.typing-animation"))

    let dirty = false

    let limit = 0
    for (;;) {
      const text = finalText + interimText
      const graphemes = graphemesOf(text)
      const remaining = graphemes.length - limit
      const textToShow = graphemes.slice(0, limit).join("")

      if (done && remaining <= 0) {
        break
      } else {
        if (textToShow !== self.innerText) {
          yield* info("replacing text", { textToShow, text })
          yield* replaceChildren(textToShow)
        }
        limit = Math.min(graphemes.length, limit + 1)
        yield* sleep(40)
      }
    }
  })

  const finalTask = yield* task("final", function* () {
    yield* foreach(finalStream, function* (phrase) {
      yield* info("got phrase from finalStream", phrase)
      if (plainConcatenation(phrase) === "over") {
        yield* info("got 'over', stopping")
        return "stop"
      }
      const punctuated = punctuatedConcatenation(phrase)
      yield* info("updating finalText with", punctuated)
      finalText += punctuated + " "
    })
  })

  yield* task("interim", function* () {
    yield* foreach(interimStream, function* (phrase) {
      const punctuated = punctuatedConcatenation(phrase)
      yield* info("updating interimText", punctuated)
      interimText = punctuated
      lastChangeTime = Date.now()
    })
  })

  yield* finalTask
  done = true
  yield* typingAnimationTask

  return finalText
}
