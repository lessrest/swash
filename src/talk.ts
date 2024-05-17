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

import { Word, plainConcatenation, punctuatedConcatenation } from "./text.ts"

import { useAudioRecorder } from "./demo.ts"
import { html } from "./html.ts"
import { ChatCompletionRequest, ChatMessage, gpt4o, stream } from "./mind.ts"
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
  flux: Stream<Word[], void>,
  firm: Stream<Word[], void>,
  save: (text: string) => Operation<void>,
): Operation<string> {
  return resource(function* (provide) {
    yield* info("🆕 new speech")

    let fine = ""
    let line = ""
    let exit = false

    const psst = createChannel<"change">()
    const gong = createChannel<"timeout">()

    const pane = yield* nest(html("section"))
    const tray = html("div.text")

    yield* task("text", function* () {
      const drip = yield* task("drip", function* () {
        yield* nest(tray)
        let limit = 0
        for (;;) {
          const text = fine + line
          const graphemes = graphemesOf(text)
          const remaining = graphemes.length - limit
          const textToShow = graphemes.slice(0, limit).join("")
          //yield* info("text", `"${fine}" + "${line}"`)
          if (!(textToShow.trim() === pane.innerText.trim())) {
            fade(() => {
              show(textToShow)
            })
          }

          if (exit && remaining <= 0) {
            break
          } else {
            limit = Math.min(graphemes.length, limit + 1)
            if (remaining <= 0) {
              yield* hold("want more text")
            } else {
              // Examples of the formula Math.max(50 - (remaining * 5), 10):
              // If remaining is 0, the result is Math.max(50 - (0 * 5), 10) = Math.max(50, 10) = 50
              // If remaining is 5, the result is Math.max(50 - (5 * 5), 10) = Math.max(25, 10) = 25
              // If remaining is 10, the result is Math.max(50 - (10 * 5), 10) = Math.max(0, 10) = 10
              // If remaining is 15, the result is Math.max(50 - (15 * 5), 10) = Math.max(-25, 10) = 10
              const sleepDuration = Math.max(50 - remaining * 5, 20)
              yield* sleep(sleepDuration)
            }
          }
        }
      })

      function show(text: string) {
        console.info("TEXT", JSON.stringify(text))
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
        yield* hold("waiting to start")
        for (;;) {
          if ((yield* withTimeout(hold(), 1500)) === timedOut) {
            yield* info("tentative timeout")
            if (
              !(yield* race([
                call(function* () {
                  // const { paragraphs } = yield* redo(() => hark(tape, "en"))
                  // if (paragraphs) {
                  //   fine = paragraphsToText(paragraphs) + " "
                  //   tray.classList.add("did-retranscribe")
                  //   yield* gong.send("timeout")
                  //   return true
                  // }
                  yield* gong.send("timeout")
                  return true
                }),
                hold("waiting to be interrupted"),
              ]))
            ) {
              yield* info("speech while retranscribing; restarting countdown")
              continue
            }

            yield* psst.send("change")

            const past = document.body.innerText

            const editRequest: ChatCompletionRequest = {
              systemMessage:
                "Return the user input edited to be clear and concise—but also be more sardonic, deadpan funny, and understatedly witty. Fix likely transcription errors. Split run-on sentences. Use CAPS EMPHASIS—only for IMPORTANT NOUN PHRASES and salient ACTION VERBS. Prefix each sentence with an appropriate emoji icon.",
              messages: [
                {
                  role: "user",
                  content: `<context>${past}</context><input>${fine}</input><format>Output only the edited input; the context is already displayed.</format>`,
                },
              ],
              temperature: 1.0,
              maxTokens: 1000,
            }

            fine = yield* mend(yield* stream(gpt4o, editRequest))

            // const wish: ChatCompletionRequest = {
            //   messages: [
            //     {
            //       role: "user",
            //       content: tidy(fine),
            //     },
            //   ],
            //   temperature: 0,
            //   maxTokens: 1000,
            // }

            // fine = yield* mend(yield* stream(gpt4o, wish))

            yield* save(fine)

            break
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
        if (exit) break

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
      yield* processStream(firm, function* (phrase) {
        if (plainConcatenation(phrase) === "over") {
          yield* info("got 'over', stopping")
          return
        }

        if (plainConcatenation(phrase) === "reload") {
          document.location.reload()
          return
        }

        yield* info("FIRM", JSON.stringify(phrase))
        fine += processPhrase(phrase)
        line = ""
        yield* psst.send("change")
      })
    })

    yield* task("interim processor", function* () {
      yield* processStream(flux, function* (phrase) {
        const processed = processPhrase(phrase).trim()
        if (processed !== "" && line !== processed) {
          line = processed
          yield* info("flux", line, "==>", processed)
          yield* psst.send("change")
        }
      })
    })

    function* hold(note?: string) {
      if (note) {
        yield* info("hold", note)
      }

      try {
        yield* waitForStream(psst)
      } finally {
        if (note) yield* info("held", note)
      }
    }

    try {
      yield* waitForStream(gong)
    } catch (e) {
      yield* info("error waiting for timeout", e)
    } finally {
      yield* info("paragraph done")
      exit = true
    }

    try {
      yield* provide(fine)
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
