import { Stream, call, resource } from "effection"
import { appendNewTarget, foreach, useClassName } from "./kernel.ts"
import { tag } from "./tag.ts"
import { info, task } from "./task.ts"
import { useTelegramClient } from "./telegram-service.ts"

export function* telegramClient(saveChannel: Stream<string, void>) {
  return yield* task("telegram client", function* () {
    yield* appendNewTarget(
      tag("telegram-client", {
        style: {
          backgroundImage:
            "url(https://upload.wikimedia.org/wikipedia/commons/8/82/Telegram_logo.svg)",
          backgroundSize: "cover",
          backgroundPosition: "center",
          backgroundRepeat: "no-repeat",
          width: "1.5em",
          height: "1.5em",
        },
      }),
    )

    const handle = yield* useTelegramClient()

    const self: string = yield* resource(function* (provide) {
      yield* foreach(handle, function* (update) {
        if (update["@type"] === "updateAuthorizationState") {
          const { authorization_state } = update as UpdateAuthorizationState
          yield* info("has authorization state", authorization_state)

          switch (authorization_state["@type"]) {
            case "authorizationStateWaitTdlibParameters": {
              yield* info("waiting for tdlib parameters")
              yield* handle.send({
                "@type": "setTdlibParameters",
                "parameters": {
                  use_test_dc: false,
                  api_id: window.env.telegram.api_id,
                  api_hash: window.env.telegram.api_hash,
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
              yield* handle.send({
                "@type": "checkDatabaseEncryptionKey",
                "encryption_key": "",
              })
              break
            }

            case "authorizationStateWaitPhoneNumber": {
              yield* info("waiting for phone number")
              yield* handle.send({
                "@type": "setAuthenticationPhoneNumber",
                "phone_number": prompt("Telegram phone number"),
              })
              break
            }

            case "authorizationStateWaitCode": {
              yield* info("waiting for code")
              yield* handle.send({
                "@type": "checkAuthenticationCode",
                "code": prompt("Telegram code"),
              })
              break
            }

            case "authorizationStateReady": {
              yield* info("authorized")
              const me = (yield* call(handle.send({ "@type": "getMe" }))) as {
                id: string
              }
              yield* info("has me", me)
              yield* useClassName("connected")
              yield* provide(me.id as string)
              break
            }
          }
        }
      })
    })

    yield* info("has self", self)

    yield* task("saver", function* () {
      yield* foreach(saveChannel, function* (text) {
        yield* info("TODO saving", text)
        yield* handle.send({
          "@type": "sendMessage",
          "chat_id": self,
          "input_message_content": {
            "@type": "inputMessageText",
            "text": { "@type": "formattedText", "text": text },
          },
        })
      })
    })

    for (;;) {
      const next = yield* handle.next()
      if (next.done) break
      // yield* info("received update", next.value)
    }
  })
}
