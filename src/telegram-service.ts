// deno-lint-ignore-file prefer-const
type TdWebOptions = {
  onUpdate: (update: AnyUpdate) => void
  instanceName: string
  jsLogVerbosityLevel: string
  useDatabase: boolean
}

type TdWebInstance = {
  send: <R>(query: TelegramFunction<R>) => Promise<R>
}

declare global {
  interface Window {
    tdweb: {
      default: {
        new (options: TdWebOptions): TdWebInstance
      }
    }
  }
}

export class TelegramService {
  private subscribers: Set<(update: AnyUpdate) => void> = new Set()
  private client: TdWebInstance
  self: any

  start() {
    this.client = new window.tdweb.default({
      onUpdate: (x: AnyUpdate) => this.handleUpdate(x),
      instanceName: "charliebot",
      jsLogVerbosityLevel: "info",
      useDatabase: true,
    })
  }

  subscribe(subscriber: (update: AnyUpdate) => void) {
    this.subscribers.add(subscriber)
  }

  unsubscribe(subscriber: (update: AnyUpdate) => void) {
    this.subscribers.delete(subscriber)
  }

  async send<R, T extends TelegramFunction<R>>(fn: T): Promise<R> {
    return await this.client.send(fn)
  }

  async ready() {
    this.self = await this.send({ "@type": "getMe" })
    console.log({ self: this.self })
    if (this.self.type["@type"] === "userTypeBot") {
      console.log("Bot")
    } else {
      await this.loadChats()
    }
  }

  async loadChats() {
    return await this.send({
      "@type": "getChats",
      "chat_list": null,
      "limit": 20,
    })
  }

  /**
   * @param {AnyUpdate} update - The update to handle.
   */
  handleUpdate(update: AnyUpdate) {
    if (update["@type"] === "updateAuthorizationState") {
      this.handleAuthorizationState(update as UpdateAuthorizationState)
    }

    for (const subscriber of this.subscribers) {
      subscriber(update)
    }
  }

  authorizationStateHandlers = (send: (x: any) => void) => ({
    authorizationStateWaitTdlibParameters: () =>
      send({
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
      }),
    authorizationStateWaitEncryptionKey: () =>
      send({
        "@type": "checkDatabaseEncryptionKey",
        "encryption_key": "",
      }),
    authorizationStateWaitPhoneNumber: () => {
      let phoneNumber = prompt("Phone number") as string
      if (phoneNumber.startsWith("+")) {
        send({
          "@type": "setAuthenticationPhoneNumber",
          "phone_number": phoneNumber,
        })
      } else {
        send({
          "@type": "checkAuthenticationBotToken",
          "token": phoneNumber,
        })
      }
    },
    authorizationStateWaitCode: () =>
      send({
        "@type": "checkAuthenticationCode",
        "code": prompt("Code") as string,
      }),
    authorizationStateReady: () => {
      this.ready()
    },
  })

  /**
   * @param {UpdateAuthorizationState} update
   */
  handleAuthorizationState(update: UpdateAuthorizationState) {
    const { authorization_state } = update
    if (!authorization_state) return

    const handler = this.authorizationStateHandlers((x) => this.send(x))[
      authorization_state["@type"]
    ]
    if (handler) {
      handler()
    }
  }
}
