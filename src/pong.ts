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
import { into, nest } from "./nest.ts"

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
  | ["ball moved", { x: number; y: number }]
  | ["ball changed velocity", { x: number; y: number }]
  | ["paddle A moved", { y: number }]
  | ["paddle B moved", { y: number }]
  | ["paddle A scored"]
  | ["paddle B scored"]
  | ["ball bounced off paddle A"]
  | ["ball bounced off paddle B"]
  | ["ball bounced off top"]
  | ["ball bounced off bottom"]

export const pong = system<Step>(function* (rule, sync) {
  yield* into(document.body)

  const canvas = yield* nest(
    html<HTMLCanvasElement>("canvas", {
      width: "800px",
      height: "600px",
    }),
  )

  const ctx = canvas.getContext("2d")!

  const state = {
    ball: { position: { x: 400, y: 300 }, velocity: { x: 0, y: 0 } },
    a: { y: 50, score: 0 },
    b: { y: 50, score: 0 },
  }

  const mutationQueue: ((document: Document) => void)[] = []

  function* exec<
    T extends Step,
  >(body: () => Operation<T>, spec: Partial<SyncSpec<Step>> = {}): Generator<Sync<Step>, T, Step> {
    const step = yield sync({ ...spec, exec: body })
    return step as T
  }

  yield* rules({
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

    *["Animation frames are requested continuously."]() {
      for (;;) {
        yield* post("request animation frame")
        yield* wait("animation frame began")
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

    *["The canvas is initially a dark blue color."]() {
      yield* exec(function* () {
        ctx.fillStyle = "darkblue"
        ctx.fillRect(0, 0, ctx.canvas.width, ctx.canvas.height)
        return ["document mutation applied"]
      })
    },

    *["The ball moves according to its velocity."]() {
      let t0 = yield* wait("animation frame began")
      for (;;) {
        const t = yield* wait("animation frame began")
        const dt = (t - t0) / 1000
        t0 = t

        state.ball.position.x += state.ball.velocity.x * dt
        state.ball.position.y += state.ball.velocity.y * dt

        yield* post("ball moved", state.ball.position)
      }
    },

    *["The ball velocity is initially random."]() {
      yield* post("ball changed velocity", {
        x: Math.random() * 100 - 50,
        y: Math.random() * 100 - 50,
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
