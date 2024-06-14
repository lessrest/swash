import {
  Context,
  Operation,
  Task,
  createContext,
  main,
  sleep,
  spawn,
} from "effection"

import { Sync, SyncSpec, sync, system } from "../sync.ts"

type Rule<Sign, V = void> = Generator<Sync<Sign>, V, Sign>

export class BehavioralProgram<Sign> {
  rules: Map<string, () => Rule<Sign>> = new Map()
  tasks: Map<string, () => Operation<void>> = new Map()

  Thread: Context<(name: string, body: () => Rule<Sign>) => Operation<void>> =
    createContext("thread");

  *sync(spec: Partial<SyncSpec<Sign>>): Rule<Sign, Sign> {
    return yield sync(spec)
  }

  *post(sign: Sign): Operation<void> {
    const thread = yield* this.Thread.get()
    if (thread) {
      yield* thread("emit", function* () {
        yield sync({ post: [sign] })
      })
    } else {
      throw new Error("not running")
    }
  }

  play(): Operation<void> {
    const { rules, tasks, Thread } = this

    return system<Sign>(function* (thread, _$) {
      yield* Thread.set(thread)

      for (const [name, rule] of rules) {
        yield* thread(name, rule)
      }

      const spawned: Task<void>[] = []
      for (const [_, task] of tasks) {
        spawned.push(yield* spawn(task))
      }

      return yield* spawn(function* () {
        for (const task of spawned) {
          yield* task
        }
      })
    })
  }
}

function behavior(
  target: (this: BehavioralProgram<Sign>) => Rule<Sign>,
  context: ClassMethodDecoratorContext<
    BehavioralProgram<Sign>,
    (this: BehavioralProgram<Sign>) => Rule<Sign>
  >,
) {
  context.addInitializer(function () {
    const methodName = context.name as string
    this.rules.set(methodName, target.bind(this))
  })
}

function repeating<T, R, N, This>(
  target: (this: This) => Generator<T, R, N>,
  _context: ClassMethodDecoratorContext<
    This,
    (this: This) => Generator<T, R, N>
  >,
) {
  return function* (this: This): Generator<T, R, N> {
    for (;;) {
      yield* target.call(this)
    }
  }
}

function interactor(
  target: (this: BehavioralProgram<Sign>) => Operation<void>,
  context: ClassMethodDecoratorContext<
    BehavioralProgram<Sign>,
    (this: BehavioralProgram<Sign>) => Operation<void>
  >,
) {
  context.addInitializer(function () {
    const methodName = context.name as string
    this.tasks.set(methodName, target.bind(this))
  })
}

type Sign = ["time", Date] | ["stupid", "stupid"]

export class Swash extends BehavioralProgram<Sign> {
  @behavior
  @repeating
  *["Signs are logged to the console."]() {
    console.info(yield* this.sync({ wait: () => true }))
  }

  @behavior
  @repeating
  *["Every third second do something stupid."]() {
    yield* this.sync({ wait: () => true })
    yield* this.sync({ wait: () => true })
    yield* this.sync({ wait: () => true })
    yield* this.sync({ post: [["stupid", "stupid"]] })
  }

  @interactor
  @repeating
  *["Post the current time every second."]() {
    yield* this.post(["time", new Date()])
    yield* sleep(1000)
  }
}

await main(function* () {
  const game = new Swash()
  yield* game.play()
})
