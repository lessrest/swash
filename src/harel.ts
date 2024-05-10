interface Sync<T> {
  post?: T[]
  wait?: T[]
  stop?: T[]
}

interface Thread<T> {
  name: string
  cont: Generator<Sync<T>>
  sync: Sync<T>
}

class Multimap<K, V> extends Map<K, V[]> {
  add(key: K, value: V) {
    this.set(key, [...this.get(key), value])
  }

  get(key: K): V[] {
    return super.get(key) ?? []
  }
}

export function step<Post>(threads: Set<Thread<Post>>) {
  const posters = new Multimap<Post, Thread<Post>>()
  const waiters = new Multimap<Post, Thread<Post>>()
  const stopped = new Set<Post>()
  const exiters = new Set<Thread<Post>>()

  for (const thread of threads) {
    const { post, wait, stop } = thread.sync
    for (const x of post ?? []) posters.add(x, thread)
    for (const x of wait ?? []) waiters.add(x, thread)
    for (const x of stop ?? []) stopped.add(x)
  }

  const chosen = [...posters.keys()].find((x) => !stopped.has(x))

  if (chosen) {
    console.log("✱", chosen)
    for (const thread of new Set([
      ...posters.get(chosen),
      ...waiters.get(chosen),
    ])) {
      const { done, value } = thread.cont.next()
      if (done) {
        exiters.add(thread)
      } else {
        thread.sync = value
      }
    }

    return threads.difference(exiters)
  }

  return null
}

export function* exec<T>(
  threads: Set<Thread<T>>,
): Generator<{ state: string; threads: Set<Thread<T>> }> {
  for (;;) {
    const result = step(threads)

    if (result === null) {
      yield { state: "blocked", threads }
    } else {
      threads = result
    }
  }
}

export function run<T>(threads: Iterable<Thread<T>>) {
  const { done, value } = exec(new Set(threads)).next()
  if (!done) {
    console.log("◼︎", value.state)
  }
}

function noop<T>(name: string): Thread<T> {
  return {
    name,
    cont: (function* () {})(),
    sync: {},
  }
}

export function start<T>(
  name: string,
  thunk: () => Generator<Sync<T>>,
): Thread<T> {
  const cont = thunk()
  const { done, value: sync } = cont.next()
  return done ? noop(name) : { name, cont, sync }
}

function demo() {
  run([
    start("fill hot", function* () {
      for (;;) {
        yield { wait: ["water-low"] }
        yield { post: ["add-hot"] }
        yield { post: ["add-hot"] }
        yield { post: ["add-hot"] }
      }
    }),
    start("initially low", function* () {
      yield { post: ["water-low"] }
    }),
    start("fill cold", function* () {
      for (;;) {
        yield { wait: ["water-low"] }
        yield { post: ["add-cold"] }
        yield { post: ["add-cold"] }
        yield { post: ["add-cold"] }
      }
    }),
    start("balance hot/cold", function* () {
      for (;;) {
        yield { wait: ["add-hot"], stop: ["add-cold"] }
        yield { wait: ["add-cold"], stop: ["add-hot"] }
      }
    }),
  ])
}

demo()
