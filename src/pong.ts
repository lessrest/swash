import {
  Operation,
  Stream,
  Subscription,
  action,
  call,
  createSignal,
  each,
  main,
  on,
  resource,
  spawn,
  suspend,
  useScope,
} from "effection"

import "@types/dom-view-transitions"

import { html } from "./html.ts"
import { Sync, SyncSpec, sync, system } from "./sync.ts"
import { into } from "./nest.ts"

type TagName = Step[0]
type Payload<T extends TagName> = Extract<Step, [T, unknown]>[1]

function* wait<T extends TagName>(
  tag: T,
  predicate?: (payload: Payload<T>) => boolean,
): Generator<Sync<Step>, Payload<T>, Step> {
  return (
    (yield sync<Step>({
      wait: (t) =>
        t[0] === tag && (!predicate || predicate(t[1] as Payload<T>)),
    })) as [T, Payload<T>]
  )[1]
}

type Step =
  | ["request animation frame"]
  | ["animation frame began", number]
  | ["document mutation requested", (document: Document) => void]
  | ["document mutation applied"]
  | ["the game ends"]

export const pong = system<Step>(function* (rule, sync) {
  yield* into(document.body)

  const scope = yield* useScope()

  function run(body: () => Operation<void>) {
    scope.run(body)
  }

  const mutationQueue: ((document: Document) => void)[] = []

  function* exec<
    T extends Step,
  >(body: () => Operation<T>, spec: Partial<SyncSpec<Step>> = {}): Generator<Sync<Step>, T, Step> {
    const step = yield sync({ ...spec, exec: body })
    return step as T
  }

  yield* rules({
    *["Infinite blocking loop..."]() {
      yield* wait("the game ends")
    },

    *["Document mutations are queued."]() {
      for (;;) {
        mutationQueue.push(yield* wait("document mutation requested"))
      }
    },

    *["Animation frames are triggered on request."]() {
      for (;;) {
        yield* wait("request animation frame")
        yield* exec(() =>
          action(function* (resolve) {
            console.log("request animation frame")
            const id = requestAnimationFrame((x) => {
              console.log("animation frame began", x)
              resolve(["animation frame began", x])
            })
            try {
              yield* suspend()
            } finally {
              console.log("cancelling animation frame", id)
              cancelAnimationFrame(id)
            }
          }),
        )
      }
    },

    *["The mutation queue is applied in animation frames."]() {
      for (;;) {
        yield* wait("document mutation requested")
        yield* post("request animation frame")
        yield* wait("animation frame began")
        yield* exec(function* () {
          yield* call(applyMutationQueue(mutationQueue))
          return ["document mutation applied"]
        })
      }
    },

    *["A canvas element is created."]() {
      yield* post("document mutation requested", (document) => {
        const canvas = html("canvas", {
          width: "100%",
          height: "100%",
        })
        document.body.append(canvas)
      })
    },
  })

  function* post<T extends TagName>(tag: T, payload?: Payload<T>) {
    yield sync({ post: [[tag, payload] as Step] })
  }

  function* rules(
    rules: Record<string, () => Generator<Sync<Step>, void, Step>>,
  ) {
    for (const [name, ruleBody] of Object.entries(rules)) {
      yield* rule(name, ruleBody)
    }
  }

  return yield* spawn(function* () {
    console.log("spawning pong")
    try {
      yield* suspend()
    } finally {
      console.log("ending pong")
    }
  })
})

function applyMutationQueue(queue: ((document: Document) => void)[]) {
  const f = () => {
    for (const thunk of queue) {
      thunk(document)
    }
    queue.length = 0
  }

  if (document.startViewTransition) {
    return document.startViewTransition(f).updateCallbackDone
  } else {
    f()
    return Promise.resolve()
  }
}
