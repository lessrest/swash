import {
  Channel,
  Operation,
  Result,
  Task,
  createChannel,
  main,
  sleep,
  spawn,
  suspend,
} from "effection"

type Exec<Post> =
  | { state: "none" }
  | {
      state: "pending"
      op: () => Operation<Post>
    }
  | {
      state: "running"
      task: Task<void>
    }
  | {
      state: "done"
      result: Result<Post>
    }

export interface Sync<Post> {
  post: Post[]
  wait: (t: Post) => boolean
  halt: (t: Post) => boolean
  exec: Exec<Post>
}

export interface Thread<Post> {
  name: string
  sync: Sync<Post>
  proc: Generator<Sync<Post>, void, Post>
  prio: number
}

function* work<Post>(
  threads: Set<Thread<Post>>,
  gong: Channel<void, void>,
): Operation<boolean> {
  let didWork = false

  for (const thread of threads) {
    if (thread.sync.exec.state === "done") {
      try {
        const { result } = thread.sync.exec
        console.log("exec done", thread.name, result)
        const { done, value: sync } = result.ok
          ? thread.proc.next(result.value)
          : thread.proc.throw(result.error)
        if (done) {
          threads.delete(thread)
        } else {
          thread.sync = sync
          yield* doexec<Post>(thread, gong)
        }
      } finally {
        console.groupEnd()
      }

      didWork = true
    }
  }

  const chosen = [...threads]
    .sort((a, b) => b.prio - a.prio)
    .flatMap((x) => x.sync.post)
    .find((x) => ![...threads].some((y) => y.sync.halt(x)))

  if (chosen) {
    console.log("Chosen", chosen)
    for (const thread of threads) {
      const { post, wait, exec } = thread.sync
      if (post.includes(chosen) || wait(chosen)) {
        console.group("Thread", thread.name)
        try {
          if (exec.state === "running") {
            console.log("Halting thread exec", thread.name)
            yield* exec.task.halt() // hmm
            thread.sync.exec = { state: "none" }
          }

          const { done, value } = thread.proc.next(chosen)
          if (done) {
            threads.delete(thread)
          } else {
            thread.sync = value
            yield* doexec<Post>(thread, gong)
          }
        } finally {
          console.groupEnd()
        }
      }
    }

    didWork = true
  }

  return didWork
}

function* doexec<Post>(thread: Thread<Post>, gong: Channel<void, void>) {
  if (thread.sync.exec.state === "pending") {
    console.log("Spawning task for thread", thread.name)
    const op = thread.sync.exec.op
    const task = yield* spawn(function* () {
      try {
        const x = yield* op()
        console.log("Thread exec done", thread.name, x)
        thread.sync.exec = {
          state: "done",
          result: { ok: true, value: x },
        }
      } catch (e) {
        console.error("Thread exec error", thread.name, e)
        thread.sync.exec = {
          state: "done",
          result: { ok: false, error: e },
        }
      } finally {
        yield* gong.send()
      }
    })

    thread.sync.exec = { state: "running", task }
  }
}

interface SyncSpec<Post> {
  post?: Post[]
  wait?: (t: Post) => boolean
  halt?: (t: Post) => boolean
  exec?: () => Operation<Post>
}

export function sync<Post>({
  post,
  wait,
  halt,
  exec,
}: Partial<SyncSpec<Post>>): Sync<Post> {
  return {
    post: post ?? [],
    wait: wait ?? (() => false),
    halt: halt ?? (() => false),
    exec: exec ? { state: "pending", op: exec } : { state: "none" },
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
    },
    $: typeof sync<T>,
  ) => Operation<Task<V>>,
): Operation<V> {
  const gong = createChannel<void>()
  const born = new Set<Thread<T>>()

  const bodyTask = yield* body(function* (
    name: string,
    init: () => Generator<Sync<T>, void, T>,
  ) {
    const thread = makeThread({ name, init })
    yield* doexec(thread, gong)
    born.add(thread)
    yield* gong.send()
  },
  sync<T>)

  let threads = new Set<Thread<T>>()

  const systemTask = yield* spawn(function* () {
    for (;;) {
      console.group("Superstep")
      try {
        if (born.size > 0) {
          console.log("Born threads", born)
          threads = threads.union(born)
          born.clear()
        }

        const gongs = yield* gong

        for (;;) {
          if (false === (yield* work(threads, gong))) {
            break
          }
        }

        console.log("Waiting")
        const gongsResult = yield* gongs.next()
        console.log("Gongs result:", gongsResult)
        if (gongsResult.done) {
          console.log("Breaking outer loop")
          break
        }
      } finally {
        console.groupEnd()
      }
    }
  })

  try {
    return yield* bodyTask
  } finally {
    yield* gong.close()
    yield* systemTask
  }
}

export async function foo() {
  await main(() =>
    system(function* (thread, sync) {
      yield* thread("test", function* () {
        for (;;) {
          const x = yield sync({
            wait: (x) => x === "test",
            exec: function* () {
              try {
                yield* sleep(1000)
                console.log("slept")
              } finally {
                console.log("sleep over")
              }
              return "second"
            },
          })

          yield sync({
            post: [x],
          })
        }
      })

      yield* thread("test2", function* () {
        for (;;) {
          yield sync({ wait: (x) => x === "second" })
          yield sync({ wait: (x) => x === "second" })
          yield sync({ wait: (x) => x === "second" })
          yield sync({ post: ["test"] })
        }
      })

      return yield* spawn(function* () {
        yield* suspend()
        console.log("done")
      })
    }),
  )
}
