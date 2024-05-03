import {
  Operation,
  Stream,
  Subscription,
  call,
  createSignal,
  resource,
} from "effection"

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

export interface TelegramHandle
  extends Subscription<AnyUpdate, void>,
    Stream<AnyUpdate, void> {
  send: <R>(query: TelegramFunction<R>) => Operation<R>
}

export function useTelegramClient(): Operation<TelegramHandle> {
  return resource(function* (provide) {
    const updateSignal = createSignal<AnyUpdate, void>()
    const updateSubscription = yield* updateSignal

    const client = new window.tdweb.default({
      onUpdate: (x) => updateSignal.send(x),
      instanceName: "charliebot",
      jsLogVerbosityLevel: "info",
      useDatabase: true,
    })

    const handle: TelegramHandle = {
      send: (query) => call(client.send(query)),
      next: updateSubscription.next,
      [Symbol.iterator]: updateSignal[Symbol.iterator],
    }

    try {
      yield* provide(handle)
    } finally {
      yield* call(client.send({ "@type": "close" }))
    }
  })
}
