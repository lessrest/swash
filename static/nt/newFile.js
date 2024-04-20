import { call, each, main, sleep } from "effection";
import { extractNounsFromText, modelsByName } from "./ntchat";
await main(function* () {
    const text = yield* call(fetch("https://www.less.rest/song/files/How%20Does%20Metaphysics%20Reveal%20Reality/lyrics.json").then((res) => res.json()));
    const words = text.words.map(({ word }) => word);
    const chunkSize = 50;
    const chunks = [];
    for (let i = 0; i < words.length; i += chunkSize) {
        chunks.push(words.slice(i, i + chunkSize));
    }
    const nouns = [];
    for (const chunk of chunks) {
        console.log("Processing chunk:", chunk);
        const string = chunk.join(" ");
        console.log("Chunk as string:", string);
        const channel = yield* extractNounsFromText(string, modelsByName["Claude III Sonnet"]);
        let lastContent = "";
        for (const { role, content } of yield* each(channel)) {
            console.log(`${role}: ${content}`);
            lastContent = content;
            yield* each.next();
        }
        console.log("Last content:", lastContent);
        const chunkNouns = lastContent.replace(/<\/words>$/, "").split(/, */);
        console.log("Extracted nouns:", chunkNouns);
        nouns.push(...chunkNouns);
        yield* sleep(500);
    }
    console.log(nouns);
});
