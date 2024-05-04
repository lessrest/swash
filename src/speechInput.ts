import { Operation, Stream, sleep } from "effection"
import { graphemesOf } from "./graphemes.ts"

import {
  appendNewTarget,
  foreach,
  replaceChildren,
  useClassName,
} from "./kernel.ts"

import {
  SpokenWord,
  plainConcatenation,
  punctuatedConcatenation,
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

  let done = false

  const typingAnimationTask = yield* task("typing animation", function* () {
    const self = yield* appendNewTarget(tag("p.typing-animation"))

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
      finalText += (punctuated + " ").replaceAll(". ", ".\n")
    })
  })

  yield* task("interim", function* () {
    yield* foreach(interimStream, function* (phrase) {
      const punctuated = punctuatedConcatenation(phrase)
      if (interimText !== punctuated) {
        yield* info("updating interimText", punctuated)
        interimText = punctuated
      }
    })
  })

  // yield* task("retranscription", function* () {
  //   const blobs: Blob[] = yield* useAudioRecorder()
  //   for (;;) {
  //     yield* waitForButton("Retranscribe")
  //     yield* info("retranscribing")
  //     const result = yield* transcribe(blobs, "en")
  //     finalText = paragraphsToText(result.paragraphs) + " "
  //     yield* info("updated finalText", finalText)
  //   }
  // })

  yield* finalTask
  done = true
  yield* typingAnimationTask

  return finalText
}
