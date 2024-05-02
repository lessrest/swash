onload = async () => {
  await Scheme.load_main("snoot.wasm", {}, {
	console: {
	  log: x => { console.log(x) },
	  group: x => { console.group(x) },
	  groupEnd: () => { console.groupEnd() },
	}
  })
}
