import {
  Operation,
  Stream,
  Subscription,
  call,
  createContext,
  createSignal,
  resource,
} from "effection"
import { html } from "./html.ts"
import { nest, pull, seem } from "./nest.ts"
import { info, task } from "./task.ts"

const $api = createContext<{ api_id: string; api_hash: string }>("api")

export function* tele(saveChannel: Stream<string, void>) {
  return yield* task("telegram client", function* () {
    yield* nest(
      html("telegram-client", {
        style: {
          backgroundImage:
            "url(http" +
            "s://upload.wikimedia.org/wikipedia/commons/8/82/Telegram_logo.svg)",
          backgroundSize: "cover",
          backgroundPosition: "center",
          backgroundRepeat: "no-repeat",
          width: "1.5em",
          height: "1.5em",
        },
      }),
    )

    const handle = yield* useTelegramClient()

    const self: string = yield* resource<string>(function* (provide) {
      yield* pull(handle, function* (update) {
        if (update["@type"] === "updateAuthorizationState") {
          const { authorization_state } = update as UpdateAuthorizationState
          yield* info("has authorization state", authorization_state)

          switch (authorization_state["@type"]) {
            case "authorizationStateWaitTdlibParameters": {
              yield* info("waiting for tdlib parameters")
              const { api_id, api_hash } = yield* $api
              yield* send("setTdlibParameters", {
                parameters: {
                  use_test_dc: false,
                  api_id,
                  api_hash,
                  system_language_code: "en",
                  device_model: "Desktop",
                  system_version: "",
                  application_version: "1.0",
                  enable_storage_optimizer: true,
                  use_pfs: true,
                  database_directory: "tdlib",
                  use_file_database: true,
                  use_chat_info_database: true,
                  use_message_database: true,
                },
              })
              break
            }

            case "authorizationStateWaitEncryptionKey": {
              yield* info("waiting for encryption key")
              yield* send("checkDatabaseEncryptionKey", {
                encryption_key: "",
              })
              break
            }

            case "authorizationStateWaitPhoneNumber": {
              yield* info("waiting for phone number")
              yield* send("setAuthenticationPhoneNumber", {
                phone_number: prompt("Telegram phone number"),
              })
              break
            }

            case "authorizationStateWaitCode": {
              yield* info("waiting for code")
              yield* send("checkAuthenticationCode", {
                code: prompt("Telegram code"),
              })
              break
            }

            case "authorizationStateReady": {
              yield* info("authorized")
              const me = (yield* send("getMe", {})) as unknown as {
                id: string
              }
              yield* info("has me", me)
              yield* seem("connected")
              yield* provide(me.id as string)
              break
            }
          }
        }
      })
    })

    yield* info("has self", self)

    yield* task("saver", function* () {
      yield* pull(saveChannel, function* (text) {
        yield* info("TODO saving", text)
        yield* send("sendMessage", {
          chat_id: self,
          input_message_content: {
            "@type": "inputMessageText",
            "text": { "@type": "formattedText", "text": text },
          },
        })
      })
    })

    for (;;) {
      const next = yield* handle.next()
      if (next.done) break
      yield* info("received update", next.value)
    }
  })
}

// deno-lint-ignore-file prefer-const
type TdWebOptions = {
  onUpdate: (update: AnyUpdate) => void
  instanceName: string
  jsLogVerbosityLevel: string
  useDatabase: boolean
}

interface Wire {
  send<O, I>(body: I): Promise<O>
}

declare global {
  interface Window {
    tdweb: {
      default: {
        new (options: TdWebOptions): Wire
      }
    }
  }
}

const $wire = createContext<Wire>("wire")

function* send<O, I>(verb: string, body: I): Operation<O> {
  const client = yield* $wire
  return yield* call(client.send<O, I>({ "@type": verb, ...body }))
}

export const setTdlibParameters = (parameters: TdlibParameters) =>
  send("setTdlibParameters", parameters) as Operation<Ok>

export interface News
  extends Subscription<AnyUpdate, void>,
    Stream<AnyUpdate, void> {}

export function useTelegramClient(): Operation<News> {
  return resource(function* (provide) {
    const news = createSignal<AnyUpdate, void>()
    const feed = yield* news

    const wire = new window.tdweb.default({
      onUpdate: (x) => news.send(x),
      instanceName: "charliebot",
      jsLogVerbosityLevel: "warning",
      useDatabase: true,
    })

    try {
      yield* provide({
        next: feed.next,
        [Symbol.iterator]: news[Symbol.iterator],
      })
    } finally {
      yield* call(wire.send({ "@type": "close" }))
    }
  })
}
