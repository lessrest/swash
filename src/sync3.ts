// new implementation of behavioral threads
// integrated with structured concurrency

import { Operation } from "effection"

interface Sync<Post> {
  tell: Post[]
  wait: (t: Post) => boolean
  hush: (t: Post) => boolean
}

type Behavior<Post> = Generator<Sync<Post>, void, Post>

function* program<Post>(body: () => Operation<void>) {
  const x = new Program<Post>()
  yield* x.run(body)
}

class Program<Post> {
  *run(body: (this: Program<Post>) => Operation<void>) {}
}

function* foo() {
  yield* program<string>(function* () {
    yield* this.start(function* () {
      yield this.sync.tell("a")
      yield this.sync.tell("b")
    })

    yield* this.start(function* () {
      yield this.sync.hush((x) => x === "a").tell("c")
    })
  })
}
