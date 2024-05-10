import {
  Operation,
  Stream,
  action,
  call,
  createChannel,
  race,
  resource,
  sleep,
  suspend,
} from "effection"
import { graphemesOf } from "./graphemes.ts"

import { appendNewTarget, replaceChildren, useClassName } from "./kernel.ts"

import {
  SpokenWord,
  paragraphsToText,
  plainConcatenation,
  punctuatedConcatenation,
  transcribe,
} from "./transcription.ts"

import { Claude_III_Opus, stream } from "./llm.ts"
import { cleanupPrompt } from "./prompt.ts"
import { useAudioRecorder } from "./swash.ts"
import { tag } from "./tag.ts"
import { info, task } from "./task.ts"

const timedOut: unique symbol = Symbol("timedOut")

function* returning<T>(
  value: T,
  operation: Operation<unknown>,
): Operation<T> {
  yield* operation
  return value
}

function* withTimeout<T>(
  operation: Operation<T>,
  timeout: number,
): Operation<T | typeof timedOut> {
  return yield* race([operation, returning(timedOut, sleep(timeout))])
}

function* waitForAnimationFrame() {
  yield* action(function* (resolve) {
    const x = requestAnimationFrame(() => resolve(undefined))
    try {
      yield* suspend()
    } finally {
      cancelAnimationFrame(x)
    }
  })
}

export function speechInput(
  interimStream: Stream<SpokenWord[], void>,
  finalStream: Stream<SpokenWord[], void>,
  onFinalText: (text: string) => Operation<void>,
): Operation<string> {
  return resource(function* (provide) {
    yield* info("🆕 new speech")
    let finalText = ""
    let interimText = ""

    let paragraphDone = false

    const activityChannel = createChannel<"change">()
    const timeoutChannel = createChannel<"timeout">()

    const self = yield* appendNewTarget(tag("section"))

    yield* task("text", function* () {
      const subTask = yield* task("animation", function* () {
        yield* appendNewTarget(tag("div"))
        let limit = 0
        for (;;) {
          const text = finalText + interimText
          const graphemes = graphemesOf(text)
          const remaining = graphemes.length - limit
          const textToShow = graphemes.slice(0, limit).join("")
          const areEqualAlready = textToShow.trim() === self.innerText.trim()
          if (!areEqualAlready) {
            yield* updateDisplay(textToShow)
          }

          if (paragraphDone && remaining <= 0) {
            break
          } else {
            limit = Math.min(graphemes.length, limit + 1)
            yield* waitForAnimationFrame()
          }
        }
      })

      function* updateDisplay(textToShow: string) {
        yield* replaceChildren(
          ...textToShow
            .trim()
            .split("\n")
            .filter((line) => line.trim() !== "")
            .map((line) => tag("p", {}, line)),
        )
        scrollTo({
          top: document.body.scrollHeight,
          behavior: "smooth",
        })
      }

      const blobs: Blob[] = yield* useAudioRecorder()

      const countdownTask = yield* task("enhancement", function* () {
        yield* awaitActivity()
        for (;;) {
          if ((yield* withTimeout(awaitActivity(), 4000)) === timedOut) {
            yield* timeoutChannel.send("timeout")

            yield* call(function* () {
              yield* useClassName("working-1")

              const { paragraphs } = yield* transcribe(blobs, "en")
              if (paragraphs) {
                finalText = paragraphsToText(paragraphs) + " "
                yield* updateDisplay(finalText)
              }
            })

            yield* call(function* () {
              yield* useClassName("working-2")

              const llmSubscription = yield* stream(Claude_III_Opus, {
                messages: [
                  {
                    role: "user",
                    content: cleanupPrompt(finalText),
                  },
                ],
                temperature: 0,
                maxTokens: 1000,
              })

              let cleanedText = ""
              for (;;) {
                const { value: message, done } = yield* llmSubscription.next()
                if (done) break
                cleanedText += message.content
              }

              cleanedText = cleanedText.replaceAll(/[.?!]\s/g, "$&\n")
              yield* updateDisplay(cleanedText)
              yield* onFinalText(cleanedText)
            })

            // yield* call(function* () {
            //   yield* useClassName("working-3")

            //   const paragraphs = [
            //     ...self.closest("article")!.querySelectorAll("p"),
            //   ]
            //   const messages = paragraphs.map((p) => ({
            //     role: p.className.includes("response") ? "assistant" : "user",
            //     content: p.innerText,
            //   }))
            //   const messagesMerged = mergeConsecutive(
            //     messages,
            //     (a, b) => a.role === b.role,
            //   )

            //   function mergeConsecutive<
            //     T,
            //   >(messages: T[], areConsecutive: (a: T, b: T) => boolean): T[] {
            //     const result: T[] = []
            //     for (const message of messages) {
            //       if (
            //         result.length === 0 ||
            //         !areConsecutive(result[result.length - 1], message)
            //       ) {
            //         result.push(message)
            //       }
            //     }
            //     return result
            //   }

            //   yield* appendNewTarget(tag("p.response"))
            //   const llmSubscription = yield* stream(Claude_III_Opus, {
            //     messages: messagesMerged as ChatMessage[],
            //     temperature: 0.6,
            //     maxTokens: 1000,
            //     systemMessage: mdma1,
            //   })
            //   let response = ""
            //   for (;;) {
            //     const { value: message, done } = yield* llmSubscription.next()
            //     yield* info("message", message)
            //     if (message) {
            //       response += message.content
            //       yield* replaceChildren(response)
            //     }

            //     if (done) break
            //   }
            // })

            break
          } else {
            //            animation.currentTime = 0
          }
        }
      })

      yield* subTask
      yield* countdownTask
    })

    function* processStream(
      stream: Stream<SpokenWord[], void>,
      onPhrase: (phrase: SpokenWord[]) => Operation<void>,
    ) {
      const subscription = yield* stream
      for (;;) {
        if (paragraphDone) break

        const { value: phrase, done } = yield* subscription.next()
        if (done) break

        yield* onPhrase(phrase)
      }
    }

    function processPhrase(phrase: SpokenWord[]): string {
      const punctuated = punctuatedConcatenation(phrase)
      return (punctuated + " ").replaceAll(/[.?!]\s/g, "$&\n")
    }

    yield* task("final processor", function* () {
      yield* processStream(finalStream, function* (phrase) {
        yield* activityChannel.send("change")
        if (plainConcatenation(phrase) === "over") {
          yield* info("got 'over', stopping")
          return
        }

        finalText += processPhrase(phrase)
      })
    })

    yield* task("interim processor", function* () {
      yield* processStream(interimStream, function* (phrase) {
        const processed = processPhrase(phrase).trim()
        if (interimText !== processed) {
          yield* info("interim text changed", interimText, processed)
          interimText = processed
          yield* activityChannel.send("change")
        }
      })
    })

    function awaitActivity() {
      return waitForStream(activityChannel)
    }

    try {
      yield* waitForStream(timeoutChannel)
    } catch (e) {
      yield* info("error waiting for timeout", e)
    } finally {
      yield* info("paragraph done")
      paragraphDone = true
    }

    try {
      yield* provide(finalText)
      yield* suspend()
    } catch (e) {
      yield* info("error in speechInput", e)
    }
  })
}

function* waitForStream<T>(stream: Stream<T, void>) {
  const subscription = yield* stream
  const result = yield* subscription.next()
  return result
}
