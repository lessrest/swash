import {
  Operation,
  Stream,
  Subscription,
  action,
  call,
  createChannel,
  race,
  resource,
  sleep,
  suspend,
} from "effection"

import { nest } from "./nest.ts"
import { graphemesOf } from "./text.ts"

import {
  Word,
  hark,
  paragraphsToText,
  plainConcatenation,
  punctuatedConcatenation,
} from "./text.ts"

import { useAudioRecorder } from "./demo.ts"
import { html } from "./html.ts"
import {
  ChatCompletionRequest,
  ChatMessage,
  c3haiku,
  c3opus,
  stream,
} from "./mind.ts"
import { info, task } from "./task.ts"
import { tidy } from "./wish.ts"

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

export function* waitForAnimationFrame() {
  yield* action(function* (resolve) {
    const x = requestAnimationFrame(() => resolve(undefined))
    try {
      yield* suspend()
    } finally {
      cancelAnimationFrame(x)
    }
  })
}

export function talk(
  interimStream: Stream<Word[], void>,
  finalStream: Stream<Word[], void>,
  _onFinalText: (text: string) => Operation<void>,
): Operation<string> {
  return resource(function* (provide) {
    yield* info("🆕 new speech")
    let best = ""
    let interimText = ""

    let paragraphDone = false

    const psst = createChannel<"change">()
    const gong = createChannel<"timeout">()

    const pane = yield* nest(html("section"))
    const tray = html("div.text")

    yield* task("text", function* () {
      const drip = yield* task("drip", function* () {
        yield* nest(tray)
        let limit = 0
        for (;;) {
          const text = best + interimText
          const graphemes = graphemesOf(text)
          const remaining = graphemes.length - limit
          const textToShow = graphemes.slice(0, limit).join("")
          if (!(textToShow.trim() === pane.innerText.trim())) {
            fade(() => {
              show(textToShow)
            })
          }

          if (paragraphDone && remaining <= 0) {
            break
          } else {
            limit = Math.min(graphemes.length, limit + 1)
            yield* sleep(45)
          }
        }
      })

      function show(text: string) {
        tray.replaceChildren(
          ...text
            .trim()
            .split("\n")
            .filter((line) => line.trim() !== "")
            .map((line) => html("p", {}, line)),
        )
        scrollTo({
          top: document.body.scrollHeight,
        })
      }

      function* mend(
        recv: Subscription<ChatMessage, void>,
        html: boolean = false,
      ) {
        let text = ""
        for (;;) {
          const { value, done } = yield* recv.next()
          if (done) break
          text += value.content
        }

        text = text.replaceAll(/[.?!]\s/g, "$&\n")
        fade(() => {
          if (html) {
            tray.innerHTML = text
          } else {
            show(text)
          }
        })

        return text
      }

      const tape: Blob[] = yield* useAudioRecorder()

      const wand = yield* task("wand", function* () {
        yield* hold()
        for (;;) {
          if ((yield* withTimeout(hold(), 2500)) === timedOut) {
            yield* info("tentative timeout")
            if (
              !(yield* race([
                call(function* () {
                  const { paragraphs } = yield* hark(tape, "en")
                  if (paragraphs) {
                    best = paragraphsToText(paragraphs) + " "
                    fade(() => {
                      show(best)
                      tray.classList.add("did-retranscribe")
                    })
                    yield* gong.send("timeout")
                    return true
                  }
                }),
                hold(),
              ]))
            ) {
              yield* info("speech while retranscribing; restarting countdown")
              continue
            }

            const wish: ChatCompletionRequest = {
              messages: [
                {
                  role: "user",
                  content: tidy(best),
                },
              ],
              temperature: 0,
              maxTokens: 1000,
            }

            yield* mend(yield* stream(c3haiku, wish))
            yield* mend(yield* stream(c3opus, wish))

            break
          } else {
            //            animation.currentTime = 0
          }
        }
      })

      yield* drip
      yield* wand
    })

    function* processStream(
      stream: Stream<Word[], void>,
      onPhrase: (phrase: Word[]) => Operation<void>,
    ) {
      const subscription = yield* stream
      for (;;) {
        if (paragraphDone) break

        const { value: phrase, done } = yield* subscription.next()
        if (done) break

        yield* onPhrase(phrase)
      }
    }

    function processPhrase(phrase: Word[]): string {
      const punctuated = punctuatedConcatenation(phrase)
      return (punctuated + " ").replaceAll(/[.?!]\s/g, "$&\n")
    }

    yield* task("final processor", function* () {
      yield* processStream(finalStream, function* (phrase) {
        yield* psst.send("change")
        if (plainConcatenation(phrase) === "over") {
          yield* info("got 'over', stopping")
          return
        }

        if (plainConcatenation(phrase) === "reload") {
          document.location.reload()
          return
        }

        best += processPhrase(phrase)
      })
    })

    yield* task("interim processor", function* () {
      yield* processStream(interimStream, function* (phrase) {
        const processed = processPhrase(phrase).trim()
        if (interimText !== processed) {
          yield* info("interim text changed", interimText, processed)
          interimText = processed
          yield* psst.send("change")
        }
      })
    })

    function hold() {
      return waitForStream(psst)
    }

    try {
      yield* waitForStream(gong)
    } catch (e) {
      yield* info("error waiting for timeout", e)
    } finally {
      yield* info("paragraph done")
      paragraphDone = true
    }

    try {
      yield* provide(best)
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

function fade(fn: () => void) {
  if ("startViewTransition" in document) {
    ;(document.startViewTransition as (callback: () => void) => void)(fn)
  } else {
    fn()
  }
}
