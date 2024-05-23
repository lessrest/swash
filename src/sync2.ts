import { Operation, Task, createChannel, sleep, spawn } from "effection"

export interface Sync<Sign> {
  post: Sign[]
  wait: (t: Sign) => boolean
  halt: (t: Sign) => boolean
}

export interface Thread<Sign> {
  name: string
  sync: Sync<Sign>
  proc: Generator<Sync<Sign>, void, Sign>
  prio: number
}

function both<V>(x: Set<V> | undefined, y: Set<V> | undefined): Set<V> {
  return (x ?? new Set()).union(y ?? new Set())
}

export function work<Sign>(system: Set<Thread<Sign>>): boolean {
  const postedBy = new Map<Sign, Set<Thread<Sign>>>()
  for (const thread of system) {
    for (const postedSign of thread.sync.post) {
      const set = postedBy.get(postedSign) ?? new Set()
      set.add(thread)
      postedBy.set(postedSign, set)
    }
  }

  const wantedBy = new Map<Sign, Set<Thread<Sign>>>()
  for (const e of postedBy.keys()) {
    const havers = new Set<Thread<Sign>>()

    for (const task of system) {
      if (task.sync.wait(e)) {
        havers.add(task)
      }
    }

    if (havers.size > 0) {
      wantedBy.set(e, havers)
    }
  }

  const electedSign = [...system]
    .sort((a, b) => b.prio - a.prio)
    .flatMap((x) => x.sync.post)
    .find((x) => ![...system].some((y) => y.sync.halt(x)))

  if (!electedSign) {
    return false
  } else {
    const affectedThreads = both(
      postedBy.get(electedSign),
      wantedBy.get(electedSign),
    )

    for (const thread of affectedThreads) {
      const { done, value } = thread.proc.next(electedSign)
      if (done) {
        system.delete(thread)
      } else {
        thread.sync = value
      }
    }

    return true
  }
}

export function sync<Sign>({
  post,
  wait,
  halt,
}: Partial<Sync<Sign>>): Sync<Sign> {
  return {
    post: post ?? [],
    wait: wait ?? (() => false),
    halt: halt ?? (() => false),
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
