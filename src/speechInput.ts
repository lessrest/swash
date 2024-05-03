import { Operation, Stream, sleep } from "effection"
import { graphemesOf } from "./graphemes.ts"

import {
  append,
  appendNewTarget,
  foreach,
  replaceChildren,
  useClassName,
} from "./kernel.ts"

import { SpokenWord } from "./SpokenWord.ts"
import {
  innerTextWithBr,
  nbsp,
  paragraphsToText,
  plainConcatenation,
  punctuatedConcatenation,
  transcribe,
  useAudioRecorder,
} from "./swash.ts"

import { tag } from "./tag.ts"
import { info, task } from "./task.ts"

export function* speechInput(
  interimStream: Stream<SpokenWord[], void>,
  finalStream: Stream<SpokenWord[], void>,
): Operation<string> {
  const root = yield* appendNewTarget(tag("ins.user"))

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
    yield* appendNewTarget(tag("p.typing-animation", {}, nbsp))

    let limit = 0
    for (;;) {
      const spans = [
        ...root.querySelectorAll<HTMLElement>(".final, .interim"),
      ]
      const text = spans.map(innerTextWithBr).join("")
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
    yield* appendNewTarget(tag("div.final"))

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
