import { Operation, Task, createChannel, sleep, spawn } from "effection"

export interface Sync<Post> {
  post: Post[]
  wait: (t: Post) => boolean
  halt: (t: Post) => boolean
  exec: Operation<Post>[]
}

export interface Thread<Post> {
  name: string
  sync: Sync<Post>
  proc: Generator<Sync<Post>, void, Post>
  prio: number
}

export function work<Post>(threads: Set<Thread<Post>>): boolean {
  const post = [...threads]
    .sort((a, b) => b.prio - a.prio)
    .flatMap((x) => x.sync.post)
    .find((x) => ![...threads].some((y) => y.sync.halt(x)))

  if (!post) {
    return false
  } else {
    for (const thread of threads) {
      if (thread.sync.post.includes(post) || thread.sync.wait(post)) {
        const { done, value } = thread.proc.next(post)
        if (done) {
          threads.delete(thread)
        } else {
          thread.sync = value
        }
      }
    }

    return true
  }
}

export function sync<Post>({
  post,
  wait,
  halt,
  exec,
}: Partial<Sync<Post>>): Sync<Post> {
  return {
    post: post ?? [],
    wait: wait ?? (() => false),
    halt: halt ?? (() => false),
    exec: exec ?? [],
  }
}

function noop<T>(name: string): Thread<T> {
  return {
    name,
    proc: (function* () {})(),
    sync: sync({}),
    prio: 0,
  }
}

interface Behavior<T> {
  name: string
  prio?: number
  init: () => Generator<Sync<T>, void, T>
}

export function makeThread<T>({
  name,
  prio = 1,
  init,
}: Behavior<T>): Thread<T> {
  const proc = init()
  const { done, value: sync } = proc.next()
  return done ? noop(name) : { name, prio, proc, sync }
}

export function* system<T, V = void>(
  body: (
    thread: {
      (name: string, init: () => Generator<Sync<T>, void, T>): Operation<void>
      (spec: Behavior<T>): Operation<void>
    },
    $: typeof sync<T>,
  ) => Operation<Task<V>>,
): Operation<V> {
  const newThreadChannel = createChannel<void>()
  const newlyStartedThreads = new Set<Thread<T>>()

  const bodyTask = yield* body(function* (
    nameOrSpec: string | Behavior<T>,
    init?: () => Generator<Sync<T>, void, T>,
  ) {
    const spec: Behavior<T> =
      typeof nameOrSpec === "string"
        ? { name: nameOrSpec, init: init! }
        : nameOrSpec
    newlyStartedThreads.add(makeThread(spec))

    // These are ignored until the subscription starts.
    yield* newThreadChannel.send()
  },
  sync<T>)

  const newThreadSubscription = yield* newThreadChannel

  // Trigger the subscription once after initial setup.
  yield* newThreadChannel.send()

  let threads = new Set<Thread<T>>()

  const systemTask = yield* spawn(function* () {
    for (;;) {
      if ((yield* newThreadSubscription.next()).done) break

      threads = threads.union(newlyStartedThreads)
      newlyStartedThreads.clear()

      for (;;) {
        if (work(threads) === false) {
          break
        }
      }
    }
  })

  try {
    return yield* bodyTask
  } finally {
    yield* newThreadChannel.close()
    yield* systemTask
  }
}

export const syncdemo2 = system<string>(function* (thread) {
  yield* thread({
    name: "show",
    prio: 1,
    init: function* () {
      for (;;) {
        console.log(yield sync({ wait: () => true }))
      }
    },
  })

  yield* thread({
    name: "step",
    prio: 1,
    init: function* () {
      for (;;) {
        yield sync({ post: ["tick"] })
        yield sync({ post: ["tock"] })
      }
    },
  })

  yield* thread({
    name: "flip",
    prio: 1,
    init: function* () {
      for (;;) {
        yield sync({
          wait: (t) => t === "tick",
          halt: (t) => t === "tock",
        })
        yield sync({
          wait: (t) => t === "tock",
          halt: (t) => t === "tick",
        })
      }
    },
  })

  yield* thread({
    name: "time",
    prio: 1,
    init: function* () {
      for (;;) {
        yield sync({
          wait: (t) => t === "second",
          halt: (t) => t === "tick" || t === "tock",
        })
        yield sync({ wait: (t) => t === "tick" || t === "tock" })
      }
    },
  })

  return yield* spawn(function* () {
    for (let i = 0; i < 5; i++) {
      yield* sleep(1000)
      yield* thread({
        name: `t${i}`,
        prio: 0,
        init: function* () {
          yield sync({ post: ["second"] })
        },
      })
    }
  })
})

// await main(() => syncdemo2)
