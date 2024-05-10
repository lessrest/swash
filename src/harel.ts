interface Sync<T> {
  have?: T[]
  want?: T[]
  deny?: T[]
}

interface Task<T> {
  name: string
  sync: Sync<T>
  proc: Generator<Sync<T>>
}

class Multimap<K, V> extends Map<K, Set<V>> {
  add(key: K, value: V) {
    this.get(key).add(value)
  }

  get(key: K): Set<V> {
    this.set(key, super.get(key) ?? new Set())
    return super.get(key)!
  }
}

export function* work<Item>(
  team: Set<Task<Item>>,
): Generator<Set<Task<Item>>, Set<Task<Item>>> {
  for (;;) {
    const have = new Multimap<Item, Task<Item>>()
    const want = new Multimap<Item, Task<Item>>()
    const deny = new Multimap<Item, Task<Item>>()

    for (const task of team) {
      for (const x of task.sync.have ?? []) have.add(x, task)
      for (const x of task.sync.want ?? []) want.add(x, task)
      for (const x of task.sync.deny ?? []) deny.add(x, task)
    }

    const item = [...have.keys()].find((x) => !deny.has(x))

    if (!item) {
      return team
    } else {
      console.log("✱", item)

      for (const task of have.get(item).union(want.get(item))) {
        const { done, value } = task.proc.next()
        if (done) {
          team.delete(task)
        } else {
          task.sync = value
        }
      }

      yield team
    }
  }
}

export function exec<T>(tasks: Iterable<Task<T>>) {
  let x = new Set(tasks)
  for (;;) {
    const { done, value } = work(x).next()
    if (done) {
      console.log("◼︎", value)
      return
    } else {
      console.log("✅", value)
      x = value
    }
  }
}

function noop<T>(name: string): Task<T> {
  return {
    name,
    proc: (function* () {})(),
    sync: {},
  }
}

export function task<T>(
  name: string,
  init: () => Generator<Sync<T>>,
): Task<T> {
  const proc = init()
  const { done, value: sync } = proc.next()
  return done ? noop(name) : { name, proc, sync }
}

function demo() {
  exec([
    task("fill hot", function* () {
      for (;;) {
        yield { want: ["water-low"] }
        yield { have: ["add-hot"] }
        yield { have: ["add-hot"] }
        yield { have: ["add-hot"] }
      }
    }),
    task("initially low", function* () {
      yield { have: ["water-low"] }
    }),
    task("fill cold", function* () {
      for (;;) {
        yield { want: ["water-low"] }
        yield { have: ["add-cold"] }
        yield { have: ["add-cold"] }
        yield { have: ["add-cold"] }
      }
    }),
    task("balance hot/cold", function* () {
      for (;;) {
        yield { want: ["add-hot"], deny: ["add-cold"] }
        yield { want: ["add-cold"], deny: ["add-hot"] }
      }
    }),
  ])
}

demo()
